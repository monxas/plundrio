package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/elsbrock/plundrio/internal/download"
	"github.com/elsbrock/plundrio/internal/log"
)

// handleRPC processes transmission-rpc requests
func (s *Server) handleRPC(w http.ResponseWriter, r *http.Request) {
	// Check for session ID header
	sessionID := r.Header.Get("X-Transmission-Session-Id")
	if sessionID == "" {
		// Client needs to authenticate - send session ID
		log.Info("rpc").
			Str("client_addr", r.RemoteAddr).
			Msg("Client needs authentication - sending session ID")
		w.Header().Set("X-Transmission-Session-Id", "123") // Using a simple static ID for now
		http.Error(w, "409 Conflict", http.StatusConflict)
		return
	}

	log.Debug("rpc").
		Str("client_addr", r.RemoteAddr).
		Str("session_id", sessionID).
		Str("method", r.Method).
		Msg("Handling RPC request")

	var req struct {
		Method    string          `json:"method"`
		Arguments json.RawMessage `json:"arguments"`
		Tag       interface{}     `json:"tag,omitempty"`
	}

	// Handle GET method for session-get
	if r.Method == http.MethodGet {
		req.Method = "session-get"
		log.Debug("rpc").
			Str("client_addr", r.RemoteAddr).
			Str("method", "GET").
			Msg("GET request converted to session-get")
	} else if r.Method == http.MethodPost {
		// Parse RPC request for POST method
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			log.Error("rpc").
				Str("client_addr", r.RemoteAddr).
				Str("method", "POST").
				Err(err).
				Msg("Failed to decode request")
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}
		log.Debug("rpc").
			Str("client_addr", r.RemoteAddr).
			Str("method", "POST").
			Str("rpc_method", req.Method).
			Msg("Decoded RPC request")
	} else {
		log.Error("rpc").
			Str("client_addr", r.RemoteAddr).
			Str("method", r.Method).
			Msg("Invalid HTTP method")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Handle different RPC methods
	var (
		result interface{}
		err    error
	)

	log.Debug("rpc").
		Str("client_addr", r.RemoteAddr).
		Str("rpc_method", req.Method).
		Msg("Processing RPC method")

	switch req.Method {
	case "torrent-add":
		result, err = s.handleTorrentAdd(req.Arguments)
	case "torrent-get":
		result, err = s.handleTorrentGet(req.Arguments)
	case "torrent-remove":
		result, err = s.handleTorrentRemove(req.Arguments)
	case "session-get":
		result = map[string]interface{}{
			"download-dir":        s.cfg.TargetDir,
			"version":             "2.94", // Transmission version to report
			"rpc-version":         15,     // RPC version to report
			"rpc-version-minimum": 1,
		}
		log.Debug("rpc").
			Str("client_addr", r.RemoteAddr).
			Str("download_dir", s.cfg.TargetDir).
			Msg("Session information requested")
	default:
		// Return empty success for unsupported methods
		result = struct{}{}
		log.Debug("rpc").
			Str("client_addr", r.RemoteAddr).
			Str("rpc_method", req.Method).
			Msg("Unsupported RPC method called")
	}

	// Send response
	if err != nil {
		s.sendError(w, err)
		return
	}

	log.Debug("rpc").
		Str("client_addr", r.RemoteAddr).
		Str("rpc_method", req.Method).
		Msg("Sending RPC response")

	s.sendResponse(w, req.Tag, result)
}

// HealthResponse contains the health check response data
type HealthResponse struct {
	Status           string                 `json:"status"`
	Uptime           string                 `json:"uptime"`
	ActiveTransfers  int                    `json:"active_transfers"`
	IncompleteCount  int                    `json:"incomplete_count"`
	TotalDownloadRate int64                 `json:"total_download_rate_bytes"`
	Transfers        []TransferHealthStatus `json:"transfers,omitempty"`
}

// TransferHealthStatus contains status for a single transfer
type TransferHealthStatus struct {
	ID             int64   `json:"id"`
	Name           string  `json:"name"`
	State          string  `json:"state"`
	Progress       float64 `json:"progress_percent"`
	DownloadRate   int64   `json:"download_rate_bytes"`
	CompletedFiles int32   `json:"completed_files"`
	TotalFiles     int32   `json:"total_files"`
}

var serverStartTime = time.Now()

// handleHealth returns health status including download progress
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	coordinator := s.dlManager.GetCoordinator()

	response := HealthResponse{
		Status:          "healthy",
		Uptime:          time.Since(serverStartTime).Round(time.Second).String(),
		ActiveTransfers: 0,
		IncompleteCount: 0,
		TotalDownloadRate: 0,
		Transfers:       []TransferHealthStatus{},
	}

	// Get all active transfers
	transfers := coordinator.GetAllTransfers()
	for _, ctx := range transfers {
		// Get a thread-safe snapshot of the transfer state
		snapshot := ctx.GetSnapshot()

		// Calculate progress
		var progress float64
		if snapshot.TotalSize > 0 {
			progress = float64(snapshot.DownloadedSize) / float64(snapshot.TotalSize) * 100
		}

		// Calculate download rate (simplified - from coordinator)
		var downloadRate int64 = 0
		if snapshot.State == download.TransferLifecycleDownloading {
			// Estimate rate based on time since start
			elapsed := time.Since(snapshot.StartTime).Seconds()
			if elapsed > 0 {
				downloadRate = int64(float64(snapshot.DownloadedSize) / elapsed)
			}
		}

		status := TransferHealthStatus{
			ID:             snapshot.ID,
			Name:           snapshot.Name,
			State:          snapshot.State.String(),
			Progress:       progress,
			DownloadRate:   downloadRate,
			CompletedFiles: snapshot.CompletedFiles,
			TotalFiles:     snapshot.TotalFiles,
		}

		response.Transfers = append(response.Transfers, status)
		response.ActiveTransfers++

		if progress < 100 {
			response.IncompleteCount++
			response.TotalDownloadRate += downloadRate
		}
	}

	// Determine overall health
	// Unhealthy if we have incomplete transfers with 0 download rate
	if response.IncompleteCount > 0 && response.TotalDownloadRate == 0 {
		response.Status = "stalled"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
