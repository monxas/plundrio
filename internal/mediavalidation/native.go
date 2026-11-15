package mediavalidation

import (
	"encoding/hex"
	"path/filepath"
	"strconv"
)

// NativeResolution records a migration decision backed by native ARR history and
// destination checks. It makes no claim about vanished download originals.
type NativeResolution struct {
	ID             int64               `json:"id"`
	Status         string              `json:"status"`
	EvidenceSHA256 string              `json:"evidence_sha256"`
	Files          []NativeDestination `json:"files"`
}
type NativeDestination struct {
	Path     string   `json:"path"`
	Identity Identity `json:"identity"`
}

// A migration receipt is historical evidence, not continuous library validation.
// Imported destinations need not be mounted into the downloader. The deployment
// actor verifies their identities immediately before installing these receipts.
func (v *Validator) nativeResolution(id int64) *NativeResolution {
	var r NativeResolution
	if readJSON(filepath.Join(v.state, "native", strconv.FormatInt(id, 10)+".json"), &r) != nil || r.ID != id || len(r.Files) == 0 || len(r.Files) > 1024 {
		return nil
	}
	h, e := hex.DecodeString(r.EvidenceSHA256)
	if e != nil || len(h) != 32 {
		return nil
	}
	if r.Status != "native_import_adopted" && r.Status != "requested_episode_superseded" {
		return nil
	}
	return &r
}
func (v *Validator) NativeRecorded(id int64) string {
	r := v.nativeResolution(id)
	if r == nil {
		return ""
	}
	return r.Status
}
func (v *Validator) NativeState(id int64) string {
	return v.NativeRecorded(id)
}
