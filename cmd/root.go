// file: cmd/root.go
// version: 1.15.0
// guid: 6a7b8c9d-0e1f-2a3b-4c5d-6e7f8a9b0c1d
// last-edited: 2026-07-01

package cmd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/mediainfo"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/playlist"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
	"github.com/falkcorp/audiobook-organizer/internal/server"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
	"github.com/falkcorp/audiobook-organizer/internal/telemetry"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfgFile string
var rootDir string
var databasePath string
var databaseType string
var enableSQLite bool
var playlistDir string
var logLevel string
var metadataInspectFile string

var (
	initializeStore        = database.InitializeStore
	closeStore             = database.CloseStore
	scanDirectory          = scanner.ScanDirectory // func(context.Context, string, logger.Logger) ([]Book, error)
	processBooks           = scanner.ProcessBooks  // func([]Book, logger.Logger) error
	generatePlaylists      = playlist.GeneratePlaylistsForSeries
	updateSeriesTags       = tagger.UpdateSeriesTags
	initEncryption         = database.InitEncryption
	loadConfigFromDB       = config.LoadConfigFromDatabase
	syncConfigFromEnv      = config.SyncConfigFromEnv
	newServer              = server.NewServer
	getDefaultServerConfig = server.GetDefaultServerConfig
	startServer            = func(srv *server.Server, cfg server.ServerConfig) error {
		return srv.Start(cfg)
	}
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "audiobook-organizer",
	Short: "Organize audiobooks into series and generate playlists",
	Long: `Audiobook Organizer scans your audiobook files, identifies series
using metadata and filenames, and generates iTunes-compatible playlists.

It also updates metadata tags in the audio files to include series information.`,
}

// scanCmd represents the scan command
var scanCmd = &cobra.Command{
	Use:   "scan",
	Short: "Scan audiobook directories",
	Long:  `Scan audiobook directories to identify books and series.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Setup file logging
		logFile, err := setupFileLogging()
		if err != nil {
			fmt.Printf("Warning: Could not setup file logging: %v\n", err)
		} else {
			defer logFile.Close()
		}

		if config.AppConfig.RootDir == "" {
			return fmt.Errorf("root directory not specified")
		}

		// Initialize database
		if _, err := initializeStore(config.AppConfig.DatabaseType, config.AppConfig.DatabasePath, config.AppConfig.EnableSQLite); err != nil {
			return fmt.Errorf("failed to initialize database: %w", err)
		}
		defer closeStore()

		fmt.Printf("Using database: %s (%s)\n", config.AppConfig.DatabasePath, config.AppConfig.DatabaseType)
		fmt.Printf("Scanning directory: %s\n", config.AppConfig.RootDir)

		// Start scanning
		books, err := scanDirectory(cmd.Context(), config.AppConfig.RootDir, nil)
		if err != nil {
			return fmt.Errorf("scan error: %w", err)
		}

		fmt.Printf("Found %d audiobooks\n", len(books))

		// Process books and identify series
		if err := processBooks(books, nil); err != nil {
			return fmt.Errorf("processing error: %w", err)
		}

		return nil
	},
}

// playlistCmd represents the playlist command
var playlistCmd = &cobra.Command{
	Use:   "playlist",
	Short: "Generate playlists for audiobook series",
	Long:  `Generate iTunes-compatible playlists for each audiobook series.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Initialize database
		if _, err := initializeStore(config.AppConfig.DatabaseType, config.AppConfig.DatabasePath, config.AppConfig.EnableSQLite); err != nil {
			return fmt.Errorf("failed to initialize database: %w", err)
		}
		defer closeStore()

		fmt.Printf("Using database: %s (%s)\n", config.AppConfig.DatabasePath, config.AppConfig.DatabaseType)
		fmt.Println("Generating playlists for audiobook series...")

		// Generate playlists
		if err := generatePlaylists(); err != nil {
			return fmt.Errorf("failed to generate playlists: %w", err)
		}

		fmt.Printf("Playlists saved to: %s\n", config.AppConfig.PlaylistDir)
		return nil
	},
}

