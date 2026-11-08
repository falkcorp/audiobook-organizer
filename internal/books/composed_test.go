// file: internal/books/composed_test.go
// version: 1.1.1
// guid: 4b8f2e61-9a3d-4c75-a0e2-6d1f8b3c5e97
// last-edited: 2026-11-08

package books

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/opsmetrics"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
	"github.com/falkcorp/audiobook-organizer/internal/server"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/httpapi"
)

// freshProcessEnv names the test a child test process runs (inFreshProcess).
const freshProcessEnv = "BOOKS_TEST_FRESH_PROCESS"

// inFreshProcess runs the calling test again in a child test process, where
// its App is the first in the process to install the process defaults, and
// reports whether the caller is the parent, which then returns. The child's
// combined stdout and stderr come back for the parent to check. env is added
// to the child's environment.
//
// A fresh process is what production has, one App per process, and what the
// recorders need: OTel's global delegate binds every instrument made before
// the first SetMeterProvider to the first provider set, and ABO's recorders
// (opsmetrics.Default, the AI instruments) are made once per process, so in
// a shared test binary a later App would not get them.
func inFreshProcess(t *testing.T, env ...string) (parent bool, output string) {
	t.Helper()
	if os.Getenv(freshProcessEnv) == t.Name() {
		return false, ""
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+regexp.QuoteMeta(t.Name())+"$", "-test.count=1", "-test.v")
	cmd.Env = append(append(os.Environ(), freshProcessEnv+"="+t.Name()), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the child test process failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "--- PASS: "+t.Name()) {
		t.Fatalf("the child test process did not run %s:\n%s", t.Name(), out)
	}
	return true, string(out)
}

// composed is serve's composition (cmd/serve_app.go) up to Run, over a
// fresh in-memory store: books.Module, Options, app.New,
// app.InstallProcessDefaults and Attach.
type composed struct {
	app   *app.App
	mod   *booksModule
	srv   *server.Server
	store *database.PebbleStore
	// newLog is the App's output that is not for the activity feed: every
	// line until Attach, and the scoped loggers' lines (core's own, module=)
	// after it.
	newLog *syncBuffer
}

// bootComposed boots the composition with the ABS surface on or off; edit,
// when set, adjusts the config before the store opens. pre, when set, runs
// on the store after migrations and before the module is built (a stored
// settings blob, say). Cleanup releases the unstarted server, puts the
// process defaults back and closes the store.
func bootComposed(t *testing.T, abs bool, edit func(*config.Config), pre func(*database.PebbleStore)) *composed {
	t.Helper()
	return bootComposedWith(t, abs, edit, pre, nil)
}

// bootComposedWith is bootComposed with opt, when set, adjusting the App's
// options after books.Options builds them and before app.New.
func bootComposedWith(t *testing.T, abs bool, edit func(*config.Config), pre func(*database.PebbleStore), opt func(*app.Options)) *composed {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := tempDir(t)
	c := config.Config{
		DatabaseType: "pebble",
		DatabasePath: filepath.Join(dir, "books.pebble"),
		RootDir:      dir,
		// The fingerprint worker routes answer past their flag guard, as the
		// manifest test's engine does.
		FingerprintRemoteWorkersEnabled: true,
		ABSAPIEnabled:                   abs,
	}
	if abs {
		c.ABSJWTSecret = "synthetic-composed-app-secret-0123456789abcdef"
	}
	if edit != nil {
		edit(&c)
	}
	withConfig(t, c)
	store, err := database.NewPebbleStoreInMemory(config.AppConfig.DatabasePath)
	require.NoError(t, err)
	database.SetGlobalStore(store)
	t.Cleanup(func() {
		database.SetGlobalStore(nil)
		scanner.SetStore(nil)
		_ = store.Close()
	})
	require.NoError(t, database.RunMigrations(store))
	_, _, err = auth.SeedRoles(store)
	require.NoError(t, err)
	if pre != nil {
		pre(store)
	}

	mod := Module(store).(*booksModule)
	newLog := &syncBuffer{}
	mod.logOut.stdout = newLog
	cfg := server.GetDefaultServerConfig()
	cfg.Host, cfg.Port = "127.0.0.1", "0"
	opts, err := Options(mod, ServeInputs{Server: cfg, Telemetry: config.Snapshot()})
	require.NoError(t, err)
	if opt != nil {
		opt(&opts)
	}
	a, err := app.New(t.Context(), opts, mod)
	t.Cleanup(func() { Release(mod) })
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = a.Metrics().Shutdown(ctx)
	})
	restore, err := app.InstallProcessDefaults(a)
	require.NoError(t, err)
	t.Cleanup(restore)
	server.AttachProviderThrottleStore(store)
	require.NoError(t, Attach(mod, a))
	srv := mod.srv.Load()
	require.NotNil(t, srv, "Register built no server")
	return &composed{app: a, mod: mod, srv: srv, store: store, newLog: newLog}
}

