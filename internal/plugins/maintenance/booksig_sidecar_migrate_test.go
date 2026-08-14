// file: internal/plugins/maintenance/booksig_sidecar_migrate_test.go
// version: 1.0.0
// guid: 7f3d1b58-6c24-4a09-8e57-3b90d2f6c418
// last-edited: 2026-08-13

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// These tests cover the OP's orchestration — dry-run defaulting, the canary
// limit, error tolerance, and refusing to run against a store that cannot
// migrate. The migration SEMANTICS (row/sidecar pairing, CAS-and-skip, the
// surgical strip) are proven in internal/database, where raw Pebble rows are
// reachable and six mutation controls hold the tests honest. Splitting it this
// way keeps the op tests from re-asserting storage behaviour through a keyhole.

// scriptedMigrator wraps a real store (for ListBookIDs) but replaces the
// per-book migration with a scripted outcome, recording every call. Embedding
// database.Store — not *PebbleStore — means the real primitive is never
// invoked, so an op bug cannot be masked by the primitive doing the right
// thing anyway.
type scriptedMigrator struct {
	database.Store
	mu      sync.Mutex
	ids     []string
	dryRuns []bool
	outcome func(id string) (database.BookSigMigrateOutcome, error)
}

func (s *scriptedMigrator) MigrateBookSigToSidecar(id string, dryRun bool) (database.BookSigMigrateOutcome, error) {
	s.mu.Lock()
	s.ids = append(s.ids, id)
	s.dryRuns = append(s.dryRuns, dryRun)
	s.mu.Unlock()
	if s.outcome == nil {
		return database.BookSigMigrateMigrated, nil
	}
	return s.outcome(id)
}

func (s *scriptedMigrator) snapshot() (ids []string, dryRuns []bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.ids...), append([]bool(nil), s.dryRuns...)
}

// migrateOpFixture seeds n books and returns the op plugin plus the scripted
// migrator wrapping the real store.
func migrateOpFixture(t *testing.T, n int) (*Plugin, *scriptedMigrator, []string) {
	t.Helper()
	if testing.Short() {
		t.Skip("seeds a real PebbleStore; skipped in -short")
	}
	s, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	want := make([]string, 0, n)
	for i := 0; i < n; i++ {
		b, err := s.CreateBook(&database.Book{
			Title:    fmt.Sprintf("Migrate Op %02d", i),
			FilePath: fmt.Sprintf("/lib/migrate_op_%02d.m4b", i),
		})
		if err != nil {
			t.Fatalf("CreateBook %d: %v", i, err)
		}
		want = append(want, b.ID)
	}

	sm := &scriptedMigrator{Store: s}
	return &Plugin{deps: fakeDeps{store: sm}}, sm, want
}

// TestBookSigSidecarMigrate_DefaultsToDryRun is the gate.
//
// This op performs the only irreversible step in the #2387 design. If an
// operator triggers it with no params — the single most likely way it will ever
// be invoked by hand — it must classify and report, not rewrite 67,824 rows.
// A plain `var params T` would default DryRun to FALSE, which is exactly
// backwards, so the safe default is set explicitly before unmarshalling.
func TestBookSigSidecarMigrate_DefaultsToDryRun(t *testing.T) {
	p, sm, want := migrateOpFixture(t, 3)

	for _, raw := range []json.RawMessage{nil, json.RawMessage(`{}`), json.RawMessage(`{"limit":0}`)} {
		sm.mu.Lock()
		sm.ids, sm.dryRuns = nil, nil
		sm.mu.Unlock()

		if err := p.runBookSigSidecarMigrate(context.Background(), raw, &concurrentReporter{}); err != nil {
			t.Fatalf("run with raw=%q: %v", string(raw), err)
		}
		ids, dryRuns := sm.snapshot()
		if len(ids) != len(want) {
			t.Fatalf("raw=%q: examined %d books, want %d", string(raw), len(ids), len(want))
		}
		for i, dr := range dryRuns {
			if !dr {
				t.Fatalf("raw=%q: book %s was migrated for real; params with no explicit dryRun MUST default to a dry run", string(raw), ids[i])
			}
		}
	}
}

