// file: internal/plugins/maintenance/merge_cleanup_fixer_test.go
// version: 1.0.0
// guid: 5f0f2a63-1d4e-4b6e-9f55-2f3a7c0b8e14
// last-edited: 2026-11-18

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/decisions"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
)

// mcFixture is a synthetic library of merged books. Every title, person and
// series is invented (Batman comics and characters); no row is a real one.
type mcFixture struct {
	t        *testing.T
	st       *database.PebbleStore
	root     string
	ids      map[string]string // fixture name -> book id
	names    map[string]string // book id -> fixture name
	authors  map[string]int
	chapters map[string]int // chapter count a path probes to
	f        *mergeCleanupFixer
	vetoes   OwnerVetoReader
}

// mcFile is one book_file row of a fixture book. secs < 0 is a duration that
// was never measured.
type mcFile struct {
	secs    int
	hash    string
	size    int64
	missing bool
}

// mcSpec is one fixture book.
type mcSpec struct {
	title   string
	authors []string
	group   string
	primary *bool
	state   string // "" is organized
	series  string
	seq     *int
	asin    string
	msh     string
	trashed bool
	files   []mcFile
}

func newMCFixture(t *testing.T) *mcFixture {
	t.Helper()
	st, err := database.NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	st.WaitForWarmup()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	x := &mcFixture{t: t, st: st, root: root, ids: map[string]string{}, names: map[string]string{},
		authors: map[string]int{}, chapters: map[string]int{}}
	withRoot(t, root)
	x.rebuild()
	return x
}

// rebuild makes the fixer over the fixture's current deps.
func (x *mcFixture) rebuild() {
	x.f = newMergeCleanupFixer(&Plugin{deps: fakeDeps{store: x.st, root: x.root, vetoes: x.vetoes}})
	x.f.vgProbe = func(_ context.Context, path string) (int, error) {
		if n, ok := x.chapters[path]; ok {
			return n, nil
		}
		return 0, fmt.Errorf("no such file in the stub")
	}
}

func mcTrue() *bool  { return retiredPtr(true) }
func mcFalse() *bool { return retiredPtr(false) }

// one is a single present file of secs seconds at 64 kbps. The size matters:
// the store's write chokepoint rewrites a duration as milliseconds when the
// size implies an impossibly low bitrate, so a fake size would change the
// runtime the test is about.
func one(secs int) []mcFile { return []mcFile{{secs: secs, size: int64(secs) * 8000}} }

func (x *mcFixture) author(name string) int {
	x.t.Helper()
	if id, ok := x.authors[name]; ok {
		return id
	}
	a, err := x.st.CreateAuthor(name)
	require.NoError(x.t, err)
	x.authors[name] = a.ID
	return a.ID
}

// mk creates one fixture book with real files under the root.
func (x *mcFixture) mk(name string, s mcSpec) string {
	x.t.Helper()
	state := s.state
	if state == "" {
		state = "organized"
	}
	b := &database.Book{Title: s.title, FilePath: filepath.Join(x.root, "stale", name+".m4b"), LibraryState: &state}
	if s.group != "" {
		b.VersionGroupID = retiredPtr(s.group)
	}
	if s.asin != "" {
		b.ASIN = retiredPtr(s.asin)
	}
	if s.msh != "" {
		b.MetadataSourceHash = retiredPtr(s.msh)
	}
	if s.series != "" {
		se, err := x.st.CreateSeries(s.series, nil)
		require.NoError(x.t, err)
		b.SeriesID = &se.ID
		b.SeriesSequence = s.seq
	}
	created, err := x.st.CreateBook(b)
	require.NoError(x.t, err)
	id := created.ID
	x.ids[name], x.names[id] = id, name
	// CreateBook may default the flag; pin the stored tri-state the test asked for.
	_, err = x.st.ModifyBook(id, func(row *database.Book) error {
		row.IsPrimaryVersion = s.primary
		if s.trashed {
			row.MarkedForDeletion = retiredPtr(true)
			row.MarkedForDeletionAt = retiredPtr(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
		}
		return nil
	})
	require.NoError(x.t, err)
	var credits []database.BookAuthor
	for i, n := range s.authors {
		credits = append(credits, database.BookAuthor{BookID: id, AuthorID: x.author(n), Role: "author", Position: i})
	}
	if len(credits) > 0 {
		require.NoError(x.t, x.st.SetBookAuthors(id, credits))
	}
	for i, f := range s.files {
		path := filepath.Join(x.root, "Library", name, fmt.Sprintf("%s-%d.m4b", name, i))
		require.NoError(x.t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(x.t, os.WriteFile(path, []byte("synthetic "+name), 0o644))
		bf := &database.BookFile{ID: fmt.Sprintf("bf-%s-%d", name, i), BookID: id, FilePath: path, Format: "m4b",
			FileSize: f.size, FileHash: f.hash, Missing: f.missing}
		if f.secs >= 0 {
			d := f.secs
			bf.Duration = &d
		}
		require.NoError(x.t, x.st.CreateBookFile(bf))
		x.chapters[path] = 1
	}
	return id
}

// point sets a merge pointer the way MATCH-4 leaves it: the pointer and an
// explicit false primary flag, nothing else.
func (x *mcFixture) point(from, to string) {
	x.t.Helper()
	_, err := x.st.ModifyBook(x.ids[from], func(b *database.Book) error {
		b.MergedIntoBookID = retiredPtr(x.ids[to])
		b.IsPrimaryVersion = mcFalse()
		return nil
	})
	require.NoError(x.t, err)
}

// pointRaw sets only the pointer, leaving the flag alone.
func (x *mcFixture) pointRaw(from, to string) {
	x.t.Helper()
	_, err := x.st.ModifyBook(x.ids[from], func(b *database.Book) error {
		b.MergedIntoBookID = retiredPtr(x.ids[to])
		return nil
	})
	require.NoError(x.t, err)
}

func (x *mcFixture) setChapters(name string, n int) {
	x.t.Helper()
	files, err := x.st.GetBookFiles(x.ids[name])
	require.NoError(x.t, err)
	for _, f := range files {
		x.chapters[f.FilePath] = n
	}
}

// plan runs Plan and keys the rows by fixture name.
func (x *mcFixture) plan() map[string]repairs.Row {
	x.t.Helper()
	rows, err := x.f.Plan(context.Background(), nil, &fakeReporter{})
	require.NoError(x.t, err)
	out := map[string]repairs.Row{}
	for _, r := range rows {
		name, ok := x.names[r.RowID]
		require.True(x.t, ok, "row %s names a book the fixture did not make", r.RowID)
		out[name] = r
	}
	return out
}

// wantRow asserts a row's class and skip kind ("" is applicable).
func wantRow(t *testing.T, rows map[string]repairs.Row, name, class, skip string) repairs.Row {
	t.Helper()
	r, ok := rows[name]
	require.True(t, ok, "no row for %s (rows: %v)", name, mcRowNames(rows))
	require.Equal(t, class, r.Class, "%s class (skip %q: %s)", name, r.Skipped, r.SkipReason)
	require.Equal(t, skip, r.Skipped, "%s skip kind (class %s: %s)", name, r.Class, r.SkipReason)
	return r
}

func mcRowNames(rows map[string]repairs.Row) []string {
	var out []string
	for n, r := range rows {
		out = append(out, n+"="+r.Class+"/"+r.Skipped)
	}
	slices.Sort(out)
	return out
}

// wrongWork is the standard UNMERGE shape: a different title and a different
// author, no group, both runtimes complete and apart.
func (x *mcFixture) wrongWork(loser, target string) {
	x.t.Helper()
	x.mk(target, mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)})
	x.mk(loser, mcSpec{title: "The Long Halloween", authors: []string{"Bruce Wayne"}, files: one(40000)})
	x.point(loser, target)
}

// fragment is the standard SWAP shape: the same title and author, the target a
// quarter of the loser's length.
func (x *mcFixture) fragment(loser, target, group string) {
	x.t.Helper()
	x.mk(target, mcSpec{title: "Year One", authors: []string{"Bruce Wayne"}, files: one(9000), group: group, primary: mcTrueIf(group)})
	x.mk(loser, mcSpec{title: "Year One", authors: []string{"Bruce Wayne"}, files: one(36000), group: group})
	x.point(loser, target)
}

func mcTrueIf(group string) *bool {
	if group == "" {
		return nil
	}
	return mcTrue()
}

// ---- population ----

func TestMergeCleanupPopulation(t *testing.T) {
	x := newMCFixture(t)
	x.wrongWork("pop-loser", "pop-target")

	// Not merged: no row.
	x.mk("pop-plain", mcSpec{title: "Knightfall", files: one(1000)})
	// Merged and trashed: not this fixer's (retired-versions owns it).
	x.mk("pop-trashed-target", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)})
	x.mk("pop-trashed", mcSpec{title: "Knightfall", authors: []string{"Bruce Wayne"}, files: one(40000), trashed: true})
	x.point("pop-trashed", "pop-trashed-target")
	// Merged, not trashed, but with no file rows at all, or only a missing one.
	x.mk("pop-nofiles", mcSpec{title: "Knightfall", authors: []string{"Bruce Wayne"}})
	x.point("pop-nofiles", "pop-target")
	x.mk("pop-missing", mcSpec{title: "Knightfall", authors: []string{"Bruce Wayne"}, files: []mcFile{{secs: 100, missing: true}}})
	x.point("pop-missing", "pop-target")
	// Labelled "deleted" without the soft-delete bit is trash too.
	x.mk("pop-labelled", mcSpec{title: "Knightfall", authors: []string{"Bruce Wayne"}, files: one(40000), state: "deleted"})
	x.point("pop-labelled", "pop-target")

	rows, census, err := x.f.plan(context.Background(), nil, &fakeReporter{})
	require.NoError(t, err)
	var got []string
	for _, r := range rows {
		got = append(got, x.names[r.RowID])
	}
	require.Equal(t, []string{"pop-loser"}, got, "only the merged, live, file-bearing book is a row")
	require.Equal(t, 1, census.Rows)
	require.Equal(t, 2, census.ExcludedNoFiles, "no file rows, and only a missing one")
	require.Equal(t, 1, census.ExcludedTrashed, "the labelled-deleted book; a soft-deleted one never reaches the listing")
}

