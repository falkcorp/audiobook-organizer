// file: internal/reconcile/elect_primaries_test.go
// version: 1.6.0
// guid: aa557927-956b-41a5-a90b-6ef0093fdcbc
// last-edited: 2026-09-24

package reconcile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
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

	// root is the library root the election is run against; addElectBook
	// gives every book one real .m4b under it, organized, so by default a
	// member is eligible and the members of a group tie on content and
	// metadata (the rule then falls through to creation order, then ID).
	root     string
	files    map[string][]database.BookFile
	chapters map[string]int // probe result by file path; absent = 0

	// onGroupRead, when non-nil, runs inside GetBooksByVersionGroup before the
	// members are returned. It is the seam used to simulate another writer
	// electing a primary between the initial scan and this worker's re-read.
	onGroupRead func(gid string, members []database.Book) []database.Book
}

func newElectFakeStore(t *testing.T) *electFakeStore {
	t.Helper()
	return &electFakeStore{
		byID:     map[string]*database.Book{},
		byGroup:  map[string][]database.Book{},
		updated:  map[string]*database.Book{},
		mu:       make(chan struct{}, 1),
		root:     t.TempDir(),
		files:    map[string][]database.BookFile{},
		chapters: map[string]int{},
	}
}

// electEnv is the ElectEnv for this fake: its root, and a prober that answers
// from the chapters map (no real ffprobe in unit tests).
func (f *electFakeStore) electEnv() ElectEnv {
	return ElectEnv{RootDir: f.root, Probe: func(_ context.Context, path string) (int, error) {
		f.lock()
		defer f.unlock()
		return f.chapters[path], nil
	}}
}

func (f *electFakeStore) GetBookFiles(id string) ([]database.BookFile, error) {
	f.lock()
	defer f.unlock()
	return append([]database.BookFile(nil), f.files[id]...), nil
}

func (f *electFakeStore) GetChaptersForBook(string) ([]database.Chapter, error) { return nil, nil }

// setElectState sets LibraryState on id in every projection.
func (f *electFakeStore) setElectState(id, state string) {
	f.lock()
	defer f.unlock()
	st := state
	f.byID[id].LibraryState = &st
	for gid, ms := range f.byGroup {
		for i := range ms {
			if ms[i].ID == id {
				f.byGroup[gid][i].LibraryState = &st
			}
		}
	}
}

// electFilePath is the one file addElectBook creates for id.
func (f *electFakeStore) electFilePath(id string) string {
	return filepath.Join(f.root, "Author", id, id+".m4b")
}

func (f *electFakeStore) lock()   { f.mu <- struct{}{} }
func (f *electFakeStore) unlock() { <-f.mu }

// addElectBook registers one book in all three projections the pass reads.
func (f *electFakeStore) addElectBook(id, title, gid string, primary bool, created time.Time) {
	g := gid
	p := primary
	c := created
	organized := "organized"
	core := database.BookCore{ID: id, Title: title, CreatedAt: &c}
	full := &database.Book{ID: id, Title: title, CreatedAt: &c, LibraryState: &organized}
	path := f.electFilePath(id)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(path, []byte("m4b"), 0o644); err != nil {
		panic(err)
	}
	f.files[id] = []database.BookFile{{ID: "f-" + id, BookID: id, FilePath: path}}
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
	// An independent copy per read, as PebbleStore unmarshals a new Book each
	// time: handing back the stored pointer would let the caller's edits land
	// on the row ModifyBook re-reads, so a merge-onto-stored assertion could
	// pass without the merge.
	b := f.byID[id]
	if b == nil {
		return nil, nil
	}
	return database.SnapshotBook(b)
}

func (f *electFakeStore) UpdateBook(id string, book *database.Book) (*database.Book, error) {
	f.lock()
	defer f.unlock()
	f.updated[id] = book
	return book, nil
}

