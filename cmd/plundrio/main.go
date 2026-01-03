package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/elsbrock/plundrio/internal/api"
	"github.com/elsbrock/plundrio/internal/config"
	"github.com/elsbrock/plundrio/internal/download"
	"github.com/elsbrock/plundrio/internal/log"
	"github.com/elsbrock/plundrio/internal/server"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	version = "dev"
)

var rootCmd = &cobra.Command{
	Use:     "plundrio",
	Short:   "Put.io automation tool",
	Version: version,
}

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Run the download manager",
	Run: func(cmd *cobra.Command, args []string) {
		// Initialize Viper
		viper.SetEnvPrefix("PLDR")
		viper.AutomaticEnv()

		configFile, _ := cmd.Flags().GetString("config")
		if configFile != "" {
			viper.SetConfigFile(configFile)
			if err := viper.ReadInConfig(); err != nil {
				log.Fatal("config").Str("file", configFile).Err(err).Msg("Error reading config file")
			}
			log.Info("config").Str("file", viper.ConfigFileUsed()).Msg("Using config file")
		}

		// Bind flags to Viper
		viper.BindPFlags(cmd.Flags())

		// Set log level from env/config/flag (in that order)
		logLevel := viper.GetString("log-level")
		if logLevel != "" {
			log.SetLevel(log.LogLevel(logLevel))
		}

		log.Debug("startup").
			Str("version", version).
			Str("log_level", logLevel).
			Msg("Starting plundrio")

		// Get configuration values from viper (which checks env vars, config file, and flags)
		targetDir := viper.GetString("target")
		putioFolder := strings.ToLower(viper.GetString("folder"))
		oauthToken := viper.GetString("token")
		listenAddr := viper.GetString("listen")
		workerCount := viper.GetInt("workers")

		// Sonarr/Radarr integration
		sonarrURL := viper.GetString("sonarr-url")
		sonarrAPIKey := viper.GetString("sonarr-api-key")
		radarrURL := viper.GetString("radarr-url")
		radarrAPIKey := viper.GetString("radarr-api-key")
		remotePath := viper.GetString("remote-path")
		localPath := viper.GetString("local-path")

		// Notification settings
		ntfyURL := viper.GetString("ntfy-url")
		ntfyTopic := viper.GetString("ntfy-topic")

		// Auto-cancel settings
		autoCancelStuck := viper.GetBool("auto-cancel-stuck")
		autoCancelTimeout := viper.GetDuration("auto-cancel-timeout")
		autoCancelResearch := viper.GetBool("auto-cancel-research")
		autoCancelMinRetry := viper.GetDuration("auto-cancel-min-retry")

		// Retry settings
		maxRetries := viper.GetInt("max-retries")
		retryBaseDelay := viper.GetDuration("retry-base-delay")
		retryMaxDelay := viper.GetDuration("retry-max-delay")

		// Priority settings
		priorityEnabled := viper.GetBool("priority-enabled")
		smallFilePriority := viper.GetBool("small-file-priority")
		sequentialDownload := viper.GetBool("sequential-download")

		log.Debug("config").
			Str("target_dir", targetDir).
			Str("putio_folder", putioFolder).
			Str("listen_addr", listenAddr).
			Int("workers", workerCount).
			Msg("Configuration loaded")

		// Log *arr integration status
		if sonarrURL != "" || radarrURL != "" {
			log.Info("config").
				Bool("sonarr_configured", sonarrURL != "").
				Bool("radarr_configured", radarrURL != "").
				Msg("*arr integration enabled")
		}

		// Log notification status
		if ntfyURL != "" && ntfyTopic != "" {
			log.Info("config").
				Str("ntfy_topic", ntfyTopic).
				Msg("ntfy notifications enabled")
		}

		// Log auto-cancel status
		if autoCancelStuck {
			log.Info("config").
				Dur("timeout", autoCancelTimeout).
				Bool("research_enabled", autoCancelResearch).
				Msg("Auto-cancel stuck transfers enabled")
		}

		// Validate required configuration values
		// Security warning for token in config file
		if viper.ConfigFileUsed() != "" && viper.IsSet("token") {
			log.Warn("security").
				Str("file", viper.ConfigFileUsed()).
				Msg("OAuth token found in config file - consider using environment variable PLDR_TOKEN instead")
		}

		if targetDir == "" || putioFolder == "" || oauthToken == "" {
			log.Error("config").Msg("Not all required configuration values were provided")
			cmd.Usage()
			os.Exit(1)
		}

		// Verify target directory exists
		stat, err := os.Stat(targetDir)
		if err != nil {
			if os.IsNotExist(err) {
				log.Fatal("config").Str("dir", targetDir).Msg("Target directory does not exist")
			}
			log.Fatal("config").Str("dir", targetDir).Err(err).Msg("Error checking target directory")
		}
		if !stat.IsDir() {
			log.Fatal("config").Str("dir", targetDir).Msg("Target path is not a directory")
		}

		// Initialize configuration
		cfg := &config.Config{
			TargetDir:   targetDir,
			PutioFolder: putioFolder,
			OAuthToken:  oauthToken,
			ListenAddr:  listenAddr,
			WorkerCount: workerCount,

			// Sonarr/Radarr integration
			SonarrURL:    sonarrURL,
			SonarrAPIKey: sonarrAPIKey,
			RadarrURL:    radarrURL,
			RadarrAPIKey: radarrAPIKey,
			RemotePath:   remotePath,
			LocalPath:    localPath,

			// Notifications
			NtfyURL:   ntfyURL,
			NtfyTopic: ntfyTopic,

			// Auto-cancel stuck transfers
			AutoCancelStuck:    autoCancelStuck,
			AutoCancelTimeout:  autoCancelTimeout,
			AutoCancelResearch: autoCancelResearch,
			AutoCancelMinRetry: autoCancelMinRetry,

			// Retry configuration
			MaxRetries:     maxRetries,
			RetryBaseDelay: retryBaseDelay,
			RetryMaxDelay:  retryMaxDelay,

			// Priority configuration
			PriorityEnabled:    priorityEnabled,
			SmallFilePriority:  smallFilePriority,
			SequentialDownload: sequentialDownload,
		}

		// Initialize Put.io API client
		client := api.NewClient(cfg.OAuthToken)

		// Authenticate and get account info
		log.Info("auth").Msg("Authenticating with Put.io...")
		if err := client.Authenticate(); err != nil {
			log.Fatal("auth").Err(err).Msg("Failed to authenticate with Put.io")
		}
		log.Info("auth").Msg("Authentication successful")

		// Create/get folder ID
		log.Info("setup").Str("folder", cfg.PutioFolder).Msg("Setting up Put.io folder")
		folderID, err := client.EnsureFolder(cfg.PutioFolder)
		if err != nil {
			log.Fatal("setup").Str("folder", cfg.PutioFolder).Err(err).Msg("Failed to create/get folder")
		}
		cfg.FolderID = folderID
		log.Info("setup").
			Str("folder", cfg.PutioFolder).
			Int64("folder_id", folderID).
			Msg("Using Put.io folder")

		// Initialize download manager
		dlManager := download.New(cfg, client)
		dlManager.Start()
		defer dlManager.Stop()
		log.Info("manager").
			Int("workers", cfg.WorkerCount).
			Msg("Download manager started")

		// Initialize and start RPC server
		srv := server.New(cfg, client, dlManager)
		go func() {
			log.Info("server").
				Str("addr", cfg.ListenAddr).
				Msg("Starting transmission-rpc server")
			if err := srv.Start(); err != nil {
				log.Fatal("server").Err(err).Msg("Server error")
			}
		}()

		// Wait for interrupt signal
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigChan
		log.Info("shutdown").
			Str("signal", sig.String()).
			Msg("Received signal, shutting down...")

		// Cleanup and exit
		log.Info("shutdown").Msg("Stopping download manager...")
		dlManager.Stop()

		log.Info("shutdown").Msg("Stopping server...")
		if err := srv.Stop(); err != nil {
			log.Error("shutdown").Err(err).Msg("Error stopping server")
		}
	},
}