// ---- row kinds ----

func TestMergeCleanupUnmergeRow(t *testing.T) {
	x := newMCFixture(t)
	x.wrongWork("loser", "target")
	r := wantRow(t, x.plan(), "loser", mcKindUnmerge, "")
	require.Equal(t, x.ids["loser"], r.RowID, "the row is keyed by the loser: its kind may change between plans, its id does not")
	require.Equal(t, repairs.RiskReview, r.Risk)
	require.Equal(t, x.ids["target"], r.Current["target_book_id"])
	require.Equal(t, "live", r.Current["chain_end"])
	require.Contains(t, r.Current["signals"], "TITLE_DIFF")
	require.Contains(t, r.Current["signals"], "AUTH_DIFF")
	require.Equal(t, "", r.Proposed["merged_into_book_id"])
	require.Equal(t, "unset", r.Proposed["is_primary_version"])
	require.Equal(t, "group_less", r.Current["group_effect"])
	require.ElementsMatch(t, []string{x.ids["loser"], x.ids["target"]}, r.BookIDs)
	require.NotEmpty(t, r.Fingerprint)
	require.False(t, r.OwnerApplicable, "PR 1 has no apply path, so nothing is owner-applicable")
}

func TestMergeCleanupSwapRow(t *testing.T) {
	x := newMCFixture(t)
	x.fragment("loser", "target", "")
	r := wantRow(t, x.plan(), "loser", mcKindSwap, "")
	require.Equal(t, x.ids["loser"], r.Proposed["keeper_book_id"])
	require.Equal(t, x.ids["target"], r.Proposed["retired_book_id"])
	require.Equal(t, "250", r.Current["runtime_ratio_permille"])
	require.Equal(t, "36000", r.Current["loser_runtime_seconds"])
	require.Equal(t, "9000", r.Current["target_runtime_seconds"])
	require.Contains(t, r.Current["signals"], "TITLE_MATCH")
	require.Contains(t, r.Current["signals"], "DUR_FAR")
}

func TestMergeCleanupVerdictSkips(t *testing.T) {
	x := newMCFixture(t)

	// runtime_incomplete: the target has two files and one was never measured.
	x.mk("inc-target", mcSpec{title: "Year One", authors: []string{"Bruce Wayne"}, files: []mcFile{{secs: 4000}, {secs: -1}}})
	x.mk("inc-loser", mcSpec{title: "Year One", authors: []string{"Bruce Wayne"}, files: one(36000)})
	x.point("inc-loser", "inc-target")

	// runtime_ambiguous: the target is half the loser (DUR_2X), and a probe.
	x.mk("half-target", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(18000)})
	x.mk("half-loser", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(36000)})
	x.point("half-loser", "half-target")
	x.mk("probe-target", mcSpec{title: "Knightfall", authors: []string{"Bruce Wayne"}, files: one(1200)})
	x.mk("probe-loser", mcSpec{title: "Knightfall", authors: []string{"Bruce Wayne"}, files: one(36000)})
	x.point("probe-loser", "probe-target")

	// merge_looks_correct: the same work and the target is not a fragment.
	x.mk("ok-target", mcSpec{title: "Year One", authors: []string{"Bruce Wayne"}, files: one(35000)})
	x.mk("ok-loser", mcSpec{title: "Year One", authors: []string{"Bruce Wayne"}, files: one(36000)})
	x.point("ok-loser", "ok-target")

	// suspicious: the same title, a different author.
	x.mk("sus-target", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(9000)})
	x.mk("sus-loser", mcSpec{title: "Hush", authors: []string{"Alfred Pennyworth"}, files: one(36000)})
	x.point("sus-loser", "sus-target")

	// cannot_tell: no credits, titles neither equal nor apart.
	x.mk("ct-target", mcSpec{title: "The Long Halloween", files: one(9000)})
	x.mk("ct-loser", mcSpec{title: "Long Halloween Part Two", files: one(36000)})
	x.point("ct-loser", "ct-target")

	// runtime_incomplete on the wrong-work side too.
	x.mk("winc-target", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: []mcFile{{secs: 100}, {secs: -1}}})
	x.mk("winc-loser", mcSpec{title: "The Long Halloween", authors: []string{"Bruce Wayne"}, files: one(40000)})
	x.point("winc-loser", "winc-target")

	rows := x.plan()
	wantRow(t, rows, "inc-loser", mcKindSwap, mcSkipRuntimeIncomplete)
	wantRow(t, rows, "half-loser", mcKindSwap, mcSkipRuntimeAmbiguous)
	wantRow(t, rows, "probe-loser", mcKindSwap, mcSkipRuntimeAmbiguous)
	wantRow(t, rows, "ok-loser", mcClassNoVerdict, mcSkipMergeLooksCorrect)
	wantRow(t, rows, "sus-loser", mcClassNoVerdict, mcSkipSuspicious)
	wantRow(t, rows, "ct-loser", mcClassNoVerdict, mcSkipCannotTell)
	wantRow(t, rows, "winc-loser", mcKindUnmerge, mcSkipRuntimeIncomplete)
	for _, name := range []string{"inc-loser", "half-loser", "sus-loser", "ct-loser"} {
		require.NotEmpty(t, rows[name].SkipReason, name)
		require.NotEmpty(t, rows[name].Current["signals"]+rows[name].Current["title_similarity"], "a skipped row still lists its signals")
	}
}

// ---- chains ----

func TestMergeCleanupChainShapes(t *testing.T) {
	x := newMCFixture(t)

	// L -> B (merged, not trashed) -> C live: the verdict is made against C, the
	// live end, not against the loser B.
	x.mk("hop-c", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)})
	x.mk("hop-b", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(29000)})
	x.point("hop-b", "hop-c")
	x.mk("hop-l", mcSpec{title: "The Long Halloween", authors: []string{"Bruce Wayne"}, files: one(40000)})
	x.point("hop-l", "hop-b")

	// L -> B (trashed) -> C live: reached through the trash.
	x.mk("thr-c", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)})
	x.mk("thr-b", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), trashed: true})
	x.pointRaw("thr-b", "thr-c")
	x.mk("thr-l", mcSpec{title: "The Long Halloween", authors: []string{"Bruce Wayne"}, files: one(40000)})
	x.point("thr-l", "thr-b")

	// A <-> B cycle, and a tail into it.
	x.mk("cyc-a", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)})
	x.mk("cyc-b", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(34000)})
	x.point("cyc-a", "cyc-b")
	x.point("cyc-b", "cyc-a")
	x.mk("cyc-tail", mcSpec{title: "Knightfall", authors: []string{"Bruce Wayne"}, files: one(1000)})
	x.point("cyc-tail", "cyc-a")

	// A chain longer than the cap.
	for i := 0; i <= 11; i++ {
		x.mk(fmt.Sprintf("long-%d", i), mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(1000 + i)})
	}
	for i := 0; i < 11; i++ {
		x.point(fmt.Sprintf("long-%d", i), fmt.Sprintf("long-%d", i+1))
	}

	rows := x.plan()

	r := wantRow(t, rows, "hop-l", mcKindUnmerge, "")
	require.Equal(t, x.ids["hop-c"], r.Current["target_book_id"], "the verdict target is the live end")
	require.Contains(t, r.Flags, mcFlagChainViaMerged)
	require.Contains(t, r.BookIDs, x.ids["hop-b"], "every hop is a book the row reads")

	r = wantRow(t, rows, "thr-l", mcKindUnmerge, "")
	require.Equal(t, x.ids["thr-c"], r.Current["target_book_id"])
	require.Contains(t, r.Flags, mcFlagChainViaTrashed)

	// The head of the cycle is the member with the longest known runtime.
	wantRow(t, rows, "cyc-b", mcKindUnmerge, "")
	wantRow(t, rows, "cyc-a", mcKindUnmerge, mcSkipCycleNotHead)
	r = wantRow(t, rows, "cyc-tail", mcKindRepoint, mcSkipCycleNotHead)
	require.Equal(t, "cycle", r.Current["chain_end"])
	require.Contains(t, r.SkipReason, "re-plan")

	r = wantRow(t, rows, "long-0", mcKindRepoint, mcSkipChainOverCap)
	require.Equal(t, "over_cap", r.Current["chain_end"])
}

