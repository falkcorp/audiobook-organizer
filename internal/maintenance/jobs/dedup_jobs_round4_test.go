// file: internal/maintenance/jobs/dedup_jobs_round4_test.go
// version: 1.0.0
// guid: 6d2f8b41-9c3e-4a57-b0e8-1f7a5c9d3e62
// last-edited: 2026-09-13

package jobs

import (
	"context"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Round-4 regression tests. They use only helpers that already existed at
// a47de0a9f, so they compile against that head and show it failing.

func ddClearGroup(t *testing.T, s *database.PebbleStore, id string) {
	t.Helper()
	if _, err := s.ModifyBook(id, func(b *database.Book) error {
		b.VersionGroupID = nil
		b.IsPrimaryVersion = nil
		return nil
	}); err != nil {
		t.Fatalf("clear group: %v", err)
	}
}

// A refused junk row J shares its folder path with the real book B. Phase 1
// refuses J (its own row lies under B's folder), so J stays live; phase 2
// then grouped J with B by path, and J -- with a narrator and duration --
// out-scored B and became the keeper: B's rows moved onto J and B was
// retired. The real book disappeared.
func TestDedupBooks_RefusedJunkNeverBecomesKeeper(t *testing.T) {
	s := ddRealStore(t)
	dir := filepath.Join(t.TempDir(), "Author", "Real Book")
	realFile := filepath.Join(dir, "02.mp3")
	junkFile := filepath.Join(dir, "01.mp3")
	ddWriteAudio(t, realFile)
	ddWriteAudio(t, junkFile)
	b := ddMustBook(t, s, &database.Book{Title: "Real Book", FilePath: dir})
	realRow := ddMustFile(t, s, &database.BookFile{BookID: b.ID, FilePath: realFile})
	narr := "Some Narrator"
	dur := 3600
	j := ddMustBook(t, s, &database.Book{Title: "Read by Narrator", FilePath: dir, Narrator: &narr, Duration: &dur})
	ddMustFile(t, s, &database.BookFile{BookID: j.ID, FilePath: junkFile})

	if err := (&dedupBooksJob{}).Run(context.Background(), s, ddJobReporter{}, false); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if g := ddMustGet(t, s, b.ID); g.IsSoftDeleted() {
		t.Fatal("the real book was retired into a refused junk row")
	}
	rows, _ := s.GetBookFiles(b.ID)
	if len(rows) != 1 || rows[0].ID != realRow.ID {
		t.Fatalf("real book rows = %+v, want its own row", rows)
	}
	if g := ddMustGet(t, s, j.ID); g.IsSoftDeleted() {
		t.Fatal("refused junk row was retired anyway")
	}
}

// A junk row is never picked as keeper over a real book, whatever its score.
func TestDDPickKeeperIdx_JunkIsNeverKeeper(t *testing.T) {
	narr := "Narrator"
	dur := 3600
	books := []database.Book{
		{ID: "junk", Title: "Read by Narrator", Narrator: &narr, Duration: &dur},
		{ID: "real", Title: "Real Book"},
	}
	if got := books[ddPickKeeperIdx(books)].ID; got != "real" {
		t.Fatalf("keeper = %s, want the real book", got)
	}
}

// ddR4Seed builds the stale-copy shape. G = {M1 (nil primary), M2 (not
// primary)}. K (groupless) shares M1's path, so phase 2 keeps K: K joins G
// and takes the primary. X shares K's title, author and folder and scores
// higher, so phase 3 keeps X and retires K. Copies are loaded up front, as
// Run loads allBooks.
func ddR4Seed(t *testing.T, s *database.PebbleStore) (map[string]*database.Book, map[string]string) {
	t.Helper()
	author := 7
	desc := "A description"
	no := false
	const dir = "/lib/Author A"
	books := map[string]*database.Book{
		"M1": ddMustBook(t, s, &database.Book{Title: "Book", FilePath: dir + "/k.m4b"}),
		"K":  ddMustBook(t, s, &database.Book{Title: "Book", FilePath: dir + "/k.m4b", AuthorID: &author}),
		"X":  ddMustBook(t, s, &database.Book{Title: "Book", FilePath: dir + "/x.m4b", AuthorID: &author, Description: &desc}),
		"M2": ddMustBook(t, s, &database.Book{Title: "Other Title", FilePath: "/lib/Elsewhere/m2.m4b"}),
	}
	ddSetGroup(t, s, books["M1"].ID, "G", nil)
	ddSetGroup(t, s, books["M2"].ID, "G", &no)
	ddClearGroup(t, s, books["K"].ID)
	ddClearGroup(t, s, books["X"].ID)
	label := map[string]string{}
	for k, b := range books {
		books[k] = ddMustGet(t, s, b.ID)
		label[b.ID] = k
	}
	return books, label
}

func ddR4LivePrimaries(t *testing.T, s *database.PebbleStore, label map[string]string) []string {
	t.Helper()
	members, err := s.GetBooksByVersionGroup("G")
	if err != nil {
		t.Fatalf("GetBooksByVersionGroup: %v", err)
	}
	var out []string
	for _, m := range members {
		if !m.IsSoftDeleted() && m.IsPrimaryVersion != nil && *m.IsPrimaryVersion {
			out = append(out, label[m.ID])
		}
	}
	sort.Strings(out)
	return out
}

// The same live primaries as ddR4LivePrimaries, as the dry run's overlay sees
// them: store members plus books joined through the fill, minus retirements,
// with promotions counted.
func ddR4SimPrimaries(t *testing.T, s *database.PebbleStore, sim *ddSim, label map[string]string) []string {
	t.Helper()
	members, err := s.GetBooksByVersionGroup("G")
	if err != nil {
		t.Fatalf("GetBooksByVersionGroup: %v", err)
	}
	seen := map[string]bool{}
	for _, m := range members {
		seen[m.ID] = true
	}
	for _, j := range sim.joined["G"] {
		if !seen[j.ID] {
			members = append(members, j)
		}
	}
	var out []string
	for _, m := range members {
		if m.IsSoftDeleted() || sim.retired[m.ID] {
			continue
		}
		if sim.promoted[m.ID] || (m.IsPrimaryVersion != nil && *m.IsPrimaryVersion) {
			out = append(out, label[m.ID])
		}
	}
	sort.Strings(out)
	return out
}

// Through Run: the keeper promoted in phase 2 is phase 3's dup. Read from the
// stale copy it looked groupless, no hand-off was planned, the soft-delete
// demoted it, and G was left with no live primary.
func TestDedupBooks_Phase2KeeperRetiredInPhase3KeepsOnePrimary(t *testing.T) {
	s := ddRealStore(t)
	_, label := ddR4Seed(t, s)
	if err := (&dedupBooksJob{}).Run(context.Background(), s, ddJobReporter{}, false); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := ddR4LivePrimaries(t, s, label); !reflect.DeepEqual(got, []string{"X"}) {
		t.Fatalf("G's live primaries = %v, want [X]", got)
	}
}

// The same two merges with the stale copies, in dry-run and apply. Both must
// plan the same hand-offs and leave exactly one live primary in G.
func TestDedupBooks_StaleKeeperCopyDryRunMatchesApply(t *testing.T) {
	run := func(dry bool) (handoffs, primaries []string) {
		s := ddRealStore(t)
		books, label := ddR4Seed(t, s)
		k1, m1 := *books["K"], *books["M1"]
		x, k2 := *books["X"], *books["K"] // k2: phase 3's stale copy of K
		sim := newDDSim()
		if err := ddMergeDuplicateBookSim(s, sim, &k1, &m1, dry, nil); err != nil {
			t.Fatalf("dry=%v phase-2 merge: %v", dry, err)
		}
		if err := ddMergeDuplicateBookSim(s, sim, &x, &k2, dry, nil); err != nil {
			t.Fatalf("dry=%v phase-3 merge: %v", dry, err)
		}
		for _, id := range sim.handoffs {
			handoffs = append(handoffs, label[id])
		}
		if dry {
			return handoffs, ddR4SimPrimaries(t, s, sim, label)
		}
		return handoffs, ddR4LivePrimaries(t, s, label)
	}
	dryH, dryP := run(true)
	applyH, applyP := run(false)
	want := []string{"K", "X"}
	if !reflect.DeepEqual(applyH, want) || !reflect.DeepEqual(dryH, want) {
		t.Fatalf("hand-offs: dry-run %q, apply %q, want %q", dryH, applyH, want)
	}
	if !reflect.DeepEqual(applyP, []string{"X"}) || !reflect.DeepEqual(dryP, []string{"X"}) {
		t.Fatalf("G's live primaries: dry-run %v, apply %v, want [X] in both", dryP, applyP)
	}
}
