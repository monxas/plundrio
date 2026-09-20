package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A minimal bencoded torrent-ish payload; we only care that bytes round-trip.
const fakeTorrent = "d8:announce20:http://tracker/announce4:infod4:name9:Some.Fileee"

func TestFetchTorrentFromURL_ServesTorrentBytes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-bittorrent")
		w.Header().Set("Content-Disposition", `attachment; filename="Some.File.S01E01.torrent"`)
		_, _ = w.Write([]byte(fakeTorrent))
	}))
	defer srv.Close()

	data, filename, magnet, err := fetchTorrentFromURL(srv.URL + "/api/v1/indexer/1/download?apikey=x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if magnet != "" {
		t.Fatalf("expected no magnet, got %q", magnet)
	}
	if string(data) != fakeTorrent {
		t.Fatalf("torrent bytes mismatch: got %q", string(data))
	}
	if filename != "Some.File.S01E01.torrent" {
		t.Fatalf("filename mismatch: got %q", filename)
	}
}

func TestFetchTorrentFromURL_FilenameFallsBackToURLPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(fakeTorrent))
	}))
	defer srv.Close()

	_, filename, _, err := fetchTorrentFromURL(srv.URL + "/dl/Another.Release")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filename != "Another.Release.torrent" {
		t.Fatalf("expected .torrent extension appended, got %q", filename)
	}
}

func TestFetchTorrentFromURL_RedirectToMagnet(t *testing.T) {
	const want = "magnet:?xt=urn:btih:2FCAB232E5C128CDBD638B2C6F6799D17D218C49&dn=Test"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, want, http.StatusFound)
	}))
	defer srv.Close()

	data, _, magnet, err := fetchTorrentFromURL(srv.URL + "/redirect")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if magnet != want {
		t.Fatalf("magnet mismatch:\n got %q\nwant %q", magnet, want)
	}
	if data != nil {
		t.Fatalf("expected no torrent data alongside magnet, got %d bytes", len(data))
	}
}

func TestFetchTorrentFromURL_MagnetInBody(t *testing.T) {
	const want = "magnet:?xt=urn:btih:abcdef0123456789abcdef0123456789abcdef01"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("  " + want + "\n"))
	}))
	defer srv.Close()

	_, _, magnet, err := fetchTorrentFromURL(srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if magnet != want {
		t.Fatalf("magnet mismatch: got %q", magnet)
	}
}

func TestFetchTorrentFromURL_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	if _, _, _, err := fetchTorrentFromURL(srv.URL); err == nil {
		t.Fatal("expected an error for a 404 response")
	} else if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error should mention the status code, got %v", err)
	}
}

func TestFetchTorrentFromURL_EmptyBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if _, _, _, err := fetchTorrentFromURL(srv.URL); err == nil {
		t.Fatal("expected an error for an empty body")
	}
}

func TestFetchTorrentFromURL_UnreachableHost(t *testing.T) {
	// Mirrors the real failure mode: an internal host Put.io could never reach.
	if _, _, _, err := fetchTorrentFromURL("http://127.0.0.1:1/never"); err == nil {
		t.Fatal("expected a transport error for an unreachable host")
	}
}

func TestEnsureTorrentExt(t *testing.T) {
	cases := map[string]string{
		"file":                 "file.torrent",
		"file.torrent":         "file.torrent",
		"FILE.TORRENT":         "FILE.TORRENT",
		"Show.S01E01.1080p":    "Show.S01E01.1080p.torrent",
		"weird.name.torrent.x": "weird.name.torrent.x.torrent",
	}
	for in, want := range cases {
		if got := ensureTorrentExt(in); got != want {
			t.Errorf("ensureTorrentExt(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFilenameFromResponse_StripsPathTraversal(t *testing.T) {
	resp := &http.Response{Header: http.Header{}}
	resp.Header.Set("Content-Disposition", `attachment; filename="../../etc/passwd"`)
	if got := filenameFromResponse(resp, "http://indexer/dl"); got != "passwd.torrent" {
		t.Fatalf("expected traversal stripped, got %q", got)
	}
}

func TestFilenameFromResponse_DefaultsWhenNothingUsable(t *testing.T) {
	resp := &http.Response{Header: http.Header{}}
	if got := filenameFromResponse(resp, "http://indexer"); got != "download.torrent" {
		t.Fatalf("expected default name, got %q", got)
	}
}

func TestFirstHTTPURL(t *testing.T) {
	const magnet = "magnet:?xt=urn:btih:abcdef0123456789abcdef0123456789abcdef01"
	const httpURL = "http://prowlarr:9696/api/v1/indexer/5/download?apikey=k"

	cases := []struct {
		name       string
		candidates []string
		want       string
	}{
		// Sonarr/Radarr put the .torrent URL in "filename".
		{"filename holds the url", []string{httpURL, ""}, httpURL},
		// The magnetLink field used to be forwarded to Put.io verbatim,
		// producing `putio error code:404 FileNotFound` for internal hosts.
		{"magnetLink holds the url", []string{"", httpURL}, httpURL},
		{"https is accepted", []string{"https://indexer/dl.torrent"}, "https://indexer/dl.torrent"},
		// Real magnets must fall through to the magnet branch.
		{"magnet is not an http url", []string{magnet, magnet}, ""},
		{"empty", []string{"", ""}, ""},
		// Guard against scheme-prefix lookalikes.
		{"not a url", []string{"httpsomething"}, ""},
	}
	for _, tc := range cases {
		if got := firstHTTPURL(tc.candidates...); got != tc.want {
			t.Errorf("%s: firstHTTPURL(%q) = %q, want %q", tc.name, tc.candidates, got, tc.want)
		}
	}
}

func TestMagnetInfoHashFromRedirectedMagnet(t *testing.T) {
	// The URL branch relies on magnetInfoHash to keep OnePacerr labels working.
	const magnet = "magnet:?xt=urn:btih:2FCAB232E5C128CDBD638B2C6F6799D17D218C49&dn=Test"
	if got := magnetInfoHash(magnet); !strings.EqualFold(got, "2FCAB232E5C128CDBD638B2C6F6799D17D218C49") {
		t.Fatalf("magnetInfoHash = %q", got)
	}
}