func TestMergeCleanupDeadEndChains(t *testing.T) {
	x := newMCFixture(t)

	// L -> B (trashed, no pointer). B's group has a live primary: repoint to it.
	x.mk("a-heir", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g-a", primary: mcTrue()})
	x.mk("a-dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g-a", primary: mcFalse(), trashed: true})
	x.mk("a-loser", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)})
	x.point("a-loser", "a-dead")

	// B retired its rows onto a live holder: repoint to the holder.
	x.mk("b-holder", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)})
	x.mk("b-dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	retireRowOnto(t, x.st, x.ids["b-dead"], x.ids["b-holder"], "rb", filepath.Join(x.root, "retired", "b.m4b"))
	x.mk("b-loser", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)})
	x.point("b-loser", "b-dead")

	// Two different live books are reachable: never guessed.
	x.mk("c-heir", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g-c", primary: mcTrue()})
	x.mk("c-holder", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)})
	x.mk("c-dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, group: "g-c", primary: mcFalse(), trashed: true})
	retireRowOnto(t, x.st, x.ids["c-dead"], x.ids["c-holder"], "rc", filepath.Join(x.root, "retired", "c.m4b"))
	x.mk("c-loser", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)})
	x.point("c-loser", "c-dead")

	// Nothing live is reachable: the loser has to stand alone.
	x.mk("d-dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	x.mk("d-loser", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)})
	x.point("d-loser", "d-dead")

	// The pointer names a book that does not exist.
	x.mk("e-loser", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)})
	_, err := x.st.ModifyBook(x.ids["e-loser"], func(b *database.Book) error {
		b.MergedIntoBookID = retiredPtr("01HZZZZZZZZZZZZZZZZZZZZZZZ")
		b.IsPrimaryVersion = mcFalse()
		return nil
	})
	require.NoError(t, err)

	rows := x.plan()

	r := wantRow(t, rows, "a-loser", mcKindRepoint, "")
	require.Equal(t, x.ids["a-heir"], r.Proposed["merged_into_book_id"])
	require.Equal(t, x.ids["a-dead"], r.Current["merged_into_book_id"])
	require.Equal(t, "dead", r.Current["chain_end"])
	require.Contains(t, r.BookIDs, x.ids["a-heir"])

	r = wantRow(t, rows, "b-loser", mcKindRepoint, "")
	require.Equal(t, x.ids["b-holder"], r.Proposed["merged_into_book_id"])

	r = wantRow(t, rows, "c-loser", mcKindRepoint, mcSkipRepointAmbiguous)
	require.Contains(t, r.SkipReason, x.ids["c-heir"])
	require.Contains(t, r.SkipReason, x.ids["c-holder"])
	require.Empty(t, r.Proposed["merged_into_book_id"], "two candidates are never guessed between")

	r = wantRow(t, rows, "d-loser", mcKindUnmerge, "")
	require.Equal(t, "dead", r.Current["chain_end"])
	require.Equal(t, "", r.Proposed["merged_into_book_id"])
	require.Empty(t, r.Current["target_book_id"], "a dead end has no target to compare against")

	r = wantRow(t, rows, "e-loser", mcKindUnmerge, "")
	require.Equal(t, "dead", r.Current["chain_end"])
	for _, id := range r.BookIDs {
		require.NotEqual(t, "01HZZZZZZZZZZZZZZZZZZZZZZZ", id, "a book that does not exist is not a book the guards can read")
	}
}

// ---- gates ----

func TestMergeCleanupRetiredShellIsHeld(t *testing.T) {
	x := newMCFixture(t)
	x.wrongWork("loser", "target")
	x.mk("holder", mcSpec{title: "Knightfall", authors: []string{"Bruce Wayne"}, files: one(1000)})
	// The loser also gave rows to a retired version of another book.
	retireRowOnto(t, x.st, x.ids["loser"], x.ids["holder"], "rl", filepath.Join(x.root, "retired", "l.m4b"))
	rows := x.plan()
	wantRow(t, rows, "loser", mcClassNoVerdict, mcSkipRetiredShell)
}

func TestMergeCleanupCrossGroupSwapIsHeld(t *testing.T) {
	x := newMCFixture(t)
	x.mk("target", mcSpec{title: "Year One", authors: []string{"Bruce Wayne"}, files: one(9000), group: "g-t", primary: mcTrue()})
	x.mk("loser", mcSpec{title: "Year One", authors: []string{"Bruce Wayne"}, files: one(36000), group: "g-l"})
	x.point("loser", "target")
	wantRow(t, x.plan(), "loser", mcKindSwap, mcSkipCrossGroup)
}

func TestMergeCleanupGroupedSwap(t *testing.T) {
	t.Run("target is the primary and the loser would be crowned", func(t *testing.T) {
		x := newMCFixture(t)
		x.fragment("loser", "target", "g")
		r := wantRow(t, x.plan(), "loser", mcKindSwap, "")
		require.Equal(t, x.ids["loser"], r.Current["election_winner_after_swap"])
	})
	t.Run("target is not the group's primary", func(t *testing.T) {
		x := newMCFixture(t)
		x.mk("prim", mcSpec{title: "Year One", authors: []string{"Bruce Wayne"}, files: one(36000), group: "g", primary: mcTrue()})
		x.mk("target", mcSpec{title: "Year One", authors: []string{"Bruce Wayne"}, files: one(9000), group: "g", primary: mcFalse()})
		x.mk("loser", mcSpec{title: "Year One", authors: []string{"Bruce Wayne"}, files: one(36000), group: "g"})
		x.point("loser", "target")
		wantRow(t, x.plan(), "loser", mcKindSwap, mcSkipSwapTargetNotPrimary)
	})
	t.Run("another member would be crowned instead of the loser", func(t *testing.T) {
		x := newMCFixture(t)
		x.fragment("loser", "target", "g")
		// A bystander ranks above the loser (chapters) but not above the primary,
		// which already wins on the explicit flag, so the version-group fixer
		// leaves the group alone.
		x.mk("bystander", mcSpec{title: "Year One", authors: []string{"Bruce Wayne"}, files: one(36000), group: "g", primary: mcFalse()})
		x.setChapters("target", 20)
		x.setChapters("bystander", 20)
		wantRow(t, x.plan(), "loser", mcKindSwap, mcSkipSwapNoCrown)
	})
}

func TestMergeCleanupUnmergeGroupEffect(t *testing.T) {
	t.Run("the loser leaves a group that holds its target", func(t *testing.T) {
		x := newMCFixture(t)
		x.mk("target", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g", primary: mcTrue()})
		x.mk("loser", mcSpec{title: "The Long Halloween", authors: []string{"Bruce Wayne"}, files: one(40000), group: "g"})
		x.point("loser", "target")
		r := wantRow(t, x.plan(), "loser", mcKindUnmerge, "")
		require.Equal(t, "leaves_group", r.Current["group_effect"])
		require.Equal(t, "", r.Proposed["version_group_id"])
	})
	t.Run("the loser stays in a group of its own that keeps a primary", func(t *testing.T) {
		x := newMCFixture(t)
		x.mk("target", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)})
		x.mk("keeper", mcSpec{title: "The Long Halloween", authors: []string{"Bruce Wayne"}, files: one(40000), group: "own", primary: mcTrue()})
		x.mk("loser", mcSpec{title: "The Long Halloween", authors: []string{"Bruce Wayne"}, files: one(40000), group: "own"})
		x.point("loser", "target")
		r := wantRow(t, x.plan(), "loser", mcKindUnmerge, "")
		require.Equal(t, "stays_in_group", r.Current["group_effect"])
		require.Equal(t, "unchanged", r.Proposed["version_group_id"])
		require.NotContains(t, r.Flags, mcFlagGroupNeedsHandoff)
	})
	t.Run("the loser would be left as the only member with no primary", func(t *testing.T) {
		x := newMCFixture(t)
		x.mk("target", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)})
		x.mk("loser", mcSpec{title: "The Long Halloween", authors: []string{"Bruce Wayne"}, files: one(40000), group: "own"})
		x.point("loser", "target")
		r := wantRow(t, x.plan(), "loser", mcKindUnmerge, mcSkipGroupNeedsHandoff)
		require.Contains(t, r.Flags, mcFlagGroupNeedsHandoff)
	})
}

func TestMergeCleanupSyncRedirect(t *testing.T) {
	x := newMCFixture(t)
	x.wrongWork("loser", "target")
	x.wrongWork("calm", "calm-target")
	_, err := x.st.MintOrGetSyncID(x.ids["loser"])
	require.NoError(t, err)
	require.NoError(t, x.st.RecordSyncMerge(x.ids["loser"], x.ids["target"]))
	_, err = x.st.MintOrGetSyncID(x.ids["calm"])
	require.NoError(t, err)

	rows := x.plan()
	r := wantRow(t, rows, "loser", mcKindUnmerge, mcSkipSyncRedirect)
	require.Contains(t, r.Flags, mcFlagSyncRedirect)
	r = wantRow(t, rows, "calm", mcKindUnmerge, "")
	require.NotContains(t, r.Flags, mcFlagSyncRedirect, "a sync item that still resolves to the book is no redirect")
}

// A flag is a fact the row carries beside its skip kind: SkippedByKind counts
// only the first hold, so a row held twice shows both only through its flags.
func TestMergeCleanupFlagsSurviveAnEarlierHold(t *testing.T) {
	x := newMCFixture(t)
	x.mk("target", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)})
	x.mk("loser", mcSpec{title: "The Long Halloween", authors: []string{"Bruce Wayne"}, files: one(40000), group: "own"})
	x.point("loser", "target")
	_, err := x.st.MintOrGetSyncID(x.ids["loser"])
	require.NoError(t, err)
	require.NoError(t, x.st.RecordSyncMerge(x.ids["loser"], x.ids["target"]))
	r := wantRow(t, x.plan(), "loser", mcKindUnmerge, mcSkipGroupNeedsHandoff)
	require.Contains(t, r.Flags, mcFlagSyncRedirect)
	require.Contains(t, r.Flags, mcFlagGroupNeedsHandoff)
	var kinds []string
	for _, h := range r.Holds {
		kinds = append(kinds, h.Kind)
	}
	require.Equal(t, []string{mcSkipGroupNeedsHandoff, mcSkipSyncRedirect}, kinds, "every hold, in the order the gates ran")
}

