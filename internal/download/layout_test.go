package download

import (
	"path/filepath"
	"testing"

	"github.com/elsbrock/go-putio"
	"github.com/elsbrock/plundrio/internal/naming"
)

func layoutProcessor(t *testing.T) *TransferProcessor {
	t.Helper()
	m := &Manager{}
	m.SetNameStore(naming.NewStore(t.TempDir()))
	return &TransferProcessor{manager: m, targetDir: "/downloads"}
}

const s02Hash = "255ab56c438f1456c8822e272686405758b109af"

func TestLocalRelPathKeepsHistoricLayout(t *testing.T) {
	p := layoutProcessor(t)
	tr := &putio.Transfer{Name: "Show.S01.1080p", Hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", FileID: 1}
	got := p.localRelPath(tr, &putio.File{ID: 2, Name: "Show.S01E01.mkv"})
	if want := filepath.Join("Show.S01.1080p", "Show.S01E01.mkv"); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	// Single-file transfer: historic "<name>/<name>" is preserved byte for byte.
	single := &putio.Transfer{Name: "Movie.2019.mkv", Hash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", FileID: 9}
	if got := p.localRelPath(single, &putio.File{ID: 9, Name: "Movie.2019.mkv"}); got != filepath.Join("Movie.2019.mkv", "Movie.2019.mkv") {
		t.Fatalf("single-file layout changed: %q", got)
	}
}

func TestLocalRelPathUsesReleaseIdentityForRenamedTransfers(t *testing.T) {
	p := layoutProcessor(t)
	p.manager.NameStore().Remember(s02Hash, "The Golden Girls S02 DVDrip")
	tr := &putio.Transfer{Name: "Season 2", Hash: s02Hash, FileID: 100}
	got := p.localRelPath(tr, &putio.File{ID: 101, Name: "01. End Of The Curse.mkv"})
	want := filepath.Join("The Golden Girls S02 DVDrip", "Season 2", "01. End Of The Curse.mkv")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if root := p.manager.LocalRoot(tr.Name, tr.Hash); root != "The Golden Girls S02 DVDrip" {
		t.Fatalf("RPC/health root %q differs from disk layout", root)
	}
	// Single-file transfer under a release root: no doubled name.
	p.manager.NameStore().Remember("cccccccccccccccccccccccccccccccccccccccc", "Movie 2019 1080p BluRay")
	single := &putio.Transfer{Name: "movie.mkv", Hash: "cccccccccccccccccccccccccccccccccccccccc", FileID: 7}
	if got := p.localRelPath(single, &putio.File{ID: 7, Name: "movie.mkv"}); got != filepath.Join("Movie 2019 1080p BluRay", "movie.mkv") {
		t.Fatalf("single-file release root: %q", got)
	}
}

func TestLocalRelPathGenericRootsNeverCollide(t *testing.T) {
	p := layoutProcessor(t)
	a := &putio.Transfer{Name: "Season 1", Hash: "1111111111111111111111111111111111111111", FileID: 1}
	b := &putio.Transfer{Name: "Season 1", Hash: "2222222222222222222222222222222222222222", FileID: 2}
	f := &putio.File{ID: 10, Name: "01. Pilot.mkv"}
	if pa, pb := p.localRelPath(a, f), p.localRelPath(b, f); pa == pb {
		t.Fatalf("two different torrents share %q", pa)
	}
}

func TestLocalRelPathKeepsSubfoldersAndStaysInside(t *testing.T) {
	p := layoutProcessor(t)
	tr := &putio.Transfer{Name: "Complete.Series.DVDRip", Hash: "dddddddddddddddddddddddddddddddddddddddd", FileID: 1}
	s1 := p.localRelPath(tr, &putio.File{ID: 2, Name: "Season 1/01.mkv"})
	s2 := p.localRelPath(tr, &putio.File{ID: 3, Name: "Season 2/01.mkv"})
	if s1 == s2 {
		t.Fatalf("season folders were flattened into %q", s1)
	}
	evil := p.localRelPath(tr, &putio.File{ID: 4, Name: "../../../etc/passwd"})
	if rel, err := filepath.Rel("/downloads", filepath.Join("/downloads", evil)); err != nil || filepath.IsAbs(rel) || rel == ".." || len(rel) >= 2 && rel[:3] == "../" {
		t.Fatalf("path escapes the download dir: %q", evil)
	}
}
