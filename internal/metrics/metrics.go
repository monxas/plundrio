package metrics

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Metrics holds all Prometheus metrics for Plundrio
type Metrics struct {
	mu sync.RWMutex

	// Counters
	downloadsCompletedTotal int64
	downloadsFailedTotal    int64
	downloadsCancelledTotal int64
	bytesDownloadedTotal    int64
	researchesTriggered     int64
	retriesTotal            int64

	// Gauges
	activeDownloads       int
	queuedJobs            int
	putioTransfersTotal   int
	putioTransfersStuck   int
	putioTransfersSeeding int
	putioTransfersError   int
	workerPoolStalled     bool
	uptimeSeconds         float64

	// Info
	startTime time.Time
}

// NewMetrics creates a new metrics instance
func NewMetrics() *Metrics {
	return &Metrics{
		startTime: time.Now(),
	}
}

// IncrDownloadsCompleted increments the completed downloads counter
func (m *Metrics) IncrDownloadsCompleted() {
	m.mu.Lock()
	m.downloadsCompletedTotal++
	m.mu.Unlock()
}

// IncrDownloadsFailed increments the failed downloads counter
func (m *Metrics) IncrDownloadsFailed() {
	m.mu.Lock()
	m.downloadsFailedTotal++
	m.mu.Unlock()
}

// IncrDownloadsCancelled increments the cancelled downloads counter
func (m *Metrics) IncrDownloadsCancelled() {
	m.mu.Lock()
	m.downloadsCancelledTotal++
	m.mu.Unlock()
}

// AddBytesDownloaded adds bytes to the total downloaded counter
func (m *Metrics) AddBytesDownloaded(bytes int64) {
	m.mu.Lock()
	m.bytesDownloadedTotal += bytes
	m.mu.Unlock()
}

// IncrResearchesTriggered increments the research trigger counter
func (m *Metrics) IncrResearchesTriggered() {
	m.mu.Lock()
	m.researchesTriggered++
	m.mu.Unlock()
}

// IncrRetries increments the retry counter
func (m *Metrics) IncrRetries() {
	m.mu.Lock()
	m.retriesTotal++
	m.mu.Unlock()
}

// SetActiveDownloads sets the current number of active downloads
func (m *Metrics) SetActiveDownloads(count int) {
	m.mu.Lock()
	m.activeDownloads = count
	m.mu.Unlock()
}

// SetQueuedJobs sets the current number of queued jobs
func (m *Metrics) SetQueuedJobs(count int) {
	m.mu.Lock()
	m.queuedJobs = count
	m.mu.Unlock()
}

// SetPutioTransfers sets Put.io transfer counts
func (m *Metrics) SetPutioTransfers(total, stuck, seeding, errored int) {
	m.mu.Lock()
	m.putioTransfersTotal = total
	m.putioTransfersStuck = stuck
	m.putioTransfersSeeding = seeding
	m.putioTransfersError = errored
	m.mu.Unlock()
}

// SetWorkerPoolStalled sets the worker pool stalled state
func (m *Metrics) SetWorkerPoolStalled(stalled bool) {
	m.mu.Lock()
	m.workerPoolStalled = stalled
	m.mu.Unlock()
}

