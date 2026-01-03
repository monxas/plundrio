package notify

import (
	"bytes"
	"fmt"
	"net/http"
	"time"

	"github.com/elsbrock/plundrio/internal/config"
	"github.com/elsbrock/plundrio/internal/log"
)

// Priority levels for notifications
type Priority string

const (
	PriorityMin     Priority = "min"
	PriorityLow     Priority = "low"
	PriorityDefault Priority = "default"
	PriorityHigh    Priority = "high"
	PriorityMax     Priority = "max"
)

// EventType represents the type of event being notified
type EventType string

const (
	EventTransferComplete   EventType = "transfer_complete"
	EventTransferFailed     EventType = "transfer_failed"
	EventTransferStuck      EventType = "transfer_stuck"
	EventTransferCancelled  EventType = "transfer_cancelled"
	EventWorkerPoolStalled  EventType = "worker_pool_stalled"
	EventResearchTriggered  EventType = "research_triggered"
	EventImportComplete     EventType = "import_complete"
	EventDiskSpaceLow       EventType = "disk_space_low"
)

// Notifier handles sending notifications
type Notifier struct {
	cfg        *config.Config
	httpClient *http.Client
}

// NewNotifier creates a new notification handler
func NewNotifier(cfg *config.Config) *Notifier {
	return &Notifier{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Enabled returns true if notifications are configured
func (n *Notifier) Enabled() bool {
	return n.cfg.HasNtfy()
}

// Notification represents a notification to be sent
type Notification struct {
	Title    string
	Message  string
	Priority Priority
	Tags     []string
	Event    EventType
}

// Send sends a notification via ntfy
func (n *Notifier) Send(notif Notification) error {
	if !n.Enabled() {
		return nil
	}

	url := fmt.Sprintf("%s/%s", n.cfg.NtfyURL, n.cfg.NtfyTopic)

	req, err := http.NewRequest("POST", url, bytes.NewBufferString(notif.Message))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Title", notif.Title)
	req.Header.Set("Priority", string(notif.Priority))

	// Set tags based on event type and custom tags
	tags := n.getTagsForEvent(notif.Event)
	tags = append(tags, notif.Tags...)
	if len(tags) > 0 {
		tagStr := ""
		for i, tag := range tags {
			if i > 0 {
				tagStr += ","
			}
			tagStr += tag
		}
		req.Header.Set("Tags", tagStr)
	}

	resp, err := n.httpClient.Do(req)
	if err != nil {
		log.Error("notify").
			Err(err).
			Str("title", notif.Title).
			Msg("Failed to send notification")
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy returned status %d", resp.StatusCode)
	}

	log.Debug("notify").
		Str("title", notif.Title).
		Str("event", string(notif.Event)).
		Msg("Notification sent")

	return nil
}

// getTagsForEvent returns appropriate emoji tags for an event type
func (n *Notifier) getTagsForEvent(event EventType) []string {
	switch event {
	case EventTransferComplete:
		return []string{"white_check_mark", "package"}
	case EventTransferFailed:
		return []string{"x", "warning"}
	case EventTransferStuck:
		return []string{"hourglass", "warning"}
	case EventTransferCancelled:
		return []string{"no_entry", "arrows_counterclockwise"}
	case EventWorkerPoolStalled:
		return []string{"rotating_light", "construction"}
	case EventResearchTriggered:
		return []string{"mag", "arrows_counterclockwise"}
	case EventImportComplete:
		return []string{"tada", "clapper"}
	case EventDiskSpaceLow:
		return []string{"warning", "floppy_disk"}
	default:
		return []string{"bell"}
	}
}

// Helper methods for common notifications

// TransferComplete sends a notification for a completed transfer
func (n *Notifier) TransferComplete(name string, sizeMB float64) {
	n.Send(Notification{
		Title:    "Download Complete",
		Message:  fmt.Sprintf("%s (%.1f MB)", name, sizeMB),
		Priority: PriorityDefault,
		Event:    EventTransferComplete,
	})
}

// TransferFailed sends a notification for a failed transfer
func (n *Notifier) TransferFailed(name string, reason string) {
	n.Send(Notification{
		Title:    "Download Failed",
		Message:  fmt.Sprintf("%s\nReason: %s", name, reason),
		Priority: PriorityHigh,
		Event:    EventTransferFailed,
	})
}

// TransferStuck sends a notification for a stuck transfer
func (n *Notifier) TransferStuck(name string, reason string, willCancel bool) {
	msg := fmt.Sprintf("%s\nReason: %s", name, reason)
	if willCancel {
		msg += "\nAuto-cancelling and re-searching..."
	}
	n.Send(Notification{
		Title:    "Transfer Stuck on Put.io",
		Message:  msg,
		Priority: PriorityHigh,
		Event:    EventTransferStuck,
	})
}

// TransferCancelled sends a notification for a cancelled transfer with re-search
func (n *Notifier) TransferCancelled(name string, researchTriggered bool) {
	msg := fmt.Sprintf("Cancelled: %s", name)
	if researchTriggered {
		msg += "\nNew search triggered in Sonarr/Radarr"
	}
	n.Send(Notification{
		Title:    "Transfer Auto-Cancelled",
		Message:  msg,
		Priority: PriorityDefault,
		Event:    EventTransferCancelled,
	})
}

// WorkerPoolStalled sends a notification when the worker pool is stalled
func (n *Notifier) WorkerPoolStalled(activeDownloads int, queuedJobs int) {
	n.Send(Notification{
		Title:    "Worker Pool Stalled",
		Message:  fmt.Sprintf("Active: %d, Queued: %d\nDownloads may be stuck", activeDownloads, queuedJobs),
		Priority: PriorityMax,
		Event:    EventWorkerPoolStalled,
	})
}

// ResearchTriggered sends a notification when a re-search is triggered
func (n *Notifier) ResearchTriggered(name string, mediaType string) {
	n.Send(Notification{
		Title:    "Re-Search Triggered",
		Message:  fmt.Sprintf("%s (%s)\nSearching for alternative release...", name, mediaType),
		Priority: PriorityLow,
		Event:    EventResearchTriggered,
	})
}

// DiskSpaceLow sends a notification when Put.io disk space is low
func (n *Notifier) DiskSpaceLow(usedPercent float64, availableMB int64) {
	n.Send(Notification{
		Title:    "Put.io Disk Space Low",
		Message:  fmt.Sprintf("%.1f%% used, %d MB available", usedPercent, availableMB),
		Priority: PriorityHigh,
		Event:    EventDiskSpaceLow,
	})
}
