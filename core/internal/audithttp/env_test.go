// file: core/internal/audithttp/env_test.go
// version: 1.0.1
// guid: 6d7005e2-ed4b-444a-a9c3-60d66b5fec9d
// last-edited: 2026-11-10

package audithttp

import (
	"context"
	"errors"
	"iter"
	"log/slog"
	"net/netip"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// The tests open their store with storage.Open over MemFS (never
// coretest.NewStore, which the kernel's import graph must not reach) and
// write through the real audit.Store, so redaction, sequencing and the
// store's filters are the real ones.

const auditOwner storage.Owner = "core/audit"

// clock is a settable time source.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// env is a booted audit store and its clock.
type env struct {
	store *audit.Store
	clock *clock
}

var epoch = time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC)

func newEnv(t testing.TB) *env {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path:            filepath.Join(t.TempDir(), "main.pebble"),
		Owners:          []storage.OwnerSpec{{Owner: auditOwner, Spec: storage.Spec{Keyspace: true}, Enabled: true}},
		Build:           "coretest+fixture",
		FreeSpaceMargin: 1,
		FS:              storage.FSOf(vfs.NewMem()),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		if cerr := o.Close(); cerr != nil {
			t.Logf("close after a failed boot: %v", cerr)
		}
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	ks, ok := db.Keyspace(auditOwner)
	if !ok {
		t.Fatal("no audit keyspace")
	}
	clk := &clock{t: epoch}
	st, err := audit.New(ctx, audit.Options{}, audit.Deps{Keyspace: ks, Seq: ks.Sequence("seq"), Now: clk.now})
	if err != nil {
		t.Fatalf("audit.New: %v", err)
	}
	return &env{store: st, clock: clk}
}

// write records e, a minute after the last, and returns its seq.
func (e *env) write(t testing.TB, ev audit.Event) uint64 {
	t.Helper()
	e.clock.advance(time.Minute)
	if err := e.store.Record(context.Background(), ev); err != nil {
		t.Fatalf("Record(%s): %v", ev.Action, err)
	}
	var seq uint64
	for rec, err := range e.store.Query(context.Background(), audit.Filter{Limit: 1}) {
		if err != nil {
			t.Fatal(err)
		}
		seq = rec.Seq
	}
	return seq
}

// The cast (users bruce, dick, barbara, alfred, selina).
func userRef(id string) entity.Ref { return entity.Ref{Module: "core", Kind: "user", ID: id} }

func actor(id, name string) audit.Actor {
	return audit.Actor{UserID: id, Username: name, Method: "session", ClientAddr: "192.0.2.10"}
}

// roster writes the records the visibility tests read, oldest first, and
// returns the seq of each by label.
func (e *env) roster(t testing.TB) map[string]uint64 {
	t.Helper()
	seqs := map[string]uint64{}
	for _, r := range []struct {
		label string
		ev    audit.Event
	}{
		{"dick-login", audit.Event{Action: "core.auth.login", Outcome: audit.Success, Actor: actor("u-dick", "dick"), Target: userRef("u-dick")}},
		{"patrol", audit.Event{Action: "core.role.update", Outcome: audit.Success, Actor: actor("u-barbara", "barbara"), Target: entity.Ref{Module: "core", Kind: "role", ID: "gotham"}}},
		{"bruce-disables-selina", audit.Event{Action: "core.user.disable", Outcome: audit.Success, Actor: actor("u-bruce", "bruce"), Target: userRef("u-selina")}},
		{"anon-fail", audit.Event{Action: "core.auth.login_failed", Outcome: audit.Failure, Actor: audit.Actor{ClientAddr: "192.0.2.99"}}},
		{"barbara-update", audit.Event{Action: "core.user.update", Outcome: audit.Success, Actor: actor("u-barbara", "barbara"), Target: userRef("u-barbara")}},
		{"alfred-denied", audit.Event{Action: "core.permission.deny", Outcome: audit.Denied, Actor: actor("u-alfred", "alfred"), Permission: "core:roles.manage", Reason: "scope_escalation"}},
		{"dick-logout-noTarget", audit.Event{Action: "core.auth.logout", Outcome: audit.Success, Actor: actor("u-dick", "dick")}},
	} {
		seqs[r.label] = e.write(t, r.ev)
	}
	return seqs
}

// grants is a stand-in checker holding exactly the permissions named.
type grants struct{ held []rbac.Permission }

func (g grants) Has(p rbac.Permission) bool { return slices.Contains(g.held, p) }

func (g grants) Satisfies(r rbac.Requirement) bool {
	switch r.Kind() {
	case rbac.KindAllOf:
		for _, p := range r.Permissions() {
			if !g.Has(p) {
				return false
			}
		}
		return len(r.Permissions()) > 0
	case rbac.KindAnyOf:
		return slices.ContainsFunc(r.Permissions(), g.Has)
	}
	return false
}

func (g grants) Confined() bool              { return false }
func (g grants) Concrete() []rbac.Permission { return slices.Clone(g.held) }

// principal is id on a session holding perms.
func principal(id, name string, perms ...rbac.Permission) *auth.Principal {
	return &auth.Principal{
		User: &auth.User{ID: auth.UserID(id), Username: name}, Method: auth.MethodSession,
		Perms: grants{held: perms}, ClientAddr: netip.MustParseAddr("192.0.2.10"), CredentialID: "sess-batcave",
	}
}

// auditor holds audit.view and nothing else; commissioner also holds
// users.manage.
func auditor(id, name string) *auth.Principal { return principal(id, name, permView) }
func commissioner() *auth.Principal {
	return principal("u-bruce", "bruce", permView, permUsers)
}

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

// fakeReader is an audit.Reader over a fixed list, newest first, that applies
// BeforeSeq and Limit as the store does, counts its calls, and can fail.
type fakeReader struct {
	mu    sync.Mutex
	recs  []audit.Record // newest first
	err   error          // yielded after failAfter records of the first call
	after int
	calls []audit.Filter
}

func (f *fakeReader) Query(_ context.Context, flt audit.Filter) iter.Seq2[audit.Record, error] {
	f.mu.Lock()
	f.calls = append(f.calls, flt)
	recs, err, after := f.recs, f.err, f.after
	f.mu.Unlock()
	return func(yield func(audit.Record, error) bool) {
		n := 0
		for _, r := range recs {
			if flt.BeforeSeq != 0 && r.Seq >= flt.BeforeSeq {
				continue
			}
			if err != nil && n == after {
				yield(audit.Record{}, err)
				return
			}
			if !yield(r, nil) {
				return
			}
			if n++; flt.Limit > 0 && n >= flt.Limit {
				return
			}
		}
		if err != nil && n == after {
			yield(audit.Record{}, err)
		}
	}
}

func (f *fakeReader) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// recorder is the export's Recorder: it counts calls, can fail, and runs a
// hook inside Record (to look at what has been sent by then).
type recorder struct {
	mu     sync.Mutex
	events []audit.Event
	fail   error
	during func()
}

func (r *recorder) Record(_ context.Context, e audit.Event) error {
	if r.during != nil {
		r.during()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return r.fail
	}
	r.events = append(r.events, e)
	return nil
}

func (r *recorder) recorded() []audit.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

var errStore = errors.New("pebble: corrupt block at /var/lib/gotham/audit")