func TestMergeCleanupOwnerVetoes(t *testing.T) {
	x := newMCFixture(t)
	x.fragment("swap-loser", "swap-target", "")
	x.wrongWork("un-loser", "un-target")
	veto := &mcFakeVetoes{pairs: map[string]bool{}}
	veto.pairs[x.ids["swap-loser"]+"|"+x.ids["swap-target"]] = true
	veto.pairs[x.ids["un-loser"]+"|"+x.ids["un-target"]] = true
	x.vetoes = veto
	x.rebuild()
	rows := x.plan()
	// The owner said the pair is not one work: that holds a SWAP (which would
	// call them one work) and agrees with an UNMERGE.
	wantRow(t, rows, "swap-loser", mcKindSwap, mcSkipOwnerSaysDistinct)
	r := wantRow(t, rows, "un-loser", mcKindUnmerge, "")
	require.Contains(t, r.Flags, mcFlagOwnerSaysDistinct)

	t.Run("a reject that cannot be resolved holds a swap", func(t *testing.T) {
		x := newMCFixture(t)
		x.fragment("loser", "target", "")
		x.vetoes = &mcFakeVetoes{unresolvable: true}
		x.rebuild()
		wantRow(t, x.plan(), "loser", mcKindSwap, mcSkipVetoUnresolvable)
	})
}

// ---- the boundary with the version-group fixer ----

func TestMergeCleanupDefersToVersionGroupRepair(t *testing.T) {
	f := newVGRepairFixture(t)
	f.seedBackwardsMerge(t, "vg-rev", "SRC", "ORG")
	withRoot(t, f.root)
	mc := newMergeCleanupFixer(&Plugin{deps: fakeDeps{store: f.s, root: f.root}})
	mc.vgProbe = f.probe

	rows, err := mc.Plan(context.Background(), nil, &fakeReporter{})
	require.NoError(t, err)
	var org repairs.Row
	for _, r := range rows {
		if r.RowID == "ORG" {
			org = r
		}
	}
	require.Equal(t, "ORG", org.RowID, "the revived copy is in the census")
	require.Equal(t, mcSkipHandledByVG, org.Skipped, "the version-group fixer writes this group, so merge-cleanup must not")
	require.Contains(t, org.SkipReason, "revive_merged_copy")
}

// No book is planned for a write by both fixers: every book a merge-cleanup
// row would write is absent from every group decision the version-group fixer
// would write.
func TestMergeCleanupAndVersionGroupRepairNeverWriteTheSameBook(t *testing.T) {
	f := newVGRepairFixture(t)
	f.seed(t)
	f.seedBackwardsMerge(t, "vg-rev", "SRC", "ORG")
	withRoot(t, f.root)
	// Wrong-work and fragment shapes inside groups the version-group fixer sees.
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	f.book(t, "WT", "vg-w", "organized", "true", 10, false, base, func(b *database.Book) { b.Title = "Hush" })
	f.book(t, "WL", "vg-w", "organized", "nil", 10, false, base.Add(time.Hour), func(b *database.Book) { b.Title = "The Long Halloween" })
	require.NoError(t, f.s.FlagMetadataHashDuplicate("WT", "WL"))
	// A wrong-work merge needs two signals and a measured runtime on both sides,
	// which the version-group fixture's bare files do not have.
	for id, v := range map[string]struct {
		author string
		secs   int
	}{"WT": {"Selina Kyle", 30000}, "WL": {"Bruce Wayne", 40000}} {
		a, err := f.s.CreateAuthor(v.author)
		require.NoError(t, err)
		require.NoError(t, f.s.SetBookAuthors(id, []database.BookAuthor{{BookID: id, AuthorID: a.ID, Role: "author"}}))
		files, err := f.s.GetBookFiles(id)
		require.NoError(t, err)
		require.Len(t, files, 1)
		bf := files[0]
		d := v.secs
		bf.Duration, bf.FileSize = &d, int64(v.secs)*8000
		require.NoError(t, f.s.UpdateBookFile(bf.ID, &bf))
	}

	mc := newMergeCleanupFixer(&Plugin{deps: fakeDeps{store: f.s, root: f.root}})
	mc.vgProbe = f.probe
	rows, err := mc.Plan(context.Background(), nil, &fakeReporter{})
	require.NoError(t, err)
	ours := map[string]bool{}
	for _, r := range rows {
		if r.Applicable() {
			for _, id := range mcWrittenBooks(r) {
				ours[id] = true
			}
		}
	}

	ops := fakeDeps{store: f.s, root: f.root}.OpsStore()
	vp := newVGPrimaryFixer(&Plugin{deps: fakeDeps{store: f.s, root: f.root}})
	vp.probe = f.probe
	names, err := vp.series(ops)
	require.NoError(t, err)
	a := &vgApplier{store: ops, reporter: &fakeReporter{}, seriesNames: names, paths: repairs.NewPathResolver()}
	loader := versionprimary.Loader{Files: ops, Chapters: fakeDeps{store: f.s, root: f.root}.VersionPrimaryStore(),
		RootDir: f.root, Probe: f.probe}
	groups := map[string]bool{}
	all, err := ops.GetAllBooksCore(0, 0)
	require.NoError(t, err)
	for _, b := range all {
		if b.VersionGroupID != nil && *b.VersionGroupID != "" {
			groups[*b.VersionGroupID] = true
		}
	}
	require.NotEmpty(t, groups)
	theirs := map[string]bool{}
	for gid := range groups {
		for _, id := range vgWrittenBooks(a.planGroup(context.Background(), loader, gid)) {
			theirs[id] = true
		}
	}
	require.NotEmpty(t, theirs, "the version-group fixer writes something in this library, or the test proves nothing")
	for id := range ours {
		require.False(t, theirs[id], "book %s is written by both fixers", id)
	}
	var dump []string
	for _, r := range rows {
		dump = append(dump, r.RowID+"="+r.Class+"/"+r.Skipped+" "+r.SkipReason)
	}
	require.NotEmpty(t, ours, "merge-cleanup plans a write somewhere, or the test proves nothing; rows: %v", dump)
}

// ---- origin evidence ----

func TestMergeCleanupOriginEvidence(t *testing.T) {
	x := newMCFixture(t)
	// MATCH-4's own signature: the pointer, an explicit false flag, and the
	// metadata-source hash the pair shares.
	x.mk("m4-target", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), msh: "hash-1"})
	x.mk("m4-loser", mcSpec{title: "The Long Halloween", authors: []string{"Bruce Wayne"}, files: one(40000), msh: "hash-1"})
	require.NoError(t, x.st.FlagMetadataHashDuplicate(x.ids["m4-target"], x.ids["m4-loser"]))
	// The pointer and the flag but no shared hash.
	x.mk("part-target", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), msh: "hash-2"})
	x.mk("part-loser", mcSpec{title: "The Long Halloween", authors: []string{"Bruce Wayne"}, files: one(40000), msh: "hash-3"})
	x.point("part-loser", "part-target")
	// The pointer and the flag, and no metadata-source hash on either book: two
	// empty hashes are not a shared hash.
	x.mk("none-target", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)})
	x.mk("none-loser", mcSpec{title: "The Long Halloween", authors: []string{"Bruce Wayne"}, files: one(40000)})
	x.point("none-loser", "none-target")
	// A pointer without MATCH-4's flag: another writer left it.
	x.mk("oth-target", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)})
	x.mk("oth-loser", mcSpec{title: "The Long Halloween", authors: []string{"Bruce Wayne"}, files: one(40000)})
	x.pointRaw("oth-loser", "oth-target")

	rows := x.plan()
	r := wantRow(t, rows, "m4-loser", mcKindUnmerge, "")
	require.Contains(t, r.Flags, mcFlagOriginMatch4)
	require.Equal(t, "match4", r.Current["origin"])
	r = wantRow(t, rows, "part-loser", mcKindUnmerge, "")
	require.Contains(t, r.Flags, mcFlagOriginPartial)
	require.Equal(t, "match4_partial", r.Current["origin"])
	r = wantRow(t, rows, "none-loser", mcKindUnmerge, "")
	require.Equal(t, "match4_partial", r.Current["origin"], "no hash on either side is not a shared hash")
	r = wantRow(t, rows, "oth-loser", mcKindUnmerge, "")
	require.Contains(t, r.Flags, mcFlagOriginOther)
	require.Equal(t, "other", r.Current["origin"])
	require.NotContains(t, r.Flags, mcFlagOriginMatch4)
}

// ---- the census through the engine ----

