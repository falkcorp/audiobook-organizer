// file: core/app/defaults.go
// version: 1.1.1
// guid: 2d0f6c4e-8b1a-4e57-9f3c-6a2b7d9e1c40
// last-edited: 2026-11-08

package app

import (
	"context"
	"errors"
	"fmt"
	stdlog "log"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/stdr"
	"go.opentelemetry.io/otel"
)

// ErrProcessDefaultsTaken is InstallProcessDefaults' error when an App
// already owns the process defaults (05-logging §6): one owner of the
// process globals.
var ErrProcessDefaultsTaken = errors.New("app: the process defaults are already installed by an App")

// InstallProcessDefaults points the process-wide defaults at this App's
// pipeline and telemetry (05-logging §3, MOD4-3):
//
//   - slog.SetDefault(the App's process default logger, Logging.Root():
//     the whole pipeline, with no module attribute, because what logs
//     through slog's default is the application's own code and third-party
//     libraries; core's own components keep module=core through their own
//     loggers), which also redirects the standard library's log package;
//   - otel.SetLogger(a sink that keeps the message and the key names and
//     drops every value), because the SDK logs raw env input there;
//   - otel.SetErrorHandler(a class-keyed rate limiter logging through the
//     App's pipeline);
//   - otel.SetMeterProvider, otel.SetTracerProvider and
//     otel.SetTextMapPropagator to the App's Provider, so code that still
//     creates instruments or spans through the globals binds to this App.
//
// An App built with Options.ProcessDefaults has had the logger and the error
// handler since New, installed before its Provider (installOTelDiagnostics);
// InstallProcessDefaults adopts them and installs the rest. Otherwise it
// installs all seven here.
//
// It never touches the Prometheus default registry (05-metrics §6). restore
// puts all seven back (tests); it is idempotent. The OTel logger has no
// getter, so restore installs the SDK's own default (stdr to stderr) rather
// than whatever was set before. A second call, for this App or another,
// while the defaults are installed returns ErrProcessDefaultsTaken. The test
// is stateless (core keeps no package state, 03 R5): the installed error
// handler is this file's type until restore replaces it, and it records
// whether InstallProcessDefaults has completed the set.
func InstallProcessDefaults(a *App) (restore func(), err error) {
	if a == nil || a.k == nil || a.k.Provider() == nil {
		return nil, errors.New("app: InstallProcessDefaults needs an App returned by New")
	}
	cur, held := otel.GetErrorHandler().(*processErrorHandler)
	d := a.otel
	switch {
	case d != nil && cur == d.handler:
		// New's early install, still in place: adopt it, once.
		if d.handler.complete.Load() {
			return nil, ErrProcessDefaultsTaken
		}
	case held:
		return nil, ErrProcessDefaultsTaken
	default:
		if d, err = installOTelDiagnostics(a.k.CoreLogger("otel")); err != nil {
			return nil, err
		}
	}
	p := a.k.Provider()

	// What restore puts back. The getters are read here, in one of the two
	// functions allowed to write them (core/app/noglobals_test.go).
	prevSlog := slog.Default()
	prevLogOut, prevLogFlags := stdlog.Writer(), stdlog.Flags()
	prevMP, prevTP, prevProp := otel.GetMeterProvider(), otel.GetTracerProvider(), otel.GetTextMapPropagator()

	slog.SetDefault(a.k.RootLogger())
	otel.SetMeterProvider(p.MeterProvider())
	otel.SetTracerProvider(p.TracerProvider())
	otel.SetTextMapPropagator(p.Propagator())
	d.handler.complete.Store(true)

	var once sync.Once
	return func() {
		once.Do(func() {
			otel.SetTextMapPropagator(prevProp)
			otel.SetTracerProvider(prevTP)
			otel.SetMeterProvider(prevMP)
			d.handler.complete.Store(false)
			d.restore()
			slog.SetDefault(prevSlog)
			// slog.SetDefault leaves log's output alone when the restored
			// logger is slog's own default, so it is put back explicitly.
			stdlog.SetOutput(prevLogOut)
			stdlog.SetFlags(prevLogFlags)
		})
	}, nil
}

