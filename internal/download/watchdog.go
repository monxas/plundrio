package download

import (
	"sync/atomic"
	"time"

	"github.com/elsbrock/plundrio/internal/log"
)

// Watchdog monitors the health of the download system and detects stalls
type Watchdog struct {
	manager          *Manager
	lastActivityTime atomic.Value // time.Time
	activeDownloads  atomic.Int32
	stalledWorkers   atomic.Bool
}

// NewWatchdog creates a new watchdog for the download manager
func NewWatchdog(m *Manager) *Watchdog {
	w := &Watchdog{
		manager: m,
	}
	w.lastActivityTime.Store(time.Now())
	return w
}

// Start begins the watchdog monitoring goroutines
func (w *Watchdog) Start() {
	go w.monitorWorkerPool()
	go w.monitorTransferContexts()
	go w.monitorPutioTransfers()
}

// RecordActivity updates the last activity timestamp
func (w *Watchdog) RecordActivity() {
	w.lastActivityTime.Store(time.Now())
}

// SetActiveDownloads updates the count of active downloads
func (w *Watchdog) SetActiveDownloads(count int32) {
	w.activeDownloads.Store(count)
}

// GetWorkerPoolStatus returns the current worker pool health status
func (w *Watchdog) GetWorkerPoolStatus() WorkerPoolStatus {
	lastActivity, _ := w.lastActivityTime.Load().(time.Time)

	return WorkerPoolStatus{
		TotalWorkers:    w.manager.cfg.WorkerCount,
		ActiveDownloads: int(w.activeDownloads.Load()),
		QueuedJobs:      len(w.manager.jobs),
		StallDetected:   w.stalledWorkers.Load(),
		LastActivity:    lastActivity,
	}
}

// monitorWorkerPool checks if all workers are stalled
func (w *Watchdog) monitorWorkerPool() {
	ticker := time.NewTicker(w.manager.dlConfig.WorkerStallCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-w.manager.stopChan:
			return
		case <-ticker.C:
			w.checkWorkerHealth()
		}
	}
}

// checkWorkerHealth determines if the worker pool is healthy
func (w *Watchdog) checkWorkerHealth() {
	activeDownloads := w.activeDownloads.Load()
	queuedJobs := len(w.manager.jobs)
	lastActivity, _ := w.lastActivityTime.Load().(time.Time)
	timeSinceActivity := time.Since(lastActivity)

	// Workers are stalled if:
	// 1. There are queued jobs waiting
	// 2. We have active downloads but no progress for a while
	// 3. All workers are at capacity but no progress

	isStalled := false
	reason := ""

	if queuedJobs > 0 && activeDownloads >= int32(w.manager.cfg.WorkerCount) {
		// Queue is backing up and all workers are busy
		if timeSinceActivity > w.manager.dlConfig.DownloadStallTimeout {
			isStalled = true
			reason = "all workers busy with no progress"
		}
	}

	if activeDownloads > 0 && timeSinceActivity > w.manager.dlConfig.DownloadStallTimeout*2 {
		isStalled = true
		reason = "active downloads but no progress"
	}

	previousState := w.stalledWorkers.Load()
	w.stalledWorkers.Store(isStalled)

	if isStalled && !previousState {
		log.Warn("watchdog").
			Int32("active_downloads", activeDownloads).
			Int("queued_jobs", queuedJobs).
			Dur("time_since_activity", timeSinceActivity).
			Str("reason", reason).
			Msg("Worker pool stall detected")
	} else if !isStalled && previousState {
		log.Info("watchdog").
			Int32("active_downloads", activeDownloads).
			Msg("Worker pool recovered from stall")
	}
}

// monitorTransferContexts checks for orphaned or stuck transfer contexts
func (w *Watchdog) monitorTransferContexts() {
	// Check every minute
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-w.manager.stopChan:
			return
		case <-ticker.C:
			w.cleanupStaleContexts()
		}
	}
}

