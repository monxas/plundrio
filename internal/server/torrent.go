package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/elsbrock/go-putio"
	"github.com/elsbrock/plundrio/internal/log"
)

// fetchTorrentFromURL downloads a .torrent from an arbitrary URL.
//
// Put.io resolves transfer URLs from its own cloud, so it cannot reach hosts
// that only exist on our internal network (e.g. a Prowlarr instance addressed
// as http://prowlarr:9696). When a Transmission client (Sonarr/Radarr/Lidarr)
// hands us such a URL we fetch the torrent ourselves and upload the file bytes
// to Put.io instead. Some indexers 30x-redirect a .torrent URL straight to a
// magnet: link; in that case we surface the magnet so the caller can add it as
// a transfer.
func fetchTorrentFromURL(rawurl string) (data []byte, filename string, magnet string, err error) {
	var magnetLink string
	client := &http.Client{
		Timeout: 60 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL != nil && req.URL.Scheme == "magnet" {
				magnetLink = req.URL.String()
				return http.ErrUseLastResponse
			}
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			return nil
		},
	}

	resp, err := client.Get(rawurl)
	if err != nil {
		if magnetLink != "" {
			return nil, "", magnetLink, nil
		}
		return nil, "", "", err
	}
	defer resp.Body.Close()

	if magnetLink != "" {
		return nil, "", magnetLink, nil
	}
	if loc := resp.Header.Get("Location"); strings.HasPrefix(loc, "magnet:") {
		return nil, "", loc, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", "", fmt.Errorf("unexpected status %d fetching torrent from %s", resp.StatusCode, rawurl)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20)) // 32 MiB cap
	if err != nil {
		return nil, "", "", err
	}
	// The body itself may be a magnet link (some indexers return it as text).
	if trimmed := strings.TrimSpace(string(body)); strings.HasPrefix(trimmed, "magnet:") {
		return nil, "", trimmed, nil
	}
	return body, filenameFromResponse(resp, rawurl), "", nil
}

// filenameFromResponse derives a .torrent filename from a Content-Disposition
// header, falling back to the URL path.
func filenameFromResponse(resp *http.Response, rawurl string) string {
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if _, params, err := mime.ParseMediaType(cd); err == nil {
			if fn := params["filename"]; fn != "" {
				return ensureTorrentExt(fn)
			}
		}
	}
	if u, err := url.Parse(rawurl); err == nil {
		if base := path.Base(u.Path); base != "" && base != "/" && base != "." {
			return ensureTorrentExt(base)
		}
	}
	return "download.torrent"
}

func ensureTorrentExt(name string) string {
	if !strings.HasSuffix(strings.ToLower(name), ".torrent") {
		return name + ".torrent"
	}
	return name
}

// normalizeHash converts a hash to lowercase for internal processing
func normalizeHash(hash string) string {
	return strings.ToLower(hash)
}

// hashesMatch performs case-insensitive hash comparison
func hashesMatch(hash1, hash2 string) bool {
	return normalizeHash(hash1) == normalizeHash(hash2)
}

// standardizeHashResponse converts hash to uppercase for response consistency
func standardizeHashResponse(hash string) string {
	return strings.ToUpper(hash)
}

// findTransferByHash finds a transfer by its hash string (case-insensitive)
func (s *Server) findTransferByHash(hash string) (*putio.Transfer, error) {
	transfers, err := s.client.GetTransfers()
	if err != nil {
		return nil, err
	}
	for _, t := range transfers {
		if hashesMatch(t.Hash, hash) {
			return t, nil
		}
	}
	return nil, fmt.Errorf("transfer not found with hash: %s", hash)
}