// tagCmd represents the tag command
var tagCmd = &cobra.Command{
	Use:   "tag",
	Short: "Update audio file tags with series information",
	Long:  `Update the metadata tags of audio files to include series information.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Initialize database
		if _, err := initializeStore(config.AppConfig.DatabaseType, config.AppConfig.DatabasePath, config.AppConfig.EnableSQLite); err != nil {
			return fmt.Errorf("failed to initialize database: %w", err)
		}
		defer closeStore()

		fmt.Printf("Using database: %s (%s)\n", config.AppConfig.DatabasePath, config.AppConfig.DatabaseType)
		fmt.Println("Updating audio file tags with series information...")

		// Update tags
		if err := updateSeriesTags(); err != nil {
			return fmt.Errorf("failed to update tags: %w", err)
		}

		return nil
	},
}

// organizeCmd represents the organize command
var organizeCmd = &cobra.Command{
	Use:   "organize",
	Short: "Run the complete organization process",
	Long:  `Scan audiobooks, identify series, generate playlists, and update tags.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Setup file logging
		logFile, err := setupFileLogging()
		if err != nil {
			fmt.Printf("Warning: Could not setup file logging: %v\n", err)
		} else {
			defer logFile.Close()
		}

		if config.AppConfig.RootDir == "" {
			return fmt.Errorf("root directory not specified")
		}

		// Initialize database
		if _, err := initializeStore(config.AppConfig.DatabaseType, config.AppConfig.DatabasePath, config.AppConfig.EnableSQLite); err != nil {
			return fmt.Errorf("failed to initialize database: %w", err)
		}
		defer closeStore()

		// Step 1: Scan files
		fmt.Printf("Using database: %s (%s)\n", config.AppConfig.DatabasePath, config.AppConfig.DatabaseType)
		fmt.Printf("Scanning directory: %s\n", config.AppConfig.RootDir)
		books, err := scanDirectory(cmd.Context(), config.AppConfig.RootDir, nil)
		if err != nil {
			return fmt.Errorf("scan error: %w", err)
		}
		fmt.Printf("Found %d audiobooks\n", len(books))

		// Step 2: Process books and identify series
		fmt.Println("Processing audiobooks and identifying series...")
		if err := processBooks(books, nil); err != nil {
			return fmt.Errorf("processing error: %w", err)
		}

		// Step 3: Generate playlists
		fmt.Println("Generating playlists...")
		if err := generatePlaylists(); err != nil {
			return fmt.Errorf("playlist generation error: %w", err)
		}

		// Step 4: Update tags
		fmt.Println("Updating audio file tags...")
		if err := updateSeriesTags(); err != nil {
			return fmt.Errorf("tag update error: %w", err)
		}

		fmt.Println("\nAudiobook organization complete!")
		fmt.Printf("- Database: %s\n", config.AppConfig.DatabasePath)
		fmt.Printf("- Playlists: %s\n", config.AppConfig.PlaylistDir)

		return nil
	},
}

