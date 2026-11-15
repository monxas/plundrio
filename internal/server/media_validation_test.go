package server

import (
	"context"
	"github.com/elsbrock/plundrio/internal/config"
	"github.com/elsbrock/plundrio/internal/download"
	"github.com/elsbrock/plundrio/internal/mediavalidation"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRPCCompletionGateReadsPersistentAdmissionAcrossRestart(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "clip.mp4")
	_ = os.WriteFile(path, []byte("healthy"), 0600)
	cfg := &config.Config{TargetDir: root, FolderID: 1, MediaValidationEnabled: true, MediaValidationStateDir: state}
	check := func(want bool) {
		t.Helper()
		s := New(cfg, nil, download.New(cfg, nil))
		status, progress, left, reason := s.gateMediaCompletion(12, 6, 1, 0)
		finished := progress >= 1
		if finished != want {
			t.Fatalf("completion=%v want=%v", finished, want)
		}
		if !want && (status == 6 || left == 0 || reason == "") {
			t.Fatal("not explicitly held")
		}
	}
	check(false)
	v := mediavalidation.New(root, state, func(ctx context.Context, args []string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "packet=pts_time") {
			return []byte(`{"packets":[{"pts_time":"9.9","duration_time":"0.1"}]}`), nil
		}
		return []byte(`{"format":{"duration":"10","size":"7"},"streams":[{"codec_type":"video"}]}`), nil
	})
	_ = v.Start(12, 1)
	r := v.Check(context.Background(), path)
	if !r.Accepted() {
		t.Fatal(r)
	}
	_ = v.Track(12, 11, path, r)
	if e := v.Complete(12); e != nil {
		t.Fatal(e)
	}
	check(true)
	_ = os.Chtimes(path, time.Now(), time.Now().Add(time.Second))
	check(false)
}
