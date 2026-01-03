package download

import (
	"fmt"
)

// DownloadError is the base error type for download-related errors
type DownloadError struct {
	Type    string
	Message string
}

// Error implements the error interface
func (e *DownloadError) Error() string {
	return fmt.Sprintf("%s: %s", e.Type, e.Message)
}

// NewDownloadCancelledError creates a new error for cancelled downloads
func NewDownloadCancelledError(filename, reason string) error {
	return &DownloadError{
		Type:    "DownloadCancelled",
		Message: fmt.Sprintf("Download of %s was cancelled: %s", filename, reason),
	}
}

// NewTransferNotFoundError creates a new error for transfer not found situations
func NewTransferNotFoundError(transferID int64) error {
	return &DownloadError{
		Type:    "TransferNotFound",
		Message: fmt.Sprintf("Transfer ID %d not found", transferID),
	}
}

// NewNoFilesFoundError creates a new error for transfers with no files
func NewNoFilesFoundError(transferID int64) error {
	return &DownloadError{
		Type:    "NoFilesFound",
		Message: fmt.Sprintf("No files found for transfer %d", transferID),
	}
}

// NewDownloadStalledError creates a new error for stalled downloads
// This is a transient error that should trigger a retry
func NewDownloadStalledError(filename string, stallDuration string) error {
	return &DownloadError{
		Type:    "DownloadStalled",
		Message: fmt.Sprintf("Download of %s stalled for %s", filename, stallDuration),
	}
}