// runningApp is a composition whose App runs as serve runs it, with its
// listener on a loopback port opened by books' R1Listen.
type runningApp struct {
	*composed
	addr   net.Addr
	cancel context.CancelFunc
	ran    chan error
	once   sync.Once
	runErr error
}

// runComposed boots the composition and runs the App until stop (or the
// test's end) cancels it.
func runComposed(t *testing.T) *runningApp {
	t.Helper()
	addrs := make(chan net.Addr, 1)
	c := bootComposedWith(t, false, nil, nil, func(o *app.Options) {
		listen := o.R1Listen
		o.R1Listen = func(lo httpapi.ListenOptions, h http.Handler, l *slog.Logger) (app.R1Server, error) {
			srv, err := listen(lo, h, l)
			if err == nil {
				addrs <- srv.Addr()
			}
			return srv, err
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	r := &runningApp{composed: c, cancel: cancel, ran: make(chan error, 1)}
	go func() { r.ran <- c.app.Run(ctx) }()
	t.Cleanup(func() {
		if err := r.stop(); err != nil {
			t.Errorf("Run: %v", err)
		}
	})
	select {
	case r.addr = <-addrs:
	case err := <-r.ran:
		r.once.Do(func() { r.runErr = err })
		t.Fatalf("Run returned before the listener opened: %v", err)
	case <-time.After(60 * time.Second):
		t.Fatal("the App never opened its listener")
	}
	return r
}

// stop cancels Run and waits for it to return; later calls return the same
// result.
func (r *runningApp) stop() error {
	r.once.Do(func() {
		r.cancel()
		select {
		case r.runErr = <-r.ran:
		case <-time.After(60 * time.Second):
			r.runErr = errors.New("Run did not return after its context was cancelled")
		}
	})
	return r.runErr
}

// serve sends one request through the App's root handler.
func (c *composed) serve(method, path string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	c.app.Handler().ServeHTTP(w, req)
	return w
}

// configBlob is the stored config_blob, decoded by key.
func configBlob(t *testing.T, store *database.PebbleStore) map[string]json.RawMessage {
	t.Helper()
	s, err := store.GetSetting("config_blob")
	require.NoError(t, err)
	require.NotNil(t, s, "no config_blob stored")
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(s.Value), &m))
	return m
}

// TestR1AppConfigFromBlob (06 §6.10; COR4-1, R4-6): in R1 a value set only in
// the settings blob is effective after boot (in AppConfig, and in the
// R1Values the kernel reads at start step 7), and a PUT /api/v1/config that
// changes one field leaves every other blob field byte-identical.
func TestR1AppConfigFromBlob(t *testing.T) {
	defaults := config.Snapshot()
	const rate, jsonMB = 37, 9
	require.NotEqual(t, rate, defaults.APIRateLimitPerMinute)
	require.NotEqual(t, jsonMB, defaults.JSONBodyLimitMB)

	c := bootComposed(t, false, nil, func(store *database.PebbleStore) {
		blob := fmt.Sprintf(`{"api_rate_limit_per_minute":%d,"json_body_limit_mb":%d}`, rate, jsonMB)
		require.NoError(t, store.SetSetting("config_blob", blob, "json", false))
		// serve's pre-server step loadConfigFromDB (cmd/root.go).
		require.NoError(t, config.LoadConfigFromDatabase(store))
	})
	snap := config.Snapshot()
	assert.Equal(t, rate, snap.APIRateLimitPerMinute, "blob-only value not effective in AppConfig")
	assert.Equal(t, jsonMB, snap.JSONBodyLimitMB, "blob-only value not effective in AppConfig")
	opts, err := Options(c.mod, ServeInputs{Server: c.mod.serveConfig(), Telemetry: snap})
	require.NoError(t, err)
	v := opts.R1Values()
	assert.Equal(t, rate, v.RateLimitPerMinute, "R1Values ignores the blob")
	assert.Equal(t, int64(jsonMB)<<20, v.JSONMaxBytes, "R1Values ignores the blob")

	put := func(field string, value int) {
		t.Helper()
		w := c.serve(http.MethodPut, "/api/v1/config", []byte(fmt.Sprintf(`{%q:%d}`, field, value)))
		require.Equal(t, http.StatusOK, w.Code, "PUT /api/v1/config: %s", w.Body.String())
	}
	put("concurrent_scans", 3)
	first := configBlob(t, c.store)
	assert.JSONEq(t, strconv.Itoa(rate), string(first["api_rate_limit_per_minute"]))
	assert.Equal(t, strconv.Itoa(rate), string(first["api_rate_limit_per_minute"]), "a blob-only field changed bytes")
	assert.Equal(t, strconv.Itoa(jsonMB), string(first["json_body_limit_mb"]), "a blob-only field changed bytes")
	assert.Equal(t, "3", string(first["concurrent_scans"]))

	// The PUT wrote the whole config; a second one-field PUT must leave every
	// other field of that full blob byte-identical.
	put("concurrent_scans", 4)
	second := configBlob(t, c.store)
	require.Greater(t, len(first), 50, "the blob after a PUT is not the whole config")
	for k, before := range first {
		if k == "concurrent_scans" {
			continue
		}
		if !bytes.Equal(before, second[k]) {
			t.Errorf("blob field %s changed on a PUT of concurrent_scans: %s -> %s", k, before, second[k])
		}
	}
	assert.Equal(t, "4", string(second["concurrent_scans"]))
	assert.Len(t, second, len(first), "the PUT added or dropped blob fields")
}

// TestABOMetricsGatherNoDuplicate (05-metrics §6; F2-03 extends it): the
// App's Provider with ABO's real recorders on it (InstallProcessDefaults,
// the legacy client_golang families through Registerer(), opsmetrics.Default()
// and the AI call recorders on the global meter provider) gathers with no
// error after one legacy request on a sampled route and one AI call through a
// fake endpoint, and no family is served twice.
func TestABOMetricsGatherNoDuplicate(t *testing.T) {
	if parent, _ := inFreshProcess(t); parent {
		return
	}
	c := bootComposed(t, false, nil, nil)

	w := c.serve(http.MethodGet, "/api/v1/health", nil)
	require.Equal(t, http.StatusOK, w.Code, "GET /api/v1/health: %s", w.Body.String())
	opsmetrics.Default().Started("batcave-scan")

	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		content, _ := json.Marshal(`{"title":"The Long Halloween","author":"Harvey Dent"}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"x","object":"chat.completion","created":0,"model":"oracle","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":%s}}],"usage":{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8}}`, content)
	}))
	defer fake.Close()
	p := ai.NewOpenAIParserWithBaseURL(nil, "oracle-key", fake.URL+"/v1", "oracle", true)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_, _ = p.ParseFilename(ctx, "The Long Halloween - Harvey Dent.m4b")

	mfs, err := c.app.Metrics().Gatherer().Gather()
	require.NoError(t, err, "the served registry has a family with two owners")
	seen := map[string]bool{}
	var names []string
	for _, mf := range mfs {
		require.False(t, seen[mf.GetName()], "family %s gathered twice", mf.GetName())
		seen[mf.GetName()] = true
		names = append(names, mf.GetName())
	}
	// Each leg reached the served registry: the legacy client_golang
	// families, the OTel recorders through the global meter provider, and the
	// kernel's own.
	for _, re := range []string{`^http_server_request_duration_seconds$`, `^audiobook_organizer_ops_runs_total$`,
		`^ai_requests_total$`, `^audiobook_organizer_kernel_`, `^audiobook_organizer_pebble_`} {
		if !slices.ContainsFunc(names, regexp.MustCompile(re).MatchString) {
			t.Errorf("no gathered family matches %s; the leg did not reach the served registry:\n%s", re, strings.Join(names, "\n"))
		}
	}

	// The legacy /metrics route serves the same registry.
	w = c.serve(http.MethodGet, "/metrics", nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "audiobook_organizer_kernel_")
}

