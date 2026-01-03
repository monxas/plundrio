package config

import "time"

// Config holds the runtime configuration
type Config struct {
	// TargetDir is where completed downloads will be stored
	TargetDir string

	// PutioFolder is the name of the folder in Put.io
	PutioFolder string

	// FolderID is the Put.io folder ID (set after creation/lookup)
	FolderID int64

	// OAuthToken is the Put.io OAuth token
	OAuthToken string

	// ListenAddr is the address to listen for transmission-rpc requests
	ListenAddr string

	// WorkerCount is the number of concurrent download workers (default: 4)
	WorkerCount int

	// Sonarr integration
	SonarrURL    string
	SonarrAPIKey string

	// Radarr integration
	RadarrURL    string
	RadarrAPIKey string

	// Path mapping for *arr apps (container path -> local path)
	RemotePath string // Path as seen by Sonarr/Radarr (e.g., /data/downloads)
	LocalPath  string // Path as seen by Plundrio (e.g., /downloads)

	// Notifications
	NtfyURL   string // ntfy server URL (e.g., https://ntfy.sh)
	NtfyTopic string // ntfy topic name

	// Auto-cancel stuck transfers
	AutoCancelStuck     bool          // Enable auto-cancellation of stuck transfers
	AutoCancelTimeout   time.Duration // How long before auto-cancelling (default: 12h)
	AutoCancelResearch  bool          // Trigger *arr re-search after cancelling
	AutoCancelMinRetry  time.Duration // Minimum time between re-search attempts per item

	// Retry configuration
	MaxRetries       int           // Maximum retry attempts per file (default: 3)
	RetryBaseDelay   time.Duration // Base delay for exponential backoff (default: 30s)
	RetryMaxDelay    time.Duration // Maximum delay between retries (default: 30m)

	// Priority configuration
	PriorityEnabled    bool // Enable priority-based download ordering
	SmallFilePriority  bool // Prioritize smaller files first
	SequentialDownload bool // Download files in sequential order (e.g., S01E01 before S01E02)
}

// GetDefaults returns a Config with default values filled in
func GetDefaults() *Config {
	return &Config{
		WorkerCount:         4,
		AutoCancelStuck:     false, // Disabled by default, opt-in
		AutoCancelTimeout:   12 * time.Hour,
		AutoCancelResearch:  true,
		AutoCancelMinRetry:  1 * time.Hour,
		MaxRetries:          3,
		RetryBaseDelay:      30 * time.Second,
		RetryMaxDelay:       30 * time.Minute,
		PriorityEnabled:     true,
		SmallFilePriority:   true,
		SequentialDownload:  true,
	}
}

// HasSonarr returns true if Sonarr integration is configured
func (c *Config) HasSonarr() bool {
	return c.SonarrURL != "" && c.SonarrAPIKey != ""
}

// HasRadarr returns true if Radarr integration is configured
func (c *Config) HasRadarr() bool {
	return c.RadarrURL != "" && c.RadarrAPIKey != ""
}

// HasNtfy returns true if ntfy notifications are configured
func (c *Config) HasNtfy() bool {
	return c.NtfyURL != "" && c.NtfyTopic != ""
}

// HasArrIntegration returns true if any *arr integration is configured
func (c *Config) HasArrIntegration() bool {
	return c.HasSonarr() || c.HasRadarr()
}
