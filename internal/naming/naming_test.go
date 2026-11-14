package naming

import (
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The real magnet Sonarr sent on 2026-11-14 (trackers trimmed).
const goldenS02 = "magnet:?xt=urn:btih:255AB56C438F1456C8822E272686405758B109AF" +
	"&dn=The+Golden+Girls+S02+DVDrip&tr=udp%3A%2F%2Ftracker.opentrackr.org%3A1337%2Fannounce"

func TestMagnetDisplayName(t *testing.T) {
	cases := map[string]string{
		goldenS02: "The Golden Girls S02 DVDrip",
		"magnet:?xt=urn:btih:E91ADF0C5EC1AE96781F1438DA21C6F48D274AB1&dn=The+Golden+Girls+1985+S03+DVDRip+x264+%5Bi_c%5D": "The Golden Girls 1985 S03 DVDRip x264 [i_c]",
		"magnet:?xt=urn:btih:abc":                        "",
		"magnet:?dn=..%2F..%2Fetc%2Fpasswd&xt=urn:btih:a": "etc passwd",
		"magnet:?dn=bad%zzvalue&xt=urn:btih:a":            "",
		"https://example.com/x.torrent":                   "",
	}
	for in, want := range cases {
		if got := MagnetDisplayName(in); got != want {
			t.Errorf("MagnetDisplayName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLocalRoot(t *testing.T) {
	h := "255ab56c438f1456c8822e272686405758b109af"
	cases := []struct{ putio, req, want string }{
		// The incident: Put.io renamed the transfer to the torrent root.
		{"Season 2", "The Golden Girls S02 DVDrip", "The Golden Girls S02 DVDrip"},
		// Put.io name is fine but differs: the client's identity wins.
		{"The Golden Girls 1985 Season 3 Complete DVDRip x264 [i_c]",
			"The Golden Girls 1985 S03 DVDRip x264 [i_c]", "The Golden Girls 1985 S03 DVDRip x264 [i_c]"},
		// Same name (case-insensitive): unchanged, historic layout.
		{"Show.S01.1080p", "show.s01.1080p", "Show.S01.1080p"},
		// Nothing requested, meaningful name: unchanged.
		{"Show.S01.1080p", "", "Show.S01.1080p"},
		// Nothing requested, generic name: disambiguated, never shared.
		{"Season 1", "", "Season 1 [255ab56c]"},
		{"Disc 2", "", "Disc 2 [255ab56c]"},
		{"Extras", "", "Extras [255ab56c]"},
		// Hostile names never escape the download root.
		{"../../etc", "", "etc"},
		{"", "", "transfer"},
		{"x", "unknown.torrent", "x"},
	}
	for _, c := range cases {
		if got := LocalRoot(c.putio, c.req, h); got != c.want {
			t.Errorf("LocalRoot(%q, %q) = %q, want %q", c.putio, c.req, got, c.want)
		}
	}
}

func TestIsGeneric(t *testing.T) {
	for _, s := range []string{"Season 2", "season.02", "S03", "Temporada 1", "Disc 1", "CD2", "Vol 3",
		"Extras", "Featurettes", "Specials", "Subs", "New Folder", "07"} {
		if !IsGeneric(s) {
			t.Errorf("IsGeneric(%q) = false", s)
		}
	}
	for _, s := range []string{"The Golden Girls S02 DVDrip", "Season 2 Complete Golden Girls",
		"Breaking.Bad.S01.1080p", "Severance", "Specials of Mars"} {
		if IsGeneric(s) {
			t.Errorf("IsGeneric(%q) = true", s)
		}
	}
}

func TestSafeComponentAndRelPath(t *testing.T) {
	if got := SafeComponent("a/b\\c\x00d\te"); got != "a b c de" && got != "a b cde" {
		t.Errorf("SafeComponent = %q", got)
	}
	if got := SafeComponent(".."); got != "" {
		t.Errorf("SafeComponent(..) = %q", got)
	}
	long := strings.Repeat("ñ", 300)
	if got := SafeComponent(long); len(got) > MaxComponentBytes || !strings.HasPrefix(long, got) {
		t.Errorf("SafeComponent did not cut on a rune boundary: %d bytes", len(got))
	}
	if got := SafeRelPath("Season 2/../../01. Title.mkv"); got != "Season 2/01. Title.mkv" {
		t.Errorf("SafeRelPath = %q", got)
	}
}

func bstr(s string) string { return itoa(len(s)) + ":" + s }
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for ; n > 0; n /= 10 {
		d = append([]byte{byte('0' + n%10)}, d...)
	}
	return string(d)
}

func TestTorrentInfo(t *testing.T) {
	info := "d" + bstr("files") + "ld" + bstr("length") + "i12345e" + bstr("path") + "l" +
		bstr("01. End Of The Curse.mkv") + "eee" + bstr("name") + bstr("Season 2") +
		bstr("piece length") + "i262144e" + bstr("pieces") + bstr(strings.Repeat("x", 20)) + "e"
	data := []byte("d" + bstr("announce") + bstr("udp://t") + bstr("info") + info + "e")
	sum := sha1.Sum([]byte(info))
	hash, name, err := TorrentInfo(data)
	if err != nil || hash != hex.EncodeToString(sum[:]) || name != "Season 2" {
		t.Fatalf("TorrentInfo = %q %q %v", hash, name, err)
	}
	for _, bad := range []string{"", "x", "d", "d4:info", "d4:infoi1", "d99999999999:x", "l" + strings.Repeat("l", 100)} {
		if _, _, err := TorrentInfo([]byte(bad)); err == nil {
			t.Errorf("TorrentInfo(%q) accepted malformed input", bad)
		}
	}
}

func TestStorePersistsAndPrunes(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	s.Remember("255AB56C438F1456C8822E272686405758B109AF", "The Golden Girls S02 DVDrip")
	s.Remember("", "ignored")
	s.Remember("abc", "magnet:?xt=urn:btih:abc") // never a name

	reloaded := NewStore(dir)
	if got := reloaded.Root("Season 2", "255ab56c438f1456c8822e272686405758b109af"); got != "The Golden Girls S02 DVDrip" {
		t.Fatalf("Root after reload = %q", got)
	}
	if reloaded.Requested("abc") != "" {
		t.Fatal("stored a magnet as a name")
	}
	// Old entries are pruned on the next write.
	reloaded.now = func() time.Time { return time.Now().Add(Retention + time.Hour) }
	reloaded.Remember("ffff", "Something New")
	if reloaded.Requested("255ab56c438f1456c8822e272686405758b109af") != "" {
		t.Fatal("expired entry survived")
	}
	if _, err := os.Stat(filepath.Join(dir, "transfer-names.json")); err != nil {
		t.Fatal(err)
	}
	var nilStore *Store
	if nilStore.Root("Season 1", "deadbeefcafe") != "Season 1 [deadbeef]" {
		t.Fatal("nil store must behave as empty")
	}
}
