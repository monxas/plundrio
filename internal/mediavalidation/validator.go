// Package mediavalidation admits existing local media before ARR publication.
// It never downloads, deletes, moves, decodes a library, or sends notifications.
package mediavalidation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const Policy = 1
const maxJSON = 2 << 20

type Identity struct {
	Device  uint64 `json:"device"`
	Inode   uint64 `json:"inode"`
	Size    int64  `json:"size"`
	Mtime   int64  `json:"mtime_ns"`
	Changed int64  `json:"changed_ns"`
}
type Result struct {
	Policy     int       `json:"policy"`
	Identity   Identity  `json:"identity"`
	Status     string    `json:"status"`
	Reason     string    `json:"reason"`
	Duration   float64   `json:"duration_seconds,omitempty"`
	LastPacket float64   `json:"last_packet_seconds,omitempty"`
	Observed   time.Time `json:"observed_at"`
}

func (r Result) Accepted() bool { return r.Status == "accepted" || r.Status == "not_video" }

type entry struct {
	Path   string `json:"path"`
	Result Result `json:"result"`
}
type transfer struct {
	Policy   int             `json:"policy"`
	ID       int64           `json:"id"`
	Expected int             `json:"expected_files"`
	Files    map[int64]entry `json:"files"`
	Complete bool            `json:"complete"`
}
type Probe func(context.Context, []string) ([]byte, error)
type Validator struct {
	root, state string
	probe       Probe
	timeout     time.Duration
	mu          sync.Mutex
	slots       chan struct{}
}

func New(root, state string, probe Probe) *Validator {
	return &Validator{root: root, state: state, probe: probe, timeout: 20 * time.Second, slots: make(chan struct{}, 1)}
}
func (v *Validator) Enabled() bool { return v != nil }
func identity(p string) (Identity, error) {
	s, e := os.Lstat(p)
	if e != nil {
		return Identity{}, e
	}
	if !s.Mode().IsRegular() {
		return Identity{}, errors.New("not_regular_file")
	}
	sys, ok := s.Sys().(*syscall.Stat_t)
	if !ok {
		return Identity{}, errors.New("identity_unavailable")
	}
	return Identity{uint64(sys.Dev), uint64(sys.Ino), s.Size(), s.ModTime().UnixNano(), changedNanoseconds(sys)}, nil
}
func (v *Validator) safePath(p string) (string, error) {
	root, e := filepath.EvalSymlinks(v.root)
	if e != nil {
		return "", e
	}
	root, e = filepath.Abs(root)
	if e != nil {
		return "", e
	}
	resolved, e := filepath.EvalSymlinks(p)
	if e != nil {
		return "", e
	}
	resolved, e = filepath.Abs(resolved)
	if e != nil {
		return "", e
	}
	rel, e := filepath.Rel(root, resolved)
	if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", errors.New("outside_download_root")
	}
	if s, e := os.Lstat(p); e != nil || !s.Mode().IsRegular() {
		return "", errors.New("not_regular_file")
	}
	return resolved, nil
}
func fileKey(p string) string { h := sha256.Sum256([]byte(p)); return hex.EncodeToString(h[:]) }
func (v *Validator) filePath(p string) string {
	return filepath.Join(v.state, "files", fileKey(p)+".json")
}
func (v *Validator) transferPath(id int64) string {
	return filepath.Join(v.state, "transfers", strconv.FormatInt(id, 10)+".json")
}
func readJSON(p string, dst interface{}) error {
	f, e := os.Open(p)
	if e != nil {
		return e
	}
	defer f.Close()
	s, e := f.Stat()
	if e != nil || !s.Mode().IsRegular() || s.Size() > maxJSON {
		return errors.New("invalid_journal")
	}
	return json.NewDecoder(f).Decode(dst)
}
func writeJSON(p string, value interface{}) error {
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(p), ".admission-")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if e = f.Chmod(0600); e == nil {
		e = json.NewEncoder(f).Encode(value)
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	if e != nil {
		return e
	}
	return os.Rename(name, p)
}
func video(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".mkv", ".mp4", ".m4v", ".avi", ".mov", ".webm", ".ts", ".m2ts", ".mpg", ".mpeg", ".wmv", ".mts", ".vob", ".m2v", ".ogv", ".flv", ".asf", ".divx":
		return true
	}
	return false
}
func number(s string) (float64, bool) {
	f, e := strconv.ParseFloat(s, 64)
	return f, e == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
}
func baseArgs() []string {
	return []string{"-v", "error", "-protocol_whitelist", "file", "-max_alloc", "33554432", "-probesize", "16777216", "-analyzeduration", "5000000", "-threads", "1"}
}