// serveCmd represents the serve command
var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the web server",
	Long:  `Start the web server to provide a web interface for audiobook management.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Setup file logging (logs to both file and stdout)
		logFile, err := setupFileLogging()
		if err != nil {
			fmt.Printf("Warning: Could not setup file logging: %v\n", err)
			fmt.Println("Continuing with stdout-only logging...")
		} else {
			defer logFile.Close()
		}

		// Initialize database. Capture the returned Store so we can pass
		// it explicitly down the call chain (SERVER-GLOBAL-STORE-AUDIT).
		store, err := initializeStore(config.AppConfig.DatabaseType, config.AppConfig.DatabasePath, config.AppConfig.EnableSQLite)
		if err != nil {
			return fmt.Errorf("failed to initialize database: %w", err)
		}
		defer closeStore()

		fmt.Printf("Using database: %s (%s)\n", config.AppConfig.DatabasePath, config.AppConfig.DatabaseType)

		// Initialize OpenTelemetry (optional; disabled if OTEL_EXPORTER_OTLP_ENDPOINT not set)
		otelCfg := telemetry.LoadConfig("audiobook-organizer")
		otelShutdown, err := telemetry.InitOTEL(context.Background(), otelCfg)
		if err != nil {
			return fmt.Errorf("failed to initialize OpenTelemetry: %w", err)
		}
		defer otelShutdown(context.Background())

		// Initialize encryption for settings (generates key if needed)
		dbDir := filepath.Dir(config.AppConfig.DatabasePath)
		if err := initEncryption(dbDir); err != nil {
			return fmt.Errorf("failed to initialize encryption: %w", err)
		}
		fmt.Println("Settings encryption initialized")

		// Load configuration from database (overrides defaults with persisted values)
		if err := loadConfigFromDB(store); err != nil {
			fmt.Printf("Warning: Could not load config from database: %v\n", err)
		} else {
			fmt.Println("Configuration loaded from database")
			fmt.Printf("  - Root dir (organize output): %s\n", config.AppConfig.RootDir)
			fmt.Printf("  - OpenAI API key: %s\n", func() string {
				if config.AppConfig.OpenAIAPIKey != "" {
					return "***" + config.AppConfig.OpenAIAPIKey[len(config.AppConfig.OpenAIAPIKey)-4:]
				}
				return "(not set)"
			}())
		}

		// Apply env var overrides (command line takes precedence over DB)
		syncConfigFromEnv()

		if err := config.AppConfig.Validate(); err != nil {
			return fmt.Errorf("invalid configuration: %w", err)
		}

		// Log import path count
		if folders, err := store.GetAllImportPaths(); err == nil {
			fmt.Printf("  - Import paths (scan paths): %d configured\n", len(folders))
			for _, folder := range folders {
				fmt.Printf("    * %s (%s)\n", folder.Name, folder.Path)
			}
		}

		fmt.Println("Starting audiobook organizer web server...")

		// Create and start server. Store is passed explicitly per the 4.4 DI
		// migration; database.GlobalStore remains assigned for call sites that
		// haven't yet been migrated to use s.Store().
		srv := newServer(store)

		fmt.Println("Server initialized (hub, batcher, file I/O pool)")
		cfg := getDefaultServerConfig()

		// Apply command line flags (use defaults or user-provided values)
		cfg.Port = cmd.Flag("port").Value.String()
		cfg.Host = cmd.Flag("host").Value.String()

		if rt := cmd.Flag("read-timeout").Value.String(); rt != "" {
			if d, err := time.ParseDuration(rt); err == nil {
				cfg.ReadTimeout = d
			}
		}
		if wt := cmd.Flag("write-timeout").Value.String(); wt != "" {
			if d, err := time.ParseDuration(wt); err == nil {
				cfg.WriteTimeout = d
			}
		}
		if it := cmd.Flag("idle-timeout").Value.String(); it != "" {
			if d, err := time.ParseDuration(it); err == nil {
				cfg.IdleTimeout = d
			}
		}

		// TLS configuration (defaults to snake oil certs)
		cfg.TLSCertFile = cmd.Flag("tls-cert").Value.String()
		cfg.TLSKeyFile = cmd.Flag("tls-key").Value.String()
		cfg.HTTP3Port = cmd.Flag("http3-port").Value.String()

		// Public-facing origin for absolute link generation (e.g. temp-login URLs).
		cfg.ExternalURL = cmd.Flag("external-url").Value.String()

		return startServer(srv, cfg)
	},
}

// SetVersion sets the version string on the root command.
// Called from main before Execute so --version and the version command work.
func SetVersion(v string) {
	rootCmd.Version = v
}

// Execute adds all child commands to the root command and sets flags appropriately
func Execute() error {
	// Set umask on Unix so os.Create yields 0664 and os.MkdirAll yields 0775.
	// This preserves group-write required for POSIX ACLs.
	setUmask()
	return rootCmd.Execute()
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.audiobook-organizer.yaml)")
	rootCmd.PersistentFlags().StringVar(&rootDir, "dir", "", "root directory containing audiobooks")
	rootCmd.PersistentFlags().StringVar(&databasePath, "db", "audiobooks.pebble", "path to database (default: audiobooks.pebble for PebbleDB)")
	rootCmd.PersistentFlags().StringVar(&databaseType, "db-type", "pebble", "database type: pebble (default) or sqlite")
	rootCmd.PersistentFlags().BoolVar(&enableSQLite, "enable-sqlite3-i-know-the-risks", false, "enable SQLite3 database (WARNING: cross-compilation issues, PebbleDB recommended)")
	rootCmd.PersistentFlags().StringVar(&playlistDir, "playlists", "playlists", "directory to store generated playlists")
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "info", "log level: debug, info, warn, error")

	viper.BindPFlag("root_dir", rootCmd.PersistentFlags().Lookup("dir"))
	viper.BindPFlag("database_path", rootCmd.PersistentFlags().Lookup("db"))
	viper.BindPFlag("database_type", rootCmd.PersistentFlags().Lookup("db-type"))
	viper.BindPFlag("enable_sqlite3_i_know_the_risks", rootCmd.PersistentFlags().Lookup("enable-sqlite3-i-know-the-risks"))
	viper.BindPFlag("playlist_dir", rootCmd.PersistentFlags().Lookup("playlists"))

	rootCmd.AddCommand(scanCmd)
	rootCmd.AddCommand(playlistCmd)
	rootCmd.AddCommand(tagCmd)
	rootCmd.AddCommand(organizeCmd)
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(diagnosticsCmd)
	rootCmd.AddCommand(metadataInspectCmd)
	rootCmd.AddCommand(seedCmd)

	// Add serve command specific flags
	serveCmd.Flags().String("port", "8484", "port to run the web server on")
	serveCmd.Flags().String("host", "localhost", "host to bind the web server to")
	serveCmd.Flags().String("read-timeout", "0s", "read timeout (0s disables timeout for SSE compatibility)")
	serveCmd.Flags().String("write-timeout", "0s", "write timeout (0s disables timeout for SSE compatibility)")
	serveCmd.Flags().String("idle-timeout", "120s", "idle timeout (e.g. 60s, 2m)")
	serveCmd.Flags().String("tls-cert", "certs/localhost.crt", "TLS certificate file for HTTPS/HTTP2/HTTP3")
	serveCmd.Flags().String("tls-key", "certs/localhost.key", "TLS key file for HTTPS/HTTP2/HTTP3")
	serveCmd.Flags().String("http3-port", "8484", "HTTP/3 (QUIC) port on UDP (same as --port for best compatibility)")
	serveCmd.Flags().String("external-url", "", "public-facing origin used in generated links, e.g. https://books.example.com (prevents Host-header injection)")
	serveCmd.Flags().Int("workers", 2, "number of background operation workers")

	metadataInspectCmd.Flags().StringVar(&metadataInspectFile, "file", "", "audio file to inspect (can also pass as positional argument)")
}

var metadataInspectCmd = &cobra.Command{
	Use:   "inspect-metadata [file]",
	Short: "Extract metadata for a single audiobook file",
	Long: `Runs the metadata extraction pipeline for a single file and prints the