// cleanupStaleContexts removes transfer contexts that have been stuck too long
func (w *Watchdog) cleanupStaleContexts() {
	timeout := w.manager.dlConfig.TransferContextTimeout
	now := time.Now()

	var staleContexts []int64

	w.manager.coordinator.transfers.Range(func(key, value interface{}) bool {
		transferID := key.(int64)
		ctx := value.(*TransferContext)

		ctx.mu.RLock()
		state := ctx.State
		startTime := ctx.StartTime
		lastProgress := ctx.LastProgressTime
		name := ctx.Name
		ctx.mu.RUnlock()

		// Only check transfers that are supposed to be downloading
		if state != TransferLifecycleDownloading {
			return true
		}

		// Use last progress time if available, otherwise start time
		checkTime := lastProgress
		if checkTime.IsZero() {
			checkTime = startTime
		}

		// If no progress for too long, mark as stale
		if !checkTime.IsZero() && now.Sub(checkTime) > timeout {
			log.Warn("watchdog").
				Int64("transfer_id", transferID).
				Str("name", name).
				Dur("stuck_duration", now.Sub(checkTime)).
				Time("last_progress", checkTime).
				Msg("Found stale transfer context")
			staleContexts = append(staleContexts, transferID)
		}

		return true
	})

	// Clean up stale contexts
	for _, transferID := range staleContexts {
		log.Warn("watchdog").
			Int64("transfer_id", transferID).
			Msg("Cleaning up stale transfer context")

		// Mark as failed so it can be retried
		w.manager.coordinator.FailTransfer(transferID,
			&DownloadError{Type: "TransferStalled", Message: "Transfer context timed out"})
	}
}

// monitorPutioTransfers checks for stuck transfers on Put.io
func (w *Watchdog) monitorPutioTransfers() {
	// Check every 5 minutes
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-w.manager.stopChan:
			return
		case <-ticker.C:
			w.checkPutioTransfers()
		}
	}
}

// checkPutioTransfers looks for transfers stuck on Put.io
func (w *Watchdog) checkPutioTransfers() {
	if w.manager.processor == nil {
		return
	}

	transfers := w.manager.processor.GetTransfers()
	timeout := w.manager.dlConfig.PutioStuckTransferTimeout
	now := time.Now()

	for _, t := range transfers {
		// Skip completed/seeding transfers
		if t.Status == "COMPLETED" || t.Status == "SEEDING" {
			continue
		}

		// Check if transfer is stuck
		isStuck, reason := w.isTransferStuck(t, now, timeout)

		if isStuck {
			log.Warn("watchdog").
				Int64("transfer_id", t.ID).
				Str("name", t.Name).
				Str("status", t.Status).
				Int("percent_done", t.PercentDone).
				Int("availability", t.Availability).
				Str("reason", reason).
				Msg("Detected stuck Put.io transfer")
		}
	}
}

// isTransferStuck determines if a Put.io transfer is stuck
// Note: This is a placeholder that always returns false - actual stuck detection
// is done in GetStuckPutioTransfers which has direct access to transfer data
func (w *Watchdog) isTransferStuck(t interface{}, now time.Time, timeout time.Duration) (bool, string) {
	return false, ""
}

// GetStuckPutioTransfers returns a list of transfers that appear stuck on Put.io
func (w *Watchdog) GetStuckPutioTransfers() []PutioTransferInfo {
	if w.manager.processor == nil {
		return nil
	}

	var stuckTransfers []PutioTransferInfo
	transfers := w.manager.processor.GetTransfers()
	timeout := w.manager.dlConfig.PutioStuckTransferTimeout
	now := time.Now()

	for _, t := range transfers {
		// Skip completed/seeding transfers
		if t.Status == "COMPLETED" || t.Status == "SEEDING" {
			continue
		}

		info := PutioTransferInfo{
			ID:           t.ID,
			Name:         t.Name,
			Status:       t.Status,
			PercentDone:  t.PercentDone,
			Availability: t.Availability,
		}

		// Convert putio.Time to time.Time if CreatedAt is available
		var createdAt time.Time
		if t.CreatedAt != nil {
			createdAt = t.CreatedAt.Time
			info.CreatedAt = createdAt
		}

		// Determine if stuck
		if t.Status == "DOWNLOADING" && !createdAt.IsZero() {
			age := now.Sub(createdAt)

			if t.Availability < 10 && age > timeout {
				info.IsStuck = true
				info.StuckReason = "low availability for extended period"
			} else if t.PercentDone == 0 && age > 1*time.Hour {
				info.IsStuck = true
				info.StuckReason = "no progress after 1 hour"
			} else if t.PercentDone < 50 && t.Availability == 0 && age > 2*time.Hour {
				info.IsStuck = true
				info.StuckReason = "no seeders and under 50%"
			}
		}

		// Also check for ERROR status
		if t.Status == "ERROR" {
			info.IsStuck = true
			info.StuckReason = "transfer in error state"
		}

		if info.IsStuck {
			stuckTransfers = append(stuckTransfers, info)
		}
	}

	return stuckTransfers
}
