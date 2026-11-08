// file: core/internal/httpmw/chain.go
// version: 1.0.0
// guid: 43e0ed35-4635-49ca-aaab-d5bfe2f6fa1b
// last-edited: 2026-11-08

package httpmw

import (
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/metrics"
)

// Limit defaults (05-http-api §10).
const (
	DefaultPerMinute  = 100
	DefaultMaxClients = 10000
	DefaultJSONMax    = 1 << 20
	DefaultUploadMax  = 10 << 20
)

// ProblemFunc writes a problem+json answer (httpapi's writer, so every core
// stage answers in the one Problem shape of 05-http-api §3.3).
type ProblemFunc func(w http.ResponseWriter, r *http.Request, status int, code string, header http.Header)

// Deps are the chain's fixed dependencies, set once when httpapi builds it.
type Deps struct {
	Log      *slog.Logger
	Redactor *log.Redactor // the kernel's one Redactor (05-logging); nil = a default one
	// Metrics is core's "<namespace>.http" scope (§11): inflight, panics,
	// rate_limited, cross_origin_rejected, peer_refused. nil records nothing.
	Metrics *metrics.Scope
	// Meter is the "httpapi" meter of the App's MeterProvider: stage 2's
	// three semconv instruments (§4.2 stage 2). nil records nothing.
	Meter      metric.Meter
	Tracer     trace.Tracer // stage 2's server spans; nil = no spans
	Propagator propagation.TextMapPropagator
	ServerName string // server.address (MOD10-2)
	Problem    ProblemFunc
	// BuildID is stage 5's X-App-Build: the "/" owner's UI build ID, "" =
	// "dev" (FE3-2).
	BuildID string
	// Observe, when set, receives each request's recorded route after the
	// handler returned (a test seam beside stage 2's own recording).
	Observe func(route string)
	// Trace, when set, is called with each stage's number as the stage
	// starts (TestChain_OrderGolden).
	Trace func(stage int)
	// Inner, when set, wraps the router inside stage 9 (a test seam: a
	// stage between stage 2 and the mux that clones the request).
	Inner func(http.Handler) http.Handler
	// OwnerOf names the owner of a request no route has claimed yet, for
	// the owner label of a refusal before routing (stage 7); nil = "core".
	OwnerOf func(*http.Request) string
}

// Peer is a prefix or a single address that may carry an IPv6 zone
// (httpapi.TrustedPeer): Prefix when Addr is zero.
type Peer struct {
	Prefix netip.Prefix
	Addr   netip.Addr
}

// Settings are the perimeter values that may change after Build: in R1 set
// once per boot from app.Options.R1Values before the first listener opens
// (05-config §1.12.1), hot-reloaded from F3 (05-http-api §9).
type Settings struct {
	// PeerAllowlist: nil or empty accepts every peer (stage 0 off).
	PeerAllowlist  []Peer
	TrustedProxies []Peer
	ClientIPHeader string   // "" = RemoteAddr only
	AllowedOrigins []string // CORS allow-list and stage 7's trusted origins
	RateLimit      bool
	PerMinute      int // 0 = DefaultPerMinute
	Burst          int // 0 = max(PerMinute/5, 10)
	MaxClients     int // 0 = DefaultMaxClients; read once, at New
	JSONMaxBytes   int64
	UploadMaxBytes int64
}

// Chain is one router's perimeter: its compiled settings, rate limiter and
// instruments. Outer wraps the router, Route wraps each route's handler.
type Chain struct {
	deps    Deps
	cur     atomic.Pointer[compiled]
	recomp  sync.Mutex // one re-resolve of pending zones at a time
	limiter *limiter
	inst    instruments
	refused *onceSet   // stage 0's refused peers, logged once each
	gzip    *sync.Pool // stage 9's compressors

	ifaces      func() ([]net.Interface, error) // this host's interfaces (zone resolution)
	rand        func([]byte) (int, error)       // stage 1's random source
	fallbackSeq atomic.Uint64                   // stage 1's local-<n> fallback IDs
}

