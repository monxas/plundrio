// Package naming decides the local folder a Put.io transfer is downloaded to.
//
// # The problem it solves
//
// Put.io renames a transfer to the root entry of the torrent once metadata is
// known. Most torrents have a meaningful root ("Show.S03.1080p..."), but some
// season packs are built with a root that is literally "Season 2" and files
// called "01. Title.mkv". The release identity ("The Golden Girls S02 DVDrip")
// then only survives in the magnet's dn= parameter, i.e. in what the *arr
// asked for. Downloading to /downloads/Season 2 loses the series name, makes
// the pack unimportable, and lets two unrelated series collide in the same
// folder (The Golden Girls, 2026-11-14).
//
// # The rule
//
//  1. If the client told us the release name when adding the torrent and Put.io
//     reports a different name, the local root is the requested name and Put.io's
//     own tree is kept underneath it:
//     /downloads/The Golden Girls S02 DVDrip/Season 2/01. End Of The Curse.mkv
//  2. Otherwise, if Put.io's name is generic ("Season 2", "Disc 1", "Extras"),
//     the root is disambiguated with the info-hash: "Season 2 [255ab56c]".
//  3. Otherwise Put.io's name is used unchanged (the historic behaviour).
//
// Every name that becomes a path component is sanitised (no separators, no
// "..", no control characters, bounded length).
package naming

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxComponentBytes keeps names well under the 255-byte limit of most
// filesystems, leaving room for the " [hash]" suffix.
const MaxComponentBytes = 200

var genericName = regexp.MustCompile(`(?i)^(?:` +
	`(?:season|temporada|saison|staffel|series|serie|stagione|s)[ ._-]*\d{1,3}` +
	`|(?:disc|disk|dvd|cd|bd|part|pt|vol|volume|episode|ep)[ ._-]*\d{1,3}` +
	`|\d{1,3}` +
	`|complete|extras?|bonus|featurettes?|specials?|subs?|subtitles?|video|videos|` +
	`tv|tv ?shows?|movies?|downloads?|torrents?|new folder|untitled|files?|data|media` +
	`)$`)

// IsGeneric reports whether a name carries no release identity on its own.
func IsGeneric(name string) bool {
	return genericName.MatchString(strings.TrimSpace(name))
}

// SafeComponent turns an arbitrary name into a single safe path component.
// It returns "" when nothing usable is left.
func SafeComponent(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == '/' || r == '\\':
			b.WriteRune(' ')
		case r == 0 || unicode.IsControl(r):
			// drop
		default:
			b.WriteRune(r)
		}
	}
	// Internal whitespace is preserved on purpose: real file names such as
	// "13.  The Stan Who Came To Dinner.mkv" must land on disk unchanged.
	s := strings.TrimSpace(b.String())
	if s == "" || strings.Trim(s, ".") == "" {
		return ""
	}
	if len(s) > MaxComponentBytes {
		cut := MaxComponentBytes
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = strings.TrimSpace(s[:cut])
	}
	return s
}

// CleanRequestedName normalises a client-supplied release name: trims a
// trailing ".torrent", rejects magnet URIs and URLs, and sanitises it.
func CleanRequestedName(name string) string {
	name = strings.TrimSpace(name)
	lower := strings.ToLower(name)
	if strings.HasPrefix(lower, "magnet:") || strings.HasPrefix(lower, "http://") ||
		strings.HasPrefix(lower, "https://") {
		return ""
	}
	if strings.HasSuffix(lower, ".torrent") {
		name = name[:len(name)-len(".torrent")]
	}
	switch n := SafeComponent(name); strings.ToLower(n) {
	case "", "unknown", "download":
		return ""
	default:
		return n
	}
}

// MagnetDisplayName returns the dn= parameter of a magnet URI ("" if absent).
func MagnetDisplayName(magnet string) string {
	i := strings.Index(magnet, "?")
	if i < 0 || !strings.HasPrefix(strings.ToLower(magnet), "magnet:") {
		return ""
	}
	if q, err := url.ParseQuery(magnet[i+1:]); err == nil {
		return CleanRequestedName(q.Get("dn"))
	}
	// Lenient path for sloppy magnets with a stray '%' somewhere else.
	for _, kv := range strings.Split(magnet[i+1:], "&") {
		if strings.HasPrefix(strings.ToLower(kv), "dn=") {
			if v, err := url.QueryUnescape(kv[3:]); err == nil {
				return CleanRequestedName(v)
			}
		}
	}
	return ""
}

// LocalRoot returns the top-level folder name for a transfer (see package doc).
func LocalRoot(putioName, requested, hash string) string {
	put := SafeComponent(putioName)
	req := CleanRequestedName(requested)
	if req != "" && !strings.EqualFold(req, put) {
		return req
	}
	if put == "" {
		put = "transfer"
	}
	if IsGeneric(put) && len(hash) >= 8 {
		return put + " [" + strings.ToLower(hash[:8]) + "]"
	}
	return put
}

// SafeRelPath sanitises a slash-separated relative path component by
// component, dropping anything that could escape the download root.
func SafeRelPath(rel string) string {
	parts := strings.Split(strings.ReplaceAll(rel, "\\", "/"), "/")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if c := SafeComponent(p); c != "" {
			out = append(out, c)
		}
	}
	return strings.Join(out, "/")
}
