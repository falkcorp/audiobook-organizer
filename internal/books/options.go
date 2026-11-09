// file: internal/books/options.go
// version: 1.2.2
// guid: 3c47c7cc-6394-498f-977b-0d0fb19df982
// last-edited: 2026-11-08

package books

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/backup"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/server"
	system "github.com/falkcorp/audiobook-organizer/internal/server/handlers/system"
	"github.com/falkcorp/audiobook-organizer/internal/startup"
	"github.com/falkcorp/audiobook-organizer/internal/telemetry"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
)

// ABO's identity on core (03 §3.6, 05-metrics §2): the app ID, the metric
// namespace every served family carries and the OTel service name. They are
// the values HEAD's telemetry used, so no series changes name.
const (
	// AppID is Options.AppID. main's --version-json prints it as "app", the
	// value the updater's self-test compares with its own app ID (06 §6.4.5),
	// so both read this one constant.
	AppID           = "abo"
	metricNamespace = "audiobook_organizer"
	serviceName     = "audiobook-organizer"
	displayName     = "Audiobook Organizer"
)

// ServeInputs are the values of serve books needs.
type ServeInputs struct {
	// Server is HEAD's listener flags (--host, --port, the timeouts, the TLS
	// pair, --http3-port, --external-url). Its StartupSteps is the startup
	// runner; nil = Options makes one. Books runs its steps through it and
	// hands it to the server, so /health reports every degraded step.
	Server server.ServerConfig
	// Telemetry is the config snapshot HEAD's InitOTEL read: before the
	// stored settings and env overrides apply (books' settings boot, in
	// Register), so the OTLP endpoints, interval and environment come from
	// where they always did.
	Telemetry config.Config
	// ShutdownBudget is core.shutdown.budget as serve read it from
	// ABO_CORE_SHUTDOWN_BUDGET (R1-33). Zero is core's default (25s).
	ShutdownBudget time.Duration
	// LogOut is the stream the log pipeline prints to, beside the file when
	// there is one; nil is os.Stdout, where HEAD's lines went.
	LogOut io.Writer
}

// Options is ABO's app.Options for release R1 (06 §6.4.10, 05-config
// §1.12.1), for mod, the value Module returned:
//
//   - ABO's identity and build version;
//   - the store core opens: R1StoragePath is HEAD's database path, made
//     absolute (a database_type other than Pebble, or no path, refuses);
//     StateDir is the secure state directory ABO resolves (EnsureSecureStateDir,
//     HEAD's resolver, ABK_STATE_DIR and its fallback beside the database),
//     which core's ResolveStateDir must agree with; StorageStateFiles is the
//     state-file table the first-boot checkpoint copies (StateFiles);
//   - R1Values: the listener and perimeter values of the effective config
//     (stored settings and env overrides applied), which the kernel reads
//     once at start step 7; ShutdownBudget is ServeInputs.ShutdownBudget
//     (serve's ABO_CORE_SHUTDOWN_BUDGET reading, 0 = core's default);
//   - R1Listen: HEAD's listener stack behind the perimeter shim
//     (internal/server R1Listener);
//   - Telemetry: HEAD's OTLP tracing and metric push and its environment;
//   - LegacyAuthGinUntil: books, whose gin mount keeps HEAD's authentication
//     until the identity flip;
//   - ReservedAPINamespaces: the ABS protocol's /api/<seg> names;
//   - LogOutput, R1Log and StateDir: the log pipeline's stream, its level
//     (log_level, --log-level) and its file under the state directory
//     (logSettings); the activity feed is a tap on the pipeline, registered
//     by Attach;
//   - ProcessDefaults: serve calls app.InstallProcessDefaults, so New
//     installs the OTel SDK's value-dropping logger and error handler before
//     it builds the Provider and the OTLP exporters, which read OTEL_* from
//     the environment (HEAD's InitOTEL installed its sink first for the same
//     reason).
func Options(mod module.Module, in ServeInputs) (app.Options, error) {
	m, err := asBooks(mod)
	if err != nil {
		return app.Options{}, err
	}
	cfg := in.Server
	if cfg.StartupSteps == nil {
		cfg.StartupSteps = newSteps()
	}
	m.steps = cfg.StartupSteps
	m.cfg.Store(&cfg)

	if err := m.steps.Run("EnsureSecureStateDir", func() error {
		dir, err := config.EnsureSecureStateDir()
		if err != nil {
			return fmt.Errorf("failed to create state directory %q: %w", dir, err)
		}
		m.stateDir = dir
		return nil
	}); err != nil {
		return app.Options{}, err
	}
	dbPath, err := storeLocation(m.stateDir)
	if err != nil {
		return app.Options{}, err
	}
	snap := config.Snapshot()
	// ABO's log file lives under the state directory (core.log.file.path,
	// 05-logging section 11 item 1).
	logCfg, warn := logSettings(snap.LogLevel, m.stateDir)
	if warn != "" {
		fmt.Fprintln(os.Stderr, "Warning: "+warn)
	}
	return app.Options{
		AppID:                 AppID,
		MetricNamespace:       metricNamespace,
		ServiceName:           serviceName,
		DisplayName:           displayName,
		Version:               telemetry.Version(),
		ReservedAPINamespaces: reservedAPINamespaces(),
		LegacyAuthGinUntil:    ID,
		R1Values:              func() app.R1Values { return r1Values(cfg, config.Snapshot(), in.ShutdownBudget) },
		R1Listen:              m.r1Listen,
		Telemetry:             telemetryFromConfig(in.Telemetry),
		LogOutput:             logStream(in.LogOut),
		R1Log:                 &logCfg,
		ProcessDefaults:       true,
		StateDir:              m.stateDir,
		R1StoragePath:         dbPath,
		StorageStateFiles:     StateFiles(m.stateDir, dbPath),
	}, nil
}

