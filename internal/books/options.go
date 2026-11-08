// file: internal/books/options.go
// version: 1.1.0
// guid: 3c47c7cc-6394-498f-977b-0d0fb19df982
// last-edited: 2026-11-08

package books

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/server"
	"github.com/falkcorp/audiobook-organizer/internal/telemetry"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/module"
)

// ABO's identity on core (03 §3.6, 05-metrics §2): the app ID, the metric
// namespace every served family carries and the OTel service name. They are
// the values HEAD's telemetry used, so no series changes name.
const (
	appID           = "abo"
	metricNamespace = "audiobook_organizer"
	serviceName     = "audiobook-organizer"
	displayName     = "Audiobook Organizer"
)

// ServeInputs are the values of serve books needs beyond the store.
type ServeInputs struct {
	// Server is HEAD's listener flags (--host, --port, the timeouts, the TLS
	// pair, --http3-port, --external-url) and the startup-step runner.
	Server server.ServerConfig
	// Telemetry is the config snapshot HEAD's InitOTEL read: taken right
	// after the store opened, before the stored settings and env overrides
	// apply, so the OTLP endpoints, interval and environment come from where
	// they always did.
	Telemetry config.Config
	// StateDir is the state directory serve resolved (EnsureSecureStateDir):
	// ABO's log file lives under it (core.log.file.path, 05-logging section
	// 11 item 1). "" keeps the log on the stream only, with no file.
	StateDir string
	// LogOut is the stream the log pipeline prints to, beside the file when
	// there is one; nil is os.Stdout, where HEAD's lines went.
	LogOut io.Writer
}

// Options is ABO's app.Options for release R1 (06 §6.4.10, 05-config
// §1.12.1), for mod, the value Module returned:
//
//   - ABO's identity and build version;
//   - R1Values: the listener and perimeter values of the effective config
//     (stored settings and env overrides applied), which the kernel reads
//     once at start step 7; ShutdownBudget stays 0, core's default (R1-33
//     reads ABO_CORE_SHUTDOWN_BUDGET into it);
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
	m.cfg.Store(&cfg)
	logCfg, warn := logSettings(config.Snapshot().LogLevel, in.StateDir)
	if warn != "" {
		fmt.Fprintln(os.Stderr, "Warning: "+warn)
	}
	return app.Options{
		AppID:                 appID,
		MetricNamespace:       metricNamespace,
		ServiceName:           serviceName,
		DisplayName:           displayName,
		Version:               telemetry.Version(),
		ReservedAPINamespaces: reservedAPINamespaces(),
		LegacyAuthGinUntil:    ID,
		R1Values:              func() app.R1Values { return r1Values(cfg, config.Snapshot()) },
		R1Listen:              m.r1Listen,
		Telemetry:             telemetryFromConfig(in.Telemetry),
		LogOutput:             logStream(in.LogOut),
		R1Log:                 &logCfg,
		StateDir:              in.StateDir,
		ProcessDefaults:       true,
	}, nil
}

// r1Values maps serve's flags and the effective config onto R1Values (05-config
// §1.12.1): HEAD's --host/--port and TLS flags, ALLOWED_PEER_CIDRS,
// TUNNEL_CONNECTOR_ADDRS, the rate limit and the body limits (MiB to bytes).
func r1Values(cfg server.ServerConfig, c config.Config) app.R1Values {
	return app.R1Values{
		Listen:             net.JoinHostPort(cfg.Host, cfg.Port),
		TLSCertFile:        cfg.TLSCertFile,
		TLSKeyFile:         cfg.TLSKeyFile,
		PeerAllowlist:      splitList(c.AllowedPeerCIDRs),
		TrustedProxies:     splitList(c.TunnelConnectorAddrs),
		RateLimitEnabled:   c.EnableRateLimit,
		RateLimitPerMinute: c.APIRateLimitPerMinute,
		JSONMaxBytes:       int64(c.JSONBodyLimitMB) << 20,
		UploadMaxBytes:     int64(c.UploadBodyLimitMB) << 20,
	}
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
// legacy client_golang families register on the App's Provider registry,
// which the gin /metrics route then serves (05-metrics §6, §13 item 2), and
// the activity feed becomes a tap on the App's log pipeline. A disabled
// books (no server built) has nothing to attach.
func Attach(mod module.Module, a *app.App) error {
	m, err := asBooks(mod)
	if err != nil {
		return err
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
	return nil
}
