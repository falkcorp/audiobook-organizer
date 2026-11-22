// file: internal/books/composed_test.go
// version: 1.6.1
// guid: 4b8f2e61-9a3d-4c75-a0e2-6d1f8b3c5e97
// last-edited: 2026-11-18

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
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/opsmetrics"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
	"github.com/falkcorp/audiobook-organizer/internal/server"
	"github.com/falkcorp/mediacore/app"
	coreconfig "github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks/taskstest/parity"
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
// fresh in-memory store core opens: books.Module, Options, app.New,
// app.InstallProcessDefaults and Attach.
type composed struct {
	app   *app.App
	mod   *booksModule
	srv   *server.Server
	store *database.PebbleStore
	// newLog is the stream the App's log pipeline prints to (ServeInputs.
	// LogOut); the activity feed is a tap beside it.
	newLog *syncBuffer
	// expectViolations exempts this boot from the claim guard
	// (checkClaimViolations); expectClaimViolations sets it.
	expectViolations bool
	// restore puts the process defaults back, once: runningApp.stop calls
	// it after Run returns, as serve's deferred restore does, so a test can
	// boot the same store again; Cleanup calls it otherwise.
	restore func()
}

// expectClaimViolations exempts c from the claim guard: the test makes an
// out-of-claim access on purpose, to check strict claims refuse it.
func (c *composed) expectClaimViolations() { c.expectViolations = true }

// checkClaimViolations is the composed harness's claim guard (R1-23 review
// B1), the kernel-opened twin of the standalone opener's
// (database.claimGuardedClose): after the app has stopped it fails the test
// when core counted any out-of-claim access, naming each series and its
// first keys, unless the test called expectClaimViolations. It reads this
// app's own core DB through its admin surface, not the process-wide metric,
// so a parallel test's violation is never blamed on this one.
func checkClaimViolations(t *testing.T, a *app.App, c *composed) {
	t.Helper()
	if c != nil && c.expectViolations {
		return
	}
	adm := a.StorageAdmin()
	if adm == nil {
		return
	}
	counts, keys := adm.ClaimViolations(), adm.ClaimViolationKeys()
	var lines []string
	for v, n := range counts {
		lines = append(lines, fmt.Sprintf("%s %s %s x%d: %s", v.Owner, v.Op, v.Mode, n, strings.Join(keys[v], "; ")))
	}
	slices.Sort(lines)
	if len(lines) > 0 {
		t.Errorf("storage claim guard: out-of-claim accesses this boot did not expect (register the family in "+
			"internal/books/store/keyfamilies.go, or call expectClaimViolations):\n  %s", strings.Join(lines, "\n  "))
	}
}

// bootComposed boots the composition with the ABS surface on or off; edit,
// when set, adjusts the config before the store opens. pre, when set, runs
// on books' store in Register, after core's migrations and before the
// settings boot and the server (a stored settings blob, say). Cleanup releases the unstarted server and the
// store and puts the process defaults back.
func bootComposed(t *testing.T, abs bool, edit func(*config.Config), pre func(*database.PebbleStore)) *composed {
	t.Helper()
	return bootComposedWith(t, abs, "", edit, pre, nil)
}

// bootComposedIn is bootComposed with the secure state directory at
// stateDir (it gets the encryption key): books.Options resolves it, and the
// log file is opened under it (logSettings), as in serve. "" is a fresh
// temporary directory.
func bootComposedIn(t *testing.T, abs bool, stateDir string, edit func(*config.Config), pre func(*database.PebbleStore)) *composed {
	t.Helper()
	return bootComposedWith(t, abs, stateDir, edit, pre, nil)
}

// bootComposedWith is bootComposedIn with opt, when set, adjusting the App's
// options after books.Options builds them and before app.New, and each wrap,
// in order, wrapping the module app.New registers (preFlip boots the build
// that predates the flip). Attach and the composition's handles still reach
// the books module inside.
func bootComposedWith(t *testing.T, abs bool, stateDir string, edit func(*config.Config), pre func(*database.PebbleStore), opt func(*app.Options), wrap ...func(module.Module) module.Module) *composed {
	t.Helper()
	mod, opts, newLog := composedOptions(t, abs, stateDir, edit, pre)
	if opt != nil {
		opt(&opts)
	}
	var registered module.Module = mod
	for _, w := range wrap {
		registered = w(registered)
	}
	a, err := app.New(t.Context(), opts, registered)
	var c *composed
	if a != nil {
		// Registered first, so it runs last, once the app has stopped and
		// every writer is done.
		t.Cleanup(func() { checkClaimViolations(t, a, c) })
		// Registered before Release, so it runs after it: books' store
		// closes, then core (a no-op once Run has begun, whose stop closed
		// both).
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = a.Close(ctx)
		})
	}
	t.Cleanup(func() { Release(mod) })
	require.NoError(t, err)
	installed, err := app.InstallProcessDefaults(a)
	require.NoError(t, err)
	restore := sync.OnceFunc(installed)
	t.Cleanup(restore)
	require.NoError(t, Attach(mod, a))
	srv := mod.srv.Load()
	require.NotNil(t, srv, "Register built no server")
	store := mod.store.Load()
	require.NotNil(t, store, "Register built no store")
	c = &composed{app: a, mod: mod, srv: srv, store: store, newLog: newLog, restore: restore}
	return c
}

