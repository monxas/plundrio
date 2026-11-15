package download

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/elsbrock/go-putio"
	"github.com/elsbrock/plundrio/internal/config"
	"github.com/elsbrock/plundrio/internal/mediavalidation"
)

func TestInvalidExistingMediaCannotCompleteOrRedownloadAcrossRestart(t *testing.T) {
	root := t.TempDir()
	state := t.TempDir()
	p := filepath.Join(root, "release", "clip.mp4")
	_ = os.MkdirAll(filepath.Dir(p), 0700)
	_ = os.WriteFile(p, []byte("broken"), 0600)
	cfg := &config.Config{TargetDir: root, MediaValidationEnabled: true, MediaValidationStateDir: state}
	newManager := func() *Manager {
		m := New(cfg, nil)
		m.validation = mediavalidation.New(root, state, func(context.Context, []string) ([]byte, error) {
			return []byte(`{"format":{"duration":"0","size":"6"},"streams":[]}`), nil
		})
		return m
	}
	for cycle := 0; cycle < 2; cycle++ {
		m := newManager()
		transfer := &putio.Transfer{ID: 12, Name: "release", FileID: 10, Hash: "hash"}
		file := &putio.File{ID: 11, Name: "clip.mp4", Size: 6}
		if !m.processor.initializeTransfer(transfer, 1) {
			t.Fatal("init")
		}
		if n := m.processor.queueTransferFiles(transfer, []*putio.File{file}); n != 0 {
			t.Fatal("invalid existing media was queued for redownload")
		}
		if m.coordinator.CompleteTransfer(12) == nil || m.MediaReady(12) {
			t.Fatal("invalid media was published")
		}
		ctx, _ := m.coordinator.GetTransferContext(12)
		if ctx.CompletedFiles != 0 || ctx.FailedFiles != 1 {
			t.Fatalf("invalid accounting: %+v", ctx.GetSnapshot())
		}
	}
	if _, e := os.Stat(p); e != nil {
		t.Fatal("retained file was removed")
	}
}
func TestCompletionRequiresAcceptedPersistentMediaManifest(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "clip.mp4")
	_ = os.WriteFile(p, []byte("healthy"), 0600)
	m := New(&config.Config{TargetDir: root, MediaValidationEnabled: true, MediaValidationStateDir: t.TempDir()}, nil)
	m.coordinator.InitiateTransfer(99, "release", 9, 1)
	_ = m.coordinator.StartDownload(99)
	ctx, _ := m.coordinator.GetTransferContext(99)
	ctx.CompletedFiles = 1
	ctx.State = TransferLifecycleCompleted
	if m.coordinator.CompleteTransfer(99) == nil || m.MediaReady(99) {
		t.Fatal("memory completion bypassed persistent admission")
	}
}
func TestMediaHeldErrorsNeverRetryAutomatically(t *testing.T) {
	for _, kind := range []string{"MediaValidationHeld", "MediaValidationPending"} {
		e := &DownloadError{Type: kind, Message: "retained"}
		if isRetryableError(e) {
			t.Fatal("media hold becomes redownload loop")
		}
	}
}