// Handler returns an HTTP handler for the /metrics endpoint
func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.RLock()
		defer m.mu.RUnlock()

		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

		// Info metric
		fmt.Fprintf(w, "# HELP plundrio_info Plundrio build information\n")
		fmt.Fprintf(w, "# TYPE plundrio_info gauge\n")
		fmt.Fprintf(w, "plundrio_info{version=\"dev\"} 1\n")
		fmt.Fprintln(w)

		// Uptime
		uptime := time.Since(m.startTime).Seconds()
		fmt.Fprintf(w, "# HELP plundrio_uptime_seconds Time since Plundrio started\n")
		fmt.Fprintf(w, "# TYPE plundrio_uptime_seconds counter\n")
		fmt.Fprintf(w, "plundrio_uptime_seconds %.2f\n", uptime)
		fmt.Fprintln(w)

		// Downloads completed
		fmt.Fprintf(w, "# HELP plundrio_downloads_completed_total Total number of completed downloads\n")
		fmt.Fprintf(w, "# TYPE plundrio_downloads_completed_total counter\n")
		fmt.Fprintf(w, "plundrio_downloads_completed_total %d\n", m.downloadsCompletedTotal)
		fmt.Fprintln(w)

		// Downloads failed
		fmt.Fprintf(w, "# HELP plundrio_downloads_failed_total Total number of failed downloads\n")
		fmt.Fprintf(w, "# TYPE plundrio_downloads_failed_total counter\n")
		fmt.Fprintf(w, "plundrio_downloads_failed_total %d\n", m.downloadsFailedTotal)
		fmt.Fprintln(w)

		// Downloads cancelled
		fmt.Fprintf(w, "# HELP plundrio_downloads_cancelled_total Total number of cancelled downloads\n")
		fmt.Fprintf(w, "# TYPE plundrio_downloads_cancelled_total counter\n")
		fmt.Fprintf(w, "plundrio_downloads_cancelled_total %d\n", m.downloadsCancelledTotal)
		fmt.Fprintln(w)

		// Bytes downloaded
		fmt.Fprintf(w, "# HELP plundrio_bytes_downloaded_total Total bytes downloaded\n")
		fmt.Fprintf(w, "# TYPE plundrio_bytes_downloaded_total counter\n")
		fmt.Fprintf(w, "plundrio_bytes_downloaded_total %d\n", m.bytesDownloadedTotal)
		fmt.Fprintln(w)

		// Researches triggered
		fmt.Fprintf(w, "# HELP plundrio_researches_triggered_total Total number of *arr re-searches triggered\n")
		fmt.Fprintf(w, "# TYPE plundrio_researches_triggered_total counter\n")
		fmt.Fprintf(w, "plundrio_researches_triggered_total %d\n", m.researchesTriggered)
		fmt.Fprintln(w)

		// Retries
		fmt.Fprintf(w, "# HELP plundrio_retries_total Total number of download retries\n")
		fmt.Fprintf(w, "# TYPE plundrio_retries_total counter\n")
		fmt.Fprintf(w, "plundrio_retries_total %d\n", m.retriesTotal)
		fmt.Fprintln(w)

		// Active downloads gauge
		fmt.Fprintf(w, "# HELP plundrio_active_downloads Current number of active downloads\n")
		fmt.Fprintf(w, "# TYPE plundrio_active_downloads gauge\n")
		fmt.Fprintf(w, "plundrio_active_downloads %d\n", m.activeDownloads)
		fmt.Fprintln(w)

		// Queued jobs gauge
		fmt.Fprintf(w, "# HELP plundrio_queued_jobs Current number of queued download jobs\n")
		fmt.Fprintf(w, "# TYPE plundrio_queued_jobs gauge\n")
		fmt.Fprintf(w, "plundrio_queued_jobs %d\n", m.queuedJobs)
		fmt.Fprintln(w)

		// Put.io transfers
		fmt.Fprintf(w, "# HELP plundrio_putio_transfers Current number of Put.io transfers by status\n")
		fmt.Fprintf(w, "# TYPE plundrio_putio_transfers gauge\n")
		fmt.Fprintf(w, "plundrio_putio_transfers{status=\"total\"} %d\n", m.putioTransfersTotal)
		fmt.Fprintf(w, "plundrio_putio_transfers{status=\"stuck\"} %d\n", m.putioTransfersStuck)
		fmt.Fprintf(w, "plundrio_putio_transfers{status=\"seeding\"} %d\n", m.putioTransfersSeeding)
		fmt.Fprintf(w, "plundrio_putio_transfers{status=\"error\"} %d\n", m.putioTransfersError)
		fmt.Fprintln(w)

		// Worker pool stalled
		stalledVal := 0
		if m.workerPoolStalled {
			stalledVal = 1
		}
		fmt.Fprintf(w, "# HELP plundrio_worker_pool_stalled Whether the worker pool is stalled (1 = stalled)\n")
		fmt.Fprintf(w, "# TYPE plundrio_worker_pool_stalled gauge\n")
		fmt.Fprintf(w, "plundrio_worker_pool_stalled %d\n", stalledVal)
	})
}