// otelDiagnostics is the OTel SDK logger and error handler one App
// installed: the handler, whose type and identity are how an install is
// recognised, and restore, which puts back the handler that was there before
// and the SDK's default logger. restore is idempotent.
type otelDiagnostics struct {
	handler *processErrorHandler
	restore func()
}

// installOTelDiagnostics installs the OTel SDK's logger (sdkLogSink, which
// drops every value) and error handler (the class-keyed rate limiter), both
// logging through log, unless an App holds them (ErrProcessDefaultsTaken).
// New calls it before the Provider is built when Options.ProcessDefaults is
// set; otherwise InstallProcessDefaults does.
func installOTelDiagnostics(log *slog.Logger) (*otelDiagnostics, error) {
	if _, taken := otel.GetErrorHandler().(*processErrorHandler); taken {
		return nil, ErrProcessDefaultsTaken
	}
	limiter := newClassLimiter(otelErrorLogInterval, time.Now, log)
	prevHandler := otel.GetErrorHandler()
	d := &otelDiagnostics{handler: &processErrorHandler{h: limiter}}
	otel.SetLogger(logr.New(&sdkLogSink{h: limiter}))
	otel.SetErrorHandler(d.handler)
	var once sync.Once
	d.restore = func() {
		once.Do(func() {
			otel.SetErrorHandler(prevHandler)
			otel.SetLogger(stdr.New(stdlog.New(os.Stderr, "", stdlog.LstdFlags|stdlog.Lshortfile)))
		})
	}
	return d, nil
}

// otelErrorLogInterval is the least gap between two logged lines of one
// error class (HEAD's exportErrorLogInterval).
const otelErrorLogInterval = 10 * time.Minute

// maxErrorClasses bounds the class table; past it, expired classes are
// dropped first, then new classes share one "(other)" bucket.
const maxErrorClasses = 32

// processErrorHandler is the otel.ErrorHandler installOTelDiagnostics
// installs. Its type is how another App's install is recognised; complete is
// set while InstallProcessDefaults' set is in place, which is how a second
// install by the same App is.
type processErrorHandler struct {
	h        *classLimiter
	complete atomic.Bool
}

// Handle implements otel.ErrorHandler.
func (e *processErrorHandler) Handle(err error) {
	if err == nil {
		return
	}
	e.h.handleAt(slog.LevelError, "OpenTelemetry error (rate limited)", err.Error())
}

// classLimiter logs each distinct error class at most once per interval
// and counts the repeats it drops in between (moved from ABO's
// internal/telemetry/errorhandler.go, 05-logging §11 item 4). A periodic OTLP
// export that fails hands the SDK one error per attempt: 17,280 lines a day
// at the 5 s minimum interval for one dead collector. One shared bucket would
// let a dead metric collector hide a different trace TLS error, so the key is
// the redacted message with digit runs normalised (ports, counts, addresses).
type classLimiter struct {
	interval time.Duration
	now      func() time.Time
	log      *slog.Logger
	digits   *regexp.Regexp // digit runs, normalised out of the class key
	urlToken *regexp.Regexp // URL-looking tokens, for redactEndpointSecrets

	mu      sync.Mutex
	classes map[string]*errorClass
}

type errorClass struct {
	last       time.Time
	suppressed int
}

