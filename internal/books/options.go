// file: internal/books/options.go
// version: 1.0.5
// guid: 3c47c7cc-6394-498f-977b-0d0fb19df982
// last-edited: 2026-11-08

package books

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/server"
	"github.com/falkcorp/audiobook-organizer/internal/telemetry"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/httpapi"
	corelog "github.com/falkcorp/mediacore/log"
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
	// ShutdownBudget is core.shutdown.budget as serve read it from
	// ABO_CORE_SHUTDOWN_BUDGET (R1-33). Zero is core's default (25s).
	ShutdownBudget time.Duration
}

// Options is ABO's app.Options for release R1 (06 §6.4.10, 05-config
// §1.12.1), for mod, the value Module returned:
//
//   - ABO's identity and build version;
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
//   - LogOutput: the activity feed's tee (see logTee);
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
	return app.Options{
		AppID:                 appID,
		MetricNamespace:       metricNamespace,
		ServiceName:           serviceName,
		DisplayName:           displayName,
		Version:               telemetry.Version(),
		ReservedAPINamespaces: reservedAPINamespaces(),
		LegacyAuthGinUntil:    ID,
		R1Values:              func() app.R1Values { return r1Values(cfg, config.Snapshot(), in.ShutdownBudget) },
		R1Listen:              m.r1Listen,
		Telemetry:             telemetryFromConfig(in.Telemetry),
		LogOutput:             &m.logOut,
		ProcessDefaults:       true,
	}, nil
}

// r1Values maps serve's flags and the effective config onto R1Values (05-config
// §1.12.1): HEAD's --host/--port and TLS flags, ALLOWED_PEER_CIDRS,
// TUNNEL_CONNECTOR_ADDRS, the rate limit and the body limits (MiB to bytes),
// and the shutdown budget serve read from ABO_CORE_SHUTDOWN_BUDGET.
func r1Values(cfg server.ServerConfig, c config.Config, shutdownBudget time.Duration) app.R1Values {
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
		ShutdownBudget:     shutdownBudget,
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
// the process default logger's lines switch to the activity feed's writer
// (see logTee). A disabled books (no server built) has nothing to attach.
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
	if w := srv.ActivityLogWriter(); w != nil {
		m.logOut.setActivity(w)
	}
	return nil
}

// logTee is the App's log output (app.Options.LogOutput). R1-18's activity
// tap replaces it.
//
// Until Attach, every line goes to stdout. Attach points activity at the
// activity feed's writer, which tees each line to stdout
// itself and parses it into the Activity Log. In HEAD, NewServer made a text
// handler over that writer slog's default, so the feed saw exactly what
// logged through slog's default. With the App's logger installed as the
// default instead (app.InstallProcessDefaults), the tee keeps that: a line
// from the process default logger goes to the feed, and a line from a scoped
// logger (core's own kernel, metrics, http and otel lines, module=core, or a
// module's own logger) goes to stdout, so it is printed but makes no
// Activity row (R1-15 review M2). The process default writes no module attribute
// (core/log Root), so the two are told apart by scopedLine.
type logTee struct {
	// stdout takes every line that does not go to the activity feed; nil =
	// os.Stdout. Production leaves it nil. A test sets it before the App is
	// built and never after, so it needs no synchronisation, unlike activity,
	// which Attach sets while the App's goroutines may be logging.
	stdout   io.Writer
	activity atomic.Pointer[io.Writer]
}

// setActivity points the process default logger's lines at the activity
// feed's writer.
func (t *logTee) setActivity(w io.Writer) { t.activity.Store(&w) }

func (t *logTee) Write(p []byte) (int, error) {
	if a := t.activity.Load(); a != nil && !scopedLine(p) {
		return (*a).Write(p)
	}
	if t.stdout != nil {
		return t.stdout.Write(p)
	}
	return os.Stdout.Write(p)
}

// scopedLine reports whether p, one record of the App's text output, came
// from a scoped logger. core/log writes a scoped logger's module attribute
// first, right after the message, and the process default logger writes
// none, so the line is scoped when " module=" follows the msg value. The slog
// text handler writes each record in one Write, quoting a message that has
// spaces, quotes or control characters. A line that does not parse (JSON
// output, which ABO does not configure) counts as unscoped and goes to the
// feed, as every line did before.
func scopedLine(p []byte) bool {
	const msgKey = " msg="
	i := bytes.Index(p, []byte(msgKey))
	if i < 0 {
		return false
	}
	rest := p[i+len(msgKey):]
	if len(rest) > 0 && rest[0] == '"' {
		j := 1
		for j < len(rest) && rest[j] != '"' {
			if rest[j] == '\\' {
				j++
			}
			j++
		}
		if j >= len(rest) {
			return false
		}
		rest = rest[j+1:]
	} else {
		j := bytes.IndexAny(rest, " \n")
		if j < 0 {
			return false
		}
		rest = rest[j:]
	}
	return bytes.HasPrefix(rest, []byte(" "+corelog.KeyModule+"="))
}