func TestMergeCleanupCensusCounts(t *testing.T) {
	x := newMCFixture(t)
	x.wrongWork("w1", "w1t")
	x.wrongWork("w2", "w2t")
	x.fragment("f1", "f1t", "")
	x.mk("ok-t", mcSpec{title: "Year One", authors: []string{"Bruce Wayne"}, files: one(35000)})
	x.mk("ok-l", mcSpec{title: "Year One", authors: []string{"Bruce Wayne"}, files: one(36000)})
	x.point("ok-l", "ok-t")
	x.mk("d-dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	x.mk("d-loser", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)})
	x.point("d-loser", "d-dead")
	_, err := x.st.MintOrGetSyncID(x.ids["w2"])
	require.NoError(t, err)
	require.NoError(t, x.st.RecordSyncMerge(x.ids["w2"], x.ids["w2t"]))

	res, err := repairs.RunPlan(context.Background(), x.f, nil, repairs.PlanDeps{Guard: x.st}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, map[string]int{mcKindUnmerge: 3, mcKindSwap: 1, mcClassNoVerdict: 1}, res.ByClass,
		"w1, w2 and the dead-end loser unmerge; f1 swaps; ok-l is left alone")
	require.Equal(t, map[string]int{mcSkipMergeLooksCorrect: 1, mcSkipSyncRedirect: 1}, res.SkippedByKind)
	require.Equal(t, 1, res.ByFlag[mcFlagSyncRedirect])
	require.Equal(t, 5, len(res.Rows))
}

// ---- Replan ----

func TestMergeCleanupReplan(t *testing.T) {
	x := newMCFixture(t)
	x.wrongWork("loser", "target")
	planned := x.plan()["loser"]
	replan := func() repairs.Row {
		r, err := x.f.Replan(context.Background(), nil, planned, &fakeReporter{})
		require.NoError(t, err)
		return r
	}

	t.Run("unchanged state is the same fingerprint", func(t *testing.T) {
		r := replan()
		require.Equal(t, planned.Fingerprint, r.Fingerprint)
		require.Equal(t, planned.Class, r.Class)
		require.Equal(t, planned.Skipped, r.Skipped)
	})
	t.Run("a runtime that moved is a different fingerprint", func(t *testing.T) {
		files, err := x.st.GetBookFiles(x.ids["target"])
		require.NoError(t, err)
		f := files[0]
		d := 31000
		f.Duration = &d
		require.NoError(t, x.st.UpdateBookFile(f.ID, &f))
		require.NotEqual(t, planned.Fingerprint, replan().Fingerprint)
	})
	t.Run("a file row that changed without moving a displayed fact is a different fingerprint", func(t *testing.T) {
		before := replan().Fingerprint
		files, err := x.st.GetBookFiles(x.ids["loser"])
		require.NoError(t, err)
		f := files[0]
		f.FileHash = "changed-hash"
		require.NoError(t, x.st.UpdateBookFile(f.ID, &f))
		require.NotEqual(t, before, replan().Fingerprint)
	})
	t.Run("a cleared pointer is no longer merged", func(t *testing.T) {
		_, err := x.st.ModifyBook(x.ids["loser"], func(b *database.Book) error { b.MergedIntoBookID = nil; return nil })
		require.NoError(t, err)
		r := replan()
		require.Equal(t, mcSkipNoLongerMerged, r.Skipped)
		require.Equal(t, x.ids["loser"], r.RowID)
		require.NotEqual(t, planned.Fingerprint, r.Fingerprint)
	})
	t.Run("a trashed loser is gone", func(t *testing.T) {
		_, err := x.st.ModifyBook(x.ids["loser"], func(b *database.Book) error {
			b.MarkedForDeletion = retiredPtr(true)
			b.MarkedForDeletionAt = retiredPtr(time.Now())
			return nil
		})
		require.NoError(t, err)
		r := replan()
		require.Equal(t, mcSkipGone, r.Skipped)
		require.Equal(t, x.ids["loser"], r.RowID)
	})
}

// ---- PlanOnly, the lane, concurrency ----

func TestMergeCleanupIsPlanOnly(t *testing.T) {
	f := newMergeCleanupFixer(&Plugin{deps: fakeDeps{}})
	require.Equal(t, "maintenance.merge-cleanup", f.ID())
	require.True(t, f.PlanOnly())
	require.True(t, f.NoScanStandDown())
	require.Equal(t, repairs.GroupDuplicates, f.Meta().Group)
	require.Equal(t, 6, f.Meta().Rank)
	require.Error(t, f.Apply(context.Background(), nil, repairs.Row{RowID: "x"}), "the fixer has no apply path")
	var _ repairs.PlanOnly = f
	var _ repairs.NoScanStandDown = f
}

func TestMergeCleanupPlansInParallel(t *testing.T) {
	x := newMCFixture(t)
	const n = 40
	for i := 0; i < n; i++ {
		x.wrongWork("l"+strconv.Itoa(i), "t"+strconv.Itoa(i))
	}
	x.f.concurrency = 4
	rows := x.plan()
	require.Len(t, rows, n)
	for name, r := range rows {
		require.Equal(t, mcKindUnmerge, r.Class, name)
		require.Empty(t, r.Skipped, name)
	}
	// The same library planned one worker at a time gives the same rows.
	x.f.concurrency = 1
	serial := x.plan()
	for name, r := range rows {
		require.Equal(t, r.Fingerprint, serial[name].Fingerprint, name)
	}
}

// mcFakeVetoes is an OwnerVetoReader that answers from a table.
type mcFakeVetoes struct {
	pairs map[string]bool
	// copyPairs are pairs the owner rejected as the same copy (the dedup
	// family), keyed like pairs.
	copyPairs    map[string]bool
	unresolvable bool
	// copyUnresolvable makes only the same_copy question unresolvable.
	copyUnresolvable bool
	// err makes every question fail with an ordinary read error.
	err error
}

func (v *mcFakeVetoes) Load(context.Context) error                   { return nil }
func (v *mcFakeVetoes) SubjectRejected(_, _, _ string) (bool, error) { return false, nil }
func (v *mcFakeVetoes) Unresolved() []decisions.UnresolvedID         { return nil }
func (v *mcFakeVetoes) PairVetoed(_, a, b, family string) (bool, error) {
	if v.err != nil {
		return false, v.err
	}
	if v.unresolvable || (v.copyUnresolvable && family == decisions.FamilySameCopy) {
		return false, &decisions.UnresolvableError{What: "the pair asked about",
			IDs: []decisions.UnresolvedID{{EntityType: "book", ID: a, Err: errors.New("two holders")}}}
	}
	table := v.pairs
	switch family {
	case decisions.FamilySameWork:
	case decisions.FamilySameCopy:
		table = v.copyPairs
	default:
		return false, nil
	}
	return table[a+"|"+b] || table[b+"|"+a], nil
}

// ---- the frontend words every kind the fixer can report ----

// mcGoConsts returns the string value of every top-level constant in the
// merge-cleanup sources whose name starts with prefix, parsed from the source
// so a constant added later is covered without anyone listing it.
func mcGoConsts(t *testing.T, prefix string) []string {
	t.Helper()
	var out []string
	fset := token.NewFileSet()
	for _, file := range []string{"merge_cleanup_signals.go", "merge_cleanup_fixer.go"} {
		f, err := parser.ParseFile(fset, file, nil, 0)
		require.NoError(t, err)
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, sp := range gd.Specs {
				vs := sp.(*ast.ValueSpec)
				for i, n := range vs.Names {
					if !strings.HasPrefix(n.Name, prefix) || i >= len(vs.Values) {
						continue
					}
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok {
						v, err := strconv.Unquote(lit.Value)
						require.NoError(t, err)
						out = append(out, v)
					}
				}
			}
		}
	}
	require.NotEmpty(t, out, "no constants with prefix %s: the parser found nothing, so the test proves nothing", prefix)
	return out
}

// mcTSLabelKeys is the set of keys of the object literal assigned to name in a
// TypeScript source file (one `key: 'words'` or `'key': 'words'` per line).
func mcTSLabelKeys(t *testing.T, path, name string) map[string]bool {
	t.Helper()
	src, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(src)
	start := strings.Index(text, name+": Record<string, string> = {")
	require.GreaterOrEqual(t, start, 0, "%s not found in %s", name, path)
	body := text[start:]
	end := strings.Index(body, "\n};")
	require.Greater(t, end, 0)
	keys := map[string]bool{}
	for _, line := range strings.Split(body[:end], "\n") {
		line = strings.TrimSpace(line)
		if i := strings.Index(line, ":"); i > 0 && !strings.HasPrefix(line, "//") {
			keys[strings.Trim(line[:i], `'"`)] = true
		}
	}
	return keys
}

// Every skip kind, class and flag this fixer can emit has words in the Repairs
// panel. Without them the chip reads "swap_would_not_crown_keeper (3)".
func TestMergeCleanupFrontendWordsEveryKind(t *testing.T) {
	web := filepath.Join("..", "..", "..", "web", "src", "components", "review")
	skips := mcTSLabelKeys(t, filepath.Join(web, "RepairsPanel.tsx"), "SKIP_KIND_LABEL")
	classes := mcTSLabelKeys(t, filepath.Join(web, "repairs", "rowHelpers.ts"), "CLASS_LABELS")
	flags := mcTSLabelKeys(t, filepath.Join(web, "repairs", "rowHelpers.ts"), "FLAG_LABELS")
	for _, v := range mcGoConsts(t, "mcSkip") {
		require.True(t, skips[v], "skip kind %q has no SKIP_KIND_LABEL entry", v)
	}
	for _, v := range append(mcGoConsts(t, "mcKind"), mcGoConsts(t, "mcClass")...) {
		require.True(t, classes[v], "class %q has no CLASS_LABELS entry", v)
	}
	for _, v := range mcGoConsts(t, "mcFlag") {
		require.True(t, flags[v], "flag %q has no FLAG_LABELS entry", v)
	}
}

// mcWrittenBooks is the books a row's repair would write: the loser, and for a
// swap the target it retires. The version-group boundary is that no book is
// in this set for a merge-cleanup row and in the version-group fixer's set.
func mcWrittenBooks(r repairs.Row) []string {
	switch r.Class {
	case mcKindSwap:
		return []string{r.Proposed["keeper_book_id"], r.Proposed["retired_book_id"]}
	case mcKindUnmerge, mcKindRepoint:
		return []string{r.RowID}
	}
	return nil
}

// ---- tests that pin one rule each ----

