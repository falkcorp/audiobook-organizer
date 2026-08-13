// file: internal/reconcile/elect_primaries_test.go
// version: 1.0.0
// guid: aa557927-956b-41a5-a90b-6ef0093fdcbc
// last-edited: 2026-08-13

package reconcile

import (
	"fmt"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// electFakeStore is a concurrency-safe Store double for the
// ElectMissingPrimaries tests. It is deliberately NOT the shared
// fakeReconcileStore: that one embeds a nil database.Store and does not
// implement GetBooksByVersionGroup, and widening a shared helper for one test
// is how parallel test suites collide. Helper names here are task-unique.
type electFakeStore struct {
	database.Store
	books   []database.BookCore
	byID    map[string]*database.Book
	byGroup map[string][]database.Book
	updated map[string]*database.Book
	mu      chan struct{} // 1-slot semaphore used as a mutex

	// onGroupRead, when non-nil, runs inside GetBooksByVersionGroup before the
	// members are returned. It is the seam used to simulate another writer
	// electing a primary between the initial scan and this worker's re-read.
	onGroupRead func(gid string, members []database.Book) []database.Book
}

func newElectFakeStore() *electFakeStore {
	return &electFakeStore{
		byID:    map[string]*database.Book{},
		byGroup: map[string][]database.Book{},
		updated: map[string]*database.Book{},
		mu:      make(chan struct{}, 1),
	}
}

func (f *electFakeStore) lock()   { f.mu <- struct{}{} }
func (f *electFakeStore) unlock() { <-f.mu }

// addElectBook registers one book in all three projections the pass reads.
func (f *electFakeStore) addElectBook(id, title, gid string, primary bool, created time.Time) {
	g := gid
	p := primary
	c := created
	core := database.BookCore{ID: id, Title: title, CreatedAt: &c}
	full := &database.Book{ID: id, Title: title, CreatedAt: &c}
	if gid != "" {
		core.VersionGroupID = &g
		full.VersionGroupID = &g
	}
	core.IsPrimaryVersion = &p
	full.IsPrimaryVersion = &p

	f.books = append(f.books, core)
	f.byID[id] = full
	if gid != "" {
		f.byGroup[gid] = append(f.byGroup[gid], *full)
	}
}

func (f *electFakeStore) GetAllBooksCore(limit, offset int) ([]database.BookCore, error) {
	if offset >= len(f.books) {
		return nil, nil
	}
	return f.books, nil
}

func (f *electFakeStore) GetBooksByVersionGroup(gid string) ([]database.Book, error) {
	f.lock()
	members := append([]database.Book(nil), f.byGroup[gid]...)
	hook := f.onGroupRead
	f.unlock()
	if hook != nil {
		members = hook(gid, members)
	}
	return members, nil
}

func (f *electFakeStore) GetBookByID(id string) (*database.Book, error) {
	f.lock()
	defer f.unlock()
	return f.byID[id], nil
}

func (f *electFakeStore) UpdateBook(id string, book *database.Book) (*database.Book, error) {
	f.lock()
	defer f.unlock()
	f.updated[id] = book
	return book, nil
}

// countGroupsWithoutPrimary is the data invariant under test: no version group
// may elect zero primaries. It reads the store's live group projection rather
// than any value the pass computed, so it cannot be satisfied by the pass
// merely reporting success.
func countGroupsWithoutPrimary(f *electFakeStore) []string {
	var bad []string
	for gid, members := range f.byGroup {
		primaries := 0
		for _, m := range members {
			// Prefer the post-run value when the book was rewritten.
			if w, ok := f.updated[m.ID]; ok {
				if w.IsPrimaryVersion != nil && *w.IsPrimaryVersion {
					primaries++
				}
				continue
			}
			if m.IsPrimaryVersion != nil && *m.IsPrimaryVersion {
				primaries++
			}
		}
		if primaries == 0 {
			bad = append(bad, gid)
		}
	}
	return bad
}

// TestVersionGroupInvariant_ZeroPrimaryGroupsAreRepaired is the headline test.
// It asserts the invariant is VIOLATED before the repair runs — proving the
// check is capable of failing rather than being vacuously green — then runs
// ElectMissingPrimaries and asserts the violation is gone. It covers both
// broken shapes seen in production: singleton groups (the iTunes importer bug)
// and multi-member groups that somehow lost their primary.
func TestVersionGroupInvariant_ZeroPrimaryGroupsAreRepaired(t *testing.T) {
	base := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	store := newElectFakeStore()

	// Broken singleton — exactly the shape importer.go used to write.
	store.addElectBook("solo-1", "Book II", "vg-solo", false, base)

	// Broken multi-member group: two members, neither primary. The earliest
	// created (multi-a) must win.
	store.addElectBook("multi-b", "Multi B", "vg-multi", false, base.Add(2*time.Hour))
	store.addElectBook("multi-a", "Multi A", "vg-multi", false, base.Add(1*time.Hour))

	// Healthy group — must be left completely alone.
	store.addElectBook("ok-primary", "OK Primary", "vg-ok", true, base)
	store.addElectBook("ok-secondary", "OK Secondary", "vg-ok", false, base.Add(time.Hour))

	// Book with no group at all: AssignOrphanVGs' job, not this pass's.
	store.addElectBook("no-group", "No Group", "", false, base)

	// RED: the invariant must be violated before we repair anything.
	if bad := countGroupsWithoutPrimary(store); len(bad) != 2 {
		t.Fatalf("precondition: want 2 groups with zero primaries before repair, got %d (%v). "+
			"If this is 0 the invariant check is vacuous and proves nothing.", len(bad), bad)
	}

	res, err := ElectMissingPrimaries(store, false)
	if err != nil {
		t.Fatalf("ElectMissingPrimaries: %v", err)
	}

	// GREEN: no group may be left electing zero primaries.
	if bad := countGroupsWithoutPrimary(store); len(bad) != 0 {
		t.Errorf("invariant violated after repair: groups with zero primaries = %v", bad)
	}

	if res.GroupsWithoutPrimary != 2 {
		t.Errorf("GroupsWithoutPrimary = %d, want 2", res.GroupsWithoutPrimary)
	}
	if res.SingletonGroups != 1 {
		t.Errorf("SingletonGroups = %d, want 1", res.SingletonGroups)
	}
	if res.MultiMemberGroups != 1 {
		t.Errorf("MultiMemberGroups = %d, want 1", res.MultiMemberGroups)
	}
	if res.BooksTrapped != 3 {
		t.Errorf("BooksTrapped = %d, want 3", res.BooksTrapped)
	}
	if res.Elected != 2 {
		t.Errorf("Elected = %d, want 2", res.Elected)
	}
	if res.BooksWithoutGroup != 1 {
		t.Errorf("BooksWithoutGroup = %d, want 1", res.BooksWithoutGroup)
	}
	if res.Errors != 0 {
		t.Errorf("Errors = %d, want 0", res.Errors)
	}

	// Exactly the two winners were written — nothing else.
	if len(store.updated) != 2 {
		t.Fatalf("wrote %d books, want 2: %v", len(store.updated), keysOfElectUpdated(store))
	}
	if _, ok := store.updated["solo-1"]; !ok {
		t.Errorf("singleton member solo-1 was not elected")
	}
	// Earliest-created wins the multi-member group.
	if _, ok := store.updated["multi-a"]; !ok {
		t.Errorf("multi-a (earliest created) was not elected; got %v", keysOfElectUpdated(store))
	}
	if _, ok := store.updated["multi-b"]; ok {
		t.Errorf("multi-b was elected but multi-a is older")
	}
	for _, id := range []string{"ok-primary", "ok-secondary", "no-group"} {
		if _, ok := store.updated[id]; ok {
			t.Errorf("%s was written, want untouched", id)
		}
	}
}

func keysOfElectUpdated(f *electFakeStore) []string {
	out := make([]string, 0, len(f.updated))
	for k := range f.updated {
		out = append(out, k)
	}
	return out
}

// TestElectMissingPrimaries_DryRunWritesNothing proves the dry run is a real
// preview: exact counts and samples, zero writes. This is the gate an operator
// reads before authorising an apply against production, so a dry run that
// silently wrote would be the worst possible failure.
func TestElectMissingPrimaries_DryRunWritesNothing(t *testing.T) {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	store := newElectFakeStore()
	for i := 0; i < 12; i++ {
		gid := fmt.Sprintf("vg-dry-%02d", i)
		store.addElectBook(fmt.Sprintf("dry-%02d", i), fmt.Sprintf("Dry %02d", i), gid, false, base)
	}

	res, err := ElectMissingPrimaries(store, true)
	if err != nil {
		t.Fatalf("ElectMissingPrimaries: %v", err)
	}
	if !res.DryRun {
		t.Errorf("DryRun = false, want true")
	}
	if res.GroupsWithoutPrimary != 12 {
		t.Errorf("GroupsWithoutPrimary = %d, want 12", res.GroupsWithoutPrimary)
	}
	if res.Elected != 0 {
		t.Errorf("Elected = %d, want 0 on a dry run", res.Elected)
	}
	if len(store.updated) != 0 {
		t.Errorf("dry run wrote %d books, want 0", len(store.updated))
	}
	if len(res.Samples) != 12 {
		t.Errorf("Samples = %d, want 12", len(res.Samples))
	}
	// Samples must be deterministic regardless of worker completion order.
	for i := 1; i < len(res.Samples); i++ {
		if res.Samples[i-1].VersionGroupID >= res.Samples[i].VersionGroupID {
			t.Errorf("samples not sorted by group id: %v", res.Samples)
			break
		}
	}
}

// TestElectMissingPrimaries_SkipsGroupThatGainedPrimary proves the clobber
// guard. The initial Core snapshot shows a primary-less group, but by the time
// the worker re-reads live membership another writer has elected a primary.
// Electing a second one would create the >1-primary corruption this pass is
// supposed to be the cure for, so the group must be skipped.
func TestElectMissingPrimaries_SkipsGroupThatGainedPrimary(t *testing.T) {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	store := newElectFakeStore()
	store.addElectBook("racy-a", "Racy A", "vg-racy", false, base)
	store.addElectBook("racy-b", "Racy B", "vg-racy", false, base.Add(time.Hour))
	store.addElectBook("calm-a", "Calm A", "vg-calm", false, base)

	// Simulate a concurrent writer: vg-racy has gained a primary since the scan.
	store.onGroupRead = func(gid string, members []database.Book) []database.Book {
		if gid != "vg-racy" {
			return members
		}
		out := append([]database.Book(nil), members...)
		yes := true
		out[0].IsPrimaryVersion = &yes
		return out
	}

	res, err := ElectMissingPrimaries(store, false)
	if err != nil {
		t.Fatalf("ElectMissingPrimaries: %v", err)
	}
	if res.SkippedConcurrent != 1 {
		t.Errorf("SkippedConcurrent = %d, want 1", res.SkippedConcurrent)
	}
	if res.Elected != 1 {
		t.Errorf("Elected = %d, want 1 (only vg-calm)", res.Elected)
	}
	if _, ok := store.updated["racy-a"]; ok {
		t.Errorf("racy-a written despite the group already having a primary")
	}
	if _, ok := store.updated["racy-b"]; ok {
		t.Errorf("racy-b written despite the group already having a primary")
	}
	if _, ok := store.updated["calm-a"]; !ok {
		t.Errorf("calm-a should have been elected")
	}
}

// TestElectPrimaryFor_DeterministicOrder pins the election rule itself:
// earliest CreatedAt wins, ties break on book ID, and a nil CreatedAt sorts
// last so a row with unknown provenance never beats a dated one. Determinism
// matters because a re-run must converge rather than churn the primary flag.
func TestElectPrimaryFor_DeterministicOrder(t *testing.T) {
	base := time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)
	older := base
	newer := base.Add(time.Hour)

	tests := []struct {
		name    string
		members []database.Book
		want    string
	}{
		{
			name: "earliest created wins regardless of slice order",
			members: []database.Book{
				{ID: "z", CreatedAt: &newer},
				{ID: "a", CreatedAt: &older},
			},
			want: "a",
		},
		{
			name: "equal timestamps tie-break on id",
			members: []database.Book{
				{ID: "b", CreatedAt: &older},
				{ID: "a", CreatedAt: &older},
			},
			want: "a",
		},
		{
			name: "nil CreatedAt sorts after a dated row",
			members: []database.Book{
				{ID: "a"},
				{ID: "z", CreatedAt: &newer},
			},
			want: "z",
		},
		{
			name:    "empty group yields no winner",
			members: nil,
			want:    "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := electPrimaryFor(tc.members)
			if tc.want == "" {
				if got != nil {
					t.Fatalf("got %q, want no winner", got.ID)
				}
				return
			}
			if got == nil {
				t.Fatalf("got no winner, want %q", tc.want)
			}
			if got.ID != tc.want {
				t.Errorf("winner = %q, want %q", got.ID, tc.want)
			}
		})
	}
}