results, including technical details from mediainfo. Useful for debugging
broken tags or filename fallbacks without running a full scan.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		target := metadataInspectFile
		if target == "" && len(args) > 0 {
			target = args[0]
		}
		if target == "" {
			return fmt.Errorf("file path must be provided via --file or positional argument")
		}

		target = filepath.Clean(target)
		if _, err := os.Stat(target); err != nil {
			return fmt.Errorf("file not accessible: %w", err)
		}

		fmt.Printf("Inspecting metadata for %s\n", target)
		meta, err := metadata.ExtractMetadata(target, nil)
		if err != nil {
			return fmt.Errorf("metadata extraction failed: %w", err)
		}

		fmt.Println("\nMetadata Fields:")
		printMetadataField("Title", meta.Title)
		printMetadataField("Author", meta.Artist)
		printMetadataField("Series", meta.Series)
		fmt.Printf("  Series Position: %d\n", meta.SeriesIndex)
		printMetadataField("Narrator", meta.Narrator)
		printMetadataField("Album", meta.Album)
		printMetadataField("Genre", meta.Genre)
		printMetadataField("Language", meta.Language)
		printMetadataField("Publisher", meta.Publisher)
		fmt.Printf("  Year: %d\n", meta.Year)
		printMetadataField("ISBN-10", meta.ISBN10)
		printMetadataField("ISBN-13", meta.ISBN13)
		printMetadataField("Comments", meta.Comments)

		if info, miErr := mediainfo.Extract(target); miErr == nil {
			fmt.Println("\nMedia Info:")
			printMetadataField("Codec", info.Codec)
			fmt.Printf("  Bitrate: %dkbps\n", info.Bitrate)
			fmt.Printf("  Sample Rate: %dHz\n", info.SampleRate)
			fmt.Printf("  Channels: %d\n", info.Channels)
			fmt.Printf("  Bit Depth: %d\n", info.BitDepth)
			printMetadataField("Quality", info.Quality)
		} else {
			fmt.Printf("\nMedia info unavailable: %v\n", miErr)
		}

		fmt.Println("\nDetailed extraction sources are logged with TRACE level in metadata package logs.")
		return nil
	},
}