// ModifyBook is the locked read-modify-write the pass now uses: fn runs on a
// copy of the stored row and the result lands in both byID and updated.
func (f *electFakeStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	f.lock()
	defer f.unlock()
	b := f.byID[id]
	if b == nil {
		return nil, nil
	}
	cp := *b
	if err := fn(&cp); err != nil {
		if errors.Is(err, database.ErrSkipBookWrite) {
			return &cp, nil
		}
		return nil, err
	}
	stored := cp
	f.byID[id] = &stored
	f.updated[id] = &stored
	return &cp, nil
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
	store := newElectFakeStore(t)

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

	res, err := ElectMissingPrimaries(store, false, nil, store.electEnv())
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
	store := newElectFakeStore(t)
	for i := range 12 {
		gid := fmt.Sprintf("vg-dry-%02d", i)
		store.addElectBook(fmt.Sprintf("dry-%02d", i), fmt.Sprintf("Dry %02d", i), gid, false, base)
	}

	res, err := ElectMissingPrimaries(store, true, nil, store.electEnv())
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
	store := newElectFakeStore(t)
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

	res, err := ElectMissingPrimaries(store, false, nil, store.electEnv())
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

// TestElectPrimaryFor_DeterministicOrder pins the tie-break at the end of the
// rule: members equal on content, metadata and every other signal are ordered
// by earliest CreatedAt, then book ID, with a nil CreatedAt sorting last so a
// row with unknown provenance never beats a dated one. Determinism matters
// because a re-run must converge rather than churn the primary flag.
func TestElectPrimaryFor_DeterministicOrder(t *testing.T) {
	base := time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)
	alive := func(string) bool { return true }
	type add struct {
		id      string
		created time.Time
		nilDate bool
	}
	tests := []struct {
		name string
		adds []add
		want string
	}{
		{"earliest created wins regardless of slice order", []add{{id: "z", created: base.Add(time.Hour)}, {id: "a", created: base}}, "a"},
		{"equal timestamps tie-break on id", []add{{id: "b", created: base}, {id: "a", created: base}}, "a"},
		{"nil CreatedAt sorts after a dated row", []add{{id: "a", nilDate: true}, {id: "z", created: base.Add(time.Hour)}}, "z"},
		{"empty group yields no winner", nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newElectFakeStore(t)
			for _, a := range tc.adds {
				store.addElectBook(a.id, a.id, "vg", false, a.created)
			}
			members := append([]database.Book(nil), store.byGroup["vg"]...)
			for i := range members {
				for _, a := range tc.adds {
					if a.id == members[i].ID && a.nilDate {
						members[i].CreatedAt = nil
					}
				}
			}
			env := store.electEnv()
			loader := versionprimary.Loader{Files: store, Chapters: store, RootDir: env.RootDir, Probe: env.Probe}
			d, err := electPrimaryFor(context.Background(), loader, members, alive)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if d.WinnerID != "" || d.Kind != versionprimary.DecisionHeld {
					t.Fatalf("got %+v, want no winner", d)
				}
				return
			}
			if d.WinnerID != tc.want {
				t.Errorf("winner = %q, want %q", d.WinnerID, tc.want)
			}
		})
	}
}

// markElectMerged sets MergedIntoBookID on id in every projection the pass
// reads (group listing and point read).
func (f *electFakeStore) markElectMerged(id, into string) {
	f.lock()
	defer f.unlock()
	v := into
	f.byID[id].MergedIntoBookID = &v
	for i := range f.books {
		if f.books[i].ID == id {
			f.books[i].MergedIntoBookID = &v
		}
	}
	for gid, ms := range f.byGroup {
		for i := range ms {
			if ms[i].ID == id {
				f.byGroup[gid][i].MergedIntoBookID = &v
			}
		}
	}
}

// A merge loser is usually the OLDEST record in its group. The pass used to
// elect by age alone, so a group that lost its primary re-crowned the book a
// merge had absorbed (09-19 census: 302 merged books primary, 218 also
// organized and listed by ABS beside their survivor).
func TestElectMissingPrimaries_NeverCrownsAMergeLoser(t *testing.T) {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	store := newElectFakeStore(t)
	store.addElectBook("loser", "Old", "vg-m", false, base)
	store.addElectBook("keeper", "New", "vg-m", false, base.Add(time.Hour))
	store.addElectBook("survivor-elsewhere", "S", "vg-s", true, base)
	store.markElectMerged("loser", "survivor-elsewhere")

	res, err := ElectMissingPrimaries(store, false, nil, store.electEnv())
	if err != nil {
		t.Fatalf("ElectMissingPrimaries: %v", err)
	}
	if w, ok := store.updated["loser"]; ok && w.IsPrimaryVersion != nil && *w.IsPrimaryVersion {
		t.Fatalf("merge loser was elected primary")
	}
	w, ok := store.updated["keeper"]
	if !ok || w.IsPrimaryVersion == nil || !*w.IsPrimaryVersion {
		t.Fatalf("live member not elected; result %+v", res)
	}
	if res.Elected != 1 {
		t.Errorf("Elected = %d, want 1", res.Elected)
	}
}