// composedOptions is the composition's module and options before app.New:
// the config (edit applied), a sandboxed state directory holding a key
// (stateDir, or a fresh temporary one), the settings load stubbed,
// books.Module and books.Options over an in-memory store at the config's
// database path. pre and the seeded roles run in Register
// (booksModule.beforeSettings).
func composedOptions(t *testing.T, abs bool, stateDir string, edit func(*config.Config), pre func(*database.PebbleStore)) (*booksModule, app.Options, *syncBuffer) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := tempDir(t)
	// The store is in memory, but config.Validate wants the database's
	// directory to exist, as it does on a real install.
	require.NoError(t, os.MkdirAll(dir, 0o700))
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
	// The store path is a layer now (core.storage.path), not the loaded
	// config's database_path: name the one the fixture's config holds.
	t.Setenv(storagePathEnv, c.DatabasePath)
	require.NoError(t, os.MkdirAll(filepath.Dir(c.DatabasePath), 0o700))
	// The ABS signing secret is an operator-only secret (class o): its layer is
	// the environment, not the loaded config the fixture also sets.
	if c.ABSJWTSecret != "" {
		t.Setenv("ABO_BOOKS_ABS_JWT_SECRET", c.ABSJWTSecret)
	}
	// Sign-in is core's auth mode (class k, fail-closed): a fixture whose
	// config leaves auth off names it the way an operator does, in the
	// environment. A fixture that turns it on leaves core's default (required).
	if !c.EnableAuth {
		t.Setenv("ABO_CORE_AUTH_MODE", "disabled")
	}
	if !c.BasicAuthEnabled {
		t.Setenv("ABO_CORE_HTTP_BASIC_GATE_ENABLED", "false")
	}
	sandboxStateDirAt(t, stateDir)
	t.Cleanup(func() {
		database.SetGlobalStore(nil)
		scanner.SetStore(nil)
	})

	mod := Module().(*booksModule)
	mod.beforeSettings = func(store *database.PebbleStore) {
		// Under the identity flip the role rows are a retired claim (core owns
		// the roles): HEAD's seed would be refused, correctly.
		if !identityOn() {
			_, _, err := auth.SeedRoles(store)
			require.NoError(t, err)
		}
		if pre != nil {
			pre(store)
		}
	}
	newLog := &syncBuffer{}
	// The listener binds core.http.listen (start step 7), not the server
	// config's host and port: an environment layer puts it on a free loopback
	// port, where ABO's compiled default (localhost:8484) would collide.
	t.Setenv("ABO_CORE_HTTP_LISTEN", "127.0.0.1:0")
	cfg := server.GetDefaultServerConfig()
	opts, err := Options(mod, ServeInputs{Server: cfg, Telemetry: config.Snapshot(), LogOut: newLog})
	require.NoError(t, err)
	opts.StorageFS = storage.MemFS()
	return mod, opts, newLog
}

// sandboxStateDir points the secure state directory at a temp dir holding an
// encryption key: an install that has run before, so the key guard runs no
// census, and no test touches the real /var/lib/audiobook-organizer.
func sandboxStateDir(t *testing.T) string {
	t.Helper()
	return sandboxStateDirAt(t, "")
}

// sandboxStateDirAt is sandboxStateDir at dir ("" is a fresh temporary
// directory).
func sandboxStateDirAt(t *testing.T, dir string) string {
	t.Helper()
	config.ResetStateDirForTest()
	t.Cleanup(config.ResetStateDirForTest)
	if dir == "" {
		dir = t.TempDir()
	}
	require.NoError(t, os.MkdirAll(dir, 0o700))
	t.Setenv(config.SecureStateDirEnv, dir)
	require.NoError(t, os.WriteFile(database.EncryptionKeyPath(dir), bytes.Repeat([]byte{9}, 32), 0o600))
	return dir
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
	return runComposedWith(t, nil, nil, nil, nil)
}