// A reject filed as the same copy (the dedup family) says as much about a swap
// as one filed as the same work: both read the pair as joined.
func TestMergeCleanupSameCopyRejectHoldsASwap(t *testing.T) {
	x := newMCFixture(t)
	x.fragment("loser", "target", "")
	x.vetoes = &mcFakeVetoes{copyPairs: map[string]bool{x.ids["loser"] + "|" + x.ids["target"]: true}}
	x.rebuild()
	r := wantRow(t, x.plan(), "loser", mcKindSwap, mcSkipOwnerSaysDistinct)
	require.Equal(t, decisions.FamilySameCopy, r.Current["owner_veto_family"])
}

// A cycle unmerges one member, chosen by runtime, then file count, then age,
// then id. The store stamps CreatedAt itself (so age always agrees with id order
// in a fixture), which is why the order is a pure function and each rule is
// tested with the next rule pointing the other way.
func TestMergeCleanupCycleHeadTieBreaks(t *testing.T) {
	jan, mar := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		in   []mcCycleCand
		want string
	}{
		{"runtime beats files, age and id",
			[]mcCycleCand{{id: "a", secs: 5000, files: 3, created: jan, hasTime: true}, {id: "b", secs: 10000, files: 1, created: mar, hasTime: true}}, "b"},
		{"files beat age and id",
			[]mcCycleCand{{id: "a", secs: 10000, files: 1, created: jan, hasTime: true}, {id: "b", secs: 10000, files: 2, created: mar, hasTime: true}}, "b"},
		{"age beats id",
			[]mcCycleCand{{id: "a", secs: 10000, files: 1, created: mar, hasTime: true}, {id: "b", secs: 10000, files: 1, created: jan, hasTime: true}}, "b"},
		{"a stored time beats none",
			[]mcCycleCand{{id: "a", secs: 10000, files: 1}, {id: "b", secs: 10000, files: 1, created: mar, hasTime: true}}, "b"},
		{"the lowest id breaks a full tie",
			[]mcCycleCand{{id: "b", secs: 10000, files: 1}, {id: "a", secs: 10000, files: 1}, {id: "c", secs: 10000, files: 1}}, "a"},
	}
	for _, c := range cases {
		require.Equal(t, c.want, mcPickCycleHead(c.in), c.name)
		// The answer does not depend on the order the members are listed in.
		rev := slices.Clone(c.in)
		slices.Reverse(rev)
		require.Equal(t, c.want, mcPickCycleHead(rev), c.name+" (reversed)")
	}
	require.Equal(t, "", mcPickCycleHead(nil))
}

// The head of a real cycle, through the plan: the longer book is unmerged, the
// other waits.
func TestMergeCleanupCycleHeadThroughThePlan(t *testing.T) {
	x := newMCFixture(t)
	x.mk("short", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(5000)})
	x.mk("long", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(10000)})
	x.point("short", "long")
	x.point("long", "short")
	rows := x.plan()
	wantRow(t, rows, "long", mcKindUnmerge, "")
	wantRow(t, rows, "short", mcKindUnmerge, mcSkipCycleNotHead)
}

// The version-group fixer's write set is checked on the swap's target as well
// as the loser: here the loser has no group (so nothing of it is written by that
// fixer) but its target's group has no primary and the fixer would crown the
// target. The hold is that the other fixer owns the target, ahead of the
// cross-group hold the same row also carries.
func TestMergeCleanupVersionGroupHoldChecksTheTargetToo(t *testing.T) {
	x := newMCFixture(t)
	x.mk("target", mcSpec{title: "Year One", authors: []string{"Bruce Wayne"}, files: one(9000), group: "g-vt", primary: mcFalse()})
	x.mk("other", mcSpec{title: "Year One", authors: []string{"Bruce Wayne"}, files: one(9000), group: "g-vt", primary: mcFalse()})
	x.setChapters("target", 20)
	x.mk("loser", mcSpec{title: "Year One", authors: []string{"Bruce Wayne"}, files: one(36000)})
	x.point("loser", "target")
	r := wantRow(t, x.plan(), "loser", mcKindSwap, mcSkipHandledByVG)
	var kinds []string
	for _, h := range r.Holds {
		kinds = append(kinds, h.Kind)
	}
	require.Equal(t, []string{mcSkipHandledByVG, mcSkipCrossGroup}, kinds)
}

// A sync redirect is a fact about a row with a verdict to hold. A row that
// proposes nothing already is not applicable, so the redirect is flagged (the
// census counts it) but is not a second hold on it.
func TestMergeCleanupSyncRedirectOnARowWithNoVerdictIsOnlyAFlag(t *testing.T) {
	x := newMCFixture(t)
	x.mk("target", mcSpec{title: "Year One", authors: []string{"Bruce Wayne"}, files: one(35000)})
	x.mk("loser", mcSpec{title: "Year One", authors: []string{"Bruce Wayne"}, files: one(36000)})
	x.point("loser", "target")
	_, err := x.st.MintOrGetSyncID(x.ids["loser"])
	require.NoError(t, err)
	require.NoError(t, x.st.RecordSyncMerge(x.ids["loser"], x.ids["target"]))
	r := wantRow(t, x.plan(), "loser", mcClassNoVerdict, mcSkipMergeLooksCorrect)
	require.Contains(t, r.Flags, mcFlagSyncRedirect)
	require.Len(t, r.Holds, 1, "the redirect is not a hold on a row that proposes nothing")
}

// The census counts every hold of a row, not only the first: a row held for
// its group and for a redirect is one first-skip kind and two holds.
func TestMergeCleanupCensusCountsEveryHold(t *testing.T) {
	x := newMCFixture(t)
	x.mk("target", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)})
	x.mk("loser", mcSpec{title: "The Long Halloween", authors: []string{"Bruce Wayne"}, files: one(40000), group: "own"})
	x.point("loser", "target")
	_, err := x.st.MintOrGetSyncID(x.ids["loser"])
	require.NoError(t, err)
	require.NoError(t, x.st.RecordSyncMerge(x.ids["loser"], x.ids["target"]))
	rows, census, err := x.f.plan(context.Background(), nil, &fakeReporter{})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, map[string]int{mcSkipGroupNeedsHandoff: 1, mcSkipSyncRedirect: 1}, census.HoldsByKind)
	require.Equal(t, 1, census.Merged)
}

// A dead end's candidate must be a live book that is not itself merged away:
// repointing at a book that points on would only lengthen the chain.
func TestMergeCleanupDeadEndCandidateThatIsMergedIsNotACandidate(t *testing.T) {
	x := newMCFixture(t)
	x.mk("z", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)})
	x.mk("heir", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g-p", primary: mcTrue()})
	x.mk("dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g-p", primary: mcFalse(), trashed: true})
	x.point("heir", "z")
	x.mk("loser", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)})
	x.point("loser", "dead")
	r := wantRow(t, x.plan(), "loser", mcKindUnmerge, "")
	require.Equal(t, "dead", r.Current["chain_end"])
	require.Empty(t, r.Proposed["merged_into_book_id"])
}

// ---- failures are loud ----

// mcNoVetoDeps is a deps whose owner-reject reader is missing.
type mcNoVetoDeps struct{ fakeDeps }

func (mcNoVetoDeps) OwnerVetoes() OwnerVetoReader { return nil }

// mcFailFilesStore fails the file read of chosen books and shows its inner
// store's capabilities through Unwrap, as a real decorator does.
type mcFailFilesStore struct {
	database.Store
	fail map[string]bool
}

func (s mcFailFilesStore) Unwrap() database.Store { return s.Store }
func (s mcFailFilesStore) GetBookFiles(id string) ([]database.BookFile, error) {
	if s.fail[id] {
		return nil, errors.New("synthetic read failure")
	}
	return s.Store.GetBookFiles(id)
}

// mcBareStore hides every capability of the inner store: it has no Unwrap.
type mcBareStore struct{ database.Store }

func TestMergeCleanupRefusesToPlanWithoutTheOwnersRejects(t *testing.T) {
	x := newMCFixture(t)
	x.wrongWork("loser", "target")
	f := newMergeCleanupFixer(&Plugin{deps: mcNoVetoDeps{fakeDeps{store: x.st, root: x.root}}})
	_, err := f.Plan(context.Background(), nil, &fakeReporter{})
	require.ErrorContains(t, err, "owner's rejects are unavailable")
}

func TestMergeCleanupPlanFailsWithoutARetiredRowReader(t *testing.T) {
	x := newMCFixture(t)
	x.wrongWork("loser", "target")
	f := newMergeCleanupFixer(&Plugin{deps: fakeDeps{store: mcBareStore{x.st}, root: x.root, vetoes: &mcFakeVetoes{}}})
	_, err := f.Plan(context.Background(), nil, &fakeReporter{})
	require.ErrorIs(t, err, database.ErrRetiredReaderUnavailable, "every row needs the retired-shell check; no row is guessed")
}

func TestMergeCleanupAReadFailureIsAnErrorRowNotAMissingRow(t *testing.T) {
	x := newMCFixture(t)
	x.wrongWork("bad", "bad-t")
	x.wrongWork("good", "good-t")
	x.f = newMergeCleanupFixer(&Plugin{deps: fakeDeps{store: mcFailFilesStore{Store: x.st, fail: map[string]bool{x.ids["bad"]: true}}, root: x.root}})
	x.f.vgProbe = func(context.Context, string) (int, error) { return 1, nil }
	rows := x.plan()
	r := wantRow(t, rows, "bad", mcClassError, mcSkipError)
	require.Contains(t, r.SkipReason, "synthetic read failure")
	require.Equal(t, "Hush"[:0]+r.Title, r.Title)
	require.NotEmpty(t, r.Title, "an error row still names the book")
	wantRow(t, rows, "good", mcKindUnmerge, "")
}

// ---- the plan writes nothing, and runs concurrently over shared state ----

