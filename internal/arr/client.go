package arr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/elsbrock/plundrio/internal/config"
	"github.com/elsbrock/plundrio/internal/log"
)

// Client handles communication with Sonarr and Radarr APIs
type Client struct {
	cfg        *config.Config
	httpClient *http.Client
}

// NewClient creates a new *arr API client
func NewClient(cfg *config.Config) *Client {
	return &Client{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// MediaType represents the type of media (TV or Movie)
type MediaType string

const (
	MediaTypeTV    MediaType = "tv"
	MediaTypeMovie MediaType = "movie"
)

// DetectMediaType determines if a name is TV or movie based on patterns
func DetectMediaType(name string) MediaType {
	// TV patterns: S01E01, S01, 1x01, Season 1, Episode 1
	tvPatterns := []string{
		`(?i)[Ss]\d{1,2}[Ee]\d{1,2}`,      // S01E01
		`(?i)[Ss]\d{1,2}(?![Ee])`,          // S01 (season pack)
		`(?i)\d{1,2}x\d{1,2}`,              // 1x01
		`(?i)season\s*\d+`,                 // Season 1
		`(?i)episode\s*\d+`,                // Episode 1
		`(?i)complete\s*series`,            // Complete Series
	}

	for _, pattern := range tvPatterns {
		if matched, _ := regexp.MatchString(pattern, name); matched {
			return MediaTypeTV
		}
	}

	return MediaTypeMovie
}

// HistoryRecord represents a history entry from Sonarr/Radarr
type HistoryRecord struct {
	ID          int       `json:"id"`
	EventType   string    `json:"eventType"`
	SourceTitle string    `json:"sourceTitle"`
	Date        time.Time `json:"date"`
	Data        struct {
		DroppedPath   string `json:"droppedPath"`
		ImportedPath  string `json:"importedPath"`
		DownloadID    string `json:"downloadId"`
		Indexer       string `json:"indexer"`
		ReleaseGroup  string `json:"releaseGroup"`
	} `json:"data"`
}

// QueueRecord represents a queue entry from Sonarr/Radarr
type QueueRecord struct {
	ID                int     `json:"id"`
	Title             string  `json:"title"`
	Status            string  `json:"status"`
	TrackedDownloadStatus string `json:"trackedDownloadStatus"`
	TrackedDownloadState  string `json:"trackedDownloadState"`
	DownloadID        string  `json:"downloadId"`
	Protocol          string  `json:"protocol"`
	Indexer           string  `json:"indexer"`
	ErrorMessage      string  `json:"errorMessage"`
	// For Sonarr
	SeriesID          int     `json:"seriesId"`
	EpisodeID         int     `json:"episodeId"`
	SeasonNumber      int     `json:"seasonNumber"`
	// For Radarr
	MovieID           int     `json:"movieId"`
}

// doRequest performs an HTTP request to an *arr API
func (c *Client) doRequest(method, url, apiKey string, body interface{}) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("X-Api-Key", apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

// TriggerDownloadedScan triggers a scan for downloaded media
func (c *Client) TriggerDownloadedScan(name string, mediaType MediaType) error {
	// Map local path to remote path if configured
	path := c.cfg.TargetDir + "/" + name
	if c.cfg.RemotePath != "" && c.cfg.LocalPath != "" {
		path = strings.Replace(path, c.cfg.LocalPath, c.cfg.RemotePath, 1)
	}

	var url, apiKey, commandName string
	if mediaType == MediaTypeTV {
		if !c.cfg.HasSonarr() {
			return fmt.Errorf("sonarr not configured")
		}
		url = c.cfg.SonarrURL + "/api/v3/command"
		apiKey = c.cfg.SonarrAPIKey
		commandName = "DownloadedEpisodesScan"
	} else {
		if !c.cfg.HasRadarr() {
			return fmt.Errorf("radarr not configured")
		}
		url = c.cfg.RadarrURL + "/api/v3/command"
		apiKey = c.cfg.RadarrAPIKey
		commandName = "DownloadedMoviesScan"
	}

	body := map[string]interface{}{
		"name":       commandName,
		"path":       path,
		"importMode": "Auto",
	}

	log.Info("arr").
		Str("command", commandName).
		Str("path", path).
		Str("media_type", string(mediaType)).
		Msg("Triggering download scan")

	_, err := c.doRequest("POST", url, apiKey, body)
	return err
}

// ResearchSeries triggers a search for a series in Sonarr
// Returns the command ID if successful
func (c *Client) ResearchSeries(name string) (int, error) {
	if !c.cfg.HasSonarr() {
		return 0, fmt.Errorf("sonarr not configured")
	}

	// First, find the series ID from the queue or lookup
	seriesID, err := c.findSeriesID(name)
	if err != nil {
		return 0, fmt.Errorf("failed to find series: %w", err)
	}

	// Trigger series search
	url := c.cfg.SonarrURL + "/api/v3/command"
	body := map[string]interface{}{
		"name":     "SeriesSearch",
		"seriesId": seriesID,
	}

	log.Info("arr").
		Str("name", name).
		Int("series_id", seriesID).
		Msg("Triggering series re-search")

	respBody, err := c.doRequest("POST", url, c.cfg.SonarrAPIKey, body)
	if err != nil {
		return 0, err
	}

	var result struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return 0, fmt.Errorf("failed to parse response: %w", err)
	}

	return result.ID, nil
}

// ResearchMovie triggers a search for a movie in Radarr
// Returns the command ID if successful
func (c *Client) ResearchMovie(name string) (int, error) {
	if !c.cfg.HasRadarr() {
		return 0, fmt.Errorf("radarr not configured")
	}

	// First, find the movie ID from the queue or lookup
	movieID, err := c.findMovieID(name)
	if err != nil {
		return 0, fmt.Errorf("failed to find movie: %w", err)
	}

	// Trigger movie search
	url := c.cfg.RadarrURL + "/api/v3/command"
	body := map[string]interface{}{
		"name":     "MoviesSearch",
		"movieIds": []int{movieID},
	}

	log.Info("arr").
		Str("name", name).
		Int("movie_id", movieID).
		Msg("Triggering movie re-search")

	respBody, err := c.doRequest("POST", url, c.cfg.RadarrAPIKey, body)
	if err != nil {
		return 0, err
	}

	var result struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return 0, fmt.Errorf("failed to parse response: %w", err)
	}

	return result.ID, nil
}

// Research triggers a re-search for the appropriate media type
func (c *Client) Research(name string) (int, error) {
	mediaType := DetectMediaType(name)
	if mediaType == MediaTypeTV {
		return c.ResearchSeries(name)
	}
	return c.ResearchMovie(name)
}

// findSeriesID finds a series ID in Sonarr by name
func (c *Client) findSeriesID(name string) (int, error) {
	// First check the queue for a matching entry
	queue, err := c.GetSonarrQueue()
	if err == nil {
		for _, q := range queue {
			if strings.Contains(strings.ToLower(q.Title), extractShowName(name)) {
				return q.SeriesID, nil
			}
		}
	}

	// If not in queue, search the series list
	url := c.cfg.SonarrURL + "/api/v3/series"
	respBody, err := c.doRequest("GET", url, c.cfg.SonarrAPIKey, nil)
	if err != nil {
		return 0, err
	}

	var series []struct {
		ID    int    `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(respBody, &series); err != nil {
		return 0, fmt.Errorf("failed to parse series: %w", err)
	}

	showName := extractShowName(name)
	for _, s := range series {
		if strings.Contains(strings.ToLower(s.Title), showName) {
			return s.ID, nil
		}
	}

	return 0, fmt.Errorf("series not found: %s", name)
}

// findMovieID finds a movie ID in Radarr by name
func (c *Client) findMovieID(name string) (int, error) {
	// First check the queue for a matching entry
	queue, err := c.GetRadarrQueue()
	if err == nil {
		for _, q := range queue {
			if strings.Contains(strings.ToLower(q.Title), extractMovieName(name)) {
				return q.MovieID, nil
			}
		}
	}

	// If not in queue, search the movie list
	url := c.cfg.RadarrURL + "/api/v3/movie"
	respBody, err := c.doRequest("GET", url, c.cfg.RadarrAPIKey, nil)
	if err != nil {
		return 0, err
	}

	var movies []struct {
		ID    int    `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(respBody, &movies); err != nil {
		return 0, fmt.Errorf("failed to parse movies: %w", err)
	}

	movieName := extractMovieName(name)
	for _, m := range movies {
		if strings.Contains(strings.ToLower(m.Title), movieName) {
			return m.ID, nil
		}
	}

	return 0, fmt.Errorf("movie not found: %s", name)
}

// GetSonarrQueue returns the current Sonarr download queue
func (c *Client) GetSonarrQueue() ([]QueueRecord, error) {
	if !c.cfg.HasSonarr() {
		return nil, fmt.Errorf("sonarr not configured")
	}

	url := c.cfg.SonarrURL + "/api/v3/queue?pageSize=100"
	respBody, err := c.doRequest("GET", url, c.cfg.SonarrAPIKey, nil)
	if err != nil {
		return nil, err
	}

	var result struct {
		Records []QueueRecord `json:"records"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse queue: %w", err)
	}

	return result.Records, nil
}

// GetRadarrQueue returns the current Radarr download queue
func (c *Client) GetRadarrQueue() ([]QueueRecord, error) {
	if !c.cfg.HasRadarr() {
		return nil, fmt.Errorf("radarr not configured")
	}

	url := c.cfg.RadarrURL + "/api/v3/queue?pageSize=100"
	respBody, err := c.doRequest("GET", url, c.cfg.RadarrAPIKey, nil)
	if err != nil {
		return nil, err
	}

	var result struct {
		Records []QueueRecord `json:"records"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse queue: %w", err)
	}

	return result.Records, nil
}

// RemoveFromQueue removes an item from the *arr queue
func (c *Client) RemoveFromQueue(mediaType MediaType, queueID int, blocklist bool) error {
	var url, apiKey string
	if mediaType == MediaTypeTV {
		if !c.cfg.HasSonarr() {
			return fmt.Errorf("sonarr not configured")
		}
		url = fmt.Sprintf("%s/api/v3/queue/%d?removeFromClient=true&blocklist=%v", c.cfg.SonarrURL, queueID, blocklist)
		apiKey = c.cfg.SonarrAPIKey
	} else {
		if !c.cfg.HasRadarr() {
			return fmt.Errorf("radarr not configured")
		}
		url = fmt.Sprintf("%s/api/v3/queue/%d?removeFromClient=true&blocklist=%v", c.cfg.RadarrURL, queueID, blocklist)
		apiKey = c.cfg.RadarrAPIKey
	}

	log.Info("arr").
		Int("queue_id", queueID).
		Bool("blocklist", blocklist).
		Str("media_type", string(mediaType)).
		Msg("Removing from queue")

	_, err := c.doRequest("DELETE", url, apiKey, nil)
	return err
}

// CheckHealth checks if Sonarr/Radarr are reachable
func (c *Client) CheckHealth() map[string]bool {
	result := make(map[string]bool)

	if c.cfg.HasSonarr() {
		url := c.cfg.SonarrURL + "/api/v3/system/status"
		_, err := c.doRequest("GET", url, c.cfg.SonarrAPIKey, nil)
		result["sonarr"] = err == nil
	}

	if c.cfg.HasRadarr() {
		url := c.cfg.RadarrURL + "/api/v3/system/status"
		_, err := c.doRequest("GET", url, c.cfg.RadarrAPIKey, nil)
		result["radarr"] = err == nil
	}

	return result
}

// extractShowName extracts the show name from a torrent name
func extractShowName(name string) string {
	// Remove common suffixes and season/episode info
	name = strings.ToLower(name)

	// Remove season/episode patterns
	patterns := []string{
		`[Ss]\d{1,2}[Ee]\d{1,2}.*`,
		`[Ss]\d{1,2}.*`,
		`\d{1,2}x\d{1,2}.*`,
		`season\s*\d+.*`,
		`complete.*`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(`(?i)` + pattern)
		name = re.ReplaceAllString(name, "")
	}

	// Remove quality, codec, group info
	name = regexp.MustCompile(`(?i)(720p|1080p|2160p|4k|x264|x265|hevc|h\.?264|h\.?265|bluray|web-?dl|webrip|hdtv|proper|repack).*`).ReplaceAllString(name, "")

	// Clean up
	name = strings.TrimSpace(name)
	name = regexp.MustCompile(`[\.\-_]+`).ReplaceAllString(name, " ")
	name = strings.TrimSpace(name)

	// Take first 30 chars max for matching
	if len(name) > 30 {
		name = name[:30]
	}

	return name
}

// extractMovieName extracts the movie name from a torrent name
func extractMovieName(name string) string {
	name = strings.ToLower(name)

	// Remove year and everything after
	name = regexp.MustCompile(`(?i)\(?(?:19|20)\d{2}\)?.*`).ReplaceAllString(name, "")

	// Remove quality, codec, group info
	name = regexp.MustCompile(`(?i)(720p|1080p|2160p|4k|x264|x265|hevc|h\.?264|h\.?265|bluray|web-?dl|webrip|hdtv|proper|repack).*`).ReplaceAllString(name, "")

	// Clean up
	name = strings.TrimSpace(name)
	name = regexp.MustCompile(`[\.\-_]+`).ReplaceAllString(name, " ")
	name = strings.TrimSpace(name)

	// Take first 30 chars max for matching
	if len(name) > 30 {
		name = name[:30]
	}

	return name
}
