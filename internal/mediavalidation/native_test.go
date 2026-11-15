package mediavalidation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeMigrationDecisions(t *testing.T) {
	state := t.TempDir()
	v := New(t.TempDir(), state, nil)
	if v.Ready(7) || v.NativeRecorded(7) != "" {
		t.Fatal("old status alone must not admit")
	}
	p := filepath.Join(state, "native", "7.json")
	r := NativeResolution{ID: 7, Status: "native_import_adopted", EvidenceSHA256: strings.Repeat("a", 64), Files: []NativeDestination{{Path: "/unmounted/imported-destination", Identity: Identity{Size: 100}}}}
	if e := writeJSON(p, r); e != nil {
		t.Fatal(e)
	}
	if !v.Ready(7) {
		t.Fatal("verified native import receipt survives restart and missing old download")
	}
	v = New(t.TempDir(), state, nil)
	if !v.Ready(7) {
		t.Fatal("receipt lost on restart")
	}
	r.Status = "requested_episode_superseded"
	writeJSON(p, r)
	if v.Ready(7) || v.NativeState(7) != "requested_episode_superseded" {
		t.Fatal("superseded is resolved but never complete")
	}
	r.EvidenceSHA256 = ""
	writeJSON(p, r)
	if v.NativeRecorded(7) != "" {
		t.Fatal("missing provenance accepted")
	}
	os.WriteFile(p, []byte(`{"id":7,"status":"Processed"}`), 0600)
	if v.Ready(7) {
		t.Fatal("legacy label accepted")
	}
}
