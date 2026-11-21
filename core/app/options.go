// file: core/app/options.go
// version: 1.7.0
// guid: 55e09ad5-34c8-4d0f-b8d5-117718ebf0ed
// last-edited: 2026-11-19

package app

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/ui"
	"github.com/falkcorp/mediacore/updater"
)

// Options configure one process (06 §6.4.5). Zero values are valid defaults
// unless noted. These are the R1 fields that need no storage (06 §6.4.10)
// and F3's config lists, plus the shell's UI and UIOptions (F9-01) and the
// identity flip's Authenticators and Auth (recorded here, read by no boot
// phase until F4-08's wiring); a field whose package lands later (Adapters,
// InterimBackends) arrives with it.
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
	// CoreDefaults are the app's compiled defaults of the core keys whose
	// default the specs leave to the app (05-http-api §10). A zero field is
	// core's own default. Like StateDir they are defaults: an operator value,
	// a flag or a stored value beats them, and they lock nothing.
	CoreDefaults CoreDefaults
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
	// ConfigPassthrough reports the operator-layer paths config reads raw and
	// owns no section for (ABO: the integrations.<kind>.<instance>.<field>
	// paths its rename table stages, 05-metadata M1), handed to
	// config.Options.Passthrough. Core places an env binding only onto a path
	// a registered section owns or this accepts, so an app that lists a
	// ConfigEnvBindings entry for an integrations path must set it too: with
	// the binding's env name set and no predicate, boot fails "which no
	// registered section owns". nil accepts no path. From F3-07.
	ConfigPassthrough func(config.Path) bool
	// ConfigFlags are the flags the operator set on the command line, by the
	// config path each one sets (05-config §1.3.3): the app builds them with
	// config.FromFlagSet's rule (only a flag the operator explicitly set, never
	// a default, so a default never locks a key) and its own table of flag name
	// to path. They are the highest operator layer, handed to
	// config.Options.Flags. A flag that names a secret key is refused when the
	// config loads (it would show in the process list). Copied, so the
	// caller's map stays its own. From F3-07.
	ConfigFlags map[config.Path]string
	// Args/Env feed the flag and env layers; nil means
	// os.Args[1:]/os.Environ(). At R1's skeleton only ResolveStateDir reads
	// Env; the flag layer arrives with config (F3).
	Args []string
	Env  []string

	// LegacyAuthGinUntil names the one module whose LegacyMount may use
	// httpapi.LegacyAuthGin (ABO: "books" in R1). R2's identity flip clears it
	// and deletes the field. "" = no module may (SEC2-17).
	LegacyAuthGinUntil module.ID

	// Authenticators are the app's identity extensions (05-authn §3.3; ABO:
	// internal/cfaccess), wired by the kernel with auth's extension
	// registration. This compile-time list is the ONLY place a MethodSpec may
	// be Interactive or set MayChangeCredentials, and the only place an
	// OwnerVerifier may come from; modules register their methods with
	// Registrar.AuthMethod, which refuses all three (SEC2-16). nil = none.
	// Not read until F4-08's boot wiring.
	Authenticators []auth.Extension // {Authenticator, Spec auth.MethodSpec, Order}

	// Auth is the app's compile-time auth configuration (05-authn §3.2
	// auth.Options): accepted API-key prefixes and the defaults of the app's
	// auth keys. ABO sets ImportedKeyPrefixes ["abk_"], because existing abk_
	// keys survive R2's identity step and keep verifying by their SHA-256 hash
	// beside new mck_ keys (D12 (b)), DefaultDisabledAllowRemote true (SEC4-8:
	// every Docker install listens on a non-loopback address, so auth-off
	// installs would otherwise refuse to boot from R2) and OIDCCallbackBase
	// "/api/v1/auth/oauth", HEAD's callback path, which core serves from F4
	// (05-authn §4.13). The zero value is what hello and new apps pass. Not
	// read until F4-08's boot wiring.
	Auth auth.Options

	// IdentityBoot declares the identity flip (F4-08): boot builds rbac and
	// auth over core's own identity stores, serves audit, opens the events bus
	// and runs P4d steps 2-4 (the revocation re-apply, the restore hooks,
	// auth.BootWrites). It is the one signal, and nothing else stands in for
	// it: Auth's zero value is what hello and new apps pass, and ABO sets Auth
	// (and Authenticators) before the flip, so neither field says "the app
	// wants core identity". false, the default, boots exactly as before the
	// flip. Temporary like the F3 config gate it follows: B10 (the ABO switch)
	// makes identity unconditional and deletes the field.
	IdentityBoot bool

	// ArchivePrefix is the prefix of the app's backup archive names and of the
	// revocation journal's file name and header (05-backup §5B.3.6 row "Where":
	// <backup dir>/<prefix>revocations.journal), so two apps sharing a backup
	// directory never share a journal. "" is <AppID>_. ABO sets
	// "audiobooks_pebble_", the prefix its archives have always had (B10).
	ArchivePrefix string

	// Logger overrides the default slog handler construction (tests); nil =
	// from config (05-logging §4).
	Logger *slog.Logger

	// StorageStateFiles is the state-file table (05-config §2.4): the files
	// the pre-migration checkpoint copies beside the store, each by its label
	// and resolved absolute path; one that does not exist is skipped. The app
	// resolves them at R1 (ABO: HEAD's .encryption_key and the gap-fill
	// config.yaml); from F3 core config builds the table.
	StorageStateFiles []storage.StateFile
	// FromRelease is the pre-freeze release's tag the build embeds (09 §9.2.1;
	// ABO: the one tag line of ci/prefreeze_release.txt). The pre-migration
	// checkpoint records it as from_release, because HEAD's store records no
	// app version, and `make rollback-restore` names it. "" = the build embeds
	// none (a build without cutover steps).
	FromRelease string
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
	// (after the hooks and the workers) and drains it first in the
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

	// UI is the app's embedded SPA (07 §7.6), from its UIBundle(): ui_embed.go
	// (embed_frontend) returns ui.MustFromFS over the built dist, ui_nonembed.go
	// the zero Bundle. A non-zero UI makes ui.Handler the "/" owner, mounted
	// by the kernel in P8; together with a ServesSPA legacy mount it is a boot
	// error (FE-1). The zero Bundle mounts nothing: a ServesSPA mount or nobody
	// owns "/" (the API-only placeholder for it arrives with F9-03).
	UI ui.Bundle

	// UIOptions carries the app's own contributions into the ui.Handler the
	// kernel builds in P8 (FE3-4): page-load middleware, extra connect-src
	// hosts and extra CSP directives (ui.AppOptions, 07 §7.6.1). A
	// compile-time value built in main before New, so a func whose inputs
	// exist only after boot reads an atomic.Pointer the module fills in its
	// Start hook and is a no-op until then (FE4-5). The zero value adds
	// nothing; it is ignored with a zero UI.
	UIOptions ui.AppOptions

	// Updater is the app's release source and binary name for core's updater
	// (05-updater §5U.3): nil = no updater, no routes and no tasks. Only
	// `updater fetch-release` reads it in this build (F8-02); F8-04 hands it
	// to the kernel, which then registers the three core.updater.* tasks and
	// mounts the four routes. The Options' Source names the repository and its
	// AssetName the release asset, so core names no app.
	Updater *updater.Options
}

// CoreDefaults is the value of Options.CoreDefaults. It mirrors
// kernel.CoreDefaults field for field, so New converts one into the other.
type CoreDefaults struct {
	// HTTPListen is core.http.listen's default (core: ":8484"). ABO names one
	// interface, "localhost:8484", as its --host flag always defaulted: a
	// bare install must not start listening on every interface.
	HTTPListen string
	// HTTPClientIPHeader is core.http.client_ip_header's default (core: "",
	// the peer's own address). ABO's tunnel connectors name the client in
	// "CF-Connecting-IP".
	HTTPClientIPHeader string
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
