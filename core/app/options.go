// file: core/app/options.go
// version: 1.5.1
// guid: 55e09ad5-34c8-4d0f-b8d5-117718ebf0ed
// last-edited: 2026-11-12

package app

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
)

// Options configure one process (06 §6.4.5). Zero values are valid defaults
// unless noted. These are the R1 fields that need no storage (06 §6.4.10)
// and F3's config lists; a field whose package lands later (UI, UIOptions,
// Authenticators, Auth, Adapters, Updater, InterimBackends) arrives with it.
type Options struct {
	AppID string // app identifier, [a-z][a-z0-9]{1,15} (03 §3.6), e.g. "abo"
	// MetricNamespace is the namespace of every series the App emits: core's
	// own, <MetricNamespace>_<subsystem>_<what>, and the modules',
	// <MetricNamespace>_<what> while the app ships one module and
	// <MetricNamespace>_<ID>_<what> with two or more ([a-z][a-z0-9_]*;
	// default "mediacore"). ABO passes "audiobook_organizer".
	MetricNamespace string
	// ServiceName is the OTel resource's service.name, and the fixed
	// server.address of the HTTP instruments core's stage 2 records (never the
	// request's Host header). The kernel passes this one value as
	// metrics.Options.ServiceName and as httpapi.Options.ServerName. Default
	// AppID; ABO passes "audiobook-organizer".
	ServiceName string
	// ReservedAPINamespaces are the second segments under /api that the app's
	// unprefixed surfaces occupy (ABO: the ABS unversioned /api/<seg> names);
	// the kernel adds them to the reserved IDs in P1 so no module can take
	// them (03 §3.6). Unprefixed top-level segments come from
	// Manifest.LegacyPrefixes instead (FE2-11).
	ReservedAPINamespaces []string
	DisplayName           string // e.g. "Audiobook Organizer"
	Version               string // the app's release version (the resource's service.version)

	// EnvPrefix is the prefix of every env name (05-config §1.3,
	// [A-Z][A-Z0-9]{1,15}); default strings.ToUpper(AppID) (ABO: "ABO").
	EnvPrefix string
	// StateDir is the compiled default of core.paths.state_dir, a pre-boot
	// key resolved once per process by ResolveStateDir (05-config §1.7). At
	// R1's skeleton no phase reads the state directory (P0a and storage land
	// later); an app that touches state files before or outside New calls
	// ResolveStateDir.
	StateDir string
	// ConfigFile is optional; layered under env/flags/DB overrides
	// (05-config). Read once config lands (F3).
	ConfigFile string

	// RetiredConfigNames are the app's retired env names and file keys (ABO:
	// generated from its rename table, 05-config §1.12.3), handed to
	// config.Options.Retired. A retired name is never read: one set for a
	// ProtectAdmin+ or secret key refuses boot naming the canonical name,
	// any other is ignored with a warning, and `<app> config check` lists
	// them (05-config §1.3.6); core holds no app name (03 R1). From F3; ABO
	// passes it from F3-07 (options_config.go).
	RetiredConfigNames []config.RetiredName
	// ConfigEnvBindings are the app's ecosystem env names for passthrough
	// paths (ABO: OPENAI_API_KEY and OPENAI_BASE_URL onto
	// integrations.ai.openai.{api_key, base_url}), handed to
	// config.Options.EnvBindings; core and module keys declare theirs with
	// config.EnvBinding (05-config §1.3.1). From F3; ABO passes it from F3-07.
	ConfigEnvBindings []config.EnvName
	// Args/Env feed the flag and env layers; nil means
	// os.Args[1:]/os.Environ(). At R1's skeleton only ResolveStateDir reads
	// Env; the flag layer arrives with config (F3).
	Args []string
	Env  []string

	// R1Values delivers ABO's stored listener and perimeter values to the
	// kernel in release R1, when core config has no DB layer (05-config
	// §1.12.1 owns the type and the rule). The kernel calls it once per boot
	// at 06 §6.5.2 start step (7), after the module start hooks of step (2)
	// and before the first listener opens. nil = every key at core's default
	// (hello). Release R1 only: F3's PR deletes the field and the type.
	R1Values func() R1Values

	// LegacyAuthGinUntil names the one module whose LegacyMount may use
	// httpapi.LegacyAuthGin (ABO: "books" in R1). R2's identity flip clears it
	// and deletes the field. "" = no module may (SEC2-17).
	LegacyAuthGinUntil module.ID

	// Logger overrides the default slog handler construction (tests); nil =
	// from config (05-logging §4).
	Logger *slog.Logger

	// R1StoragePath is the app's own resolved database path, absolute (ABO:
	// its config's database path, HEAD's --db / DATABASE_PATH, made absolute).
	// It names core.storage.path when <EnvPrefix>_CORE_STORAGE_PATH is unset,
	// so the store core opens is the one HEAD opened; both set to different
	// paths fails New. "" = core's rule alone. Release R1 only: F3's config
	// carries the renamed key (05-config's rename table) and deletes it.
	R1StoragePath string
	// StorageStateFiles is the state-file table (05-config §2.4): the files
	// the pre-migration checkpoint copies beside the store, each by its label
	// and resolved absolute path; one that does not exist is skipped. The app
	// resolves them at R1 (ABO: HEAD's .encryption_key and the gap-fill
	// config.yaml); from F3 core config builds the table.
	StorageStateFiles []storage.StateFile
	// R1BeforeSwap runs in storage restore-offline (in place) and storage
	// reset after every check passed and before the first write that stages
	// or moves anything, with core.storage.path (kernel.Config.BeforeSwap).
	// ABO removes its HNSW clean-stop marker there, so the boot after the
	// swap rebuilds the vector graph rather than trust one of the replaced
	// store. An error stops the command with nothing staged or moved. nil =
	// none. Release R1 only.
	R1BeforeSwap func(dbPath string) error
	// R1ArchiveExtract extracts an archive's store into storeDir, an empty
	// directory, for storage restore-offline --archive, and returns the
	// archive's ID (ABO: HEAD's backup format). nil = core's own extractor,
	// backup.ExtractStore (mediacore-backup/1 and legacy-abo/0 archives).
	// Release R1 only: an app that stores its backups in core's format sets
	// nothing, and ABO's R2 flip deletes the field.
	R1ArchiveExtract func(ctx context.Context, archive, storeDir string) (string, error)
	// StorageFS is the filesystem core storage opens the store on; the zero
	// value is the OS's. Tests only: coretest's harness passes
	// storage.MemFS(), so a harness boot leaves no store on disk (06 §6.4.8).
	// A store on any other filesystem is not durable.
	StorageFS storage.FS
	// LogOutput is where the App's log pipeline writes its primary output;
	// nil = core.log.output's stream (stderr until config lands). With
	// R1Log's output "file" it is the stream that also receives each line
	// (R1Log.File.TeeStderr), stderr when nil. Ignored when Logger is set.
	// ABO sets it to stdout, where HEAD's lines went, and feeds its
	// Activity page from a Logging tap, not from this stream.
	LogOutput io.Writer
	// R1Log is the core.log section (05-logging §8) in release R1, when core
	// config has no layers to read it from: the level, the per-module levels
	// and the file output. nil = log.DefaultSettings(); a non-nil value is the
	// whole section, so build it from log.DefaultSettings(). With output
	// "file", File.Path is relative to the state directory (StateDir, or
	// ResolveStateDir's other rules), which New resolves and creates, and an
	// open failure fails boot. An invalid level, format or output fails boot.
	// Ignored when Logger is set. Release R1 only: F3 deletes the field.
	R1Log *log.Settings
	// ProcessDefaults says the composition root will call
	// InstallProcessDefaults for this App (05-logging §3). New then installs
	// the OTel SDK's logger and error handler, the value-dropping sink and
	// the rate-limited handler InstallProcessDefaults lists, before it builds
	// the metrics Provider: building the resource and any OTLP exporter makes
	// the SDK read OTEL_* from the environment and report bad input, raw,
	// through those two globals, and OTEL_EXPORTER_OTLP_HEADERS can carry a
	// credential. New fails with ErrProcessDefaultsTaken when another App
	// holds them and puts them back when it fails. false = the two are
	// installed by InstallProcessDefaults, after the Provider exists (tests,
	// and apps that never install the defaults). Describe ignores it.
	ProcessDefaults bool

	// R1Listen opens the listener of 06 §6.5.2 start step (7) in release R1,
	// in place of httpapi.Listen: the kernel still decides when it opens
	// (after the hooks, the workers and R1Values) and drains it first in the
	// stop order, within min(core.shutdown.http_drain, remaining). ABO keeps
	// HEAD's listener stack behind it (HTTP/3, the port-80 redirect, the TLS
	// fallback, its timeouts, Alt-Svc and the peer allow-list) until core's
	// listener carries those (R1-17). nil = httpapi.Listen. Release R1 only.
	R1Listen func(httpapi.ListenOptions, http.Handler, *slog.Logger) (R1Server, error)

	// Telemetry is the part of the App's metrics.Options that the
	// core.metrics and core.tracing config sections describe (05-metrics §7):
	// the OTLP metric push, the OTLP span exporter and the resource's
	// deployment environment and instance ID. nil = the Prometheus scrape
	// only, no tracing. Core config has no such sections until F3, so the app
	// fills it (ABO from its effective config: OTEL_EXPORTER_OTLP_ENDPOINT and
	// friends, so production tracing survives the move to the App's
	// Provider); from F3 core config fills it. An OTLP push or span exporter
	// that cannot be built degrades the Provider and New goes on (05-metrics
	// §8, as ABO's InitOTEL did).
	Telemetry *Telemetry
}