// TestBookSigSidecarMigrate_ExplicitApplyDisablesDryRun — the gate must also
// actually open, or the op could never do its job while looking correct.
func TestBookSigSidecarMigrate_ExplicitApplyDisablesDryRun(t *testing.T) {
	p, sm, want := migrateOpFixture(t, 3)

	if err := p.runBookSigSidecarMigrate(context.Background(), json.RawMessage(`{"dryRun":false}`), &concurrentReporter{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	ids, dryRuns := sm.snapshot()
	if len(ids) != len(want) {
		t.Fatalf("examined %d books, want %d", len(ids), len(want))
	}
	for i, dr := range dryRuns {
		if dr {
			t.Fatalf("book %s ran as a dry run despite dryRun:false — the apply path is unreachable", ids[i])
		}
	}
}

// TestBookSigSidecarMigrate_LimitIsAPrefixCanary.
//
// limit exists so the first apply can be small: migrate a handful, verify the
// pairing held on prod, then run the rest. That only works if the limited run
// is a stable PREFIX of ListBookIDs order — otherwise a second run would
// re-examine an arbitrary overlapping subset instead of continuing.
func TestBookSigSidecarMigrate_LimitIsAPrefixCanary(t *testing.T) {
	p, sm, _ := migrateOpFixture(t, 6)

	all, err := sm.Store.ListBookIDs()
	if err != nil {
		t.Fatalf("ListBookIDs: %v", err)
	}
	if len(all) < 6 {
		t.Fatalf("fixture seeded %d books, want >= 6", len(all))
	}

	if err := p.runBookSigSidecarMigrate(context.Background(), json.RawMessage(`{"limit":2}`), &concurrentReporter{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	ids, _ := sm.snapshot()
	if len(ids) != 2 {
		t.Fatalf("limit=2 examined %d books; a canary that examines the whole library is not a canary", len(ids))
	}

	got := map[string]bool{ids[0]: true, ids[1]: true}
	for _, id := range all[:2] {
		if !got[id] {
			t.Fatalf("limit=2 examined %v, want the first two of ListBookIDs order %v — a limited run must be a resumable prefix", ids, all[:2])
		}
	}
}

// TestBookSigSidecarMigrate_PerBookErrorDoesNotAbortTheRun.
//
// One malformed or unreadable row out of 67,824 must not strand the other
// 67,823 un-migrated. The row is counted, logged and left exactly as it was.
func TestBookSigSidecarMigrate_PerBookErrorDoesNotAbortTheRun(t *testing.T) {
	p, sm, want := migrateOpFixture(t, 5)

	all, err := sm.Store.ListBookIDs()
	if err != nil {
		t.Fatalf("ListBookIDs: %v", err)
	}
	poison := all[2]
	sm.outcome = func(id string) (database.BookSigMigrateOutcome, error) {
		if id == poison {
			return database.BookSigMigrateNotCandidate, fmt.Errorf("simulated unreadable row")
		}
		return database.BookSigMigrateMigrated, nil
	}

	if err := p.runBookSigSidecarMigrate(context.Background(), json.RawMessage(`{"dryRun":false}`), &concurrentReporter{}); err != nil {
		t.Fatalf("a single bad row must not fail the whole op, got: %v", err)
	}
	ids, _ := sm.snapshot()
	if len(ids) != len(want) {
		t.Fatalf("examined %d books after one failure, want all %d — the run aborted early", len(ids), len(want))
	}
}

// unmigratableStore satisfies database.Store but not BookSigMigrateStore, and
// exposes no Unwrap, so AsCapability cannot descend to anything that does.
type unmigratableStore struct{ database.Store }

// TestBookSigSidecarMigrate_UnsupportedStoreFailsLoudly.
//
// Production always installs the Bleve indexedStore decorator. A bare
// `store.(*PebbleStore)` against a wrapped store is indistinguishable from a
// genuinely unsupported backend, and several ops in this repo silently no-opped
// in prod exactly that way — reporting success having migrated nothing. This op
// must error instead.
func TestBookSigSidecarMigrate_UnsupportedStoreFailsLoudly(t *testing.T) {
	p := &Plugin{deps: fakeDeps{store: unmigratableStore{}}}

	err := p.runBookSigSidecarMigrate(context.Background(), json.RawMessage(`{"dryRun":true}`), &concurrentReporter{})
	if err == nil {
		t.Fatal("a store that cannot migrate must produce an ERROR; reporting success with 0 books migrated is the exact silent no-op this check exists to prevent")
	}
}

// TestBookSigSidecarMigrateDef_Shape pins the properties that make the op safe
// to expose: it must be cancellable (it touches the whole library), and it must
// NOT be isolated — Isolate means out-of-process and Pebble is single-writer,
// so a child cannot reopen the database.
func TestBookSigSidecarMigrateDef_Shape(t *testing.T) {
	def := (&Plugin{}).bookSigSidecarMigrateDef()

	if def.ID != "maintenance.booksig-sidecar-migrate" {
		t.Fatalf("op ID = %q", def.ID)
	}
	if !def.Cancellable {
		t.Fatal("a whole-library write op must be cancellable")
	}
	if def.Isolate {
		t.Fatal("Isolate runs the op out-of-process and Pebble is single-writer; the child cannot reopen the database")
	}
	if def.Run == nil {
		t.Fatal("op has no Run function — it would register and then do nothing")
	}
}
