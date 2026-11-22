// file: internal/server/legacy_manifest_gin_gates_test.go
// version: 0.1.0
// guid: 5c3a8e1d-7b24-4f60-9d1e-3a6f8b2c4e97
// last-edited: 2026-11-22

package server

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// This file records, for every route of the real gin engine, the gates its
// chain carries (the atom of each s.perm, the kind of each admin gate, the
// credential and owner guards, the authentication gate), so
// legacy_manifest_gin_crosscheck_test.go can hold each manifest row's
// requirement to them. The gates are not read from a table: each gate
// constructor reports itself through gateTraceForTest while the engine is
// built (gate_trace.go), and the recorder it installs is a no-op gate that
// notes what it stands for. The chain a route's request meets is gin's own
// (c.Handlers), so a gate registered on a group, on a route, through credRoute,
// ownerCredRoute or adminGroup, or handed to another package's Register, is
// found the same way.
//
// A probe request never runs a real gate or handler: the probe middleware
// (first in the engine) counts the recorders in the matched chain, the
// recorders note themselves in chain order, and the last one answers 204.
// A route with no recorder in its chain stops at the probe.

// TracedGate is one gate a route's gin chain carries.
type TracedGate = gateInfo

// RouteChain is what a probe found in one route's chain.
type RouteChain struct {
	// Gates are the recorded gates, in chain order.
	Gates []TracedGate
	// Other are the names of the chain's elements that are neither a recorded
	// gate, the probe, nor the route's own handler (the last element): the
	// global and group middleware, and any gate whose constructor does not
	// report itself.
	Other []string
}

const gateTraceCtxKey = "gateTrace"

type chainTrace struct {
	total int
	gates []TracedGate
	other []string
}

// gateRecorder installs the probe and the recording gates and keeps what they
// found, by "METHOD /gin/path".
type gateRecorder struct {
	mu        sync.Mutex
	chains    map[string]RouteChain
	gateName  string
	probeName string
	probeFn   gin.HandlerFunc
}

func newGateRecorder() *gateRecorder {
	r := &gateRecorder{chains: map[string]RouteChain{}}
	r.gateName = funcName(r.gate(gateInfo{}))
	r.probeFn = r.probe
	r.probeName = funcName(r.probeFn)
	return r
}

// funcName is the name gin's HandlerNames reports for h.
func funcName(h gin.HandlerFunc) string {
	return runtime.FuncForPC(reflect.ValueOf(h).Pointer()).Name()
}

// gate is the recording gate for one gate a constructor described. Every
// instance is the same function literal, so they share one name, which
// is how the probe counts them in a chain, by the name gin reports for them.
func (r *gateRecorder) gate(info gateInfo) gin.HandlerFunc {
	return func(c *gin.Context) {
		tr, _ := c.Get(gateTraceCtxKey)
		t, ok := tr.(*chainTrace)
		if !ok {
			// A recorder that no probe set up: the engine was built with the
			// recorders and served without the probe. Refuse loudly.
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		t.gates = append(t.gates, info)
		if len(t.gates) == t.total {
			r.finish(c, t)
			return
		}
		c.Next()
	}
}

func (r *gateRecorder) probe(c *gin.Context) {
	t := &chainTrace{}
	names := c.HandlerNames()
	for i, name := range names {
		switch {
		case name == r.gateName:
			t.total++
		case name == r.probeName:
		case i == len(names)-1:
		default:
			t.other = append(t.other, name)
		}
	}
	if t.total == 0 {
		r.finish(c, t)
		return
	}
	c.Set(gateTraceCtxKey, t)
	c.Next()
}

func (r *gateRecorder) finish(c *gin.Context, t *chainTrace) {
	key := c.Request.Method + " " + c.FullPath()
	r.mu.Lock()
	r.chains[key] = RouteChain{Gates: t.gates, Other: t.other}
	r.mu.Unlock()
	c.AbortWithStatus(http.StatusNoContent)
}

// RecordedGateChains builds the real server with the ABS surface off or on,
// the recorders installed, and probes every gin route of its engine once. The
// result is keyed "METHOD /gin/path". It fails the test for a route whose probe
// did not reach the end of its chain (some element refused first, or the route
// matched under another path).
func RecordedGateChains(t *testing.T, abs bool) map[string]RouteChain {
	t.Helper()
	rec := newGateRecorder()
	prevTrace, prevHook := gateTraceForTest, basicGateHookForTest
	gateTraceForTest, basicGateHookForTest = rec.gate, rec.probeFn
	f := func() *credGuardFixture {
		defer func() { gateTraceForTest, basicGateHookForTest = prevTrace, prevHook }()
		return legacyEngine(t, abs, false)
	}()
	var keys []string
	for _, route := range f.srv.router.Routes() {
		if route.Path == "/" && (route.Method == http.MethodGet || route.Method == http.MethodHead) {
			continue // the "/" owner's page, not a manifest route
		}
		key := route.Method + " " + route.Path
		keys = append(keys, key)
		req := httptest.NewRequest(route.Method, legacyConcretePath(route.Path), nil)
		w := httptest.NewRecorder()
		f.srv.router.ServeHTTP(w, req)
		_, ok := rec.chains[key]
		require.True(t, ok, "%s: the probe never reached the end of its chain (status %d, body %q)", key, w.Code, w.Body.String())
	}
	require.NotEmpty(t, keys)
	return rec.chains
}

// GateKindOf and AdminGateOf build TracedGates for crossCheck's own tests, which
// live in the external test package and cannot name the unexported types.
func GateKindOf(kind string) TracedGate { return TracedGate{Kind: gateKind(kind)} }

func AdminGateOf(kind string) TracedGate {
	return TracedGate{Kind: gateAdmin, Admin: adminRouteKind(kind)}
}
