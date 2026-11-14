package download

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"time"
)

const admissionDefaultPath = "/run/homelab-admission/state.json"

type admissionState struct {
	Schema       int     `json:"schema"`
	ObservedAt   float64 `json:"observed_at"`
	AllowNewWork bool    `json:"allow_new_work"`
	Reason       string  `json:"reason"`
	BootID       string  `json:"boot_id"`
}

func admissionAllows(data []byte, now time.Time, bootID string) bool {
	var state admissionState
	if err := json.Unmarshal(data, &state); err != nil {
		return false
	}
	age := float64(now.UnixNano())/1e9 - state.ObservedAt
	return state.Schema == 1 && !math.IsNaN(state.ObservedAt) && !math.IsInf(state.ObservedAt, 0) &&
		age >= 0 && age <= 90 && state.AllowNewWork && state.Reason != "" &&
		bootID != "" && state.BootID == bootID
}

func newLocalWorkAllowed() bool {
	path := os.Getenv("DEGRADED_ADMISSION_FILE")
	if path == "" {
		path = admissionDefaultPath
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 65536 {
		return false
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return err == nil && admissionAllows(data, time.Now(), strings.TrimSpace(string(boot)))
}

// The existing worker owns its job while held. No payload request is started,
// failed, or cancelled; waiting ends on fresh admission or the native shutdown.
func waitForAdmission(stop <-chan struct{}, interval time.Duration, allowed func() bool, hold func()) bool {
	select {
	case <-stop:
		return false
	default:
	}
	if allowed() {
		return true
	}
	hold()
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return false
		case <-ticker.C:
			if allowed() {
				return true
			}
		}
	}
}

func (m *Manager) waitForDownloadAdmission(job downloadJob) bool {
	ctx, exists := m.coordinator.GetTransferContext(job.TransferID)
	held := false
	defer func() {
		if held && exists {
			ctx.mu.Lock()
			ctx.admissionHeldFiles--
			ctx.mu.Unlock()
		}
	}()
	return waitForAdmission(m.stopChan, m.dlConfig.TransferCheckInterval, newLocalWorkAllowed, func() {
		held = true
		if exists {
			ctx.mu.Lock()
			ctx.admissionHeldFiles++
			ctx.mu.Unlock()
		}
	})
}

// Pending ownership starts when the complete file list is initialized, before
// QueueDownload can block on either its mutex or the bounded channel. Retries
// acquire the same ownership when added to RetryQueue and retain it on dequeue.
func (m *Manager) changeAdmissionPending(transferID int64, delta int) {
	if ctx, exists := m.coordinator.GetTransferContext(transferID); exists {
		ctx.mu.Lock()
		ctx.admissionPendingFiles += delta
		if ctx.admissionPendingFiles < 0 {
			ctx.admissionPendingFiles = 0
		}
		ctx.mu.Unlock()
	}
}

func (m *Manager) downloadAdmittedFile(job downloadJob, state *DownloadState) error {
	ctx, exists := m.coordinator.GetTransferContext(job.TransferID)
	if exists {
		ctx.mu.Lock()
		if ctx.admissionPendingFiles > 0 {
			ctx.admissionPendingFiles--
		}
		if ctx.admissionActiveFiles == 0 {
			// This is actual payload admission, separate from byte progress.
			ctx.admissionStartedAt = time.Now()
		}
		ctx.admissionActiveFiles++
		ctx.mu.Unlock()
		defer func() {
			ctx.mu.Lock()
			ctx.admissionActiveFiles--
			ctx.mu.Unlock()
		}()
	}
	return m.downloadWithRetry(state)
}