// handleTorrentAdd processes torrent-add requests
func (s *Server) handleTorrentAdd(args json.RawMessage) (interface{}, error) {
	var params struct {
		Filename    string `json:"filename"`    // For .torrent files
		MetaInfo    string `json:"metainfo"`    // Base64 encoded .torrent
		MagnetLink  string `json:"magnetLink"`  // Magnet link
		DownloadDir string `json:"downloadDir"` // Ignored, we use Put.io
	}

	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	var name string

	// Handle .torrent file upload if metainfo is provided
	if params.MetaInfo != "" {
		// Decode base64 torrent data
		torrentData, err := base64.StdEncoding.DecodeString(params.MetaInfo)
		if err != nil {
			return nil, fmt.Errorf("failed to decode torrent data: %w", err)
		}

		// Upload torrent file to Put.io
		name = params.Filename
		if name == "" {
			name = "unknown.torrent"
		}
		if err := s.client.UploadFile(torrentData, name, s.cfg.FolderID); err != nil {
			return nil, fmt.Errorf("failed to upload torrent: %w", err)
		}

		log.Info("rpc").
			Str("operation", "torrent-add").
			Str("type", "torrent").
			Str("name", name).
			Int64("folder_id", s.cfg.FolderID).
			Msg("Torrent file uploaded")
	} else if params.Filename != "" && (strings.HasPrefix(params.Filename, "http://") || strings.HasPrefix(params.Filename, "https://")) {
		// Handle .torrent URLs (e.g. Prowlarr-proxied indexers). Put.io cannot
		// reach internal hosts, so fetch the torrent ourselves and upload it.
		data, fname, magnet, err := fetchTorrentFromURL(params.Filename)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch torrent from url: %w", err)
		}

		if magnet != "" {
			// The URL resolved to a magnet link; add it as a transfer instead.
			if err := s.client.AddTransfer(magnet, s.cfg.FolderID); err != nil {
				return nil, fmt.Errorf("failed to add transfer: %w", err)
			}
			log.Info("rpc").
				Str("operation", "torrent-add").
				Str("type", "magnet-via-url").
				Str("magnet", magnet).
				Int64("folder_id", s.cfg.FolderID).
				Msg("Magnet link (resolved from url) added")
			return map[string]interface{}{
				"torrent-added": map[string]interface{}{},
			}, nil
		}

		name = fname
		if name == "" {
			name = "download.torrent"
		}
		if err := s.client.UploadFile(data, name, s.cfg.FolderID); err != nil {
			return nil, fmt.Errorf("failed to upload torrent: %w", err)
		}

		log.Info("rpc").
			Str("operation", "torrent-add").
			Str("type", "torrent-via-url").
			Str("name", name).
			Int("bytes", len(data)).
			Int64("folder_id", s.cfg.FolderID).
			Msg("Torrent file (fetched from url) uploaded")
	} else {
		// Handle magnet links
		if params.MagnetLink != "" {
			name = params.MagnetLink
		} else if params.Filename != "" && strings.HasPrefix(params.Filename, "magnet:") {
			name = params.Filename
		} else {
			return nil, fmt.Errorf("invalid torrent or magnet link provided")
		}

		// Add magnet link to Put.io
		if err := s.client.AddTransfer(name, s.cfg.FolderID); err != nil {
			return nil, fmt.Errorf("failed to add transfer: %w", err)
		}

		log.Info("rpc").
			Str("operation", "torrent-add").
			Str("type", "magnet").
			Str("magnet", name).
			Int64("folder_id", s.cfg.FolderID).
			Msg("Magnet link added")

		// Return success response
		return map[string]interface{}{
			"torrent-added": map[string]interface{}{},
		}, nil
	}

	// Return success response
	return map[string]interface{}{
		"torrent-added": map[string]interface{}{},
	}, nil
}