// hostileOTLPSentinel stands in for a credential in OTEL_EXPORTER_OTLP_*.
const hostileOTLPSentinel = "Joker-OTLP-credential-sentinel"

// TestServeComposition_HostileOTLPEnvNeverLogged: serve's composition with
// OTLP tracing and metric push on and hostile OTEL_EXPORTER_OTLP_* values in
// the environment (malformed headers, endpoint and timeouts carrying a
// sentinel, as a credential would be). Building the exporters makes the OTel
// SDK report each bad value raw; the value-dropping sink, installed by
// app.New before the Provider (Options.ProcessDefaults), must be the only
// thing that hears it. The sentinel never reaches the process's stdout or
// stderr (the parent checks the child's whole output) nor the App's log
// capture, and the sink's line proves the SDK did report the input.
func TestServeComposition_HostileOTLPEnvNeverLogged(t *testing.T) {
	parent, out := inFreshProcess(t,
		"OTEL_EXPORTER_OTLP_ENDPOINT=http://192.0.2.1:4317",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT=http://192.0.2.1:4317",
		"OTEL_EXPORTER_OTLP_HEADERS="+hostileOTLPSentinel,
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://["+hostileOTLPSentinel,
		"OTEL_EXPORTER_OTLP_TIMEOUT="+hostileOTLPSentinel,
		"OTEL_EXPORTER_OTLP_METRICS_HEADERS="+hostileOTLPSentinel,
	)
	if parent {
		if strings.Contains(out, hostileOTLPSentinel) {
			t.Fatalf("a hostile OTLP env value reached the process's output:\n%s", out)
		}
		return
	}
	// serve reads the environment into AppConfig (cmd/root.go InitConfig)
	// and snapshots the telemetry config before the stored settings load.
	config.InitConfig()
	env := config.Snapshot()
	require.NotEmpty(t, env.OTelExporterOTLPEndpoint, "OTEL_EXPORTER_OTLP_ENDPOINT not read")
	require.NotEmpty(t, env.OTelMetricsOTLPEndpoint, "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT not read")
	c := bootComposed(t, false, func(c *config.Config) {
		c.OTelExporterOTLPEndpoint = env.OTelExporterOTLPEndpoint
		c.OTelMetricsOTLPEndpoint = env.OTelMetricsOTLPEndpoint
		c.OTelMetricsOTLPInsecure = true
	}, nil)
	logged := c.newLog.String()
	if strings.Contains(logged, hostileOTLPSentinel) {
		t.Fatalf("a hostile OTLP env value reached the App's log:\n%s", logged)
	}
	if !strings.Contains(logged, "values dropped") || !strings.Contains(logged, "parse headers") {
		t.Fatalf("the SDK's report of the hostile env never reached the sink (the test is blind):\n%s", logged)
	}
}