func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		home, err := os.UserHomeDir()
		cobra.CheckErr(err)

		viper.AddConfigPath(home)
		viper.SetConfigType("yaml")
		viper.SetConfigName(".audiobook-organizer")
	}

	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err == nil {
		fmt.Println("Using config file:", viper.ConfigFileUsed())
	}

	// Create playlist directory if it doesn't exist
	if playlistDir != "" {
		if err := os.MkdirAll(playlistDir, 0775); err != nil {
			fmt.Printf("Error creating playlist directory: %v\n", err)
		}
	}

	// Ensure database directory exists
	if databasePath != "" {
		dbDir := filepath.Dir(databasePath)
		if dbDir != "." {
			if err := os.MkdirAll(dbDir, 0775); err != nil {
				fmt.Printf("Error creating database directory: %v\n", err)
			}
		}
	}

	config.InitConfig()
	if logLevel != "" {
		config.AppConfig.LogLevel = logLevel
	}
	if err := config.AppConfig.Validate(); err != nil {
		fmt.Printf("Warning: configuration validation failed: %v\n", err)
	}

	// Select which library sort fields get a memdb sorted index. This MUST
	// happen before any store is opened: memdbSchema() reads the enabled set
	// once, when NewMemStore builds the schema. Doing it here — right after
	// config load, in the single cobra.OnInitialize hook — covers every
	// command that later calls initializeStore, rather than being repeated
	// at each of the four call sites and eventually missed at one.
	//
	// Unknown names are warned about rather than ignored: a typo in config
	// would otherwise silently leave a sort on the slow path with no
	// indication why.
	if unknown := database.SetEnabledSortIndexes(config.AppConfig.EnabledSortIndexes); len(unknown) > 0 {
		fmt.Printf("Warning: unknown enabled_sort_indexes entries ignored: %v\n", unknown)
	}
}

func printMetadataField(label, value string) {
	fmt.Printf("  %s: %s\n", label, formatMetadataValue(value))
}

func formatMetadataValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(empty)"
	}
	return value
}

// setupFileLogging configures logging to write to both a file and stdout.
// Creates logs directory if it doesn't exist and opens/creates a log file with timestamps.
// Returns the log file handle which should be closed when done.
func setupFileLogging() (*os.File, error) {
	// Create logs directory if it doesn't exist
	logsDir := "logs"
	if err := os.MkdirAll(logsDir, 0775); err != nil {
		return nil, fmt.Errorf("failed to create logs directory: %w", err)
	}

	// Create log file with timestamp
	timestamp := time.Now().Format("2006-01-02")
	logFile := filepath.Join(logsDir, fmt.Sprintf("audiobook-organizer-%s.log", timestamp))

	// Open log file (append mode, create if doesn't exist)
	file, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0664)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}

	// Create multi-writer to write to both file and stdout
	multiWriter := io.MultiWriter(os.Stdout, file)
	handler := slog.NewTextHandler(multiWriter, &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(handler))

	slog.Info("Audiobook Organizer Started")
	slog.Info("log file", "path", logFile)

	return file, nil
}