// New builds a chain with s compiled. An instrument that cannot be created
// is an error, joined; the chain still works without it.
func New(d Deps, s Settings) (*Chain, error) {
	if d.Redactor == nil {
		d.Redactor = log.NewRedactor(nil)
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Problem == nil {
		d.Problem = plainProblem
	}
	c := &Chain{deps: d, refused: newOnceSet(deniedPeerLogCap), gzip: newGzipPool(), ifaces: net.Interfaces, rand: rand.Read}
	maxClients := s.MaxClients
	if maxClients <= 0 {
		maxClients = DefaultMaxClients
	}
	c.limiter = newLimiter(maxClients, time.Now)
	ierr := c.inst.init(d)
	cerr := c.Set(s)
	return c, errors.Join(ierr, cerr)
}

// Set compiles s and swaps it in; readers see the old or the new value,
// never a mix (05-http-api §9). An unusable entry is logged and dropped,
// and returned joined; the rest of s applies.
func (c *Chain) Set(s Settings) error {
	cp, errs := compile(s, time.Now(), c.ifaces)
	c.cur.Store(cp)
	for _, e := range errs {
		c.deps.Log.Warn("httpapi: perimeter setting ignored", "error", e)
	}
	return errors.Join(errs...)
}

// current returns the compiled settings, first re-resolving them when an
// entry's zone named no interface and the retry interval passed (the
// interface may come up after start, client_trust.go's connector rule).
func (c *Chain) current() *compiled {
	cp := c.cur.Load()
	if !cp.pending || time.Since(cp.at) < zoneRetryAfter {
		return cp
	}
	c.recomp.Lock()
	defer c.recomp.Unlock()
	if cur := c.cur.Load(); cur != cp {
		return cur
	}
	next, _ := compile(cp.s, time.Now(), c.ifaces)
	c.cur.Store(next)
	return next
}

// stage is one numbered entry of the chain.
type stage struct {
	num  int
	wrap func(http.Handler) http.Handler
}

// outer is the chain around the router, outermost first: §4.2's table
// without stage 8 (gin's in R1) and without the route stages 10-11.
func (c *Chain) outer() []stage {
	return []stage{
		{0, c.peer},
		{1, c.requestID},
		{2, c.telemetry},
		{3, c.recovery},
		{4, c.accessLog},
		{5, c.headers},
		{6, c.cors},
		{7, c.crossOrigin},
		{9, c.compress},
	}
}

// route is the chain inside each route's wrapper.
func (c *Chain) route(spec RouteSpec) []stage {
	return []stage{
		{10, func(h http.Handler) http.Handler { return c.rateLimit(spec, h) }},
		{11, func(h http.Handler) http.Handler { return c.bodyLimit(spec, h) }},
	}
}

// Order returns the stage numbers in the order a routed request runs them:
// the outer chain, then the route chain (TestChain_OrderGolden).
func (c *Chain) Order() []int {
	var out []int
	for _, s := range c.outer() {
		out = append(out, s.num)
	}
	for _, s := range c.route(RouteSpec{}) {
		out = append(out, s.num)
	}
	return out
}

// Outer wraps the router with the stages that run for every request.
func (c *Chain) Outer(router http.Handler) http.Handler {
	h := router
	if c.deps.Inner != nil {
		h = c.deps.Inner(h)
	}
	h = c.compose(c.outer(), h)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(withState(r.Context(), &State{})))
	})
}

// RouteSpec is what the route stages need of one registered route.
type RouteSpec struct {
	Pattern string // recorded as http.route (method and host stripped)
	Policy  *Policy
	Class   string // RateClass name: "" or "exempt" skip stage 10
	// MaxBody is the route's body limit; 0 = the class default (JSON or
	// upload by Content-Type); -1 = no core limit (legacy routes, whose gin
	// groups keep their own limits).
	MaxBody int64
}

// Route wraps one registered route's handler: it records the pattern and
// the owner's policy in the request's State, then runs stages 10-11.
func (c *Chain) Route(spec RouteSpec, h http.Handler) http.Handler {
	inner := c.compose(c.route(spec), h)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if st := From(r.Context()); st != nil {
			st.SetRoute(spec.Pattern)
			c.own(r, st, spec.Policy)
		}
		inner.ServeHTTP(w, r)
	})
}

// Owned records p as the owner of a response the router writes or hands to
// a handler without a route (the "/" owner, a legacy-shape 404, a legacy
// redirect, a problem 404). nil is core's own answer.
func (c *Chain) Owned(r *http.Request, p *Policy) {
	if st := From(r.Context()); st != nil {
		c.own(r, st, p)
	}
}

func (c *Chain) own(r *http.Request, st *State, p *Policy) {
	if p != nil {
		st.SetPolicy(p)
	}
	c.enter(r.Context(), st, st.owner())
}

func (c *Chain) compose(stages []stage, h http.Handler) http.Handler {
	for i := len(stages) - 1; i >= 0; i-- {
		h = stages[i].wrap(h)
		if c.deps.Trace != nil {
			num, next := stages[i].num, h
			trace := c.deps.Trace
			h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				trace(num)
				next.ServeHTTP(w, r)
			})
		}
	}
	return h
}

// plainProblem is the fallback ProblemFunc: a minimal problem+json body.
func plainProblem(w http.ResponseWriter, r *http.Request, status int, code string, header http.Header) {
	for k, vs := range header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"type":"https://mediacore.dev/problems/%s","title":%q,"status":%d,"code":%q,"request_id":%q}`+"\n",
		code, http.StatusText(status), status, code, RequestID(r.Context()))
}