// TestComposedApp_UnmatchedMethodMatchesGin is internal/server's
// TestLegacyMount_UnmatchedMethodMatchesGin against the composed App: books'
// real mount (its claims, top segments and redirects, from Register) behind
// the kernel's root handler. Every method a manifest path does not route
// answers as HEAD's engine did: status, content type and body. OPTIONS is
// the known difference of a.Handler(): the perimeter shim in front of it in
// production sends OPTIONS straight to the engine (perimeter_shim_test.go
// pins that).
func TestComposedApp_UnmatchedMethodMatchesGin(t *testing.T) {
	for _, abs := range []bool{false, true} {
		t.Run("abs="+strconv.FormatBool(abs), func(t *testing.T) {
			c := bootComposed(t, abs, nil, nil)
			engine := c.srv.LegacyHandler()
			routed := map[string]bool{}
			var paths []string
			for _, lr := range server.LegacyRoutes(abs) {
				p := concretePath(lr.GinPath)
				if !slices.Contains(paths, p) {
					paths = append(paths, p)
				}
				routed[lr.Method+" "+p] = true
			}
			methods := []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}
			n := 0
			for _, p := range paths {
				for _, m := range methods {
					if routed[m+" "+p] {
						continue
					}
					n++
					g, k := probe(engine, m, p), probe(c.app.Handler(), m, p)
					assert.Equal(t, g.Code, k.Code, "%s %s", m, p)
					assert.Equal(t, g.Header().Get("Content-Type"), k.Header().Get("Content-Type"), "%s %s", m, p)
					assert.Equal(t, g.Body.String(), k.Body.String(), "%s %s", m, p)
				}
			}
			assert.Greater(t, n, 1000, "too few unrouted method/path pairs; the scan is broken")
			for _, p := range []string{"/login", "/socket.io/", "/ping/riddler", "/metrics/riddler", "/library/riddler"} {
				assert.Equal(t, probe(engine, http.MethodGet, p).Code, probe(c.app.Handler(), http.MethodGet, p).Code, "GET %s", p)
			}
		})
	}
}