// runComposedWith is runComposed with edit, pre, opt and wrap as
// bootComposedWith takes them, and booted, when set, called with the composition after
// app.New and before Run. When it returns, Run has opened the listener, so
// every start hook has returned (06 §6.5.2 start steps (2) and (7)).
func runComposedWith(t *testing.T, edit func(*config.Config), pre func(*database.PebbleStore), opt func(*app.Options), booted func(*composed), wrap ...func(module.Module) module.Module) *runningApp {
	t.Helper()
	return runComposedIn(t, "", edit, pre, opt, booted, wrap...)
}

// runComposedIn is runComposedWith over the state directory stateDir ("" is
// a fresh temporary one), so two boots of one store share their swap slot
// and keys.
func runComposedIn(t *testing.T, stateDir string, edit func(*config.Config), pre func(*database.PebbleStore), opt func(*app.Options), booted func(*composed), wrap ...func(module.Module) module.Module) *runningApp {
	t.Helper()
	addrs := make(chan net.Addr, 1)
	c := bootComposedWith(t, false, stateDir, edit, pre, func(o *app.Options) {
		if opt != nil {
			opt(o)
		}
		listen := o.R1Listen
		o.R1Listen = func(lo httpapi.ListenOptions, h http.Handler, l *slog.Logger) (app.R1Server, error) {
			srv, err := listen(lo, h, l)
			if err == nil {
				addrs <- srv.Addr()
			}
			return srv, err
		}
	}, wrap...)
	if booted != nil {
		booted(c)
	}
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

// stop cancels Run and waits for it to return, then puts the process
// defaults back as serve does once Run has returned; later calls return the
// same result.
func (r *runningApp) stop() error {
	r.once.Do(func() {
		r.cancel()
		select {
		case r.runErr = <-r.ran:
			r.restore()
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

	// The executor cell (05-metrics §12, §13 item 1; F2-03): a standalone
	// core executor on its own Provider, under ABO's namespace so that a
	// leak would collide by name, records all 18 of its families there.
	// ABO builds no executor before F5, so ABO's gather must still succeed
	// with one owner per family, and no ops series in it may come from
	// core/internal/executor: none carries the probe's def_id, and no
	// executor family that ABO did not already serve appears.
	probe := coretest.MetricsProvider(t, app.Options{MetricNamespace: metricNamespace})
	probeFamilies := parity.ExerciseInstruments(t, probe)
	mfs, err = c.app.Metrics().Gatherer().Gather()
	require.NoError(t, err, "ABO's registry gather after a core executor ran beside it")
	after := map[string]bool{}
	for _, mf := range mfs {
		require.False(t, after[mf.GetName()], "family %s gathered twice", mf.GetName())
		after[mf.GetName()] = true
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				assert.NotEqual(t, string(parity.ProbeKind), l.GetValue(),
					"family %s in ABO's registry has a series of the core executor's probe (label %s)", mf.GetName(), l.GetName())
			}
		}
	}
	for _, f := range probeFamilies {
		if !seen[f] {
			assert.False(t, after[f], "core executor family %s reached ABO's registry", f)
		}
	}
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
	// The telemetry config serve snapshots carries the endpoints; the hostile
	// OTEL_EXPORTER_OTLP_* values stay in the environment for the OTel SDK.
	c := bootComposed(t, false, func(c *config.Config) {
		c.OTelExporterOTLPEndpoint = "http://192.0.2.1:4317"
		c.OTelMetricsOTLPEndpoint = "http://192.0.2.1:4317"
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
	// Core storage's open and migrations ran inside app.New and asked for
	// start time per step (R1-23: the kernel opens the store); those
	// datagrams wait in the socket ahead of Run's.
	msg := next()
	for ; msg != "STATUS=starting"; msg = next() {
		require.Equal(t, "EXTEND_TIMEOUT_USEC=600000000", msg, "only start-timeout extensions may precede STATUS=starting")
	}
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

// ABO_CORE_SHUTDOWN_BUDGET is core's own name for core.shutdown.budget: serve
// no longer reads it and hands the value on (R1-33), the config boot does, so
// a value that is not a positive duration refuses the boot naming the key, and
// a good one boots.
func TestOptions_ShutdownBudgetEnvIsCoreConfig(t *testing.T) {
	t.Run("a bad value is refused naming the key", func(t *testing.T) {
		mod, opts, _ := composedOptions(t, false, "", nil, nil)
		t.Cleanup(func() { Release(mod) })
		t.Setenv("ABO_CORE_SHUTDOWN_BUDGET", "after-the-batsignal")
		a, err := app.New(t.Context(), opts, mod)
		if a != nil {
			t.Cleanup(func() { _ = a.Close(context.Background()) })
		}
		require.Error(t, err)
		assert.Contains(t, err.Error(), "core.shutdown.budget")
	})
	t.Run("a good value boots", func(t *testing.T) {
		t.Setenv("ABO_CORE_SHUTDOWN_BUDGET", "40s")
		bootComposed(t, false, nil, nil)
	})
}

// ABO's compiled core defaults (coreDefaults) are what core.http.listen and
// core.http.client_ip_header resolve to with no operator layer: one interface,
// as --host always defaulted, and the header the tunnel connectors set. An
// operator layer beats them.
func TestOptions_CoreDefaultsAreTheEffectiveValues(t *testing.T) {
	// bootComposedWith restores the process defaults the App installs; edit
	// runs after the harness set its environment and before app.New.
	lookup := func(t *testing.T, edit func()) func(coreconfig.Path) coreconfig.KeyState {
		c := bootComposedWith(t, false, "", nil, nil, func(*app.Options) { edit() })
		in := c.mod.gapFillIn.Load()
		require.NotNil(t, in, "the books module has no config access")
		return func(p coreconfig.Path) coreconfig.KeyState {
			st, ok := in.acc.Lookup(p)
			require.True(t, ok, "%s is not registered", p)
			return st
		}
	}
	t.Run("no operator layer", func(t *testing.T) {
		get := lookup(t, func() { require.NoError(t, os.Unsetenv("ABO_CORE_HTTP_LISTEN")) })
		assert.JSONEq(t, `"localhost:8484"`, string(get("core.http.listen").Effective))
		assert.JSONEq(t, `"CF-Connecting-IP"`, string(get("core.http.client_ip_header").Effective))
	})
	t.Run("an operator layer beats them", func(t *testing.T) {
		get := lookup(t, func() {
			t.Setenv("ABO_CORE_HTTP_LISTEN", "192.0.2.8:9000")
			t.Setenv("ABO_CORE_HTTP_CLIENT_IP_HEADER", "X-Wayne-Client")
		})
		assert.JSONEq(t, `"192.0.2.8:9000"`, string(get("core.http.listen").Effective))
		assert.JSONEq(t, `"X-Wayne-Client"`, string(get("core.http.client_ip_header").Effective))
	})
}

// ServeInputs.FromRelease (the pre-freeze tag main embeds) becomes
// app.Options.FromRelease, which the pre-migration checkpoint records as
// from_release (09 §9.2.1).
func TestOptions_FromReleaseReachesCore(t *testing.T) {
	c := bootComposed(t, false, nil, nil)
	snap := config.Snapshot()
	for _, want := range []string{"", "v0.232.0"} {
		opts, err := Options(c.mod, ServeInputs{Server: c.mod.serveConfig(), Telemetry: snap, FromRelease: want})
		require.NoError(t, err)
		assert.Equal(t, want, opts.FromRelease)
	}
}

// storageEnv is the core.storage.path layer a fixture that replaces
// app.Options.Env must keep: the path the fixture's config holds, which
// composedOptions put in the process environment for ResolveBootPaths.
func storageEnv() string { return storagePathEnv + "=" + os.Getenv(storagePathEnv) }

// listenEnv puts the listener on a free loopback port. A harness that
// replaces app.Options.Env drops the process environment, and with it the
// ABO_CORE_HTTP_LISTEN runComposedIn sets, so the listener would bind ABO's
// compiled default 127.0.0.1:8484 and fail wherever that port is taken (a
// host running the service, or two packages testing at once).
const listenEnv = "ABO_CORE_HTTP_LISTEN=127.0.0.1:0"

// bootEnv is the environment a harness that replaces app.Options.Env gives a
// booting service: the storage path, the free listen port, then extra.
func bootEnv(extra ...string) []string {
	return append([]string{storageEnv(), listenEnv}, extra...)
}

// absEnv is the ABS layers (flag and signing secret, both operator-only) a
// fixture that replaces app.Options.Env keeps when its config serves the ABS
// surface: the module reads the flag from the process environment, and the
// config boot must see the same value or the boot refuses (errABSFlagDrift).
func absEnv() []string {
	var env []string
	for _, k := range []string{absEnabledEnv, "ABO_BOOKS_ABS_JWT_SECRET"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}
