// file: cmd/root.go
// version: 1.31.0
// guid: 6a7b8c9d-0e1f-2a3b-4c5d-6e7f8a9b0c1d
// last-edited: 2026-11-19

package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/books"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/mediainfo"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/playlist"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
	"github.com/falkcorp/audiobook-organizer/internal/server"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/module"
	"github.com/spf13/cobra"
)

var cfgFile string
var rootDir string
var databasePath string
var databaseType string
var playlistDir string
var logLevel string
var metadataInspectFile string

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
	scanDirectory          = scanner.ScanDirectory // func(context.Context, string, logger.Logger) ([]Book, error)
	processBooks           = scanner.ProcessBooks  // func([]Book, logger.Logger) error
	generatePlaylists      = playlist.GeneratePlaylistsForSeries
	updateSeriesTags       = tagger.UpdateSeriesTags
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

		// Open the store through the guard-aware offline open (openCLIStore).
		_, closeCLIStore, err := openCLIStore(commandContext(cmd), false)
		if err != nil {
			return fmt.Errorf("failed to initialize database: %w", err)
		}
		defer closeCLIStore()

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
		// Open the store through the guard-aware offline open (openCLIStore).
		_, closeCLIStore, err := openCLIStore(commandContext(cmd), false)
		if err != nil {
			return fmt.Errorf("failed to initialize database: %w", err)
		}
		defer closeCLIStore()

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
		// Open the store through the guard-aware offline open (openCLIStore).
		_, closeCLIStore, err := openCLIStore(commandContext(cmd), false)
		if err != nil {
			return fmt.Errorf("failed to initialize database: %w", err)
		}
		defer closeCLIStore()

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

		// Open the store through the guard-aware offline open (openCLIStore).
		_, closeCLIStore, err := openCLIStore(commandContext(cmd), false)
		if err != nil {
			return fmt.Errorf("failed to initialize database: %w", err)
		}
		defer closeCLIStore()

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

		// TLS: the files reach the listener as core.http.tls.* (the flags are
		// flag layers, serveBootInputs); these two only describe the process
		// to the server config until the listener's address and files replace
		// them.
		cfg.TLSCertFile = cmd.Flag("tls-cert").Value.String()
		cfg.TLSKeyFile = cmd.Flag("tls-key").Value.String()
		cfg.HTTP3Port = cmd.Flag("http3-port").Value.String()

		// Public-facing origin for absolute link generation (e.g. temp-login URLs).
		cfg.ExternalURL = cmd.Flag("external-url").Value.String()

		boot, err := serveBootInputs(cmd.Flags())
		if err != nil {
			return err
		}
		return runApp(cmd.Context(), books.ServeInputs{Server: cfg, Telemetry: telemetryCfg, FromRelease: fromRelease, Boot: boot})
	},
}

// fromRelease is the pre-freeze release's tag main embeds (09 §9.2.1); serve
// hands it to books as ServeInputs.FromRelease.
var fromRelease string

// SetFromRelease records the pre-freeze release's tag. Called from main before
// Execute, like SetVersion.
func SetFromRelease(tag string) { fromRelease = tag }

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
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if cmd.Annotations[skipAppInitAnnotation] == "true" {
			return nil
		}
		return initConfig()
	}

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "operator config file (YAML, nested by key path); there is no default, and a $HOME/.audiobook-organizer.yaml is not read")
	rootCmd.PersistentFlags().StringVar(&rootDir, "dir", "", "root directory containing audiobooks")
	rootCmd.PersistentFlags().StringVar(&databasePath, "db", "audiobooks.pebble", "path to database (default: audiobooks.pebble for PebbleDB)")
	rootCmd.PersistentFlags().StringVar(&databaseType, "db-type", "pebble", "database type: pebble (default) or sqlite")
	rootCmd.PersistentFlags().StringVar(&playlistDir, "playlists", "playlists", "directory to store generated playlists")
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "info", "log level: debug, info, warn, error")

	// The removed --enable-sqlite3-i-know-the-risks flag: accepted, ignored,
	// and warned about, never an error. Passing it makes pflag print
	// "Flag --enable-sqlite3-i-know-the-risks has been deprecated, <message>"
	// and cobra flushes that line to stderr once the parse succeeds. Both
	// deploy units route stderr to the journal (StandardError=journal) and the
	// launchd plists to their .err file, so that one line is the warning;
	// there is deliberately no second slog line.
	//
	// It is bound to nothing: no variable and no config layer.
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
	addOfflineCommands(rootCmd, func(o app.Options) []module.Module { return []module.Module{books.OfflineModule(o)} })

	// Add serve command specific flags
	serveCmd.Flags().String("port", "8484", "port to run the web server on")
	serveCmd.Flags().String("host", "localhost", "host to bind the web server to")
	serveCmd.Flags().String("read-timeout", "0s", "read timeout (0s disables timeout for SSE compatibility)")
	serveCmd.Flags().String("write-timeout", "0s", "write timeout (0s disables timeout for SSE compatibility)")
	serveCmd.Flags().String("idle-timeout", "120s", "idle timeout (e.g. 60s, 2m)")
	serveCmd.Flags().String("tls-cert", "", "TLS certificate file for HTTPS/HTTP2/HTTP3 (core.http.tls.cert_file; with --tls-key; none = HTTP only)")
	serveCmd.Flags().String("tls-key", "", "TLS key file for HTTPS/HTTP2/HTTP3 (core.http.tls.key_file)")
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
// config projection, no playlist or database directories.
const skipAppInitAnnotation = "audiobook-organizer/skip-app-init"

func initConfig() error {
	// The operator layers alone (ABO's compiled defaults, the operator file
	// --config or ABO_CONFIG names, the ABO_* environment and the flags the
	// operator set), projected into HEAD's Config by the books module's own
	// Engine (F3-07 slice 3): no store is opened, no boot record read and no
	// directory created, so a retired name that would refuse the boot
	// refuses here first. serve's Facade replaces this projection with the
	// stored settings once the App boots.
	boot, err := rootBootInputs()
	if err != nil {
		return err
	}
	cfg, err := books.OperatorAppConfig(context.Background(), books.Module(), boot)
	if err != nil {
		return err
	}
	// --dir and --playlists are not core keys (the library root and the
	// playlist directory are stored settings), so they feed the commands that
	// read them here, as HEAD's flag binding did: only when set.
	flags := rootCmd.PersistentFlags()
	if f := flags.Lookup("dir"); f != nil && f.Changed {
		cfg.RootDir = rootDir
	}
	if f := flags.Lookup("playlists"); f != nil && f.Changed {
		cfg.PlaylistDir = playlistDir
	}
	// The library root an operator layer names (--dir, or
	// ABO_CORE_LIBRARY_ROOTS / the operator file, which the projection
	// overlays on root_dir) overrides the stored one in the offline reset
	// plan, as the boot's lock would.
	config.SetOperatorRootDir(cfg.RootDir)
	config.Project(cfg)

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

	if err := config.AppConfig.Validate(); err != nil {
		fmt.Printf("Warning: configuration validation failed: %v\n", err)
	}

	// The store tunables every CLI open path reads (openCLIStore, the
	// op-runner child): memdbSchema() reads the enabled sort indexes once,
	// when NewMemStore builds the schema, so they are set before any store
	// opens. serve sets them again in books' Register from the booted store,
	// whose stored value this projection cannot see.
	books.ApplyStoreTunables(config.AppConfig.EnabledSortIndexes, config.AppConfig.LibraryCountsCacheMinIntervalSeconds)
	return nil
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
