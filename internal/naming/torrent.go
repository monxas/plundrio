package naming

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"strconv"
)

// errBencode is returned for anything that is not a well-formed torrent.
var errBencode = errors.New("naming: malformed bencode")

// TorrentInfo extracts the BitTorrent v1 info-hash and info.name from raw
// .torrent bytes. The info-hash is SHA-1 over the exact bencoded bytes of the
// "info" dictionary, so we locate that span without re-encoding anything.
func TorrentInfo(data []byte) (infoHash string, name string, err error) {
	if len(data) == 0 || data[0] != 'd' {
		return "", "", errBencode
	}
	pos := 1
	for pos < len(data) && data[pos] != 'e' {
		key, next, err := readString(data, pos)
		if err != nil {
			return "", "", err
		}
		valEnd, err := skipValue(data, next, 0)
		if err != nil {
			return "", "", err
		}
		if key == "info" {
			sum := sha1.Sum(data[next:valEnd])
			return hex.EncodeToString(sum[:]), infoName(data[next:valEnd]), nil
		}
		pos = valEnd
	}
	return "", "", errBencode
}

// infoName returns the "name" entry of a bencoded info dictionary.
func infoName(info []byte) string {
	if len(info) == 0 || info[0] != 'd' {
		return ""
	}
	pos := 1
	for pos < len(info) && info[pos] != 'e' {
		key, next, err := readString(info, pos)
		if err != nil {
			return ""
		}
		if key == "name" && next < len(info) && info[next] >= '0' && info[next] <= '9' {
			v, _, err := readString(info, next)
			if err != nil {
				return ""
			}
			return v
		}
		end, err := skipValue(info, next, 0)
		if err != nil {
			return ""
		}
		pos = end
	}
	return ""
}

func readString(b []byte, pos int) (string, int, error) {
	colon := pos
	for colon < len(b) && b[colon] != ':' {
		if b[colon] < '0' || b[colon] > '9' {
			return "", 0, errBencode
		}
		colon++
	}
	if colon >= len(b) || colon == pos || colon-pos > 10 {
		return "", 0, errBencode
	}
	n, err := strconv.Atoi(string(b[pos:colon]))
	if err != nil || n < 0 || n > len(b)-colon-1 {
		return "", 0, errBencode
	}
	start := colon + 1
	return string(b[start : start+n]), start + n, nil
}

// skipValue returns the index just past the bencoded value starting at pos.
func skipValue(b []byte, pos, depth int) (int, error) {
	if pos >= len(b) || depth > 64 {
		return 0, errBencode
	}
	switch c := b[pos]; {
	case c == 'i':
		for i := pos + 1; i < len(b); i++ {
			if b[i] == 'e' {
				return i + 1, nil
			}
		}
		return 0, errBencode
	case c == 'l' || c == 'd':
		i := pos + 1
		for i < len(b) && b[i] != 'e' {
			if c == 'd' {
				_, next, err := readString(b, i)
				if err != nil {
					return 0, err
				}
				i = next
			}
			next, err := skipValue(b, i, depth+1)
			if err != nil {
				return 0, err
			}
			i = next
		}
		if i >= len(b) {
			return 0, errBencode
		}
		return i + 1, nil
	case c >= '0' && c <= '9':
		_, next, err := readString(b, pos)
		return next, err
	default:
		return 0, errBencode
	}
}
