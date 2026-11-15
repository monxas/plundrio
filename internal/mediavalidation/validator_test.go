package mediavalidation

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fakeProbe(size int64, end float64, calls *atomic.Int32) Probe {
	return func(ctx context.Context, args []string) ([]byte, error) {
		calls.Add(1)
		if strings.Contains(strings.Join(args, " "), "packet=pts_time") {
			return []byte(`{"packets":[{"pts_time":"` + strconv.FormatFloat(end, 'f', 3, 64) + `","duration_time":"0.1"}]}`), nil
		}
		return []byte(`{"format":{"duration":"10","start_time":"0","size":"` + strconv.FormatInt(size, 10) + `"},"streams":[{"codec_type":"video","duration":"10","start_time":"0","disposition":{"attached_pic":0}}]}`), nil
	}
}
func fixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	p := filepath.Join(root, "clip.mp4")
	if err := os.WriteFile(p, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	return root, p
}
func TestExactIdentityCacheAndChangedFile(t *testing.T) {
	root, p := fixture(t)
	var calls atomic.Int32
	v := New(root, t.TempDir(), fakeProbe(7, 9.9, &calls))
	if r := v.Check(context.Background(), p); !r.Accepted() {
		t.Fatalf("healthy: %+v", r)
	}
	if r := v.Check(context.Background(), p); !r.Accepted() || calls.Load() != 2 {
		t.Fatalf("cache not reused: %+v calls=%d", r, calls.Load())
	}
	if err := os.Chtimes(p, time.Now(), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if r := v.Check(context.Background(), p); !r.Accepted() || calls.Load() != 4 {
		t.Fatalf("changed identity not rechecked: %+v", r)
	}
}
func TestTruncatedTailHeldWithoutRetryAndSurvivesRestart(t *testing.T) {
	root, p := fixture(t)
	state := t.TempDir()
	var calls atomic.Int32
	probe := fakeProbe(7, 3, &calls)
	v := New(root, state, probe)
	if r := v.Check(context.Background(), p); r.Status != "rejected" || r.Reason != "video_ends_before_declared_duration" {
		t.Fatalf("truncated: %+v", r)
	}
	v = New(root, state, probe)
	if !v.Rejected(p) {
		t.Fatal("rejected identity was lost on restart")
	}
	if r := v.Check(context.Background(), p); r.Status != "rejected" || calls.Load() != 2 {
		t.Fatal("cached invalid file was reprobed")
	}
	if err := v.Start(1, 1); err != nil {
		t.Fatal(err)
	}
	r := v.Check(context.Background(), p)
	if err := v.Track(1, 1, p, r); err != nil {
		t.Fatal(err)
	}
	if v.Complete(1) == nil || v.Ready(1) {
		t.Fatal("invalid file promoted")
	}
}
func TestTransferJournalPersistsAndDetectsChangedExistingFiles(t *testing.T) {
	root, p := fixture(t)
	state := t.TempDir()
	var calls atomic.Int32
	v := New(root, state, fakeProbe(7, 9.9, &calls))
	_ = v.Start(2, 1)
	if v.Ready(2) || v.Complete(2) == nil {
		t.Fatal("pending promoted")
	}
	r := v.Check(context.Background(), p)
	_ = v.Track(2, 1, p, r)
	if err := v.Complete(2); err != nil {
		t.Fatal(err)
	}
	v = New(root, state, fakeProbe(7, 9.9, &calls))
	if !v.Ready(2) {
		t.Fatal("accepted journal lost on restart")
	}
	if err := os.Chtimes(p, time.Now(), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if v.Ready(2) {
		t.Fatal("changed file accepted from old manifest")
	}
}
func TestImportedFilesMayMoveAfterSuccessfulAcceptance(t *testing.T) {
	root, p := fixture(t)
	var calls atomic.Int32
	v := New(root, t.TempDir(), fakeProbe(7, 9.9, &calls))
	_ = v.Start(3, 1)
	r := v.Check(context.Background(), p)
	_ = v.Track(3, 1, p, r)
	_ = v.Complete(3)
	if err := os.Rename(p, filepath.Join(t.TempDir(), "imported.mp4")); err != nil {
		t.Fatal(err)
	}
	if !v.Ready(3) {
		t.Fatal("legitimate importer move invalidated an already accepted transfer")
	}
}
func TestProbeUnavailableTimeoutAndBadJSONNeverAccept(t *testing.T) {
	for _, which := range []string{"missing", "timeout", "bad_json"} {
		t.Run(which, func(t *testing.T) {
			root, p := fixture(t)
			var calls int
			v := New(root, t.TempDir(), func(ctx context.Context, args []string) ([]byte, error) {
				calls++
				switch which {
				case "missing":
					return nil, exec.ErrNotFound
				case "timeout":
					<-ctx.Done()
					return nil, ctx.Err()
				default:
					return []byte("invalid"), nil
				}
			})
			v.timeout = time.Millisecond
			r := v.Check(context.Background(), p)
			if r.Accepted() || r.Status != "pending" {
				t.Fatalf("%s: %+v", which, r)
			}
			v.Check(context.Background(), p)
			if calls < 2 {
				t.Fatal("pending environment result became terminal cache")
			}
		})
	}
}
func TestUnsafeSymlinkAndNonVideoScope(t *testing.T) {
	root, p := fixture(t)
	var calls atomic.Int32
	v := New(root, t.TempDir(), fakeProbe(7, 9.9, &calls))
	outside := filepath.Join(t.TempDir(), "elsewhere.mp4")
	_ = os.WriteFile(outside, []byte("fixture"), 0600)
	link := filepath.Join(root, "link.mp4")
	_ = os.Symlink(outside, link)
	if r := v.Check(context.Background(), link); r.Accepted() {
		t.Fatal("symlink escape accepted")
	}
	if r := v.Check(context.Background(), outside); r.Accepted() {
		t.Fatal("external file accepted")
	}
	note := filepath.Join(root, "info.nfo")
	_ = os.WriteFile(note, []byte("note"), 0600)
	if r := v.Check(context.Background(), note); r.Status != "not_video" || calls.Load() != 0 {
		t.Fatalf("non-video probed: %+v", r)
	}
	if r := v.Check(context.Background(), p); !r.Accepted() {
		t.Fatal(r)
	}
}
func TestActualFFprobeHealthyAndTruncatedFixtures(t *testing.T) {
	ffmpeg, e := exec.LookPath("ffmpeg")
	if e != nil {
		t.Fatal("ffmpeg required for real local fixtures")
	}
	ffprobe, e := exec.LookPath("ffprobe")
	if e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	healthy := filepath.Join(root, "healthy.mp4")
	cmd := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=64x64:r=10", "-t", "3", "-c:v", "mpeg4", "-pix_fmt", "yuv420p", healthy)
	if raw, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("generate local fixture: %v %s", e, raw)
	}
	realProbe := func(ctx context.Context, args []string) ([]byte, error) {
		command := exec.CommandContext(ctx, ffprobe, args...)
		command.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
		var out, stderr boundedBuffer
		command.Stdout = &out
		command.Stderr = &stderr
		e := command.Run()
		t.Logf("local fixture ffprobe %v => %s", args, out.Bytes())
		if e == nil && stderr.Len() > 0 {
			e = errors.New("ffprobe_error")
		}
		return out.Bytes(), e
	}
	v := New(root, t.TempDir(), realProbe)
	if r := v.Check(context.Background(), healthy); !r.Accepted() {
		raw, _ := json.Marshal(r)
		t.Fatalf("healthy fixture rejected: %s", raw)
	}

	long := filepath.Join(root, "long.mp4")
	cmd = exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=64x64:r=10", "-t", "14", "-c:v", "mpeg4", "-pix_fmt", "yuv420p", long)
	if raw, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("generate long local fixture: %v %s", e, raw)
	}
	if r := v.Check(context.Background(), long); !r.Accepted() {
		t.Fatalf("nonzero tail seek rejected: %+v", r)
	}
	data, _ := os.ReadFile(healthy)
	disguised := filepath.Join(root, "renamed.txt")
	os.WriteFile(disguised, data, 0600)
	if r := v.Check(context.Background(), disguised); r.Status != "accepted" {
		t.Fatalf("renamed video bypassed or held: %+v", r)
	}
	archive := filepath.Join(root, "archive.mp4")
	os.WriteFile(archive, []byte{'P', 'K', 3, 4, 0, 0, 0, 0}, 0600)
	if r := v.Check(context.Background(), archive); r.Accepted() || r.Reason != "archive_requires_supported_extraction" {
		t.Fatalf("archive signature bypassed: %+v", r)
	}
	bytes, _ := os.ReadFile(healthy)
	bad := filepath.Join(root, "truncated.mp4")
	if err := os.WriteFile(bad, bytes[:len(bytes)/2], 0600); err != nil {
		t.Fatal(err)
	}
	if r := v.Check(context.Background(), bad); r.Status != "rejected" {
		t.Fatalf("truncated real fixture not held: %+v", r)
	}
}
func TestProtocolAndOutputBounds(t *testing.T) {
	args := strings.Join(baseArgs(), " ")
	if !strings.Contains(args, "-protocol_whitelist file") || !strings.Contains(args, "-max_alloc 33554432") {
		t.Fatal("unbounded protocol/allocation policy")
	}
	var b boundedBuffer
	if _, e := b.Write(make([]byte, maxJSON+1)); e == nil {
		t.Fatal("unbounded output")
	}
}

