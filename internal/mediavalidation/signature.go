package mediavalidation

import (
	"bytes"
	"os"
)

// Signature checks a small fixed prefix as well as the extension: a renamed
// MP4/Matroska/transport stream cannot bypass admission as a .txt or .jpg file.
func signature(path string) (isVideo, isArchive bool) {
	f, e := os.Open(path)
	if e != nil {
		return false, false
	}
	defer f.Close()
	b := make([]byte, 1024)
	n, _ := f.Read(b)
	b = b[:n]
	if bytes.HasPrefix(b, []byte{'P', 'K', 3, 4}) || bytes.HasPrefix(b, []byte{'P', 'K', 5, 6}) || bytes.HasPrefix(b, []byte("Rar!\x1a\x07")) || bytes.HasPrefix(b, []byte{'7', 'z', 0xbc, 0xaf, 0x27, 0x1c}) {
		return false, true
	}
	if bytes.HasPrefix(b, []byte{0x1a, 0x45, 0xdf, 0xa3}) || bytes.HasPrefix(b, []byte("FLV")) || bytes.HasPrefix(b, []byte{0x30, 0x26, 0xb2, 0x75, 0x8e, 0x66, 0xcf, 0x11}) {
		return true, false
	}
	if len(b) >= 12 && ((string(b[4:8]) == "ftyp" || string(b[4:8]) == "moov" || string(b[4:8]) == "mdat") || (string(b[:4]) == "RIFF" && string(b[8:12]) == "AVI ")) {
		return true, false
	}
	if len(b) >= 4 && b[0] == 0 && b[1] == 0 && b[2] == 1 && (b[3] == 0xba || b[3] == 0xb3) {
		return true, false
	}
	if len(b) > 376 && b[0] == 0x47 && b[188] == 0x47 && b[376] == 0x47 {
		return true, false
	}
	if len(b) > 388 && b[4] == 0x47 && b[196] == 0x47 && b[388] == 0x47 {
		return true, false
	}
	return false, false
}