// storeLocation is HEAD's database path as core.storage.path, checked
// against the state directory ABO resolved: a database_type other than
// Pebble or an empty path refuses, and so does a core state directory that
// is not stateDir. One state directory per process (OPS4-8): core resolves
// it again in New (its explicit ABO_CORE_PATHS_STATE_DIR first, then this
// default), and HEAD's key, the checkpoint's state files and core's own
// state files must not land in two places.
func storeLocation(stateDir string) (string, error) {
	snap := config.Snapshot()
	if err := database.ValidateDatabaseType(snap.DatabaseType); err != nil {
		return "", err
	}
	dbPath, err := storagePath(snap.DatabasePath)
	if err != nil {
		return "", err
	}
	coreDir, err := app.ResolveStateDir(app.Options{AppID: AppID, StateDir: stateDir})
	if err != nil {
		return "", err
	}
	if filepath.Clean(coreDir) != filepath.Clean(stateDir) {
		return "", fmt.Errorf("books: core's state directory %q is not ABO's %q (%s); set one, or set both to the same path",
			coreDir, stateDir, config.SecureStateDirEnv)
	}
	return dbPath, nil
}

// OfflineOptions is ABO's app.Options for core's offline commands
// (app.OfflineCommands, 06 §6.4.5), which cmd wraps as cobra commands: the
// identity, the store and state directory serve's Options names, so an
// offline command and serve always reach the same database, serve's
// state-file table, the HNSW marker removal before a swap
// (removeHNSWCleanStop) and the archive extractor (backup.ExtractStore). It reads the
// loaded config (the root command's PersistentPreRun loads it) and builds
// no server, logging pipeline or telemetry.
func OfflineOptions() (app.Options, error) {
	stateDir, err := config.EnsureSecureStateDir()
	if err != nil {
		return app.Options{}, fmt.Errorf("failed to create state directory %q: %w", stateDir, err)
	}
	dbPath, err := storeLocation(stateDir)
	if err != nil {
		return app.Options{}, err
	}
	return app.Options{
		AppID: AppID,
		// serve's Version: the binary ID a staged swap records hashes it, so
		// a reset or restore staged offline must carry the one the serve
		// boot computes, or its P0a rejects the intent as another binary's.
		Version:       telemetry.Version(),
		StateDir:      stateDir,
		R1StoragePath: dbPath,
		// The state files serve's checkpoints copy (the encryption key):
		// storage restore-offline puts a checkpoint's copies back over them,
		// and the offline open reads the same table serve's open does.
		StorageStateFiles: StateFiles(stateDir, dbPath),
		// Before storage restore-offline or storage reset stages or moves
		// anything: the HNSW clean-stop marker of the store being replaced
		// goes, so the next boot reconciles the vector graph.
		R1BeforeSwap: removeHNSWCleanStop,
		// storage restore-offline --archive: HEAD's backup archives.
		R1ArchiveExtract: backup.ExtractStore,
		// The process logger, through internal/logger (the slog guard):
		// prune-checkpoints logs each deletion beside its stdout report.
		Logger: logger.FromContext(context.Background()).With("subsystem", "offline"),
	}, nil
}

