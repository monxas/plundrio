package naming

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/elsbrock/plundrio/internal/log"
)

// Retention bounds the store; entries are only needed while a transfer is
// alive, and *arr clients remove transfers once imported.
const Retention = 90 * 24 * time.Hour

// Entry is what we remember about a torrent the client added.
type Entry struct {
	RequestedName string    `json:"requested_name"`
	AddedAt       time.Time `json:"added_at"`
}

// Store persists info-hash -> requested release name across restarts, so a
// download that resumes after a crash keeps the same local folder.
// A nil *Store is valid and behaves as an empty store.
type Store struct {
	mu      sync.RWMutex
	entries map[string]Entry
	path    string
	now     func() time.Time
}

// NewStore loads (or creates) <dir>/transfer-names.json. An empty dir gives
// an in-memory store.
func NewStore(dir string) *Store {
	s := &Store{entries: map[string]Entry{}, now: time.Now}
	if dir != "" {
		s.path = filepath.Join(dir, "transfer-names.json")
		s.load()
	}
	return s
}

func normHash(h string) string { return strings.ToLower(strings.TrimSpace(h)) }

func (s *Store) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Warn("naming").Err(err).Str("path", s.path).Msg("Failed to read transfer name store")
		}
		return
	}
	var m map[string]Entry
	if err := json.Unmarshal(data, &m); err != nil {
		log.Warn("naming").Err(err).Str("path", s.path).Msg("Failed to parse transfer name store")
		return
	}
	for k, v := range m {
		s.entries[normHash(k)] = v
	}
	log.Info("naming").Int("count", len(s.entries)).Str("path", s.path).Msg("Loaded transfer names")
}

// saveLocked writes atomically (temp file + fsync + rename). Caller holds mu.
func (s *Store) saveLocked() {
	if s.path == "" {
		return
	}
	cutoff := s.now().Add(-Retention)
	for k, v := range s.entries {
		if !v.AddedAt.IsZero() && v.AddedAt.Before(cutoff) {
			delete(s.entries, k)
		}
	}
	data, err := json.MarshalIndent(s.entries, "", "  ")
	if err != nil {
		return
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Warn("naming").Err(err).Msg("Failed to create name store directory")
		return
	}
	tmp, err := os.CreateTemp(dir, ".transfer-names-*.json")
	if err != nil {
		log.Warn("naming").Err(err).Msg("Failed to create temp name store")
		return
	}
	_, werr := tmp.Write(data)
	serr := tmp.Sync()
	cerr := tmp.Close()
	if werr != nil || serr != nil || cerr != nil {
		os.Remove(tmp.Name())
		log.Warn("naming").Msg("Failed to write name store")
		return
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		os.Remove(tmp.Name())
		log.Warn("naming").Err(err).Msg("Failed to replace name store")
	}
}

// Remember records the release name a client asked for. Empty names or
// hashes are ignored.
func (s *Store) Remember(hash, requested string) {
	if s == nil {
		return
	}
	hash, requested = normHash(hash), CleanRequestedName(requested)
	if hash == "" || requested == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.entries[hash]; ok && cur.RequestedName == requested {
		return
	}
	s.entries[hash] = Entry{RequestedName: requested, AddedAt: s.now()}
	s.saveLocked()
	log.Info("naming").Str("hash", hash).Str("requested_name", requested).
		Msg("Remembered requested release name")
}

// Requested returns the remembered release name for a hash ("" if unknown).
func (s *Store) Requested(hash string) string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.entries[normHash(hash)].RequestedName
}

// Root is LocalRoot with the remembered requested name.
func (s *Store) Root(putioName, hash string) string {
	return LocalRoot(putioName, s.Requested(hash), hash)
}
