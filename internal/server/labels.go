package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/elsbrock/plundrio/internal/log"
)

// LabelStore persists Transmission-style labels keyed by torrent info-hash.
// OnePacerr (and *arr-adjacent tools) use labels as categories so completed
// downloads can be filtered without re-importing Sonarr/Radarr transfers.
type LabelStore struct {
	mu       sync.RWMutex
	labels   map[string][]string // lowercase hash -> labels
	filePath string
}

func NewLabelStore(configDir string) *LabelStore {
	path := filepath.Join(configDir, "torrent-labels.json")
	s := &LabelStore{
		labels:   make(map[string][]string),
		filePath: path,
	}
	s.load()
	return s
}

func (s *LabelStore) load() {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Warn("labels").Err(err).Str("path", s.filePath).Msg("Failed to read label store")
		}
		return
	}
	var m map[string][]string
	if err := json.Unmarshal(data, &m); err != nil {
		log.Warn("labels").Err(err).Msg("Failed to parse label store")
		return
	}
	normalized := make(map[string][]string, len(m))
	for k, v := range m {
		normalized[strings.ToLower(k)] = v
	}
	s.labels = normalized
	log.Info("labels").Int("count", len(s.labels)).Str("path", s.filePath).Msg("Loaded torrent labels")
}

func (s *LabelStore) save() {
	// Caller must hold write lock
	if s.filePath == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.filePath), 0o755); err != nil {
		log.Warn("labels").Err(err).Msg("Failed to create label store directory")
		return
	}
	data, err := json.MarshalIndent(s.labels, "", "  ")
	if err != nil {
		log.Warn("labels").Err(err).Msg("Failed to marshal label store")
		return
	}
	if err := os.WriteFile(s.filePath, data, 0o644); err != nil {
		log.Warn("labels").Err(err).Msg("Failed to write label store")
	}
}

func (s *LabelStore) Set(hash string, labels []string) {
	hash = strings.ToLower(strings.TrimSpace(hash))
	if hash == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(labels) == 0 {
		delete(s.labels, hash)
	} else {
		// Copy to avoid external mutation
		cp := make([]string, len(labels))
		copy(cp, labels)
		s.labels[hash] = cp
	}
	s.save()
}

func (s *LabelStore) Get(hash string) []string {
	hash = strings.ToLower(strings.TrimSpace(hash))
	s.mu.RLock()
	defer s.mu.RUnlock()
	labels := s.labels[hash]
	if len(labels) == 0 {
		return []string{}
	}
	cp := make([]string, len(labels))
	copy(cp, labels)
	return cp
}

func (s *LabelStore) Delete(hash string) {
	hash = strings.ToLower(strings.TrimSpace(hash))
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.labels, hash)
	s.save()
}

// magnetInfoHash extracts the btih info-hash from a magnet URI (hex preferred).
func magnetInfoHash(magnet string) string {
	// magnet:?xt=urn:btih:HASH&dn=...
	lower := strings.ToLower(magnet)
	const marker = "xt=urn:btih:"
	idx := strings.Index(lower, marker)
	if idx < 0 {
		return ""
	}
	start := idx + len(marker)
	end := start
	for end < len(magnet) {
		c := magnet[end]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			end++
			continue
		}
		// base32 also used (A-Z2-7)
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '2' && c <= '7') {
			end++
			continue
		}
		break
	}
	if end <= start {
		return ""
	}
	return strings.ToLower(magnet[start:end])
}