// concretePath fills a gin path's parameters with a placeholder segment.
func concretePath(gin string) string {
	segs := strings.Split(gin, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, ":") || strings.HasPrefix(s, "*") {
			segs[i] = "riddler"
		}
	}
	return strings.Join(segs, "/")
}

// probe sends one request with an already-cancelled context, so a handler
// that does run returns at once.
func probe(h http.Handler, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req.WithContext(ctx))
	return w
}

// notifySocket binds a unix datagram socket and points NOTIFY_SOCKET at it
// for the test. It lives under os.MkdirTemp rather than t.TempDir: a socket
// path is limited to 104 bytes on darwin and t.TempDir's passes that.
func notifySocket(t *testing.T) *net.UnixConn {
	t.Helper()
	dir, err := os.MkdirTemp("", "sdn")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "notify.sock")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	t.Setenv("NOTIFY_SOCKET", path)
	return conn
}

// TestComposedApp_SystemdSignals runs the composed App under a fake
// NOTIFY_SOCKET. R1-16 moved READY=1, STATUS= and STOPPING=1 from the server
// into the kernel, and books wires its search index's long open step to
// Deps.ExtendStart: the start hook and the index open must ask for start time
// (before READY=1), READY=1 must come after the listener is up, and the stop must
// send STOPPING=1.
func TestComposedApp_SystemdSignals(t *testing.T) {
	sock := notifySocket(t)
	c := bootComposed(t, false, nil, nil)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.app.Run(ctx) }()

	var got []string
	next := func() string {
		t.Helper()
		require.NoError(t, sock.SetReadDeadline(time.Now().Add(60*time.Second)))
		buf := make([]byte, 4096)
		n, err := sock.Read(buf)
		require.NoError(t, err, "datagrams so far: %q", got)
		got = append(got, string(buf[:n]))
		return got[len(got)-1]
	}
	require.Equal(t, "STATUS=starting", next())
	extends := 0
	for msg := next(); msg != "READY=1"; msg = next() {
		require.Equal(t, "EXTEND_TIMEOUT_USEC=600000000", msg, "only start-timeout extensions may precede READY=1")
		extends++
	}
	// Two: books' start hook asks for the step budget up front (HEAD's start
	// ran under TimeoutStartSec=300, the kernel's window is 60 s), and the
	// search index open asks again before its long step.
	require.Equal(t, 2, extends, "books' start and its search index open ask for start time through Deps.ExtendStart: %q", got)
	require.Equal(t, "STATUS=Serving; readiness is reported by the health endpoint", next())

	cancel()
	require.Equal(t, "STOPPING=1", next())
	require.Equal(t, "STATUS=Shutting down", next())
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(90 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}