type metadata struct {
	Format struct {
		Duration string `json:"duration"`
		Start    string `json:"start_time"`
		Size     string `json:"size"`
	} `json:"format"`
	Streams []struct {
		Index       int    `json:"index"`
		Type        string `json:"codec_type"`
		Duration    string `json:"duration"`
		Start       string `json:"start_time"`
		Disposition struct {
			AttachedPic int `json:"attached_pic"`
		} `json:"disposition"`
	} `json:"streams"`
}
type packets struct {
	Packets []struct {
		PTS      string `json:"pts_time"`
		Duration string `json:"duration_time"`
	} `json:"packets"`
}

// Check inspects only this file and reuses a terminal result only for the exact
// same file identity and policy. A pending environment result is never cached as accepted.
func (v *Validator) Check(ctx context.Context, p string) Result {
	r := Result{Policy: Policy, Status: "pending", Observed: time.Now().UTC()}
	safe, e := v.safePath(p)
	if e != nil {
		r.Reason = "unsafe_or_missing_file"
		return r
	}
	before, e := identity(safe)
	if e != nil {
		r.Reason = "identity_unavailable"
		return r
	}
	r.Identity = before
	v.mu.Lock()
	var cached Result
	cachedOK := readJSON(v.filePath(safe), &cached) == nil && cached.Policy == Policy && cached.Identity == before && (cached.Accepted() || cached.Status == "rejected")
	v.mu.Unlock()
	if cachedOK {
		return cached
	}
	signedVideo, signedArchive := signature(safe)
	if ext := strings.ToLower(filepath.Ext(safe)); signedArchive || ext == ".zip" || ext == ".rar" || ext == ".7z" {
		r.Reason = "archive_requires_supported_extraction"
	} else if !video(safe) && !signedVideo {
		r.Status = "not_video"
		r.Reason = "outside_video_scope"
	} else {
		select {
		case v.slots <- struct{}{}:
			defer func() { <-v.slots }()
		case <-ctx.Done():
			r.Reason = "validation_cancelled"
			return r
		}
		probeCtx, cancel := context.WithTimeout(ctx, v.timeout)
		defer cancel()
		args := append(baseArgs(), "-show_entries", "format=duration,start_time,size:stream=index,codec_type,duration,start_time:stream_disposition=attached_pic", "-of", "json", safe)
		raw, err := v.probe(probeCtx, args)
		if err != nil {
			r.Reason = "probe_failed"
			r.Status = "rejected"
			if errors.Is(err, exec.ErrNotFound) || probeCtx.Err() != nil {
				r.Status = "pending"
				r.Reason = "probe_unavailable_or_timeout"
			}
		} else {
			var m metadata
			duration, valid := 0.0, false
			if len(raw) > maxJSON || json.Unmarshal(raw, &m) != nil {
				r.Reason = "invalid_probe_response"
			} else {
				duration, valid = number(m.Format.Duration)
				actual, e := strconv.ParseInt(m.Format.Size, 10, 64)
				if !valid || duration <= 0 || e != nil || actual != before.Size {
					r.Status = "rejected"
					r.Reason = "invalid_duration_or_size"
				} else {
					streamFound := false
					streamIndex := 0
					start, _ := number(m.Format.Start)
					for _, s := range m.Streams {
						if s.Type == "video" && s.Disposition.AttachedPic == 0 {
							streamFound = true
							streamIndex = s.Index
							if d, ok := number(s.Duration); ok && d > 0 {
								duration = d
							}
							if n, ok := number(s.Start); ok {
								start = n
							}
							break
						}
					}
					if !streamFound {
						r.Status = "rejected"
						r.Reason = "missing_video_stream"
					} else {
						seek := math.Max(start, start+duration-10)
						tailArgs := append(baseArgs(), "-select_streams", strconv.Itoa(streamIndex), "-read_intervals", fmt.Sprintf("%.6f%%", seek), "-show_packets", "-show_entries", "packet=pts_time,duration_time", "-of", "json", safe)
						tail, tailErr := v.probe(probeCtx, tailArgs)
						var pk packets
						if tailErr != nil || len(tail) > maxJSON || json.Unmarshal(tail, &pk) != nil {
							r.Reason = "tail_probe_failed"
							r.Status = "rejected"
							if probeCtx.Err() != nil {
								r.Reason = "probe_unavailable_or_timeout"
								r.Status = "pending"
							}
						} else {
							latest := -math.MaxFloat64
							for _, packet := range pk.Packets {
								if at, ok := number(packet.PTS); ok {
									d, _ := number(packet.Duration)
									latest = math.Max(latest, at+math.Max(d, 0))
								}
							}
							r.Duration = duration
							r.LastPacket = latest
							if len(pk.Packets) == 0 || latest < start+duration-math.Max(2, duration*0.01) {
								r.Status = "rejected"
								r.Reason = "video_ends_before_declared_duration"
							} else {
								r.Status = "accepted"
								r.Reason = "structure_and_tail_validated"
							}
						}
					}
				}
			}
		}
	}
	after, e := identity(safe)
	if e != nil || after != before {
		r.Status = "pending"
		r.Reason = "file_changed_during_validation"
		return r
	}
	if r.Status != "pending" {
		v.mu.Lock()
		e = writeJSON(v.filePath(safe), r)
		v.mu.Unlock()
		if e != nil {
			r.Status = "pending"
			r.Reason = "journal_unavailable"
		}
	}
	return r
}
func (v *Validator) Start(id int64, expected int) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if id <= 0 || expected < 0 {
		return errors.New("invalid_transfer")
	}
	return writeJSON(v.transferPath(id), transfer{Policy: Policy, ID: id, Expected: expected, Files: map[int64]entry{}})
}
func (v *Validator) Track(id, fileID int64, p string, r Result) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	var t transfer
	if e := readJSON(v.transferPath(id), &t); e != nil {
		return e
	}
	if t.ID != id || t.Policy != Policy {
		return errors.New("transfer_policy_mismatch")
	}
	t.Files[fileID] = entry{p, r}
	return writeJSON(v.transferPath(id), t)
}
func (v *Validator) Complete(id int64) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	var t transfer
	if e := readJSON(v.transferPath(id), &t); e != nil {
		return e
	}
	if t.Policy != Policy || t.ID != id || t.Expected <= 0 || len(t.Files) != t.Expected {
		return errors.New("media_validation_incomplete")
	}
	for _, f := range t.Files {
		if f.Result.Policy != Policy || !f.Result.Accepted() {
			return errors.New("media_validation_not_accepted")
		}
		current, err := identity(f.Path)
		if err != nil || current != f.Result.Identity {
			return errors.New("media_changed_before_completion")
		}
	}
	t.Complete = true
	return writeJSON(v.transferPath(id), t)
}

// Ready is a small journal read used by RPC, never a probe. Missing local files
// are permitted after an accepted import moved them; an existing changed file is not.
func (v *Validator) Ready(id int64) bool {
	if v.NativeState(id) == "native_import_adopted" {
		return true
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	var t transfer
	if readJSON(v.transferPath(id), &t) != nil || t.Policy != Policy || t.ID != id || !t.Complete || t.Expected <= 0 || len(t.Files) != t.Expected {
		return false
	}
	for _, f := range t.Files {
		if f.Result.Policy != Policy || !f.Result.Accepted() {
			return false
		}
		now, e := identity(f.Path)
		if e != nil {
			if os.IsNotExist(e) {
				continue
			}
			return false
		}
		if now != f.Result.Identity {
			return false
		}
	}
	return true
}

// Rejected remembers an invalid existing identity before the downloader can
// decide to resume/overwrite it. A changed or missing file needs a new decision.
func (v *Validator) Rejected(p string) bool {
	safe, e := v.safePath(p)
	if e != nil {
		return false
	}
	now, e := identity(safe)
	if e != nil {
		return false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	var r Result
	return readJSON(v.filePath(safe), &r) == nil && r.Policy == Policy && r.Identity == now && r.Status == "rejected"
}
