// file: cmd/root.go
// version: 1.30.0
// guid: 6a7b8c9d-0e1f-2a3b-4c5d-6e7f8a9b0c1d
// last-edited: 2026-11-08

package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/books"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/mediainfo"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/playlist"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
	"github.com/falkcorp/audiobook-organizer/internal/server"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// shutdownBudgetEnv is the R1 reading of core.shutdown.budget's operator name (05-health §5H.4;
// 05-config §1.12.1). F3 deletes it with R1Values: core config reads the variable from then on.
// serve reads it first thing and passes it as books.ServeInputs.ShutdownBudget, which books.Options
// sets as R1Values.ShutdownBudget.
const shutdownBudgetEnv = "ABO_CORE_SHUTDOWN_BUDGET"

// shutdownBudgetFromEnv returns 0 (core's default) when the variable is unset or empty, the parsed
// duration when it is a positive time.ParseDuration value, and an error naming the variable otherwise.
func shutdownBudgetFromEnv(lookup func(string) (string, bool)) (time.Duration, error) {
	raw, ok := lookup(shutdownBudgetEnv)
	if !ok || raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", shutdownBudgetEnv, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s: %q must be a positive duration", shutdownBudgetEnv, raw)
	}
	return d, nil
}

var cfgFile string
var rootDir string
var databasePath string
var databaseType string
var playlistDir string
var logLevel string
var metadataInspectFile string

// persistentFlagConfigKeys maps each root persistent flag to the config key it
// feeds. It drives BOTH viper.BindPFlag and the explicit-flag capture in
// initConfig, deliberately: those two loops have to agree about which flags
// exist, and a table is the only way to guarantee they still agree after
// someone adds the next flag.
//
// The capture exists because viper flattens flags, environment, config file and
// the persisted DB blob into one namespace and remembers only the winning
// VALUE, never who supplied it. config.LoadConfigFromDatabase later overlays
// the blob on top, at which point "the operator typed --db" is unrecoverable —
// and neither viper.IsSet nor viper.Get can recover it, for reasons measured and
// tabulated on config.explicitFlags. cobra's Changed() is the only API that
// answers the question, and only while the parse is fresh.
var persistentFlagConfigKeys = map[string]string{
	"dir":       "root_dir",
	"db":        "database_path",
	"db-type":   "database_type",
	"playlists": "playlist_dir",
}

// removedSQLiteFlag is the CLI flag TASK-020 (#3268) deleted. It is still
// registered — hidden, deprecated, and bound to nothing — so that an off-repo
// systemd unit, script or alias that passes it keeps starting instead of dying
// on cobra's "unknown flag". See its registration in init.
const removedSQLiteFlag = "enable-sqlite3-i-know-the-risks"

// removedSQLiteFlagMessage completes pflag's
// "Flag --enable-sqlite3-i-know-the-risks has been deprecated, <message>".
const removedSQLiteFlagMessage = "it was removed and does nothing: SQLite is no longer selectable and " +
	"PebbleDB is the only database backend; remove it from your command line"