// mcSnapshot is every book and book_file row, as stored.
func mcSnapshot(t *testing.T, st database.Store) string {
	t.Helper()
	cores, err := st.GetAllBooksCore(0, 0)
	require.NoError(t, err)
	ids := make([]string, 0, len(cores))
	for i := range cores {
		ids = append(ids, cores[i].ID)
	}
	slices.Sort(ids)
	var sb strings.Builder
	for _, id := range ids {
		b, err := st.GetBookByID(id)
		require.NoError(t, err)
		files, err := st.GetBookFiles(id)
		require.NoError(t, err)
		bj, err := json.Marshal(b)
		require.NoError(t, err)
		fj, err := json.Marshal(files)
		require.NoError(t, err)
		sb.Write(bj)
		sb.Write(fj)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// shapes makes one of every shape the plan has a path for.
func (x *mcFixture) shapes() {
	x.wrongWork("w-loser", "w-target")
	x.fragment("f-loser", "f-target", "g-f")
	x.fragment("fu-loser", "fu-target", "")
	x.mk("d-dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, group: "g-d", primary: mcFalse(), trashed: true})
	x.mk("d-heir", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g-d", primary: mcTrue()})
	x.mk("d-loser", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)})
	x.point("d-loser", "d-dead")
	x.mk("c-a", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)})
	x.mk("c-b", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(31000)})
	x.point("c-a", "c-b")
	x.point("c-b", "c-a")
	x.mk("g-t", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g-own"})
	x.mk("g-l", mcSpec{title: "The Long Halloween", authors: []string{"Bruce Wayne"}, files: one(40000), group: "g-own"})
	x.point("g-l", "g-t")
	_, err := x.st.MintOrGetSyncID(x.ids["w-loser"])
	require.NoError(x.t, err)
	require.NoError(x.t, x.st.RecordSyncMerge(x.ids["w-loser"], x.ids["w-target"]))
}

func TestMergeCleanupPlanWritesNothing(t *testing.T) {
	x := newMCFixture(t)
	x.shapes()
	before := mcSnapshot(t, x.st)
	rows := x.plan()
	require.Len(t, rows, 7, "wrong work, a grouped and a group-less swap, a dead end, a two-book cycle and a group-leaving unmerge")
	for _, r := range rows {
		_, err := x.f.Replan(context.Background(), nil, r, &fakeReporter{})
		require.NoError(t, err)
	}
	require.Equal(t, before, mcSnapshot(t, x.st), "a plan and a replan of every row change no book and no file row")
}

// Many rows share a version group: the group read, the election signals, the
// version-group classifier and the path resolver are all hit by several
// workers at once. Run under -race, and compared with a one-worker plan.
func TestMergeCleanupPlansGroupedRowsInParallel(t *testing.T) {
	x := newMCFixture(t)
	const groups = 10
	for g := 0; g < groups; g++ {
		gid, id := "g"+strconv.Itoa(g), strconv.Itoa(g)
		x.mk("gt-"+id, mcSpec{title: "Year One", authors: []string{"Bruce Wayne"}, files: one(9000), group: gid, primary: mcTrue()})
		for i := 0; i < 3; i++ {
			n := "gl-" + id + "-" + strconv.Itoa(i)
			x.mk(n, mcSpec{title: "Year One", authors: []string{"Bruce Wayne"}, files: one(36000), group: gid})
			x.point(n, "gt-"+id)
		}
		x.mk("gu-"+id, mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: gid})
		x.point("gu-"+id, "gt-"+id)
	}
	// The probe runs once per grouped single-file member, inside the workers:
	// count how many are in it at once, so "Concurrency is set" is a measured
	// fact and not a field that is read.
	var inflight, peak atomic.Int32
	base := x.f.vgProbe
	x.f.vgProbe = func(ctx context.Context, path string) (int, error) {
		n := inflight.Add(1)
		defer inflight.Add(-1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(2 * time.Millisecond)
		return base(ctx, path)
	}
	x.f.concurrency = 8
	par := x.plan()
	require.Len(t, par, groups*4)
	require.GreaterOrEqual(t, peak.Load(), int32(2), "the plan ran more than one worker at a time")
	peak.Store(0)
	x.f.concurrency = 1
	ser := x.plan()
	require.EqualValues(t, 1, peak.Load(), "one worker means one probe at a time")
	require.Len(t, ser, len(par))
	for name, r := range par {
		require.Equal(t, ser[name].Class, r.Class, name)
		require.Equal(t, ser[name].Skipped, r.Skipped, name)
		require.Equal(t, ser[name].Fingerprint, r.Fingerprint, name)
	}
}

// ---- rules the mutant pass found uncovered ----

// mcShadowStore changes what one book read returns, as a book changing between
// the listing and its read would.
type mcShadowStore struct {
	database.Store
	over map[string]func(*database.Book) *database.Book
}

func (s mcShadowStore) Unwrap() database.Store { return s.Store }
func (s mcShadowStore) GetBookByID(id string) (*database.Book, error) {
	b, err := s.Store.GetBookByID(id)
	if f, ok := s.over[id]; ok && err == nil {
		return f(b), nil
	}
	return b, err
}

// mcGroupReadStore fails the failAt-th read (1-based; 0 never) of one group's
// members and counts the reads of it.
type mcGroupReadStore struct {
	database.Store
	gid    string
	failAt int
	mu     sync.Mutex
	calls  int
}

func (s *mcGroupReadStore) Unwrap() database.Store { return s.Store }
func (s *mcGroupReadStore) GetBooksByVersionGroup(gid string) ([]database.Book, error) {
	if gid == s.gid {
		s.mu.Lock()
		s.calls++
		n := s.calls
		s.mu.Unlock()
		if n == s.failAt {
			return nil, errors.New("synthetic group read failure")
		}
	}
	return s.Store.GetBooksByVersionGroup(gid)
}

// A book soft-deleted after being merged never reaches the classifier, so the
// census does not count it as an exclusion; one that vanishes or loses its
// pointer between the listing and its read is counted as that.
func TestMergeCleanupPopulationEdges(t *testing.T) {
	t.Run("a soft-deleted merged book is filtered out of the listing", func(t *testing.T) {
		x := newMCFixture(t)
		x.wrongWork("loser", "target")
		x.mk("sd-target", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)})
		x.mk("sd", mcSpec{title: "Knightfall", authors: []string{"Bruce Wayne"}, files: one(40000)})
		x.point("sd", "sd-target")
		_, err := x.st.ModifyBook(x.ids["sd"], func(b *database.Book) error {
			b.MarkedForDeletion = retiredPtr(true)
			b.MarkedForDeletionAt = retiredPtr(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
			return nil
		})
		require.NoError(t, err)
		got, err := x.st.GetBookByID(x.ids["sd"])
		require.NoError(t, err)
		require.True(t, got.IsSoftDeleted(), "premise")
		require.NotNil(t, got.MergedIntoBookID, "premise: it is still merged")
		rows, census, err := x.f.plan(context.Background(), nil, &fakeReporter{})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, 1, census.Merged, "only the live merged book is listed")
		require.Equal(t, 0, census.ExcludedTrashed)
	})
	t.Run("a book that vanishes or is unmerged after the listing is counted as such", func(t *testing.T) {
		x := newMCFixture(t)
		x.wrongWork("keep", "keep-t")
		x.wrongWork("vanish", "vanish-t")
		x.wrongWork("unmerged", "unmerged-t")
		shadow := mcShadowStore{Store: x.st, over: map[string]func(*database.Book) *database.Book{
			x.ids["vanish"]:   func(*database.Book) *database.Book { return nil },
			x.ids["unmerged"]: func(b *database.Book) *database.Book { c := *b; c.MergedIntoBookID = nil; return &c },
		}}
		f := newMergeCleanupFixer(&Plugin{deps: fakeDeps{store: shadow, root: x.root, vetoes: x.vetoes}})
		f.vgProbe = x.f.vgProbe
		rows, census, err := f.plan(context.Background(), nil, &fakeReporter{})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, 3, census.Merged)
		require.Equal(t, 1, census.ExcludedMissing)
		require.Equal(t, 1, census.ExcludedNotMerged)
	})
	t.Run("a replanned book that is gone is a skipped row, not an error", func(t *testing.T) {
		x := newMCFixture(t)
		x.wrongWork("loser", "target")
		r, err := x.f.Replan(context.Background(), nil, repairs.Row{RowID: "01HZZZZZZZZZZZZZZZZZZZZZZZ"}, &fakeReporter{})
		require.NoError(t, err)
		require.Equal(t, mcSkipGone, r.Skipped)
	})
}

// The origin note says MATCH-4 left the pointer only when the explicit FALSE
// primary flag is there; an explicit true is another writer's.
func TestMergeCleanupOriginOfAPointerOnAPrimaryBook(t *testing.T) {
	x := newMCFixture(t)
	x.mk("target", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), msh: "hash-1"})
	x.mk("loser", mcSpec{title: "The Long Halloween", authors: []string{"Bruce Wayne"}, files: one(40000), primary: mcTrue(), msh: "hash-1"})
	x.pointRaw("loser", "target")
	r := wantRow(t, x.plan(), "loser", mcKindUnmerge, "")
	require.Equal(t, "other", r.Current["origin"])
	require.Contains(t, r.Flags, mcFlagOriginOther)
}

