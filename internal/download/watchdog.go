package download

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/elsbrock/plundrio/internal/arr"
	"github.com/elsbrock/plundrio/internal/log"
	"github.com/elsbrock/plundrio/internal/notify"
)

// Watchdog monitors the health of the download system and detects stalls
type Watchdog struct {
	manager          *Manager
	lastActivityTime atomic.Value // time.Time
	activeDownloads  atomic.Int32
	stalledWorkers   atomic.Bool

	// Auto-cancel tracking
	cancelledTransfers sync.Map // map[int64]time.Time - tracks when transfers were cancelled
	researchCooldowns  sync.Map // map[string]time.Time - tracks last research time per show/movie

	// External integrations
	arrClient *arr.Client
	notifier  *notify.Notifier
}

// NewWatchdog creates a new watchdog for the download manager
func NewWatchdog(m *Manager) *Watchdog {
	w := &Watchdog{
		manager: m,
	}
	w.lastActivityTime.Store(time.Now())

	// Initialize arr client if configured
	if m.cfg.HasArrIntegration() {
		w.arrClient = arr.NewClient(m.cfg)
	}

	// Initialize notifier if configured
	if m.cfg.HasNtfy() {
		w.notifier = notify.NewNotifier(m.cfg)
	}

	return w
}

// Start begins the watchdog monitoring goroutines
func (w *Watchdog) Start() {
	go w.monitorWorkerPool()
	go w.monitorTransferContexts()
	go w.monitorPutioTransfers()

	// Start auto-cancel monitor if enabled
	if w.manager.cfg.AutoCancelStuck {
		go w.monitorAndCancelStuckTransfers()
	}
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

	var wasStalled bool

	for {
		select {
		case <-w.manager.stopChan:
			return
		case <-ticker.C:
			w.checkWorkerHealth()

			// Send notification on state change
			isStalled := w.stalledWorkers.Load()
			if isStalled && !wasStalled && w.notifier != nil {
				status := w.GetWorkerPoolStatus()
				w.notifier.WorkerPoolStalled(status.ActiveDownloads, status.QueuedJobs)
			}
			wasStalled = isStalled
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

// monitorAndCancelStuckTransfers handles auto-cancellation and re-search
func (w *Watchdog) monitorAndCancelStuckTransfers() {
	// Check every 10 minutes
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-w.manager.stopChan:
			return
		case <-ticker.C:
			w.processStuckTransfers()
		}
	}
}

// processStuckTransfers cancels stuck transfers and triggers re-search
func (w *Watchdog) processStuckTransfers() {
	if w.manager.processor == nil {
		return
	}

	stuckTransfers := w.GetStuckPutioTransfers()
	timeout := w.manager.cfg.AutoCancelTimeout
	now := time.Now()

	for _, info := range stuckTransfers {
		// Check if this transfer has been stuck long enough to cancel
		if !info.CreatedAt.IsZero() && now.Sub(info.CreatedAt) < timeout {
			continue // Not old enough to cancel
		}

		// Check if we already cancelled this recently
		if lastCancel, ok := w.cancelledTransfers.Load(info.ID); ok {
			if now.Sub(lastCancel.(time.Time)) < 1*time.Hour {
				continue // Already cancelled recently
			}
		}

		log.Warn("watchdog").
			Int64("transfer_id", info.ID).
			Str("name", info.Name).
			Str("reason", info.StuckReason).
			Msg("Auto-cancelling stuck transfer")

		// Send notification about stuck transfer
		if w.notifier != nil {
			w.notifier.TransferStuck(info.Name, info.StuckReason, true)
		}

		// Cancel the transfer on Put.io
		if err := w.manager.client.DeleteTransfer(info.ID); err != nil {
			log.Error("watchdog").
				Int64("transfer_id", info.ID).
				Err(err).
				Msg("Failed to cancel stuck transfer")
			continue
		}

		// Track that we cancelled this
		w.cancelledTransfers.Store(info.ID, now)

		// Update metrics
		if w.manager.metrics != nil {
			w.manager.metrics.IncrDownloadsCancelled()
		}

		// Trigger re-search if enabled and arr client is configured
		researchTriggered := false
		if w.manager.cfg.AutoCancelResearch && w.arrClient != nil {
			researchTriggered = w.triggerResearch(info.Name)
		}

		// Send notification about cancellation
		if w.notifier != nil {
			w.notifier.TransferCancelled(info.Name, researchTriggered)
		}

		log.Info("watchdog").
			Int64("transfer_id", info.ID).
			Str("name", info.Name).
			Bool("research_triggered", researchTriggered).
			Msg("Stuck transfer cancelled")
	}
}

// triggerResearch triggers a re-search in Sonarr/Radarr
func (w *Watchdog) triggerResearch(name string) bool {
	// Check cooldown
	minRetry := w.manager.cfg.AutoCancelMinRetry
	if minRetry <= 0 {
		minRetry = 1 * time.Hour
	}

	// Use a normalized name as the key for cooldown tracking
	mediaType := arr.DetectMediaType(name)
	cooldownKey := name // Could be normalized further

	if lastResearch, ok := w.researchCooldowns.Load(cooldownKey); ok {
		if time.Since(lastResearch.(time.Time)) < minRetry {
			log.Debug("watchdog").
				Str("name", name).
				Msg("Research on cooldown, skipping")
			return false
		}
	}

	// Trigger the research
	_, err := w.arrClient.Research(name)
	if err != nil {
		log.Warn("watchdog").
			Str("name", name).
			Err(err).
			Msg("Failed to trigger research")
		return false
	}

	// Update cooldown
	w.researchCooldowns.Store(cooldownKey, time.Now())

	// Update metrics
	if w.manager.metrics != nil {
		w.manager.metrics.IncrResearchesTriggered()
	}

	// Notify
	if w.notifier != nil {
		w.notifier.ResearchTriggered(name, string(mediaType))
	}

	log.Info("watchdog").
		Str("name", name).
		Str("media_type", string(mediaType)).
		Msg("Triggered re-search in *arr")

	return true
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

// GetArrClient returns the arr client for external use
func (w *Watchdog) GetArrClient() *arr.Client {
	return w.arrClient
}

// GetNotifier returns the notifier for external use
func (w *Watchdog) GetNotifier() *notify.Notifier {
	return w.notifier
}