// A group whose every member is a merge loser or soft-deleted keeps no
// primary: its work is represented by the survivor.
func TestElectMissingPrimaries_AllMergedOrDeletedGroupStaysWithoutPrimary(t *testing.T) {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	store := newElectFakeStore(t)
	store.addElectBook("m1", "A", "vg-all", false, base)
	store.addElectBook("d1", "B", "vg-all", false, base.Add(time.Minute))
	store.addElectBook("s", "S", "vg-s", true, base)
	store.markElectMerged("m1", "s")
	yes := true
	store.byID["d1"].MarkedForDeletion = &yes
	store.byGroup["vg-all"][1].MarkedForDeletion = &yes
	for i := range store.books {
		if store.books[i].ID == "d1" {
			store.books[i].MarkedForDeletion = &yes
		}
	}

	res, err := ElectMissingPrimaries(store, false, nil, store.electEnv())
	if err != nil {
		t.Fatalf("ElectMissingPrimaries: %v", err)
	}
	if len(store.updated) != 0 {
		t.Fatalf("wrote %d books, want 0", len(store.updated))
	}
	// Not a candidate at all, so "needs repair" can reach zero.
	if res.GroupsNoEligible != 1 || res.GroupsWithoutPrimary != 0 || res.Elected != 0 {
		t.Errorf("GroupsNoEligible=%d GroupsWithoutPrimary=%d Elected=%d, want 1, 0, 0",
			res.GroupsNoEligible, res.GroupsWithoutPrimary, res.Elected)
	}
}

// The winner is re-checked on the locked re-read: a merge that absorbed it
// after the group was listed must not be undone by crowning it.
func TestElectMissingPrimaries_WinnerMergedAfterGroupReadIsNotCrowned(t *testing.T) {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	store := newElectFakeStore(t)
	store.addElectBook("w", "W", "vg-race", false, base)
	store.onGroupRead = func(gid string, members []database.Book) []database.Book {
		v := "s"
		store.lock()
		store.byID["w"].MergedIntoBookID = &v
		store.unlock()
		return members // the listing still shows w unmerged
	}

	res, err := ElectMissingPrimaries(store, false, nil, store.electEnv())
	if err != nil {
		t.Fatalf("ElectMissingPrimaries: %v", err)
	}
	if w, ok := store.updated["w"]; ok && w.IsPrimaryVersion != nil && *w.IsPrimaryVersion {
		t.Fatalf("book merged after the group read was crowned")
	}
	if res.Elected != 0 || res.Errors != 0 || res.SkippedWinnerChanged != 1 {
		t.Errorf("Elected=%d Errors=%d SkippedWinnerChanged=%d, want 0, 0, 1", res.Elected, res.Errors, res.SkippedWinnerChanged)
	}
}

// A merge loser flagged primary does not give its group a primary while its
// survivor is alive (census: 302 such rows): the live member is elected.
func TestElectMissingPrimaries_MergedPrimaryDoesNotCount(t *testing.T) {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	store := newElectFakeStore(t)
	store.addElectBook("loser", "Old", "vg-p", true, base)
	store.addElectBook("live", "New", "vg-p", false, base.Add(time.Hour))
	store.addElectBook("surv", "S", "vg-s", true, base)
	store.markElectMerged("loser", "surv")

	res, err := ElectMissingPrimaries(store, false, nil, store.electEnv())
	if err != nil {
		t.Fatalf("ElectMissingPrimaries: %v", err)
	}
	w, ok := store.updated["live"]
	if !ok || w.IsPrimaryVersion == nil || !*w.IsPrimaryVersion {
		t.Fatalf("live member not elected; result %+v", res)
	}
}