var generateConfigCmd = &cobra.Command{
	Use:   "generate-config",
	Short: "Generate sample configuration file",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := `# Plundrio configuration
# Save as ~/.plundrio.yaml or specify with --config

target: /path/to/downloads	# Target directory for downloads
folder: "plundrio"					# Folder name on Put.io
token: "" 									# Get a token with get-token
listen: ":9091"							# Transmission RPC server address
workers: 4									# Number of download workers
log_level: "info"					  # Log level (trace,debug,info,warn,error,fatal,panic,none,pretty)

# Sonarr/Radarr Integration (optional)
# sonarr_url: "http://sonarr:8989"
# sonarr_api_key: ""
# radarr_url: "http://radarr:7878"
# radarr_api_key: ""
# remote_path: "/downloads"         # Path as seen by *arr
# local_path: "/mnt/downloads"      # Path as seen by Plundrio

# Notifications (optional)
# ntfy_url: "https://ntfy.sh"
# ntfy_topic: "plundrio"

# Auto-Cancel Stuck Transfers (optional)
# auto_cancel_stuck: false          # Enable auto-cancellation of stuck transfers
# auto_cancel_timeout: "12h"        # How long before cancelling a stuck transfer
# auto_cancel_research: false       # Trigger re-search in *arr after cancelling
# auto_cancel_min_retry: "1h"       # Minimum time before re-searching same media

# Retry Configuration (optional)
# max_retries: 3                    # Maximum retry attempts for failed downloads
# retry_base_delay: "30s"           # Base delay between retries
# retry_max_delay: "30m"            # Maximum delay between retries

# Priority Configuration (optional)
# priority_enabled: false           # Enable download priority system
# small_file_priority: false        # Prioritize smaller files
# sequential_download: false        # Download episodes in sequence order

# Environment variables:
# PLDR_TARGET, PLDR_FOLDER, PLDR_TOKEN, PLDR_LISTEN, PLDR_WORKERS, PLDR_LOG_LEVEL
# PLDR_SONARR_URL, PLDR_SONARR_API_KEY, PLDR_RADARR_URL, PLDR_RADARR_API_KEY
# PLDR_REMOTE_PATH, PLDR_LOCAL_PATH, PLDR_NTFY_URL, PLDR_NTFY_TOPIC
# PLDR_AUTO_CANCEL_STUCK, PLDR_AUTO_CANCEL_TIMEOUT, PLDR_AUTO_CANCEL_RESEARCH
# PLDR_AUTO_CANCEL_MIN_RETRY, PLDR_MAX_RETRIES, PLDR_RETRY_BASE_DELAY
# PLDR_RETRY_MAX_DELAY, PLDR_PRIORITY_ENABLED, PLDR_SMALL_FILE_PRIORITY
# PLDR_SEQUENTIAL_DOWNLOAD
`

		outputPath := "plundrio-config.yaml"
		if len(args) > 0 {
			outputPath = args[0]
		}

		log.Debug("config").
			Str("path", outputPath).
			Msg("Generating sample configuration")

		if err := os.WriteFile(outputPath, []byte(cfg), 0644); err != nil {
			log.Fatal("config").
				Str("file", outputPath).
				Err(err).
				Msg("Failed to write config file")
		}
		log.Info("config").
			Str("file", outputPath).
			Msg("Sample config created")
	},
}

