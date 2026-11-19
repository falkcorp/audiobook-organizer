// file: core/internal/loghttp/env_test.go
// version: 1.0.0
// guid: 86a49c71-ec1f-4b7c-8b6a-18524058a32b
// last-edited: 2026-11-18

package loghttp

import (
	"context"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/internal/logjournal"
	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// The tests open the native journal through storage.Open over MemFS (never
// coretest, which imports the kernel) and call the handler the way the router
// does once stage 12 has bound the principal and its checker. Until F4-08 a
// Router refuses every non-Public route, so no request can be sent through one.

var epoch = time.Date(2026, 11, 18, 3, 0, 0, 0, time.UTC)

func at(d time.Duration) time.Time { return epoch.Add(d) }

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

func openJournal(t testing.TB) *logjournal.Store {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path:   "/batcave/journal.pebble",
		Owners: []storage.OwnerSpec{{Owner: logjournal.Owner, Spec: logjournal.StorageSpec(), Enabled: true}},
		Build:  "loghttp+fixture", FreeSpaceMargin: 1, FS: storage.FSOf(vfs.NewMem()),
	})
	if err != nil {
		t.Fatal(err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		_ = o.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ks, ok := db.Keyspace(logjournal.Owner)
	if !ok {
		t.Fatal("no journal keyspace")
	}
	return logjournal.New(ks)
}

// grants is a stand-in checker holding exactly the permissions named.
type grants struct{ held []rbac.Permission }

func (g grants) Has(p rbac.Permission) bool { return slices.Contains(g.held, p) }

func (g grants) Satisfies(r rbac.Requirement) bool {
	if r.Kind() != rbac.KindAllOf || len(r.Permissions()) == 0 {
		return false
	}
	return !slices.ContainsFunc(r.Permissions(), func(p rbac.Permission) bool { return !g.Has(p) })
}

func (g grants) Confined() bool              { return false }
func (g grants) Concrete() []rbac.Permission { return slices.Clone(g.held) }

func principal(id string, perms ...rbac.Permission) *auth.Principal {
	return &auth.Principal{
		User: &auth.User{ID: auth.UserID(id), Username: strings.TrimPrefix(id, "u-")}, Method: auth.MethodSession,
		Perms: grants{held: perms}, ClientAddr: netip.MustParseAddr("192.0.2.10"), CredentialID: "sess-batcave",
	}
}

// bound is what stage 12 does: the principal and its checker in the context.
func bound(p *auth.Principal) context.Context {
	return rbac.WithChecker(auth.WithPrincipal(context.Background(), p), p.Perms)
}

// call runs the handler as p with req.
func call(t testing.TB, a *api, p *auth.Principal, req queryRequest) (pageView, error) {
	t.Helper()
	return a.list(bound(p), &httpapi.In[queryRequest]{Req: req, Principal: p})
}

func mustCall(t testing.TB, a *api, p *auth.Principal, req queryRequest) pageView {
	t.Helper()
	v, err := call(t, a, p, req)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	return v
}

func messages(v pageView) []string {
	out := make([]string, len(v.Records))
	for i, r := range v.Records {
		out[i] = r.Message
	}
	return out
}

// fakeJournal records the queries it is given and answers with page.
type fakeJournal struct {
	mu    sync.Mutex
	calls []log.JournalQuery
	page  log.JournalPage
	err   error
}

func (f *fakeJournal) Append(context.Context, []log.Record) error { return nil }

func (f *fakeJournal) Query(_ context.Context, q log.JournalQuery) (log.JournalPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, q)
	return f.page, f.err
}

func (f *fakeJournal) Prune(context.Context, time.Time, int) (int, bool, error) { return 0, false, nil }

func (f *fakeJournal) queries() []log.JournalQuery {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func newAPI(j log.JournalStore, pred log.OtherUserPredicate) *api {
	return &api{journal: j, otherUser: pred}
}
