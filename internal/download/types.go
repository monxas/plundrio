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
	ID                    int64
	Name                  string // local root folder (relative to TargetDir)
	Hash                  string // BitTorrent info-hash, lowercase ("" if unknown)
	PutioName             string // Put.io's own transfer name
	FileID                int64
	TotalFiles            int32
	CompletedFiles        int32
	FailedFiles           int32           // Track number of failed files
	TotalSize             int64           // Total size of all files in bytes
	DownloadedSize        int64           // Total downloaded bytes (maintained by CreditFile)
	creditedBytes         map[int64]int64 // fileID -> bytes already counted, protected by mu
	State                 TransferLifecycleState
	Error                 error
	StartTime             time.Time // When the transfer started downloading
	LastProgressTime      time.Time // Last time any progress was made (for watchdog)
	admissionPendingFiles int       // all pending initial/retry files, including channel backlog
	admissionActiveFiles  int       // files whose payload attempt has actually started
	admissionHeldFiles    int       // intentional local admission holds, protected by mu
	admissionStartedAt    time.Time // watchdog clock when payload starts from idle pending state
	mu                    sync.RWMutex
}

// TransferSnapshot is a read-only snapshot of transfer state for health monitoring
type TransferSnapshot struct {
	ID               int64
	Name             string
	Hash             string
	PutioName        string
	State            TransferLifecycleState
	TotalFiles       int32
	CompletedFiles   int32
	FailedFiles      int32
	TotalSize        int64
	DownloadedSize   int64
	StartTime        time.Time
	LastProgressTime time.Time
}

// CreditFile records that fileID now has `bytes` bytes on disk (an absolute
// value, not a delta) and adds only the increase to DownloadedSize.
//
// Progress ticks, the completion handler, resumed retries and the
// "already exists" path all report absolute sizes, so crediting is
// idempotent: a file can never be counted twice. (Before, every completed
// file was added once by progress deltas and again in full on completion,
// so a 2.2 GB pack reported 3.7 GB downloaded.)
func (tc *TransferContext) CreditFile(fileID, bytes int64) int64 {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if tc.creditedBytes == nil {
		tc.creditedBytes = make(map[int64]int64)
	}
	delta := bytes - tc.creditedBytes[fileID]
	if delta <= 0 {
		return 0
	}
	tc.creditedBytes[fileID] = bytes
	tc.DownloadedSize += delta
	tc.LastProgressTime = time.Now()
	return delta
}

// GetSnapshot returns a thread-safe snapshot of the transfer state
func (tc *TransferContext) GetSnapshot() TransferSnapshot {
	tc.mu.RLock()
	defer tc.mu.RUnlock()
	return TransferSnapshot{
		ID:               tc.ID,
		Name:             tc.Name,
		Hash:             tc.Hash,
		PutioName:        tc.PutioName,
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
