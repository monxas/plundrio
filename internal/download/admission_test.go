package download

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/elsbrock/go-putio"
	"github.com/elsbrock/plundrio/internal/config"
)

func TestEntireQueuedBacklogProtectedButMixedActiveStillTimesOut(t *testing.T) {
	past := time.Now().Add(-2 * time.Hour)
	m := &Manager{dlConfig: &DownloadConfig{TransferContextTimeout: time.Minute}}
	m.coordinator = NewTransferCoordinator(m)
	for _, id := range []int64{1, 2, 3} {
		ctx := m.coordinator.InitiateTransfer(id, "offline-queued-fixture", id, 40)
		if err := m.coordinator.StartDownload(id); err != nil {
			t.Fatal(err)
		}
		ctx.StartTime = past
		ctx.LastProgressTime = past
		if id == 1 {
			ctx.admissionPendingFiles = 1
		}
		if id == 3 {
			ctx.admissionPendingFiles = 1
			ctx.admissionActiveFiles = 1
		}
	}
	w := &Watchdog{manager: m}
	w.cleanupStaleContexts()
	for _, id := range []int64{1, 2, 3} {
		ctx, _ := m.coordinator.GetTransferContext(id)
		want := TransferLifecycleDownloading
		if id == 3 {
			want = TransferLifecycleFailed
		}
		if ctx.State != want {
			t.Fatalf("context %d: got %s want %s", id, ctx.State, want)
		}
		if !ctx.LastProgressTime.Equal(past) {
			t.Fatal("fabricated progress")
		}
	}
}

func TestExistingFileAccountingAndUnassignedChannelBacklog(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "fixture"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "fixture", "existing.bin"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	m := &Manager{cfg: &config.Config{}, jobs: make(chan downloadJob, 8), stopChan: make(chan struct{}), dlConfig: &DownloadConfig{TransferContextTimeout: time.Minute}}
	m.coordinator = NewTransferCoordinator(m)
	ctx := m.coordinator.InitiateTransfer(1, "fixture", 1, 3)
	if err := m.coordinator.StartDownload(1); err != nil {
		t.Fatal(err)
	}
	p := &TransferProcessor{manager: m, targetDir: root}
	files := []*putio.File{{ID: 10, Name: "existing.bin", Size: 1}, {ID: 11, Name: "queued-one.bin", Size: 1}, {ID: 12, Name: "queued-two.bin", Size: 1}}
	if got := p.queueTransferFiles(&putio.Transfer{ID: 1, Name: "fixture"}, files); got != 2 {
		t.Fatalf("queued %d", got)
	}
	if ctx.admissionPendingFiles != 2 || ctx.admissionActiveFiles != 0 || len(m.jobs) != 2 {
		t.Fatal("channel backlog ownership missing")
	}
	if ctx.CompletedFiles != 1 {
		t.Fatal("existing file completion changed")
	}
	ctx.StartTime = time.Now().Add(-2 * time.Hour)
	ctx.LastProgressTime = ctx.StartTime
	(&Watchdog{manager: m}).cleanupStaleContexts()
	if ctx.State != TransferLifecycleDownloading {
		t.Fatal("unassigned queued context failed")
	}
}

func TestRetryPendingOwnershipSurvivesMoveToChannel(t *testing.T) {
	for _, mode := range []string{"cancel", "ready"} {
		t.Run(mode, func(t *testing.T) {
			m := &Manager{cfg: &config.Config{}, jobs: make(chan downloadJob, 4), stopChan: make(chan struct{}), dlConfig: &DownloadConfig{}}
			m.coordinator = NewTransferCoordinator(m)
			ctx := m.coordinator.InitiateTransfer(1, "retry-fixture", 1, 1)
			m.changeAdmissionPending(1, -1) // first payload attempt was admitted
			rq := NewRetryQueue(m)
			job := downloadJob{TransferID: 1, FileID: 2, Name: "retry-fixture"}
			if !rq.Add(job, errors.New("offline transient")) {
				t.Fatal("retry not accepted")
			}
			if ctx.admissionPendingFiles != 1 {
				t.Fatal("future retry not retained")
			}
			if mode == "cancel" {
				rq.Remove(2)
				if ctx.admissionPendingFiles != 0 {
					t.Fatal("cancel leaked ownership")
				}
			} else {
				rq.items[0].NextRetry = time.Now().Add(-time.Second)
				rq.processReadyItems()
				if len(m.jobs) != 1 || ctx.admissionPendingFiles != 1 {
					t.Fatal("retry lost pending ownership on channel transfer")
				}
			}
		})
	}
}