var (
	initializeStore        = database.InitializeStore
	closeStore             = database.CloseStore
	scanDirectory          = scanner.ScanDirectory // func(context.Context, string, logger.Logger) ([]Book, error)
	processBooks           = scanner.ProcessBooks  // func([]Book, logger.Logger) error
	generatePlaylists      = playlist.GeneratePlaylistsForSeries
	updateSeriesTags       = tagger.UpdateSeriesTags
	loadConfigFromDB       = config.LoadConfigFromDatabase
	newServer              = server.NewServer
	getDefaultServerConfig = server.GetDefaultServerConfig
	// runApp is serve's composition root (serve_app.go): app.New(ctx, opts,
	// books.Module()) and Run.
	runApp = runComposedApp
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
		if config.AppConfig.RootDir == "" {
			return fmt.Errorf("root directory not specified")
		}
		defer logger.InstallStdoutDefault(config.AppConfig.LogLevel, os.Stderr)()

		// Initialize database
		if _, err := initializeStore(config.AppConfig.DatabaseType, config.AppConfig.DatabasePath); err != nil {
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
		if _, err := initializeStore(config.AppConfig.DatabaseType, config.AppConfig.DatabasePath); err != nil {
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
		if _, err := initializeStore(config.AppConfig.DatabaseType, config.AppConfig.DatabasePath); err != nil {
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
		if config.AppConfig.RootDir == "" {
			return fmt.Errorf("root directory not specified")
		}
		defer logger.InstallStdoutDefault(config.AppConfig.LogLevel, os.Stderr)()

		// Initialize database
		if _, err := initializeStore(config.AppConfig.DatabaseType, config.AppConfig.DatabasePath); err != nil {
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
		// An invalid ABO_CORE_SHUTDOWN_BUDGET refuses the start, before the
		// store opens or anything binds (R1-33).
		shutdownBudget, err := shutdownBudgetFromEnv(os.LookupEnv)
		if err != nil {
			return fmt.Errorf("serve: %w", err)
		}

		// Lines before app.New installs the log pipeline at P5 (the config
		// load here, and the kernel's store open, checkpoint and migrations at
		// P2 to P4b) go to standard output at log_level, as the rest of serve's
		// output does, not to slog's stock default on standard error. Books'
		// Register (the key guard, encryption init, settings load) runs at P6,
		// under the App's pipeline. The App's process install stacks on this
		// and restores it. nil: Options reports an unknown level once, later.
		defer logger.InstallStdoutDefault(config.AppConfig.LogLevel, nil)()

		// The kernel opens the store at P2 (R1-23): HEAD's pre-server steps
		// (the key guard, the encryption init, the settings load and env
		// sync, config.Validate) run in books' Register over that store, and
		// the secure state directory resolves in books.Options, each through
		// the startup runner books makes (06 §6.4.3: serve runs no startup
		// step of its own).

		// The telemetry settings as HEAD's InitOTEL read them: before the
		// stored settings and env overrides apply. The App's Provider
		// (app.New) builds the exporters from them; its Run flushes them on
		// stop within the shutdown budget.
		telemetryCfg := config.Snapshot()

		fmt.Printf("Using database: %s (%s)\n", config.AppConfig.DatabasePath, config.AppConfig.DatabaseType)
		fmt.Println("Starting audiobook organizer web server...")

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

		return runApp(cmd.Context(), books.ServeInputs{Server: cfg, Telemetry: telemetryCfg, ShutdownBudget: shutdownBudget})
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
	// App setup runs as the root's PersistentPreRun rather than a
	// cobra.OnInitialize hook, so it knows which command is running and can
	// skip commands annotated skipAppInitAnnotation (fp-worker). No
	// subcommand defines its own PersistentPreRun, so this one runs for all.
	rootCmd.PersistentPreRun = func(cmd *cobra.Command, _ []string) {
		if cmd.Annotations[skipAppInitAnnotation] == "true" {
			return
		}
		initConfig()
	}

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.audiobook-organizer.yaml)")
	rootCmd.PersistentFlags().StringVar(&rootDir, "dir", "", "root directory containing audiobooks")
	rootCmd.PersistentFlags().StringVar(&databasePath, "db", "audiobooks.pebble", "path to database (default: audiobooks.pebble for PebbleDB)")
	rootCmd.PersistentFlags().StringVar(&databaseType, "db-type", "pebble", "database type: pebble (default) or sqlite")
	rootCmd.PersistentFlags().StringVar(&playlistDir, "playlists", "playlists", "directory to store generated playlists")
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "info", "log level: debug, info, warn, error")

	for flagName, configKey := range persistentFlagConfigKeys {
		viper.BindPFlag(configKey, rootCmd.PersistentFlags().Lookup(flagName))
	}

	// The removed --enable-sqlite3-i-know-the-risks flag: accepted, ignored,
	// and warned about, never an error. Passing it makes pflag print
	// "Flag --enable-sqlite3-i-know-the-risks has been deprecated, <message>"
	// and cobra flushes that line to stderr once the parse succeeds. Both
	// deploy units route stderr to the journal (StandardError=journal) and the
	// launchd plists to their .err file, so that one line is the warning;
	// there is deliberately no second slog line.
	//
	// It is bound to nothing: no variable, and NOT in persistentFlagConfigKeys.
	// A viper binding would expose enable_sqlite3_i_know_the_risks to
	// config.warnRemovedViperKeys and log the removed-key WARN a second time.
	rootCmd.PersistentFlags().Bool(removedSQLiteFlag, false, "removed; ignored (PebbleDB is the only database backend)")
	if err := rootCmd.PersistentFlags().MarkDeprecated(removedSQLiteFlag, removedSQLiteFlagMessage); err != nil {
		panic(err)
	}
	if err := rootCmd.PersistentFlags().MarkHidden(removedSQLiteFlag); err != nil {
		panic(err)
	}

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

// skipAppInitAnnotation marks a command that must not run initConfig: no
// config file, no viper.AutomaticEnv, no playlist or database directories.
const skipAppInitAnnotation = "audiobook-organizer/skip-app-init"

func initConfig() {
	// FIRST: record which flags the operator actually typed, before any config
	// layer is loaded. cobra.OnInitialize runs after flag parsing and before the
	// command body, so this is the earliest point where Changed() is both
	// populated and still ahead of every consumer.
	for flagName, configKey := range persistentFlagConfigKeys {
		if rootCmd.PersistentFlags().Changed(flagName) {
			config.MarkFlagExplicit(configKey)
		}
	}

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
		config.Mutate(func(c *config.Config) { c.LogLevel = logLevel })
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
		fmt.Printf("Warning: unknown enabled_sort_indexes entries ignored: %v "+
			"(the environment form is space-separated: ENABLED_SORT_INDEXES=\"year created_at\")\n", unknown)
	}
	// Report what was BUILT, not what was asked for. enabled_sort_indexes is
	// boot-only: this is the value GET /api/v1/config shows for the life of
	// the process, LoadConfigFromDatabase pins it over whatever the stored
	// blob holds, and UpdateConfig refuses a PUT that changes it. Writing the
	// accepted set back drops unknown names, so a typo is not reported as an
	// enabled index.
	builtSortIndexes := database.EnabledSortIndexFields()
	config.Mutate(func(c *config.Config) { c.EnabledSortIndexes = builtSortIndexes })

	// Same import-cycle constraint as SetEnabledSortIndexes above:
	// internal/database can't import internal/config directly.
	database.SetLibraryCountsCacheMinInterval(config.AppConfig.LibraryCountsCacheMinIntervalSeconds)
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