// handleTorrentGet processes torrent-get requests
func (s *Server) handleTorrentGet(args json.RawMessage) (interface{}, error) {
	var params struct {
		IDs    []string `json:"ids"`
		Fields []string `json:"fields"`
	}

	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}

	// Log input parameters
	log.Debug("rpc").
		Str("operation", "torrent-get").
		Interface("ids", params.IDs).
		Interface("fields", params.Fields).
		Msg("Processing torrent-get request")

	// Log hash normalization for debugging
	if len(params.IDs) > 0 {
		for _, id := range params.IDs {
			log.Debug("rpc").
				Str("operation", "torrent-get").
				Str("query_hash", id).
				Str("normalized_hash", normalizeHash(id)).
				Msg("Hash query normalization")
		}
	}

	// Get transfers from the processor, which now keeps track of all transfers
	// including completed ones that have been processed
	processor := s.dlManager.GetTransferProcessor()

	// Check if processor is nil
	if processor == nil {
		log.Error("rpc").
			Str("operation", "torrent-get").
			Msg("Transfer processor is nil")
		return map[string]interface{}{
			"torrents": []map[string]interface{}{},
		}, nil
	}

	// Log processor details
	log.Debug("rpc").
		Str("operation", "torrent-get").
		Msg("Using transfer processor")

	transfers := processor.GetTransfers()

	log.Debug("rpc").
		Str("operation", "torrent-get").
		Int("all_transfers_count", len(transfers)).
		Msg("Retrieved all transfers from processor")

	// Convert Put.io transfers to transmission format
	torrents := make([]map[string]interface{}, 0, len(transfers))
	for _, t := range transfers {
		// Filter by IDs if specified
		if len(params.IDs) > 0 {
			found := false
			for _, id := range params.IDs {
				if hashesMatch(id, t.Hash) {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		// Calculate combined progress
		var percentDone float64
		var status int
		var leftUntilDone int64

		// Check if we have a transfer context (transfer is being processed)
		if ctx, exists := s.dlManager.GetCoordinator().GetTransferContext(t.ID); exists && ctx.TotalFiles > 0 {
			// Get the context data
			totalSize := ctx.TotalSize
			downloadedSize := ctx.DownloadedSize
			totalFiles := ctx.TotalFiles
			completedFiles := ctx.CompletedFiles
			state := ctx.State

			// Calculate total size (Put.io download + local download)
			// If we have size information, use it; otherwise fall back to the transfer size
			totalTransferSize := totalSize
			if totalTransferSize == 0 {
				totalTransferSize = int64(t.Size)
			}

			// The total download task is considered as two parts:
			// 1. Put.io downloading the torrent (50% of the total task)
			// 2. Local downloading from Put.io (50% of the total task)

			// Calculate Put.io progress (0-50%)
			putioProgress := float64(t.PercentDone) / 200.0 // Maps 0-100 to 0-0.5

			// Calculate local download progress (0-50%)
			var localProgress float64
			if totalSize > 0 {
				// If we have size information, use bytes downloaded
				localProgress = float64(downloadedSize) / float64(totalSize) * 0.5 // Maps 0-1 to 0-0.5
			} else if totalFiles > 0 {
				// Fall back to file count if size information is not available
				localProgress = float64(completedFiles) / float64(totalFiles) * 0.5 // Maps 0-1 to 0-0.5
			}

			// Combine the two progress values
			percentDone = putioProgress + localProgress

			// Calculate bytes left until done
			// First, calculate how many bytes are left on Put.io side
			putioLeftBytes := int64(float64(t.Size) * (1.0 - float64(t.PercentDone)/100.0))

			// Then, calculate how many bytes are left on local download side
			localLeftBytes := totalSize - downloadedSize

			// Total bytes left is the sum of both
			leftUntilDone = putioLeftBytes + localLeftBytes

			// Ensure leftUntilDone is never negative
			if leftUntilDone < 0 {
				leftUntilDone = 0
			}

			// Check if the transfer is in the Processed state
			if state == 5 { // TransferLifecycleProcessed = 5
				// For transfers that have been processed locally, show as 100% complete
				percentDone = 1.0 // 100%
				leftUntilDone = 0 // Nothing left to download
				status = 6        // TR_STATUS_SEED (completed/seeding)
			} else if state == 2 { // TransferLifecycleCompleted = 2
				status = s.mapPutioStatus(t.Status)
			} else {
				// If not all files are downloaded, show as downloading
				status = 4 // TR_STATUS_DOWNLOAD
			}

			log.Debug("rpc").
				Str("operation", "torrent-get").
				Int64("id", t.ID).
				Str("name", t.Name).
				Float64("putio_progress", putioProgress*100).
				Float64("local_progress", localProgress*100).
				Float64("combined_progress", percentDone*100).
				Int64("left_until_done", leftUntilDone).
				Msg("Calculated progress for transfer with context")
		} else if t.Status == "COMPLETED" || t.Status == "SEEDING" {
			// For transfers that are completed on put.io but have no corresponding entry in the processor
			// (i.e., already downloaded), show as 100% complete with status "downloaded"
			percentDone = 1.0 // 100%
			leftUntilDone = 0 // Nothing left to download
			status = 6        // TR_STATUS_SEED (completed/seeding)
		} else {
			// For other transfers not being processed, just use put.io progress (0-50%)
			putioProgress := float64(t.PercentDone) / 200.0 // Maps 0-100 to 0-0.5
			percentDone = putioProgress

			// Calculate bytes left on Put.io side only
			leftUntilDone = int64(float64(t.Size) * (1.0 - float64(t.PercentDone)/100.0))

			status = s.mapPutioStatus(t.Status)

			log.Debug("rpc").
				Str("operation", "torrent-get").
				Int64("id", t.ID).
				Str("name", t.Name).
				Float64("putio_progress", putioProgress*100).
				Float64("combined_progress", percentDone*100).
				Int64("left_until_done", leftUntilDone).
				Msg("Calculated progress for transfer without context")
		}

		torrentInfo := map[string]interface{}{
			"id":             t.ID,
			"hashString":     standardizeHashResponse(t.Hash),
			"name":           t.Name,
			"eta":            t.EstimatedTime,
			"status":         status,
			"downloadDir":    s.cfg.TargetDir,
			"totalSize":      t.Size,
			"leftUntilDone":  leftUntilDone,
			"uploadedEver":   t.Uploaded,
			"downloadedEver": t.Downloaded,
			"percentDone":    percentDone,
			"rateDownload":   t.DownloadSpeed,
			"rateUpload":     t.UploadSpeed,
			"uploadRatio": func() float64 {
				if t.Size > 0 {
					return float64(t.Uploaded) / float64(t.Size)
				}
				return 0
			}(),
			"error":       t.ErrorMessage != "",
			"errorString": t.ErrorMessage,
		}

		torrents = append(torrents, torrentInfo)

		// Log each torrent being added to the response
		log.Debug("rpc").
			Str("operation", "torrent-get").
			Int64("id", t.ID).
			Str("hash", standardizeHashResponse(t.Hash)).
			Str("name", t.Name).
			Str("status", t.Status).
			Int("size", t.Size).
			Float64("percent_done", percentDone).
			Msg("Added torrent to response")
	}

	// Log the final count of torrents in the response
	log.Debug("rpc").
		Str("operation", "torrent-get").
		Int("torrents_count", len(torrents)).
		Msg("Returning torrents")

	result := map[string]interface{}{
		"torrents": torrents,
	}

	// Log the final response structure
	resultBytes, _ := json.Marshal(result)
	log.Debug("rpc").
		Str("operation", "torrent-get").
		Str("result", string(resultBytes)).
		Msg("Final result structure")

	return result, nil
}

// handleTorrentRemove processes torrent-remove requests
func (s *Server) handleTorrentRemove(args json.RawMessage) (interface{}, error) {
	var params struct {
		IDs             []string `json:"ids"`
		DeleteLocalData bool     `json:"delete-local-data"`
	}

	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}

	for _, hash := range params.IDs {
		log.Debug("rpc").
			Str("operation", "torrent-remove").
			Str("query_hash", hash).
			Str("normalized_hash", normalizeHash(hash)).
			Msg("Hash query normalization for removal")

		transfer, err := s.findTransferByHash(hash)
		if err != nil {
			log.Error("rpc").
				Str("operation", "torrent-remove").
				Str("hash", hash).
				Err(err).
				Msg("Failed to find transfer")
			continue
		}

		// Delete the files of the transfer
		if err := s.client.DeleteFile(transfer.FileID); err != nil {
			log.Error("rpc").
				Str("operation", "torrent-remove").
				Str("hash", hash).
				Int64("transfer_id", transfer.ID).
				Err(err).
				Msg("Failed to delete transfer files")
		}

		if err := s.client.DeleteTransfer(transfer.ID); err != nil {
			log.Error("rpc").
				Str("operation", "torrent-remove").
				Str("hash", hash).
				Int64("transfer_id", transfer.ID).
				Err(err).
				Msg("Failed to delete transfer")
		} else {
			log.Info("rpc").
				Str("operation", "torrent-remove").
				Str("hash", hash).
				Int64("transfer_id", transfer.ID).
				Bool("delete_local_data", params.DeleteLocalData).
				Msg("Transfer removed")
		}
	}

	return struct{}{}, nil
}
