package server

import (
	"encoding/json"
	"github.com/elsbrock/plundrio/internal/config"
	"net/http/httptest"
	"strings"
	"testing"
)

// Sonarr ignores category labels when a client advertises a pre-4 version.
func TestSessionAdvertisesImplementedLabelCompatibility(t *testing.T) {
	s := &Server{cfg: &config.Config{TargetDir: "/downloads", DisableSessionAuth: true}}
	req := httptest.NewRequest("POST", "/transmission/rpc", strings.NewReader(`{"method":"session-get","tag":41}`))
	w := httptest.NewRecorder()
	s.handleRPC(w, req)
	var got struct {
		Arguments struct {
			Version string `json:"version"`
			RPC     int    `json:"rpc-version"`
			Dir     string `json:"download-dir"`
		} `json:"arguments"`
		Result string `json:"result"`
		Tag    int    `json:"tag"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Result != "success" || got.Tag != 41 || !strings.HasPrefix(got.Arguments.Version, "4.") || got.Arguments.RPC < 16 || got.Arguments.Dir != "/downloads" {
		t.Fatalf("unexpected session: %+v", got)
	}
}
func TestAdvertisedLabelsPersistAcrossStoreReopen(t *testing.T) {
	dir := t.TempDir()
	s := &Server{labels: NewLabelStore(dir)}
	s.labels.Set("0123456789abcdef0123456789abcdef01234567", []string{"tv-sonarr"})
	labels := NewLabelStore(dir).Get("0123456789abcdef0123456789abcdef01234567")
	if len(labels) != 1 || labels[0] != "tv-sonarr" {
		t.Fatalf("labels not preserved: %v", labels)
	}
}
