// file: core/integrations/providerhttp/holds_native_test.go
// version: 1.0.0
// guid: 395d877e-920a-459f-a3ce-7f431beb191f
// last-edited: 2026-11-11

package providerhttp_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/coretest/logcapture"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// previewRun is a FakeRun whose mode is Preview.
type previewRun struct{ *taskstest.FakeRun }

func (previewRun) Mode() tasks.Mode { return tasks.ModePreview }

// TestReleaseHoldsTask_PreviewListsLiveReleases: the Registry is the native
// backend of the kind. A Preview run lists every hold and writes nothing; a
// Live run deletes them. Quota counters and breaker state are left alone.
func TestReleaseHoldsTask_PreviewListsLiveReleases(t *testing.T) {
	clk := newClock()
	gs := providerhttp.NativeGateStore(store(t))
	r := newRegistry(t, clk, func(o *providerhttp.Options) { o.Store = gs })
	lim := limits(1000, 1000)
	lim.Daily = daily(10, 10)
	for i, c := range []integrations.Class{integrations.ClassQuota, integrations.ClassAuth, integrations.ClassRateLimited} {
		g := r.GateFor(ref([]string{"alfred", "barbara", "selina"}[i]), providerhttp.Policy{Limits: lim})
		tk, err := g.Acquire(context.Background(), "")
		if err != nil {
			t.Fatal(err)
		}
		tk.Done(&integrations.Error{Class: c, Status: 429})
	}
	ctx := context.Background()
	def := providerhttp.ReleaseHoldsTask(r) // the Registry satisfies InterimHolds: this is the native registration
	if len(tasks.Problems(def)) != 0 {
		t.Fatalf("definition problems: %v", tasks.Problems(def))
	}

	prev, err := r.ReleaseHolds(tasks.NewRC(previewRun{taskstest.NewFakeRun(ctx, providerhttp.ReleaseHoldsKind)}), providerhttp.ReleaseHoldsParams{})
	if err != nil || prev.Listed != 3 || prev.Released != 0 {
		t.Fatalf("preview = %+v, %v; want 3 listed, 0 released", prev, err)
	}
	if hs, _ := gs.Holds(ctx); len(hs) != 3 {
		t.Fatalf("a Preview run changed the store: %d holds", len(hs))
	}

	live, err := r.ReleaseHolds(taskstest.NewFakeRun(ctx, providerhttp.ReleaseHoldsKind).RC(), providerhttp.ReleaseHoldsParams{})
	if err != nil || live.Listed != 3 || live.Released != 3 {
		t.Fatalf("live = %+v, %v; want 3 listed, 3 released", live, err)
	}
	if hs, _ := gs.Holds(ctx); len(hs) != 0 {
		t.Fatalf("a Live run left %d holds in the store", len(hs))
	}
	for h := range r.Holds() {
		t.Fatalf("hold %s survived in memory", h.Class)
	}
	if st := r.GateFor(ref("alfred"), providerhttp.Policy{Limits: lim}).State(); st.QuotaUsed["requests"] != 1 {
		t.Fatalf("a release changed the quota counter: %v", st.QuotaUsed)
	}
}

// TestReleaseHolds_StoreFailureKeepsHold: a hold the store cannot delete stays
// in force, the run reports the error, and the others are still released.
func TestReleaseHolds_StoreFailureKeepsHold(t *testing.T) {
	clk := newClock()
	fs := &putFailingStore{GateStore: providerhttp.NativeGateStore(store(t))}
	r := newRegistry(t, clk, func(o *providerhttp.Options) { o.Store = fs })
	for _, n := range []string{"alfred", "barbara"} {
		g := r.GateFor(ref(n), providerhttp.Policy{Limits: limits(100, 100)})
		tk, _ := g.Acquire(context.Background(), "")
		tk.Done(&integrations.Error{Class: integrations.ClassAuth, Status: 401})
	}
	fs.failScope = ref("alfred").String()
	res, err := r.ReleaseHolds(taskstest.NewFakeRun(context.Background(), providerhttp.ReleaseHoldsKind).RC(), providerhttp.ReleaseHoldsParams{})
	if err == nil || res.Listed != 2 || res.Released != 1 {
		t.Fatalf("result = %+v, %v; want 2 listed, 1 released, an error", res, err)
	}
	if _, held := r.HoldFor(ref("alfred")); !held {
		t.Fatal("a hold whose record could not be deleted was reported released")
	}
}