// Only a live book with files can be the head of a cycle.
func TestMergeCleanupCycleHeadIsALiveBookWithFiles(t *testing.T) {
	t.Run("a trashed member is never the head", func(t *testing.T) {
		x := newMCFixture(t)
		x.mk("live", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(5000)})
		x.mk("dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(10000)})
		x.point("live", "dead")
		x.point("dead", "live")
		_, err := x.st.ModifyBook(x.ids["dead"], func(b *database.Book) error {
			b.MarkedForDeletion = retiredPtr(true)
			b.MarkedForDeletionAt = retiredPtr(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
			return nil
		})
		require.NoError(t, err)
		got, err := x.st.GetBookByID(x.ids["dead"])
		require.NoError(t, err)
		require.True(t, got.IsSoftDeleted() && got.MergedIntoBookID != nil, "premise: trashed and still pointing")
		r := wantRow(t, x.plan(), "live", mcKindUnmerge, "")
		require.Equal(t, x.ids["live"], r.Current["cycle_head"], "the longer book is trashed: it cannot be unmerged")
	})
	t.Run("a cycle of books with no files has no head and the row is an error row", func(t *testing.T) {
		x := newMCFixture(t)
		x.mk("a", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}})
		x.mk("b", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}})
		x.point("a", "b")
		x.point("b", "a")
		x.mk("loser", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)})
		x.point("loser", "a")
		r := wantRow(t, x.plan(), "loser", mcClassError, mcSkipError)
		require.Contains(t, r.SkipReason, "no live book that has files")
	})
}

// A dead end's candidate holder of retired versions must itself be a live book
// with no pointer.
func TestMergeCleanupDeadEndHolderMustBeLiveAndUnmerged(t *testing.T) {
	x := newMCFixture(t)
	x.mk("z", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)})
	// The retired rows went to a holder that is itself merged away.
	x.mk("p-dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	x.mk("p-holder", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)})
	x.point("p-holder", "z")
	retireRowOnto(t, x.st, x.ids["p-dead"], x.ids["p-holder"], "rp", filepath.Join(x.root, "retired", "p.m4b"))
	x.mk("p-loser", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)})
	x.point("p-loser", "p-dead")
	// ... or to a holder that is in the trash.
	x.mk("t-dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	x.mk("t-holder", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)})
	retireRowOnto(t, x.st, x.ids["t-dead"], x.ids["t-holder"], "rt", filepath.Join(x.root, "retired", "t.m4b"))
	_, err := x.st.ModifyBook(x.ids["t-holder"], func(b *database.Book) error {
		b.MarkedForDeletion = retiredPtr(true)
		b.MarkedForDeletionAt = retiredPtr(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
		return nil
	})
	require.NoError(t, err)
	x.mk("t-loser", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)})
	x.point("t-loser", "t-dead")

	rows := x.plan()
	for _, n := range []string{"p-loser", "t-loser"} {
		r := wantRow(t, rows, n, mcKindUnmerge, "")
		require.Equal(t, "dead", r.Current["chain_end"], n)
		require.Empty(t, r.Proposed["merged_into_book_id"], "%s: a book that points on or sits in the trash is no place to repoint to", n)
	}
}

// A version-group hold on the swap's target is a hold on a swap only: an
// unmerge writes the loser alone.
func TestMergeCleanupVersionGroupHoldOnTheTargetDoesNotHoldAnUnmerge(t *testing.T) {
	x := newMCFixture(t)
	x.mk("target", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(9000), group: "g-vt", primary: mcFalse()})
	x.mk("other", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(9000), group: "g-vt", primary: mcFalse()})
	x.setChapters("target", 20)
	x.mk("loser", mcSpec{title: "The Long Halloween", authors: []string{"Bruce Wayne"}, files: one(36000)})
	x.point("loser", "target")
	run, err := x.f.newRun(context.Background(), &fakeReporter{})
	require.NoError(t, err)
	g, err := run.vgReport(context.Background(), "g-vt")
	require.NoError(t, err)
	require.Contains(t, vgWrittenBooks(g), x.ids["target"], "premise: the version-group fixer would write the target")
	wantRow(t, x.plan(), "loser", mcKindUnmerge, "")
}

// The hold names the loser's own id: the version-group fixer writing other
// books of the loser's group is no reason to hold this row.
func TestMergeCleanupVersionGroupHoldNeedsTheRowsOwnBook(t *testing.T) {
	x := newMCFixture(t)
	x.mk("p1", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g-vp"})
	x.mk("p2", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g-vp"})
	x.setChapters("p1", 20)
	x.setChapters("p2", 20)
	x.wrongWork("loser", "target")
	_, err := x.st.ModifyBook(x.ids["loser"], func(b *database.Book) error {
		b.VersionGroupID = retiredPtr("g-vp")
		return nil
	})
	require.NoError(t, err)
	run, err := x.f.newRun(context.Background(), &fakeReporter{})
	require.NoError(t, err)
	g, err := run.vgReport(context.Background(), "g-vp")
	require.NoError(t, err)
	written := vgWrittenBooks(g)
	require.NotEmpty(t, written, "premise: the version-group fixer writes something in this group")
	require.NotContains(t, written, x.ids["loser"], "premise: but not the loser")
	r := x.plan()["loser"]
	for _, h := range r.Holds {
		require.NotEqual(t, mcSkipHandledByVG, h.Kind, "the loser is not written by the other fixer: %+v", r.Holds)
	}
}

// A group whose read fails is not guessed at: the version-group classifier's
// failure is an error row, and the unmerge gate's own read failing is a hold.
func TestMergeCleanupGroupReadFailures(t *testing.T) {
	build := func(t *testing.T, failAt int) (*mcFixture, *mcGroupReadStore) {
		x := newMCFixture(t)
		x.mk("target", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)})
		x.mk("keeper", mcSpec{title: "The Long Halloween", authors: []string{"Bruce Wayne"}, files: one(40000), group: "own", primary: mcTrue()})
		x.mk("loser", mcSpec{title: "The Long Halloween", authors: []string{"Bruce Wayne"}, files: one(40000), group: "own"})
		x.point("loser", "target")
		st := &mcGroupReadStore{Store: x.st, gid: "own", failAt: failAt}
		probe := x.f.vgProbe
		x.f = newMergeCleanupFixer(&Plugin{deps: fakeDeps{store: st, root: x.root, vetoes: x.vetoes}})
		x.f.vgProbe = probe
		return x, st
	}
	x, st := build(t, 0)
	wantRow(t, x.plan(), "loser", mcKindUnmerge, "")
	total := st.calls
	require.GreaterOrEqual(t, total, 2, "premise: the classifier and the unmerge gate each read the group")

	t.Run("the classifier's read failing is an error row", func(t *testing.T) {
		x, _ := build(t, 1)
		r := wantRow(t, x.plan(), "loser", mcClassError, mcSkipError)
		require.Contains(t, r.SkipReason, "synthetic group read failure")
	})
	t.Run("the unmerge gate's read failing holds the row for a hand-off", func(t *testing.T) {
		x, _ := build(t, total)
		r := wantRow(t, x.plan(), "loser", mcKindUnmerge, mcSkipGroupNeedsHandoff)
		require.Contains(t, r.Flags, mcFlagGroupNeedsHandoff)
		require.Contains(t, r.SkipReason, "could not be read")
	})
}

// A dead hop in the loser's own group means the unmerge takes it out of that
// group, so the group is not left without a primary by it.
func TestMergeCleanupUnmergeLeavesTheGroupOfADeadHop(t *testing.T) {
	x := newMCFixture(t)
	x.mk("dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, group: "g-x", primary: mcFalse(), trashed: true})
	x.mk("loser", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g-x"})
	x.point("loser", "dead")
	// The version-group fixer owns this group (it would elect in it), so the row
	// carries that hold; what is under test is the unmerge gate's own reading.
	r := x.plan()["loser"]
	require.Equal(t, mcKindUnmerge, r.Class, "%+v", r.Holds)
	require.Equal(t, "leaves_group", r.Current["group_effect"])
	require.NotContains(t, r.Flags, mcFlagGroupNeedsHandoff)
	for _, h := range r.Holds {
		require.NotEqual(t, mcSkipGroupNeedsHandoff, h.Kind)
	}
}

// ---- the owner's rejects, each way they can answer ----

func TestMergeCleanupOwnerRejectAnswers(t *testing.T) {
	t.Run("an unresolvable reject is a note on an unmerge, not a hold", func(t *testing.T) {
		x := newMCFixture(t)
		x.wrongWork("loser", "target")
		x.vetoes = &mcFakeVetoes{unresolvable: true}
		x.rebuild()
		r := wantRow(t, x.plan(), "loser", mcKindUnmerge, "")
		require.NotEmpty(t, r.Current["owner_veto_unresolved_ids"])
	})
	t.Run("a resolved reject settles the pair: a later unresolvable question adds no hold", func(t *testing.T) {
		x := newMCFixture(t)
		x.fragment("loser", "target", "")
		x.vetoes = &mcFakeVetoes{pairs: map[string]bool{x.ids["loser"] + "|" + x.ids["target"]: true}, copyUnresolvable: true}
		x.rebuild()
		r := wantRow(t, x.plan(), "loser", mcKindSwap, mcSkipOwnerSaysDistinct)
		require.Len(t, r.Holds, 1, "%+v", r.Holds)
		require.Empty(t, r.Current["owner_veto_unresolved_ids"])
	})
	t.Run("a reject that cannot be read is an error row, never a missing reject", func(t *testing.T) {
		x := newMCFixture(t)
		x.wrongWork("loser", "target")
		x.vetoes = &mcFakeVetoes{err: errors.New("synthetic reject read failure")}
		x.rebuild()
		r := wantRow(t, x.plan(), "loser", mcClassError, mcSkipError)
		require.Contains(t, r.SkipReason, "synthetic reject read failure")
	})
}