var getTokenCmd = &cobra.Command{
	Use:   "get-token",
	Short: "Get OAuth token using device code flow",
	Run: func(cmd *cobra.Command, args []string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		log.Debug("auth").Msg("Starting OAuth device code flow")

		// Step 1: Get OOB code from Put.io
		resp, err := http.Get("https://api.put.io/v2/oauth2/oob/code?app_id=3270")
		if err != nil {
			log.Fatal("auth").Err(err).Msg("Failed to get OOB code")
		}
		defer resp.Body.Close()

		var codeResponse struct {
			Code      string `json:"code"`
			QrCodeURL string `json:"qr_code_url"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&codeResponse); err != nil {
			log.Fatal("auth").Err(err).Msg("Failed to decode code response")
		}

		log.Info("auth").
			Str("code", codeResponse.Code).
			Str("qr_url", codeResponse.QrCodeURL).
			Msg("Visit put.io/link and enter code")
		log.Info("auth").Msg("Waiting for authorization...")

		// Step 2: Poll for token
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				log.Fatal("auth").Msg("Authorization timed out")
			case <-ticker.C:
				tokenResp, err := http.Get("https://api.put.io/v2/oauth2/oob/code/" + codeResponse.Code)
				if err != nil {
					log.Fatal("auth").Err(err).Msg("Failed to check authorization status")
				}

				var tokenResult struct {
					OAuthToken string `json:"oauth_token"`
					Status     string `json:"status"`
				}
				if err := json.NewDecoder(tokenResp.Body).Decode(&tokenResult); err != nil {
					tokenResp.Body.Close()
					continue
				}
				tokenResp.Body.Close()

				if tokenResult.Status == "OK" && tokenResult.OAuthToken != "" {
					log.Info("auth").
						Str("token", tokenResult.OAuthToken).
						Msg("Successfully obtained access token")
					return
				}

				log.Debug("auth").
					Str("status", tokenResult.Status).
					Msg("Polling for authorization")
			}
		}
	},
}

func init() {
	// Run command flags
	runCmd.Flags().String("config", "", "Config file (default $HOME/.plundrio.yaml)")
	runCmd.Flags().StringP("target", "t", "", "Target directory for downloads (required)")
	runCmd.Flags().StringP("folder", "f", "plundrio", "Put.io folder name")
	runCmd.Flags().StringP("token", "k", "", "Put.io OAuth token (required)")
	runCmd.Flags().StringP("listen", "l", ":9091", "Listen address")
	runCmd.Flags().IntP("workers", "w", 4, "Number of workers")
	runCmd.Flags().String("log-level", "", "Log level (trace,debug,info,warn,error,fatal,none,pretty)")

	// Sonarr/Radarr integration flags
	runCmd.Flags().String("sonarr-url", "", "Sonarr URL (e.g., http://sonarr:8989)")
	runCmd.Flags().String("sonarr-api-key", "", "Sonarr API key")
	runCmd.Flags().String("radarr-url", "", "Radarr URL (e.g., http://radarr:7878)")
	runCmd.Flags().String("radarr-api-key", "", "Radarr API key")
	runCmd.Flags().String("remote-path", "", "Download path as seen by *arr apps")
	runCmd.Flags().String("local-path", "", "Download path as seen by Plundrio")

	// Notification flags
	runCmd.Flags().String("ntfy-url", "https://ntfy.sh", "ntfy server URL")
	runCmd.Flags().String("ntfy-topic", "", "ntfy topic for notifications")

	// Auto-cancel flags
	runCmd.Flags().Bool("auto-cancel-stuck", false, "Auto-cancel stuck Put.io transfers")
	runCmd.Flags().Duration("auto-cancel-timeout", 12*time.Hour, "Time before considering a transfer stuck")
	runCmd.Flags().Bool("auto-cancel-research", false, "Trigger *arr re-search after cancelling")
	runCmd.Flags().Duration("auto-cancel-min-retry", 1*time.Hour, "Minimum time before re-searching same media")

	// Retry flags
	runCmd.Flags().Int("max-retries", 3, "Maximum retry attempts for failed downloads")
	runCmd.Flags().Duration("retry-base-delay", 30*time.Second, "Base delay between retries")
	runCmd.Flags().Duration("retry-max-delay", 30*time.Minute, "Maximum delay between retries")

	// Priority flags
	runCmd.Flags().Bool("priority-enabled", false, "Enable download priority system")
	runCmd.Flags().Bool("small-file-priority", false, "Prioritize smaller files first")
	runCmd.Flags().Bool("sequential-download", false, "Download episodes in sequence order")

	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(getTokenCmd)
	rootCmd.AddCommand(generateConfigCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatal("main").Err(err).Msg("Command execution failed")
	}
}