// R1Server is one listener R1Listen opens: what the supervisor drives.
// Serve blocks until the listener stops (nil after Shutdown); Shutdown
// drains within ctx and closes what is left when ctx ends.
type R1Server interface {
	Addr() net.Addr
	Serve() error
	Shutdown(ctx context.Context) error
}

// Telemetry is the value of Options.Telemetry. The propagator needs no
// field: the App's is W3C trace context plus baggage.
type Telemetry struct {
	OTLP        *metrics.OTLPMetrics // nil = no OTLP metric push
	Tracing     *metrics.OTLPTracing // nil = the no-op TracerProvider
	Environment string               // deployment.environment; "" = metrics.DefaultEnvironment
	InstanceID  string               // service.instance.id; "" = a random one per Provider
}

// R1Values is the release-R1-only value Options.R1Values returns (05-config
// §1.12.1). Deleted in F3.
type R1Values struct {
	Listen                       string        // HEAD's --host and --port → core.http.listen
	TLSCertFile, TLSKeyFile      string        // HEAD's --tls-cert, --tls-key
	PeerAllowlist                []string      // ALLOWED_PEER_CIDRS → core.http.peer_allowlist
	TrustedProxies               []string      // TUNNEL_CONNECTOR_ADDRS → core.http.trusted_proxies
	ClientIPHeader               string        // the header TrustedProxies set (ABO: CF-Connecting-IP) → core.http.client_ip_header
	RateLimitEnabled             bool          // enable_rate_limit → core.http.rate_limit.enabled
	RateLimitPerMinute           int           // api_rate_limit_per_minute → core.http.rate_limit.per_minute
	JSONMaxBytes, UploadMaxBytes int64         // json_body_limit_mb, upload_body_limit_mb (MiB → bytes)
	ShutdownBudget               time.Duration // ABO_CORE_SHUTDOWN_BUDGET; 0 = core's default (05-health §5H.4)
}