// newSteps is the startup runner when serve hands none.
func newSteps() *startup.Steps {
	return startup.New().SetErrorLog(logger.New("startup").Error)
}

// errNoDatabasePath is Options' refusal of an empty database_path: core
// would open a new, empty store at its own default path, and books would
// serve an empty library looking healthy.
var errNoDatabasePath = errors.New("books: database_path is empty; set DATABASE_PATH or --db to the Pebble store")

// storagePath is HEAD's database path as core.storage.path: absolute, as
// HEAD's open resolved a relative one against the working directory.
func storagePath(dbPath string) (string, error) {
	if strings.TrimSpace(dbPath) == "" {
		return "", errNoDatabasePath
	}
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		return "", fmt.Errorf("books: database_path %q: %w", dbPath, err)
	}
	return abs, nil
}

// StateFiles is ABO's state-file table at R1 (05-config §2.4): the files the
// pre-migration checkpoint copies beside the store. encryption_key is HEAD's
// key where InitEncryption would read it (the state directory, else the
// legacy location bootSettings passes, the database's directory); gap_fill is HEAD's
// config.yaml beside the database. A file that does not exist is skipped by
// the checkpoint.
func StateFiles(stateDir, dbPath string) []storage.StateFile {
	keyDir := database.FindEncryptionKey(stateDir, filepath.Dir(dbPath))
	if keyDir == "" {
		keyDir = stateDir
	}
	return []storage.StateFile{
		{Label: "encryption_key", Path: database.EncryptionKeyPath(keyDir)},
		{Label: "gap_fill", Path: filepath.Join(filepath.Dir(dbPath), "config.yaml")},
	}
}

// r1Values maps serve's flags and the effective config onto R1Values (05-config
// §1.12.1): HEAD's --host/--port and TLS flags, ALLOWED_PEER_CIDRS,
// TUNNEL_CONNECTOR_ADDRS with the header they set, the rate limit and the body
// limits (MiB to bytes), and the shutdown budget serve read from
// ABO_CORE_SHUTDOWN_BUDGET.
func r1Values(cfg server.ServerConfig, c config.Config, shutdownBudget time.Duration) app.R1Values {
	return app.R1Values{
		Listen:         net.JoinHostPort(cfg.Host, cfg.Port),
		TLSCertFile:    cfg.TLSCertFile,
		TLSKeyFile:     cfg.TLSKeyFile,
		PeerAllowlist:  splitList(c.AllowedPeerCIDRs),
		TrustedProxies: connectorAddrs(c.TunnelConnectorAddrs),
		// The connectors set CF-Connecting-IP (client_trust.go).
		ClientIPHeader: "CF-Connecting-IP",
		// HEAD's rule: enable_rate_limit is the switch and a positive
		// api_rate_limit_per_minute the threshold; both must hold.
		RateLimitEnabled:   c.EnableRateLimit && c.APIRateLimitPerMinute > 0,
		RateLimitPerMinute: c.APIRateLimitPerMinute,
		JSONMaxBytes:       int64(c.JSONBodyLimitMB) << 20,
		UploadMaxBytes:     int64(c.UploadBodyLimitMB) << 20,
		ShutdownBudget:     shutdownBudget,
	}
}

