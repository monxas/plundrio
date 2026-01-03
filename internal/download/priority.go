package download

import (
	"container/heap"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/elsbrock/go-putio"
	"github.com/elsbrock/plundrio/internal/log"
)

// Priority levels for downloads
type DownloadPriority int

const (
	PriorityLow    DownloadPriority = 0
	PriorityNormal DownloadPriority = 50
	PriorityHigh   DownloadPriority = 100
)

// PriorityJob extends downloadJob with priority information
type PriorityJob struct {
	downloadJob
	Priority    DownloadPriority
	Size        int64  // File size for ordering
	SeqNumber   int    // Sequential number for episode ordering
	index       int    // heap index
}

// PriorityQueue manages download jobs with priority ordering
type PriorityQueue struct {
	mu              sync.Mutex
	items           []*PriorityJob
	smallFilesFirst bool
	sequential      bool
}

// NewPriorityQueue creates a new priority queue
func NewPriorityQueue(smallFilesFirst, sequential bool) *PriorityQueue {
	pq := &PriorityQueue{
		items:           make([]*PriorityJob, 0),
		smallFilesFirst: smallFilesFirst,
		sequential:      sequential,
	}
	heap.Init(pq)
	return pq
}

// Push adds a job to the queue
func (pq *PriorityQueue) Push(x interface{}) {
	n := len(pq.items)
	item := x.(*PriorityJob)
	item.index = n
	pq.items = append(pq.items, item)
}

// Pop removes and returns the highest priority job
func (pq *PriorityQueue) Pop() interface{} {
	old := pq.items
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	item.index = -1
	pq.items = old[0 : n-1]
	return item
}

// Len returns the queue length
func (pq *PriorityQueue) Len() int { return len(pq.items) }

// Less determines job ordering
func (pq *PriorityQueue) Less(i, j int) bool {
	a, b := pq.items[i], pq.items[j]

	// First compare by priority (higher is better)
	if a.Priority != b.Priority {
		return a.Priority > b.Priority
	}

	// For same priority, use sequential ordering if enabled
	if pq.sequential && a.SeqNumber != b.SeqNumber {
		return a.SeqNumber < b.SeqNumber
	}

	// Then by size (smaller first if enabled)
	if pq.smallFilesFirst && a.Size != b.Size {
		return a.Size < b.Size
	}

	return false
}

// Swap swaps two items
func (pq *PriorityQueue) Swap(i, j int) {
	pq.items[i], pq.items[j] = pq.items[j], pq.items[i]
	pq.items[i].index = i
	pq.items[j].index = j
}

// Add adds a job with computed priority
func (pq *PriorityQueue) Add(job downloadJob, size int64, priority DownloadPriority) {
	pq.mu.Lock()
	defer pq.mu.Unlock()

	pJob := &PriorityJob{
		downloadJob: job,
		Priority:    priority,
		Size:        size,
		SeqNumber:   extractSequenceNumber(job.Name),
	}

	heap.Push(pq, pJob)

	log.Debug("priority").
		Str("file_name", job.Name).
		Int("priority", int(priority)).
		Int64("size", size).
		Int("seq_number", pJob.SeqNumber).
		Msg("Added job to priority queue")
}

// GetNext returns and removes the highest priority job
func (pq *PriorityQueue) GetNext() (downloadJob, bool) {
	pq.mu.Lock()
	defer pq.mu.Unlock()

	if len(pq.items) == 0 {
		return downloadJob{}, false
	}

	item := heap.Pop(pq).(*PriorityJob)
	return item.downloadJob, true
}

// Peek returns the next job without removing it
func (pq *PriorityQueue) Peek() (downloadJob, bool) {
	pq.mu.Lock()
	defer pq.mu.Unlock()

	if len(pq.items) == 0 {
		return downloadJob{}, false
	}

	return pq.items[0].downloadJob, true
}

// extractSequenceNumber extracts episode/sequence number from filename
// Returns 0 if no sequence can be determined
func extractSequenceNumber(name string) int {
	nameLower := strings.ToLower(name)

	// Try S01E02 format
	re := regexp.MustCompile(`s(\d+)e(\d+)`)
	if matches := re.FindStringSubmatch(nameLower); len(matches) == 3 {
		season, _ := strconv.Atoi(matches[1])
		episode, _ := strconv.Atoi(matches[2])
		return season*1000 + episode // e.g., S01E05 = 1005
	}

	// Try 1x02 format
	re = regexp.MustCompile(`(\d+)x(\d+)`)
	if matches := re.FindStringSubmatch(nameLower); len(matches) == 3 {
		season, _ := strconv.Atoi(matches[1])
		episode, _ := strconv.Atoi(matches[2])
		return season*1000 + episode
	}

	// Try Episode 1, E01 format
	re = regexp.MustCompile(`(?:episode|ep?)\s*(\d+)`)
	if matches := re.FindStringSubmatch(nameLower); len(matches) == 2 {
		episode, _ := strconv.Atoi(matches[1])
		return episode
	}

	// Try just a number at the end (like "Part 1", "CD1")
	re = regexp.MustCompile(`(?:part|cd|disc|disk)\s*(\d+)`)
	if matches := re.FindStringSubmatch(nameLower); len(matches) == 2 {
		num, _ := strconv.Atoi(matches[1])
		return num
	}

	return 0
}

