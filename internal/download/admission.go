package download

import "time"

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