// connectorAddrs is TUNNEL_CONNECTOR_ADDRS under HEAD's rule
// (client_trust.go parseConnectorPeers): a connector is trusted by its exact
// address, so an entry that is not one, a CIDR range included, is dropped.
// Core's stage 0 accepts prefixes as trusted proxies; passing a range through
// would trust every host in it to name the client. The warning for a dropped
// entry is client_trust.go's reportConnectorRejects, which parses the same
// setting with the same netip.ParseAddr rule; a second one here would show
// the operator every warning twice.
func connectorAddrs(csv string) []string {
	var out []string
	for _, e := range splitList(csv) {
		if _, err := netip.ParseAddr(e); err == nil {
			out = append(out, e)
		}
	}
	return out
}

// splitList splits a comma-separated setting into its trimmed, non-empty
// entries.
func splitList(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// telemetryFromConfig is HEAD's InitOTEL configuration as app.Telemetry:
// tracing when a trace collector is set (OTEL_EXPORTER_OTLP_ENDPOINT), the
// metric push when its own collector is set (never derived from the trace
// one), and the deployment environment. Core config fills app.Telemetry from
// F3, and this mapping goes with ABO's telemetry.Config (05-metrics §6). The instance ID is left to core, which draws one
// per Provider: ABO has one Provider, so metrics and traces share it.
func telemetryFromConfig(c config.Config) *app.Telemetry {
	t := &app.Telemetry{Environment: c.TelemetryEnvironment}
	if c.OTelExporterOTLPEndpoint != "" {
		t.Tracing = &metrics.OTLPTracing{Endpoint: c.OTelExporterOTLPEndpoint}
	}
	if c.OTelMetricsOTLPEndpoint != "" {
		t.OTLP = &metrics.OTLPMetrics{
			Endpoint: c.OTelMetricsOTLPEndpoint,
			Insecure: c.OTelMetricsOTLPInsecure,
			Interval: telemetry.ParseMetricsInterval(c.OTelMetricsOTLPInterval),
		}
	}
	return t
}

// r1Listen is books' app.Options.R1Listen: HEAD's listener stack for the
// server Register built, with serve's flags. With no server built (books
// disabled, so Register never ran) there is no HEAD stack to open, and the
// App gets core's own listener, what a nil R1Listen would have given it.
func (m *booksModule) r1Listen(o httpapi.ListenOptions, h http.Handler, log *slog.Logger) (app.R1Server, error) {
	srv := m.srv.Load()
	if srv == nil {
		ln, err := httpapi.Listen(o, h, log)
		if err != nil {
			return nil, err
		}
		return ln, nil
	}
	ln, err := srv.R1Listen(m.serveConfig())(o, h, log)
	if err != nil {
		return nil, err
	}
	return ln, nil
}

// Attach binds what needs the App once New has returned, before Run: the
// store gets core's admin surface (checkpoint, compaction, flush, size
// estimates, the collector's sample) and becomes the provider throttle
// registry's process-scoped store; the legacy client_golang families
// register on the App's Provider registry, which the gin /metrics route then
// serves (05-metrics §6, §13 item 2), the activity feed becomes a tap on
// the App's log pipeline, and GET /system/status's posture tokens read the
// App's checkpoint health (the storage_checkpoint_* tokens, 05-health
// §5H.10 R1). A disabled books (no store or server built) has
// nothing to attach.
func Attach(mod module.Module, a *app.App) error {
	m, err := asBooks(mod)
	if err != nil {
		return err
	}
	if store := m.store.Load(); store != nil {
		store.AttachStorageAdmin(a.StorageAdmin())
		// Process-scoped, so the throttle registry's store lifetime matches
		// the process; after the server is built, as before. Deliberately
		// NOT inside NewServer (see AttachProviderThrottleStore).
		server.AttachProviderThrottleStore(store)
	}
	srv := m.srv.Load()
	if srv == nil {
		return nil
	}
	p := a.Metrics()
	if err := srv.AttachMetrics(p.Registerer(), p.Gatherer(), p.Handler()); err != nil {
		return fmt.Errorf("books: attach the served registry: %w", err)
	}
	tapActivity(a, srv.ActivityLogWriter())
	srv.SetPostureTokens(func() []system.PostureToken {
		h, err := a.CheckpointHealth()
		return checkpointPosture(h, err, time.Now())
	})
	return nil
}
