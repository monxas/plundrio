package server

import (
	"crypto/sha1"
	"encoding/hex"
	"testing"

	"github.com/elsbrock/plundrio/internal/config"
	"github.com/elsbrock/plundrio/internal/download"
	"github.com/elsbrock/plundrio/internal/naming"
)

func namingServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	m := download.New(&config.Config{TargetDir: dir}, nil)
	m.SetNameStore(naming.NewStore(dir))
	return &Server{cfg: &config.Config{TargetDir: dir}, dlManager: m, labels: NewLabelStore(dir)}
}

// A minimal single-file torrent whose info.name is the generic "Season 2".
func rootlessTorrent() ([]byte, string) {
	info := "d6:lengthi1e4:name8:Season 212:piece lengthi16384e6:pieces20:aaaaaaaaaaaaaaaaaaaae"
	sum := sha1.Sum([]byte(info))
	return []byte("d8:announce7:udp://t4:info" + info + "e"), hex.EncodeToString(sum[:])
}

func TestUploadedTorrentGetsHashLabelsAndReleaseRoot(t *testing.T) {
	s := namingServer(t)
	data, want := rootlessTorrent()
	got := s.rememberTorrent(data, "The Golden Girls S02 DVDrip.torrent", []string{"tv-sonarr"})
	if got != want {
		t.Fatalf("info-hash %q, want %q", got, want)
	}
	if l := s.labels.Get(want); len(l) != 1 || l[0] != "tv-sonarr" {
		t.Fatalf("labels not stored for uploaded torrent: %v", l)
	}
	if root := s.dlManager.LocalRoot("Season 2", want); root != "The Golden Girls S02 DVDrip" {
		t.Fatalf("root %q", root)
	}
}

func TestMagnetNameIsRemembered(t *testing.T) {
	s := namingServer(t)
	s.rememberName(magnetInfoHash(goldenMagnet), naming.MagnetDisplayName(goldenMagnet))
	if root := s.dlManager.LocalRoot("Season 2", "255ab56c438f1456c8822e272686405758b109af"); root != "The Golden Girls S02 DVDrip" {
		t.Fatalf("root %q", root)
	}
	// Garbage never becomes a name and never panics.
	s.rememberName("", "x")
	if h := s.rememberTorrent([]byte("not a torrent"), "x", nil); h != "" {
		t.Fatalf("hash from garbage: %q", h)
	}
}

const goldenMagnet = "magnet:?xt=urn:btih:255AB56C438F1456C8822E272686405758B109AF&dn=The+Golden+Girls+S02+DVDrip"
