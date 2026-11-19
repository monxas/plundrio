package download

import (
	"testing"

	"github.com/elsbrock/go-putio"
	"github.com/elsbrock/plundrio/internal/config"
)

func TestProcessedTransferForgottenOnlyWhenGoneFromPutio(t *testing.T) {
	m := New(&config.Config{TargetDir: t.TempDir()}, nil)
	c := m.coordinator
	for id, state := range map[int64]TransferLifecycleState{
		1: TransferLifecycleProcessed,   // still on put.io: keep
		2: TransferLifecycleProcessed,   // gone: forget
		3: TransferLifecycleFailed,      // gone but may own downloads: keep
		4: TransferLifecycleDownloading, // gone, in flight: keep
	} {
		c.InitiateTransfer(id, "t", 0, 1).State = state
	}
	m.processor.MarkTransferProcessed(1)
	m.processor.MarkTransferProcessed(2)

	m.processor.forgetVanishedProcessed([]*putio.Transfer{{ID: 1}})

	for id, want := range map[int64]bool{1: true, 2: false, 3: true, 4: true} {
		if _, ok := c.GetTransferContext(id); ok != want {
			t.Errorf("transfer %d kept=%v, want %v", id, ok, want)
		}
	}
	if _, ok := m.processor.processedTransfers.Load(int64(2)); ok {
		t.Error("processed marker of vanished transfer kept")
	}
	if _, ok := m.processor.processedTransfers.Load(int64(1)); !ok {
		t.Error("processed marker of present transfer dropped")
	}
	if n := len(c.GetAllTransfers()); n != 3 {
		t.Errorf("health would list %d transfers, want 3", n)
	}
}