// SortedFile represents a file with computed priority information
type SortedFile struct {
	Index    int
	Name     string
	Size     int64
	SeqNum   int
	Priority DownloadPriority
}

// FileInfo holds info needed for sorting
type FileInfo struct {
	index    int
	name     string
	size     int64
	seqNum   int
	priority DownloadPriority
}

// SortFiles sorts putio.File pointers for optimal download order
// Accepts files with Name and Size fields
func SortFiles[T interface{ GetName() string; GetSize() int64 }](files []T, smallFilesFirst, sequential bool) []T {
	if len(files) == 0 {
		return files
	}

	// Build sorting info
	infos := make([]FileInfo, len(files))
	for i, f := range files {
		name := f.GetName()
		infos[i] = FileInfo{
			index:    i,
			name:     name,
			size:     f.GetSize(),
			seqNum:   extractSequenceNumber(name),
			priority: GetFilePriority(name),
		}
	}

	// Sort infos
	sort.Slice(infos, func(i, j int) bool {
		a, b := infos[i], infos[j]

		// Priority first
		if a.priority != b.priority {
			return a.priority > b.priority
		}

		// Sequential ordering
		if sequential && a.seqNum != b.seqNum && a.seqNum > 0 && b.seqNum > 0 {
			return a.seqNum < b.seqNum
		}

		// Size ordering
		if smallFilesFirst {
			return a.size < b.size
		}

		return false
	})

	// Reorder files based on sorted infos
	result := make([]T, len(files))
	for i, info := range infos {
		result[i] = files[info.index]
	}

	return result
}

// SortPutioFiles sorts a slice of putio.File pointers for optimal download order
// This is a concrete implementation that works with putio.File directly
func SortPutioFiles(files []*putio.File, smallFilesFirst, sequential bool) []*putio.File {
	if len(files) == 0 {
		return files
	}

	// Build sorting info
	infos := make([]FileInfo, len(files))
	for i, f := range files {
		infos[i] = FileInfo{
			index:    i,
			name:     f.Name,
			size:     f.Size,
			seqNum:   extractSequenceNumber(f.Name),
			priority: GetFilePriority(f.Name),
		}
	}

	// Sort infos
	sort.Slice(infos, func(i, j int) bool {
		a, b := infos[i], infos[j]

		// Priority first
		if a.priority != b.priority {
			return a.priority > b.priority
		}

		// Sequential ordering
		if sequential && a.seqNum != b.seqNum && a.seqNum > 0 && b.seqNum > 0 {
			return a.seqNum < b.seqNum
		}

		// Size ordering
		if smallFilesFirst {
			return a.size < b.size
		}

		return false
	})

	// Reorder files based on sorted infos
	result := make([]*putio.File, len(files))
	for i, info := range infos {
		result[i] = files[info.index]
	}

	return result
}

// IsVideoFile checks if a filename is likely a video file
func IsVideoFile(name string) bool {
	lower := strings.ToLower(name)
	videoExts := []string{".mkv", ".mp4", ".avi", ".mov", ".wmv", ".flv", ".webm", ".m4v", ".ts", ".m2ts"}
	for _, ext := range videoExts {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// IsSampleFile checks if a filename is likely a sample file
func IsSampleFile(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "sample") && IsVideoFile(name)
}

// IsExtraFile checks if a filename is likely an extra/bonus file
func IsExtraFile(name string) bool {
	lower := strings.ToLower(name)
	extras := []string{".nfo", ".txt", ".srt", ".sub", ".idx", ".jpg", ".jpeg", ".png", ".sfv"}
	for _, ext := range extras {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	// Also check for sample videos
	return IsSampleFile(name)
}

// GetFilePriority determines the priority for a file based on its name
func GetFilePriority(name string) DownloadPriority {
	if IsSampleFile(name) {
		return PriorityLow // Download samples last
	}
	if IsExtraFile(name) {
		return PriorityLow // Download extras last
	}
	if IsVideoFile(name) {
		return PriorityHigh // Download main video files first
	}
	return PriorityNormal
}
