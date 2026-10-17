// file: internal/server/handlers/opsv3/helpers_test.go
// version: 1.0.1
// guid: 83b0b0e7-6219-4470-952a-df087898cae5
// last-edited: 2026-10-17

package opsv3

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
)

func init() { gin.SetMode(gin.TestMode) }

// Test callers are described by headers a test middleware turns into the
// verified context the real auth stage would have built. The handler never
// reads these; only newRouter's middleware does.
const (
	hPerms  = "X-Test-Perms"  // comma-separated permissions
	hUser   = "X-Test-User"   // user id
	hMethod = "X-Test-Method" // auth.Method; absent = none
)

func testPerm(p auth.Permission) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !auth.Can(c.Request.Context(), p) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "permission denied: " + p})
			return
		}
		c.Next()
	}
}

func newRouter(h *Handler) *gin.Engine {
	r := gin.New()
	g := r.Group("/api/v3", func(c *gin.Context) {
		ctx := c.Request.Context()
		if ps := c.GetHeader(hPerms); ps != "" {
			ctx = auth.WithPermissions(ctx, strings.Split(ps, ","))
		}
		if u := c.GetHeader(hUser); u != "" {
			ctx = auth.WithUser(ctx, &database.User{ID: u})
		}
		if m := c.GetHeader(hMethod); m != "" {
			ctx = auth.WithMethod(ctx, auth.Method(m))
		}
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	h.Register(g, testPerm)
	return r
}

type caller struct {
	perms  string
	user   string
	method string
}

var (
	viewer  = caller{perms: auth.PermLibraryView, user: "u-view", method: string(auth.MethodSession)}
	admin   = caller{perms: auth.PermLibraryView + "," + auth.PermScanTrigger + "," + auth.PermSettingsManage, user: "u-admin", method: string(auth.MethodSession)}
	nobody  = caller{}
	headers = caller{perms: auth.PermLibraryView + "," + auth.PermSettingsManage, user: "u-fwd"} // no verified method
)

func do(t testing.TB, r http.Handler, who caller, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	switch b := body.(type) {
	case nil:
		rdr = bytes.NewReader(nil)
	case string:
		rdr = bytes.NewReader([]byte(b))
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if who.perms != "" {
		req.Header.Set(hPerms, who.perms)
	}
	if who.user != "" {
		req.Header.Set(hUser, who.user)
	}
	if who.method != "" {
		req.Header.Set(hMethod, who.method)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func decode[T any](t testing.TB, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("body is not the expected JSON (%d): %s", w.Code, w.Body.String())
	}
	return v
}

// --- fakes ---

type fakeDefs struct {
	defs  map[string]DefInfo
	ready map[string]string // id -> reason; present = not ready
}

func (f *fakeDefs) ListDefs() []DefInfo {
	out := make([]DefInfo, 0, len(f.defs))
	for _, d := range f.defs {
		out = append(out, d)
	}
	return out
}
func (f *fakeDefs) GetDef(id string) (DefInfo, bool) { d, ok := f.defs[id]; return d, ok }
func (f *fakeDefs) Ready(id string) (bool, string) {
	if r, bad := f.ready[id]; bad {
		return false, r
	}
	return true, ""
}

type enqueued struct {
	def    string
	params json.RawMessage
	opts   opsregistry.EnqueueOptions
}

type fakeEnq struct {
	mu        sync.Mutex
	calls     []enqueued
	cancelled []string
	cancelErr error
}

func (f *fakeEnq) EnqueueOp(_ context.Context, defID string, params any, opts ...opsregistry.EnqueueOption) (string, error) {
	raw, _ := json.Marshal(params)
	var o opsregistry.EnqueueOptions
	for _, opt := range opts {
		opt(&o)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, enqueued{def: defID, params: raw, opts: o})
	return "run-" + ulid.Make().String(), nil
}

func (f *fakeEnq) Cancel(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = append(f.cancelled, id)
	return f.cancelErr
}

func (f *fakeEnq) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

type fakeZombies []opsregistry.ZombieInfo

func (z fakeZombies) Zombies() []opsregistry.ZombieInfo { return z }

type fakeApprover struct {
	mu    sync.Mutex
	calls []database.ApprovalRecord
	err   error
}

func (f *fakeApprover) Approve(_ context.Context, _ string, rec database.ApprovalRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, rec)
	return f.err
}

// memRuns is a RunReader over fixed records, for the cases a real store makes
// awkward (a truncated timeline, a crafted progress snapshot).
type memRuns struct {
	mu          sync.Mutex
	runs        map[string]database.RunRecord
	done        []database.RunRecord
	doneCmplete bool
	sched       map[string]database.SchedState
}

func (m *memRuns) set(id string, r database.RunRecord) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs[id] = r
}

func (m *memRuns) GetRunV3(id string) (*database.RunRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.runs[id]; ok {
		return &r, nil
	}
	return nil, nil
}
func (m *memRuns) ListRunsV3(database.RunQuery) (database.RunPage, error) {
	return database.RunPage{}, nil
}
func (m *memRuns) OpCountsV3() (database.OpCounts, error) { return database.OpCounts{}, nil }
func (m *memRuns) ListDoneRunsSinceV3(time.Time, int) ([]database.RunRecord, bool, error) {
	return m.done, m.doneCmplete, nil
}
func (m *memRuns) ListItemOutcomesV3(string, database.ItemQuery) (database.ItemPage, error) {
	return database.ItemPage{}, nil
}
func (m *memRuns) GetSchedV3(id string) (*database.SchedState, error) {
	if s, ok := m.sched[id]; ok {
		return &s, nil
	}
	return nil, nil
}

// realStore is a PebbleStore with a few helpers to seed runs.
func realStore(t testing.TB) *database.PebbleStore {
	t.Helper()
	p, err := database.NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func insertRun(t testing.TB, p *database.PebbleStore, def string) string {
	t.Helper()
	id := ulid.Make().String()
	if err := p.InsertRunV3(database.RunRecord{ID: id, DefID: def, Priority: 5}); err != nil {
		t.Fatalf("InsertRunV3: %v", err)
	}
	return id
}

func step(t testing.TB, p *database.PebbleStore, id string, ev state.Event, mut func(*database.TransitionRequest)) *database.RunRecord {
	t.Helper()
	cur, err := p.GetRunV3(id)
	if err != nil || cur == nil {
		t.Fatalf("GetRunV3(%s): %v %v", id, cur, err)
	}
	req := database.TransitionRequest{ID: id, ExpectState: cur.State, ExpectAttempt: cur.Attempt, ExpectFenceEpoch: cur.FenceEpoch, Event: ev, Now: time.Now().UTC()}
	if mut != nil {
		mut(&req)
	}
	out, err := p.TransitionRunV3(req)
	if err != nil {
		t.Fatalf("TransitionRunV3(%s, %s): %v", id, ev, err)
	}
	return out
}

func finish(t testing.TB, p *database.PebbleStore, id string, ev state.Event) {
	t.Helper()
	step(t, p, id, state.EvDispatch, nil)
	step(t, p, id, ev, func(r *database.TransitionRequest) {
		if ev == state.EvReturnErr {
			r.Error = "boom"
		}
	})
}
