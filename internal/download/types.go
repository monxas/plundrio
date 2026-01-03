package download

import (
	"sync"
	"time"
)

// downloadJob represents a single download task
type downloadJob struct {
	FileID     int64
	Name       string
	IsFolder   bool
	TransferID int64 // Parent transfer ID for group tracking
}

// DownloadState tracks the progress of a file download
type DownloadState struct {
	TransferID   int64
	FileID       int64
	Name         string
	Progress     float64
	ETA          time.Time
	LastProgress time.Time
	StartTime    time.Time

	// Mutex to protect access to downloaded bytes counter
	mu         sync.Mutex
	downloaded int64
}

// TransferLifecycleState represents the possible states of a transfer
type TransferLifecycleState int32

const (
	TransferLifecycleInitial TransferLifecycleState = iota
	TransferLifecycleDownloading
	TransferLifecycleCompleted
	TransferLifecycleFailed
	TransferLifecycleCancelled
	TransferLifecycleProcessed // Added: Transfer has been processed locally and can be shown as 100% complete
)

// String returns a string representation of the transfer state
func (s TransferLifecycleState) String() string {
	switch s {
	case TransferLifecycleInitial:
		return "Initial"
	case TransferLifecycleDownloading:
		return "Downloading"
	case TransferLifecycleCompleted:
		return "Completed"
	case TransferLifecycleFailed:
		return "Failed"
	case TransferLifecycleCancelled:
		return "Cancelled"
	case TransferLifecycleProcessed:
		return "Processed"
	default:
		return "Unknown"
	}
}

// TransferContext tracks the complete state of a transfer
type TransferContext struct {
	ID               int64
	Name             string
	FileID           int64
	TotalFiles       int32
	CompletedFiles   int32
	FailedFiles      int32 // Track number of failed files
	TotalSize        int64 // Total size of all files in bytes
	DownloadedSize   int64 // Total downloaded bytes
	State            TransferLifecycleState
	Error            error
	StartTime        time.Time // When the transfer started downloading
	LastProgressTime time.Time // Last time any progress was made (for watchdog)
	mu               sync.RWMutex
}

// TransferSnapshot is a read-only snapshot of transfer state for health monitoring
type TransferSnapshot struct {
	ID               int64
	Name             string
	State            TransferLifecycleState
	TotalFiles       int32
	CompletedFiles   int32
	FailedFiles      int32
	TotalSize        int64
	DownloadedSize   int64
	StartTime        time.Time
	LastProgressTime time.Time
}

// GetSnapshot returns a thread-safe snapshot of the transfer state
func (tc *TransferContext) GetSnapshot() TransferSnapshot {
	tc.mu.RLock()
	defer tc.mu.RUnlock()
	return TransferSnapshot{
		ID:               tc.ID,
		Name:             tc.Name,
		State:            tc.State,
		TotalFiles:       tc.TotalFiles,
		CompletedFiles:   tc.CompletedFiles,
		FailedFiles:      tc.FailedFiles,
		TotalSize:        tc.TotalSize,
		DownloadedSize:   tc.DownloadedSize,
		StartTime:        tc.StartTime,
		LastProgressTime: tc.LastProgressTime,
	}
}

// PutioTransferInfo holds information about a Put.io transfer for health monitoring
type PutioTransferInfo struct {
	ID           int64
	Name         string
	Status       string
	PercentDone  int
	Availability int
	CreatedAt    time.Time
	IsStuck      bool   // True if transfer appears stuck
	StuckReason  string // Reason why it's considered stuck
}

// WorkerPoolStatus tracks the health of download workers
type WorkerPoolStatus struct {
	TotalWorkers    int
	ActiveDownloads int
	QueuedJobs      int
	StallDetected   bool
	LastActivity    time.Time
}