type putFailingStore struct {
	providerhttp.GateStore
	failScope string
}

func (p *putFailingStore) PutHold(ctx context.Context, h integrations.Hold) error {
	if h.Until.IsZero() && h.Scope == p.failScope {
		return errStore
	}
	return p.GateStore.PutHold(ctx, h)
}

// loggedRun is a FakeRun whose log is captured.
type loggedRun struct {
	*taskstest.FakeRun
	log *slog.Logger
}

func (l loggedRun) Log() *slog.Logger { return l.log }

// TestReleaseHolds_LogCarriesNoScopeOrDetailText: a caller-chosen Policy.Scope
// (here a URL with userinfo) and a hold Detail that echoes a credential never
// reach the run's log. The owning instance is named when it is known, an
// opaque digest otherwise, and the Detail passes the same scrub as an error.
func TestReleaseHolds_LogCarriesNoScopeOrDetailText(t *testing.T) {
	const secret = "joker-card-8675309"
	custom := "https://alfred:" + secret + "@gotham.example/search?apikey=" + secret
	orphan := "https://selina:" + secret + "@catwalk.example/x"
	clk := newClock()
	gs := providerhttp.NativeGateStore(store(t))
	ctx := context.Background()
	// A persisted hold whose scope no client has named, with a Detail that
	// echoes a credential and is far longer than a log line should carry.
	if err := gs.PutHold(ctx, integrations.Hold{Scope: orphan, Class: integrations.ClassQuota,
		Detail: "upstream said apikey=" + secret + " " + strings.Repeat("z", 400), Until: clk.Now().Add(time.Hour), SetAt: clk.Now()}); err != nil {
		t.Fatal(err)
	}
	r := newRegistry(t, clk, func(o *providerhttp.Options) { o.Store = gs })
	// A hold on an instance whose Policy.Scope is the custom URL.
	g := r.GateFor(ref("alfred"), providerhttp.Policy{Limits: limits(100, 100), Scope: custom})
	tk, _ := g.Acquire(ctx, "")
	tk.Done(&integrations.Error{Class: integrations.ClassAuth, Status: 401})

	lc := logcapture.New(t)
	run := loggedRun{FakeRun: taskstest.NewFakeRun(ctx, providerhttp.ReleaseHoldsKind), log: lc.Logger()}
	res, err := r.ReleaseHolds(tasks.NewRC(run), providerhttp.ReleaseHoldsParams{})
	if err != nil || res.Listed != 2 || res.Released != 2 {
		t.Fatalf("result = %+v, %v", res, err)
	}
	var seenInstance, seenDigest bool
	for _, rec := range lc.Records() {
		line := rec.Message
		rec.Attrs(func(a slog.Attr) bool {
			line += " " + a.Key + "=" + a.Value.String()
			seenInstance = seenInstance || (a.Key == "instance" && a.Value.String() == ref("alfred").String())
			seenDigest = seenDigest || (a.Key == "scope_digest" && len(a.Value.String()) == 12)
			return true
		})
		for _, leak := range []string{secret, "gotham.example", "catwalk.example", "alfred:", "selina", "apikey=" + secret, strings.Repeat("z", 200)} {
			if strings.Contains(line, leak) {
				t.Errorf("the log carries %q: %s", leak, line)
			}
		}
	}
	if !seenInstance || !seenDigest {
		t.Errorf("want the owning instance for the built scope and a digest for the unknown one: instance=%v digest=%v", seenInstance, seenDigest)
	}
	// A scope that is the default form of a Ref is logged as that Ref.
	g2 := r.GateFor(ref("barbara"), providerhttp.Policy{Limits: limits(100, 100)})
	tk, _ = g2.Acquire(ctx, "")
	tk.Done(&integrations.Error{Class: integrations.ClassAuth, Status: 401})
	lc2 := logcapture.New(t)
	run2 := loggedRun{FakeRun: taskstest.NewFakeRun(ctx, providerhttp.ReleaseHoldsKind), log: lc2.Logger()}
	if _, err := r.ReleaseHolds(tasks.NewRC(run2), providerhttp.ReleaseHoldsParams{}); err != nil {
		t.Fatal(err)
	}
	var got bool
	for _, rec := range lc2.Records() {
		rec.Attrs(func(a slog.Attr) bool {
			got = got || (a.Key == "instance" && a.Value.String() == ref("barbara").String())
			return true
		})
	}
	if !got {
		t.Error("a default-scope hold is not logged as its Ref")
	}
}
