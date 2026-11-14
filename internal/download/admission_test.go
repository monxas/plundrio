package download

import (
	"errors"
	"fmt"
	"github.com/elsbrock/go-putio"
	"github.com/elsbrock/plundrio/internal/config"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestAdmissionFreshBootAndStrictSchema(t *testing.T) {
	now := time.Unix(1000, 0)
	good := `{"schema":1,"observed_at":990,"allow_new_work":true,"reason":"online","boot_id":"boot"}`
	if !admissionAllows([]byte(good), now, "boot") {
		t.Fatal("fresh metadata held")
	}
	for _, data := range []string{
		`{}`, `{"schema":1,"observed_at":800,"allow_new_work":true,"reason":"online","boot_id":"boot"}`,
		`{"schema":1,"observed_at":1001,"allow_new_work":true,"reason":"online","boot_id":"boot"}`,
		`{"schema":1,"observed_at":990,"allow_new_work":true,"reason":"online","boot_id":"other"}`,
		`{"schema":1,"observed_at":990,"allow_new_work":false,"reason":"degraded","boot_id":"boot"}`,
		`{"schema":1,"observed_at":990,"allow_new_work":1,"reason":"online","boot_id":"boot"}`,
		`{"schema":true,"observed_at":990,"allow_new_work":true,"reason":"online","boot_id":"boot"}`,
	} {
		if admissionAllows([]byte(data), now, "boot") {
			t.Fatalf("bad metadata allowed: %s", data)
		}
	}
}

func TestHeldWorkerRetainsJobUntilAllowed(t *testing.T) {
	stop := make(chan struct{})
	var admit atomic.Bool
	held := make(chan struct{}, 1)
	done := make(chan bool, 1)
	go func() { done <- waitForAdmission(stop, time.Millisecond, admit.Load, func() { held <- struct{}{} }) }()
	<-held
	select {
	case <-done:
		t.Fatal("held worker returned before admission")
	default:
	}
	admit.Store(true)
	select {
	case result := <-done:
		if !result {
			t.Fatal("not admitted")
		}
	case <-time.After(time.Second):
		t.Fatal("worker failed to resume")
	}
}

func TestHeldWorkerStopsWithoutStarting(t *testing.T) {
	stop := make(chan struct{})
	held := make(chan struct{}, 1)
	done := make(chan bool, 1)
	go func() {
		done <- waitForAdmission(stop, time.Hour, func() bool { return false }, func() { held <- struct{}{} })
	}()
	<-held
	close(stop)
	select {
	case result := <-done:
		if result {
			t.Fatal("shutdown admitted work")
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not drain wait")
	}
}

func TestClosedShutdownNeverCallsAdmission(t *testing.T) {
	stop := make(chan struct{})
	close(stop)
	if waitForAdmission(stop, time.Second, func() bool { t.Fatal("checked after shutdown"); return true }, func() {}) {
		t.Fatal("admitted after shutdown")
	}
}

func TestWatchdogPreservesHeldAndResumedContextWithoutFakeProgress(t *testing.T) {
	for _, kind := range []string{"held", "resumed", "ordinary-stale"} {
		t.Run(kind, func(t *testing.T) {
			past := time.Now().Add(-2 * time.Hour)
			ctx := &TransferContext{ID: 1, State: TransferLifecycleDownloading, StartTime: past, LastProgressTime: past}
			if kind == "held" {
				ctx.admissionHeldFiles = 1
			}
			if kind == "resumed" {
				ctx.admissionStartedAt = time.Now()
			}
			m := &Manager{dlConfig: &DownloadConfig{TransferContextTimeout: time.Minute}}
			m.coordinator = NewTransferCoordinator(m)
			m.coordinator.transfers.Store(int64(1), ctx)
			w := &Watchdog{manager: m}
			w.cleanupStaleContexts()
			if kind == "ordinary-stale" {
				if ctx.State != TransferLifecycleFailed {
					t.Fatal("ordinary stale handling changed")
				}
			} else if ctx.State != TransferLifecycleDownloading {
				t.Fatal("held/resumed work failed")
			}
			if !ctx.LastProgressTime.Equal(past) {
				t.Fatal("fabricated payload progress")
			}
		})
	}
}

func Example_admissionAllows() {
	fmt.Println(admissionAllows([]byte(`{}`), time.Now(), "boot"))
	// Output: false
}

func TestActualWorkerHoldsOwnedJobWithoutStartingPayload(t *testing.T) {
	t.Setenv("DEGRADED_ADMISSION_FILE", t.TempDir()+"/not-published.json")
	past := time.Now().Add(-time.Hour)
	ctx := &TransferContext{ID: 1, State: TransferLifecycleDownloading, StartTime: past, LastProgressTime: past}
	m := &Manager{jobs: make(chan downloadJob, 1), stopChan: make(chan struct{}), dlConfig: &DownloadConfig{TransferCheckInterval: time.Hour}}
	m.coordinator = NewTransferCoordinator(m)
	m.coordinator.transfers.Store(int64(1), ctx)
	m.activeFiles.Store(int64(2), int64(1))
	m.jobs <- downloadJob{FileID: 2, TransferID: 1, Name: "offline-fixture"}
	done := make(chan struct{})
	go func() { m.downloadWorker(); close(done) }()
	deadline := time.Now().Add(time.Second)
	for {
		ctx.mu.RLock()
		held := ctx.admissionHeldFiles
		ctx.mu.RUnlock()
		if held == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not hold the received job")
		}
		time.Sleep(time.Millisecond)
	}
	if _, owned := m.activeFiles.Load(int64(2)); !owned {
		t.Fatal("held job ownership lost")
	}
	if ctx.State != TransferLifecycleDownloading || !ctx.LastProgressTime.Equal(past) {
		t.Fatal("held job failed or invented progress")
	}
	close(m.stopChan)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("held worker failed to shut down")
	}
	if _, owned := m.activeFiles.Load(int64(2)); owned {
		t.Fatal("shutdown retained orphan ownership")
	}
	if ctx.admissionHeldFiles != 0 || !ctx.admissionStartedAt.IsZero() {
		t.Fatal("shutdown invented resume or leaked hold")
	}
}

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
			ctx.admissionHeldFiles = 1
		}
		if id == 3 {
			ctx.admissionHeldFiles = 1
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

func TestFullChannelHeldWorkerAndBlockedProducerDrainOnStop(t *testing.T) {
	t.Setenv("DEGRADED_ADMISSION_FILE", t.TempDir()+"/missing.json")
	m := &Manager{cfg: &config.Config{}, jobs: make(chan downloadJob, 1), stopChan: make(chan struct{}), dlConfig: &DownloadConfig{TransferCheckInterval: time.Hour}, running: true}
	m.coordinator = NewTransferCoordinator(m)
	m.retryQueue = NewRetryQueue(m)
	ctx := m.coordinator.InitiateTransfer(1, "shutdown-fixture", 1, 3)
	if err := m.coordinator.StartDownload(1); err != nil {
		t.Fatal(err)
	}
	m.QueueDownload(downloadJob{FileID: 1, TransferID: 1, Name: "held-fixture"})
	m.workerWg.Add(1)
	go func() { defer m.workerWg.Done(); m.downloadWorker() }()
	deadline := time.Now().Add(time.Second)
	for {
		ctx.mu.RLock()
		held := ctx.admissionHeldFiles
		ctx.mu.RUnlock()
		if held == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker not held")
		}
		time.Sleep(time.Millisecond)
	}
	m.QueueDownload(downloadJob{FileID: 2, TransferID: 1, Name: "channel-fixture"})
	producer := make(chan struct{})
	go func() {
		m.QueueDownload(downloadJob{FileID: 3, TransferID: 1, Name: "blocked-fixture"})
		close(producer)
	}()
	// Native activeFiles is marked immediately before the blocked channel send.
	for {
		if _, found := m.activeFiles.Load(int64(3)); found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("producer not blocked")
		}
		time.Sleep(time.Millisecond)
	}
	done := make(chan struct{})
	go func() { m.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop deadlocked behind held/full channel")
	}
	select {
	case <-producer:
	default:
		t.Fatal("producer remained blocked")
	}
	ctx.mu.RLock()
	pending, held, active := ctx.admissionPendingFiles, ctx.admissionHeldFiles, ctx.admissionActiveFiles
	ctx.mu.RUnlock()
	if pending != 0 || held != 0 || active != 0 {
		t.Fatalf("orphan ownership: pending=%d held=%d active=%d", pending, held, active)
	}
	for _, id := range []int64{1, 2, 3} {
		if _, found := m.activeFiles.Load(id); found {
			t.Fatal("activeFiles orphan")
		}
	}
	m.Stop() // idempotent and no send-on-closed panic on a late producer
	m.QueueDownload(downloadJob{FileID: 4, TransferID: 1, Name: "after-stop-fixture"})
}