func TestArchiveIsRetainedForSeparateExtractionAdmission(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "release.zip")
	_ = os.WriteFile(p, []byte("archive"), 0600)
	var calls atomic.Int32
	v := New(root, t.TempDir(), fakeProbe(7, 9.9, &calls))
	r := v.Check(context.Background(), p)
	if r.Accepted() || r.Status != "pending" || calls.Load() != 0 || r.Reason != "archive_requires_supported_extraction" {
		t.Fatal(r)
	}
}

func TestChangedAfterProbeCannotCommitCompletion(t *testing.T) {
	root, p := fixture(t)
	var calls atomic.Int32
	v := New(root, t.TempDir(), fakeProbe(7, 9.9, &calls))
	_ = v.Start(8, 1)
	r := v.Check(context.Background(), p)
	_ = v.Track(8, 1, p, r)
	_ = os.Chtimes(p, time.Now(), time.Now().Add(time.Second))
	if v.Complete(8) == nil || v.Ready(8) {
		t.Fatal("changed file committed from an old probe")
	}
}

func TestTailUsesVerifiedStreamIndex(t *testing.T) {
	root, p := fixture(t)
	probe := func(ctx context.Context, args []string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "packet=pts_time") {
			for i, a := range args {
				if a == "-select_streams" && args[i+1] != "2" {
					t.Fatalf("tail selected wrong stream: %v", args)
				}
			}
			return []byte(`{"packets":[{"pts_time":"9.9","duration_time":"0.1"}]}`), nil
		}
		return []byte(`{"format":{"duration":"10","size":"7"},"streams":[{"index":0,"codec_type":"audio"},{"index":1,"codec_type":"video","disposition":{"attached_pic":1}},{"index":2,"codec_type":"video"}]}`), nil
	}
	if r := New(root, t.TempDir(), probe).Check(context.Background(), p); !r.Accepted() {
		t.Fatal(r)
	}
}
