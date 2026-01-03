package download

import (
	"container/heap"
	"sync"
	"time"

	"github.com/elsbrock/plundrio/internal/log"
)

// RetryItem represents an item waiting to be retried
type RetryItem struct {
	Job          downloadJob
	Attempts     int
	LastAttempt  time.Time
	NextRetry    time.Time
	LastError    string
	index        int // index in the heap
}

// RetryQueue manages failed downloads with exponential backoff
type RetryQueue struct {
	mu           sync.Mutex
	items        []*RetryItem
	itemsByID    map[int64]*RetryItem // fileID -> item
	maxRetries   int
	baseDelay    time.Duration
	maxDelay     time.Duration
	manager      *Manager
	stopChan     chan struct{}
	wg           sync.WaitGroup
}

// NewRetryQueue creates a new retry queue
func NewRetryQueue(m *Manager) *RetryQueue {
	maxRetries := m.cfg.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}
	baseDelay := m.cfg.RetryBaseDelay
	if baseDelay <= 0 {
		baseDelay = 30 * time.Second
	}
	maxDelay := m.cfg.RetryMaxDelay
	if maxDelay <= 0 {
		maxDelay = 30 * time.Minute
	}

	return &RetryQueue{
		items:      make([]*RetryItem, 0),
		itemsByID:  make(map[int64]*RetryItem),
		maxRetries: maxRetries,
		baseDelay:  baseDelay,
		maxDelay:   maxDelay,
		manager:    m,
		stopChan:   make(chan struct{}),
	}
}

// Start begins the retry queue processor
func (rq *RetryQueue) Start() {
	rq.wg.Add(1)
	go rq.processLoop()
}

// Stop shuts down the retry queue
func (rq *RetryQueue) Stop() {
	close(rq.stopChan)
	rq.wg.Wait()
}

// Add adds a failed job to the retry queue
func (rq *RetryQueue) Add(job downloadJob, err error) bool {
	rq.mu.Lock()
	defer rq.mu.Unlock()

	// Check if already in queue
	if existing, ok := rq.itemsByID[job.FileID]; ok {
		existing.Attempts++
		existing.LastAttempt = time.Now()
		existing.LastError = err.Error()

		if existing.Attempts >= rq.maxRetries {
			log.Warn("retry").
				Str("file_name", job.Name).
				Int("attempts", existing.Attempts).
				Msg("Max retries reached, removing from queue")
			rq.removeItem(existing)
			return false
		}

		// Update next retry time with exponential backoff
		existing.NextRetry = rq.calculateNextRetry(existing.Attempts)
		heap.Fix(rq, existing.index)

		log.Info("retry").
			Str("file_name", job.Name).
			Int("attempt", existing.Attempts).
			Time("next_retry", existing.NextRetry).
			Msg("Updated retry time")
		return true
	}

	// Create new item
	item := &RetryItem{
		Job:         job,
		Attempts:    1,
		LastAttempt: time.Now(),
		NextRetry:   rq.calculateNextRetry(1),
		LastError:   err.Error(),
	}

	heap.Push(rq, item)
	rq.itemsByID[job.FileID] = item

	log.Info("retry").
		Str("file_name", job.Name).
		Time("next_retry", item.NextRetry).
		Msg("Added to retry queue")

	return true
}

// Remove removes a job from the retry queue (e.g., when cancelled)
func (rq *RetryQueue) Remove(fileID int64) {
	rq.mu.Lock()
	defer rq.mu.Unlock()

	if item, ok := rq.itemsByID[fileID]; ok {
		rq.removeItem(item)
	}
}

// removeItem removes an item from the queue (caller must hold lock)
func (rq *RetryQueue) removeItem(item *RetryItem) {
	heap.Remove(rq, item.index)
	delete(rq.itemsByID, item.Job.FileID)
}

// GetStats returns retry queue statistics
func (rq *RetryQueue) GetStats() (pending int, totalRetries int) {
	rq.mu.Lock()
	defer rq.mu.Unlock()

	pending = len(rq.items)
	for _, item := range rq.items {
		totalRetries += item.Attempts
	}
	return
}

// calculateNextRetry computes the next retry time with exponential backoff
func (rq *RetryQueue) calculateNextRetry(attempts int) time.Time {
	delay := rq.baseDelay * time.Duration(1<<uint(attempts-1)) // 2^(attempts-1)
	if delay > rq.maxDelay {
		delay = rq.maxDelay
	}
	return time.Now().Add(delay)
}

// processLoop continuously checks for items ready to retry
func (rq *RetryQueue) processLoop() {
	defer rq.wg.Done()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-rq.stopChan:
			return
		case <-ticker.C:
			rq.processReadyItems()
		}
	}
}

// processReadyItems retries any items that are due
func (rq *RetryQueue) processReadyItems() {
	rq.mu.Lock()
	defer rq.mu.Unlock()

	now := time.Now()

	// Process all items that are ready
	for len(rq.items) > 0 && rq.items[0].NextRetry.Before(now) {
		item := heap.Pop(rq).(*RetryItem)
		delete(rq.itemsByID, item.Job.FileID)

		log.Info("retry").
			Str("file_name", item.Job.Name).
			Int("attempt", item.Attempts+1).
			Msg("Retrying download")

		// Re-queue the job
		// Note: we unlock temporarily to avoid deadlock with manager
		rq.mu.Unlock()
		rq.manager.QueueDownload(item.Job)

		// Record metrics
		if rq.manager.metrics != nil {
			rq.manager.metrics.IncrRetries()
		}

		rq.mu.Lock()
	}
}

// Heap interface implementation

func (rq *RetryQueue) Len() int { return len(rq.items) }

func (rq *RetryQueue) Less(i, j int) bool {
	return rq.items[i].NextRetry.Before(rq.items[j].NextRetry)
}

func (rq *RetryQueue) Swap(i, j int) {
	rq.items[i], rq.items[j] = rq.items[j], rq.items[i]
	rq.items[i].index = i
	rq.items[j].index = j
}

func (rq *RetryQueue) Push(x interface{}) {
	n := len(rq.items)
	item := x.(*RetryItem)
	item.index = n
	rq.items = append(rq.items, item)
}

func (rq *RetryQueue) Pop() interface{} {
	old := rq.items
	n := len(old)
	item := old[n-1]
	old[n-1] = nil  // avoid memory leak
	item.index = -1 // for safety
	rq.items = old[0 : n-1]
	return item
}