// MergedIntoBookID is never cleared. When the survivor is gone, the loser is
// the work's only copy and must be electable, or its group never has a
// primary again.
func TestElectMissingPrimaries_LoserOfDeadSurvivorIsElectable(t *testing.T) {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	store := newElectFakeStore(t)
	store.addElectBook("orphan", "O", "vg-d", false, base)
	store.markElectMerged("orphan", "hard-deleted-survivor")

	res, err := ElectMissingPrimaries(store, false, nil, store.electEnv())
	if err != nil {
		t.Fatalf("ElectMissingPrimaries: %v", err)
	}
	w, ok := store.updated["orphan"]
	if !ok || w.IsPrimaryVersion == nil || !*w.IsPrimaryVersion {
		t.Fatalf("loser of a dead survivor not elected; result %+v", res)
	}
}

// The 2026-09-24 bug. Organize leaves the old copy as organized_source and
// the library copy as organized; the source row is older, so the old
// earliest-created rule crowned it and the book stayed hidden from ABS
// (which lists only organized primaries). The library copy must win.
func TestElectMissingPrimaries_OrganizedSourcePairCrownsTheOrganizedCopy(t *testing.T) {
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	store := newElectFakeStore(t)
	store.addElectBook("src", "Book", "vg-pair", false, base)
	store.addElectBook("lib", "Book", "vg-pair", false, base.Add(time.Hour))
	store.setElectState("src", "organized_source")

	res, err := ElectMissingPrimaries(store, false, nil, store.electEnv())
	if err != nil {
		t.Fatalf("ElectMissingPrimaries: %v", err)
	}
	if res.Elected != 1 || res.GroupsHeld != 0 {
		t.Fatalf("Elected=%d GroupsHeld=%d, want 1/0", res.Elected, res.GroupsHeld)
	}
	if _, ok := store.updated["src"]; ok {
		t.Fatal("organized_source copy was crowned")
	}
	if w, ok := store.updated["lib"]; !ok || w.IsPrimaryVersion == nil || !*w.IsPrimaryVersion {
		t.Fatal("organized library copy was not crowned")
	}
}

// Behaviour change pinned on purpose (PLAN.md, owner-approved): a group
// whose only member ABS cannot show — the iTunes-imported singleton this
// endpoint was first written for — is HELD as needs_organize_or_restore and
// not crowned. Crowning an imported row makes it primary without making it
// visible in ABS, and a held group is never flipped.
func TestElectMissingPrimaries_ImportedSingletonIsHeld(t *testing.T) {
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	store := newElectFakeStore(t)
	store.addElectBook("imp", "Imported", "vg-imp", false, base)
	store.setElectState("imp", "imported")

	res, err := ElectMissingPrimaries(store, false, nil, store.electEnv())
	if err != nil {
		t.Fatalf("ElectMissingPrimaries: %v", err)
	}
	if res.Elected != 0 || res.HeldNeedsOrganizeOrRestore != 1 || len(store.updated) != 0 {
		t.Fatalf("Elected=%d HeldNeedsOrganize=%d writes=%d, want 0/1/0", res.Elected, res.HeldNeedsOrganizeOrRestore, len(store.updated))
	}
	if len(res.HeldSamples) != 1 || res.HeldSamples[0].HoldReason != versionprimary.HoldNeedsOrganizeOrRestore {
		t.Fatalf("HeldSamples = %+v", res.HeldSamples)
	}
}

// The source copy has chapters and the library copy does not: held for the
// owner, never crowned with the worse copy.
func TestElectMissingPrimaries_BetterCopyOutsideLibraryIsHeld(t *testing.T) {
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	store := newElectFakeStore(t)
	store.addElectBook("src", "Book", "vg-held", false, base)
	store.addElectBook("lib", "Book", "vg-held", false, base.Add(time.Hour))
	store.setElectState("src", "organized_source")
	store.chapters[store.electFilePath("src")] = 24

	res, err := ElectMissingPrimaries(store, false, nil, store.electEnv())
	if err != nil {
		t.Fatalf("ElectMissingPrimaries: %v", err)
	}
	if res.Elected != 0 || res.HeldBetterCopyNotInLibrary != 1 || len(store.updated) != 0 {
		t.Fatalf("Elected=%d HeldBetterCopy=%d writes=%d, want 0/1/0", res.Elected, res.HeldBetterCopyNotInLibrary, len(store.updated))
	}
}