func newClassLimiter(interval time.Duration, now func() time.Time, log *slog.Logger) *classLimiter {
	return &classLimiter{interval: interval, now: now, log: log, classes: map[string]*errorClass{},
		digits: regexp.MustCompile(`[0-9]+`), urlToken: regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"'<>]*`)}
}

// handleAt logs raw at level under msg unless its class logged within the
// interval. raw is redacted first (redactEndpointSecrets).
func (h *classLimiter) handleAt(level slog.Level, msg, raw string) {
	text := redactEndpointSecrets(h.urlToken, raw)
	key := h.digits.ReplaceAllString(text, "#")

	h.mu.Lock()
	t := h.now()
	c, ok := h.classes[key]
	if !ok {
		if len(h.classes) >= maxErrorClasses {
			for k, v := range h.classes {
				if t.Sub(v.last) >= h.interval {
					delete(h.classes, k)
				}
			}
		}
		if len(h.classes) >= maxErrorClasses {
			key = "(other)"
			c, ok = h.classes[key]
		}
		if !ok {
			c = &errorClass{last: t.Add(-h.interval - time.Nanosecond)} // logs immediately
			h.classes[key] = c
		}
	}
	if t.Sub(c.last) < h.interval {
		c.suppressed++
		h.mu.Unlock()
		return
	}
	suppressed := c.suppressed
	c.suppressed, c.last = 0, t
	h.mu.Unlock()

	h.log.Log(context.Background(), level, msg,
		"error", text, "suppressed_since_last", suppressed, "min_interval", h.interval.String())
}

// sdkLogSink is the OTel SDK's logger (moved from ABO's
// internal/telemetry/sdklog.go, 05-logging L6). The SDK reports bad
// configuration there with the raw offending input as a value (an
// unparsable OTEL_EXPORTER_OTLP_ENDPOINT as ("parse url", "input", <value>)),
// so it keeps the message, the error's type and the key names, and drops
// every value and the error's text. Errors (V(0)) and the SDK's warnings
// (V(1)) are logged at warn through the limiter; info (V(4)) and debug (V(8))
// stay off. logr.FromSlogHandler is not used: it would forward the values.
type sdkLogSink struct{ h *classLimiter }

func (s *sdkLogSink) Init(logr.RuntimeInfo) {}

func (s *sdkLogSink) Enabled(level int) bool { return level <= 1 }

func (s *sdkLogSink) Info(_ int, msg string, kv ...any) { s.log(msg, nil, kv) }

func (s *sdkLogSink) Error(err error, msg string, kv ...any) { s.log(msg, err, kv) }

func (s *sdkLogSink) WithValues(...any) logr.LogSink { return s }

func (s *sdkLogSink) WithName(string) logr.LogSink { return s }

func (s *sdkLogSink) log(msg string, err error, kv []any) {
	var keys []string
	for i := 0; i < len(kv); i += 2 {
		if k, ok := kv[i].(string); ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	errType := ""
	if err != nil {
		errType = fmt.Sprintf(" [%T]", err)
	}
	if len(msg) > 200 {
		msg = msg[:200] + "..."
	}
	s.h.handleAt(slog.LevelWarn, "OpenTelemetry SDK log (values dropped, rate limited)",
		fmt.Sprintf("%s%s (value keys: %s)", msg, errType, strings.Join(keys, ",")))
}

// redactEndpointSecrets drops the userinfo and the query and fragment of
// every URL-looking token in s (urlToken matches scheme:// up to whitespace
// or a quote): the backstop for third-party text (gRPC
// errors) that quotes an endpoint. metrics' endpoint parser, which refuses
// such endpoints outright, is the primary defence. A dns:/// target has no
// separate authority, so its userinfo is everything through the last '@'
// before any query.
func redactEndpointSecrets(urlToken *regexp.Regexp, s string) string {
	return urlToken.ReplaceAllStringFunc(s, func(tok string) string {
		i := strings.Index(tok, "://")
		head, rest := tok[:i+3], tok[i+3:]
		rest = strings.TrimLeft(rest, "/")
		head += strings.Repeat("/", len(tok)-len(head)-len(rest))
		if strings.EqualFold(strings.TrimRight(head, ":/"), "dns") {
			if qi := strings.IndexAny(rest, "?#"); qi >= 0 && qi > strings.LastIndex(rest, "@") {
				rest = rest[:qi]
			}
			if at := strings.LastIndex(rest, "@"); at >= 0 {
				rest = rest[at+1:]
			}
			if qi := strings.IndexAny(rest, "?#"); qi >= 0 {
				rest = rest[:qi]
			}
		} else {
			if end := strings.IndexAny(rest, "/?#"); end >= 0 {
				rest = rest[:end]
			}
			if at := strings.LastIndex(rest, "@"); at >= 0 {
				rest = rest[at+1:]
			}
		}
		return head + rest
	})
}
