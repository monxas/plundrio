package download

import (
	"context"
	"fmt"
)

func (m *Manager) validateLocalFile(transferID, fileID int64, path string) error {
	if m.validation == nil {
		return nil
	}
	if transfer, ok := m.coordinator.GetTransferContext(transferID); ok {
		transfer.mu.Lock()
		transfer.validationActive++
		transfer.mu.Unlock()
		defer func() { transfer.mu.Lock(); transfer.validationActive--; transfer.mu.Unlock() }()
	}
	result := m.validation.Check(context.Background(), path)
	if err := m.validation.Track(transferID, fileID, path, result); err != nil {
		return &DownloadError{Type: "MediaValidationPending", Message: "Validation journal unavailable"}
	}
	if !result.Accepted() {
		return &DownloadError{Type: "MediaValidationHeld", Message: fmt.Sprintf("%s: %s", result.Status, result.Reason)}
	}
	return nil
}

// MediaReady is deliberately a journal read: RPC never starts a probe or download.
func (m *Manager) MediaReady(transferID int64) bool {
	return m.validation == nil || m.validation.Ready(transferID)
}

// MediaSuperseded hides an explicitly reconciled legacy request without claiming completion.
func (m *Manager) MediaSuperseded(id int64) bool {
	return m.validation != nil && m.validation.NativeState(id) == "requested_episode_superseded"
}
