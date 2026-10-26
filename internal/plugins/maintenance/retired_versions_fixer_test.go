// file: internal/plugins/maintenance/retired_versions_fixer_test.go
// version: 1.3.0
// guid: 8fc253d1-c085-4eca-a2f9-52cf09ca04b0
// last-edited: 2026-10-26

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// retiredFixture is a synthetic library: live books, trashed books and the
// rows between them. Every title and path is invented; nothing here is a real
// library row.
type retiredFixture struct {
	t    *testing.T
	st   *database.PebbleStore
	root string
	now  time.Time
	ids  map[string]string // fixture name -> book id
	f    *retiredVersionsFixer
}

func newRetiredFixture(t *testing.T) *retiredFixture {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	// The complete ownership lookup is served from memdb.
	st.WaitForWarmup()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	rf := &retiredFixture{t: t, st: st, root: root, ids: map[string]string{},
		now: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	rf.f = newRetiredVersionsFixer(&Plugin{deps: fakeDeps{store: st}})
	rf.f.now = func() time.Time { return rf.now }
	return rf
}

func retiredPtr[T any](v T) *T { return &v }

// live creates a live book; a non-empty group makes it that group's primary.
func (rf *retiredFixture) live(name, group string, rows ...string) string {
	rf.t.Helper()
	b := &database.Book{Title: name, FilePath: "/lib/" + name}
	if group != "" {
		b.VersionGroupID = retiredPtr(group)
		b.IsPrimaryVersion = retiredPtr(true)
	}
	return rf.add(name, b, rows...)
}

// liveNoFlag creates a live group member whose primary flag is unset.
func (rf *retiredFixture) liveNoFlag(name, group string) string {
	rf.t.Helper()
	return rf.add(name, &database.Book{Title: name, FilePath: "/lib/" + name, VersionGroupID: retiredPtr(group)})
}

// trashed creates a book soft-deleted daysAgo days before the fixture's now.
func (rf *retiredFixture) trashed(name, group string, daysAgo int, rows ...string) string {
	rf.t.Helper()
	b := &database.Book{Title: name, FilePath: "/lib/" + name, MarkedForDeletion: retiredPtr(true),
		MarkedForDeletionAt: retiredPtr(rf.now.Add(-time.Duration(daysAgo) * 24 * time.Hour))}
	if group != "" {
		b.VersionGroupID = retiredPtr(group)
	}
	return rf.add(name, b, rows...)
}

func (rf *retiredFixture) add(name string, b *database.Book, rows ...string) string {
	rf.t.Helper()
	created, err := rf.st.CreateBook(b)
	require.NoError(rf.t, err)
	rf.ids[name] = created.ID
	for _, p := range rows {
		require.NoError(rf.t, rf.st.CreateBookFile(&database.BookFile{BookID: created.ID, FilePath: p, Format: "mp3"}))
	}
	return created.ID
}

// file writes a real file under the fixture root and returns its path.
func (rf *retiredFixture) file(rel string) string {
	rf.t.Helper()
	p := filepath.Join(rf.root, rel)
	require.NoError(rf.t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(rf.t, os.WriteFile(p, []byte("synthetic "+rel), 0o644))
	return p
}

// absent is a path under the root that does not exist.
func (rf *retiredFixture) absent(rel string) string { return filepath.Join(rf.root, "gone", rel) }

func (rf *retiredFixture) plan() map[string]repairs.Row {
	rf.t.Helper()
	rows, err := rf.f.Plan(context.Background(), nil, &fakeReporter{})
	require.NoError(rf.t, err)
	out := map[string]repairs.Row{}
	for _, r := range rows {
		out[r.RowID] = r
	}
	return out
}

// mergedInto sets a fixture book's MergedIntoBookID to another fixture book.
func (rf *retiredFixture) mergedInto(name, target string) {
	rf.t.Helper()
	b, err := rf.st.GetBookByID(rf.ids[name])
	require.NoError(rf.t, err)
	b.MergedIntoBookID = retiredPtr(rf.ids[target])
	_, err = rf.st.UpdateBook(b.ID, b)
	require.NoError(rf.t, err)
}

// seedEveryCategory builds one trashed book per category and returns the
// number of trashed books.
func (rf *retiredFixture) seedEveryCategory() int {
	t := rf.t
	// Group g1: a live primary that holds one file, plus its trashed losers.
	shared := rf.file("g1/shared.mp3")
	rf.live("g1-primary", "g1", shared)
	rf.trashed("would-retire", "g1", 30, rf.file("g1/own.mp3"))
	rf.trashed("same-path", "g1", 30, shared)
	rf.trashed("itunes", "g1", 30, "/lib/books/itunes/Author/itunes-only.m4b")
	rf.trashed("too-recent", "g1", 3, rf.file("g1/recent.mp3"))
	rf.trashed("no-files", "g1", 30)
	deleted := rf.trashed("trash-labelled", "g1", 30, rf.file("g1/deleted.mp3"))
	b, err := rf.st.GetBookByID(deleted)
	require.NoError(t, err)
	b.LibraryState = retiredPtr("deleted")
	_, err = rf.st.UpdateBook(deleted, b)
	require.NoError(t, err)
	// A trashed row with no soft-delete time: the shape older builds left. The
	// store stamps every write that trashes a row (enforceTrashStamp), and a
	// plain UpdateBook that clears the stamp gets the stored one written back,
	// so only the legacy seed can leave the row unstamped.
	unknownAge := rf.trashed("age-unknown", "g1", 30, rf.file("g1/unknown.mp3"))
	_, err = rf.st.SeedLegacyBookRowForTest(unknownAge, func(b *database.Book) error {
		b.MarkedForDeletionAt = nil
		return nil
	})
	require.NoError(t, err)
	// Listening state.
	listened := rf.trashed("listening", "g1", 30, rf.file("g1/listened.mp3"))
	u, err := rf.st.CreateUser("reader", "reader@example.com", "argon2id", "x", []string{"user"}, "active")
	require.NoError(t, err)
	require.NoError(t, rf.st.SetUserBookState(&database.UserBookState{UserID: u.ID, BookID: listened,
		Status: database.UserBookStatusInProgress, ProgressPct: 12}))

	// A shell in the live group g1 whose merged-into chain ends at a live book:
	// the chain is the target (it wins over the group primary).
	rf.trashed("merged-into", "g1", 30, rf.file("merged/own.mp3"))
	rf.mergedInto("merged-into", "g1-primary")
	// A merged-into chain does NOT make a target for a shell without a live
	// version-group member (the owner's answer): no group, and a dead group.
	rf.trashed("merged-no-group", "", 30, rf.file("merged/nogroup.mp3"))
	rf.mergedInto("merged-no-group", "g1-primary")
	rf.trashed("merged-dead-group", "g2", 30, rf.file("merged/deadgroup.mp3"))
	rf.mergedInto("merged-dead-group", "g1-primary")

	// Group g2 has no live member; no group at all. Both lose their target.
	rf.trashed("dead-group-present", "g2", 30, rf.file("g2/present.mp3"))
	rf.trashed("dead-group-missing", "g2", 30, rf.absent("g2/a.mp3"), rf.absent("g2/b.mp3"))
	rf.trashed("no-group-present", "", 30, rf.file("nogroup/present.mp3"), rf.absent("nogroup/other.mp3"))
	rf.trashed("no-group-missing", "", 30, rf.absent("nogroup/missing.mp3"))

	// Group g3: two live members, neither flagged primary.
	rf.liveNoFlag("g3-a", "g3")
	rf.liveNoFlag("g3-b", "g3")
	rf.trashed("ambiguous", "g3", 30, rf.file("g3/own.mp3"))

	// Group g4: the live primary has a file under the frozen iTunes tree, so
	// the iTunes gate holds a would-retire row (the TARGET side of the check).
	rf.live("g4-primary", "g4", "/lib/books/itunes/Author/g4-primary.m4b")
	rf.trashed("target-in-itunes", "g4", 30, rf.file("g4/own.mp3"))
	return 17
}

func TestRetiredVersions_EveryCategory(t *testing.T) {
	rf := newRetiredFixture(t)
	n := rf.seedEveryCategory()
	rows := rf.plan()
	require.Len(t, rows, n, "one row per trashed book, none for a live one")

	cases := []struct {
		name, class, skipped, method string
		target                       string // fixture name; "" = no target
	}{
		{"would-retire", retCatWouldRetire, "", retMethodGroupPrimary, "g1-primary"},
		{"same-path", retCatSamePath, "", retMethodGroupPrimary, "g1-primary"},
		{"merged-into", retCatWouldRetire, "", retMethodMergedInto, "g1-primary"},
		{"target-in-itunes", retCatITunes, retCatITunes, retMethodGroupPrimary, "g4-primary"},
		{"itunes", retCatITunes, retCatITunes, retMethodGroupPrimary, "g1-primary"},
		{"too-recent", retCatTooRecent, retCatTooRecent, retMethodGroupPrimary, "g1-primary"},
		{"age-unknown", retCatAgeUnknown, retCatAgeUnknown, retMethodGroupPrimary, "g1-primary"},
		{"listening", retCatListening, retCatListening, retMethodGroupPrimary, "g1-primary"},
		{"trash-labelled", retCatTrashLabelled, retCatTrashLabelled, retMethodGroupPrimary, "g1-primary"},
		{"no-files", retCatNoFiles, retCatNoFiles, retMethodGroupPrimary, "g1-primary"},
		{"dead-group-present", retCatNoTargetPresent, retCatNoTargetPresent, "", ""},
		{"dead-group-missing", retCatNoTargetMissing, retCatNoTargetMissing, "", ""},
		{"no-group-present", retCatNoTargetPresent, retCatNoTargetPresent, "", ""},
		{"no-group-missing", retCatNoTargetMissing, retCatNoTargetMissing, "", ""},
		{"ambiguous", retCatAmbiguous, retCatAmbiguous, "", ""},
		{"merged-no-group", retCatNoTargetPresent, retCatNoTargetPresent, "", ""},
		{"merged-dead-group", retCatNoTargetPresent, retCatNoTargetPresent, "", ""},
	}
	for _, c := range cases {
		r, ok := rows[rf.ids[c.name]]
		require.True(t, ok, "%s: no row", c.name)
		require.Equal(t, c.class, r.Class, "%s: class (%s)", c.name, r.Reason)
		require.Equal(t, c.skipped, r.Skipped, "%s: skipped", c.name)
		require.Equal(t, c.method, r.Current["target_method"], "%s: target method", c.name)
		want := ""
		if c.target != "" {
			want = rf.ids[c.target]
		}
		require.Equal(t, want, r.Current["target_book_id"], "%s: target", c.name)
		require.Equal(t, r.Skipped == "", r.Applicable(), "%s", c.name)
		require.NotEmpty(t, r.Fingerprint, c.name)
		if want != "" {
			require.Equal(t, []string{rf.ids[c.name], want}, r.BookIDs, "%s: BookIDs name the shell and the target", c.name)
			require.Len(t, r.Members, 2, c.name)
		} else {
			require.Equal(t, []string{rf.ids[c.name]}, r.BookIDs, c.name)
		}
	}

	// Evidence a row carries beyond its category.
	same := rows[rf.ids["same-path"]]
	require.Equal(t, "1", same.Current["rows_same_path"])
	require.Equal(t, rf.ids["g1-primary"], same.Current["shares_paths_with"])
	require.Equal(t, repairs.RiskLow, same.Risk, "sharing a path with the target itself is low risk")
	require.Equal(t, rf.ids["g1-primary"], same.Proposed["retired_version_of"])
	require.Equal(t, "2", rows[rf.ids["dead-group-missing"]].Current["rows_absent_on_disk"])
	require.Equal(t, "1", rows[rf.ids["no-group-present"]].Current["rows_absent_on_disk"], "one of its two files is gone, one is present")
	require.Equal(t, repairs.RiskReview, rows[rf.ids["itunes"]].Risk)
	require.Equal(t, "shell", rows[rf.ids["itunes"]].Current["itunes_side"])
	tin := rows[rf.ids["target-in-itunes"]]
	require.Equal(t, "target", tin.Current["itunes_side"], "the target's iTunes file holds the row, by the row's own class")
	require.Equal(t, repairs.RiskReview, tin.Risk)
	require.Contains(t, tin.Reason, "its target")
	require.Equal(t, rf.ids["g4-primary"], tin.Proposed["retired_version_of"])
	require.Equal(t, "g2", rows[rf.ids["dead-group-present"]].Current["group_id"])
	// A merged-into chain without a live group member is evidence, not a target.
	for _, n := range []string{"merged-no-group", "merged-dead-group"} {
		require.Equal(t, rf.ids["g1-primary"], rows[rf.ids[n]].Current["merged_into_target"], n)
		require.Empty(t, rows[rf.ids[n]].Proposed, "%s: nothing proposed without a target", n)
	}
	require.Empty(t, rows[rf.ids["merged-into"]].Current["merged_into_target"], "a real target needs no evidence line")
	require.Equal(t, retGroupWithoutLive, rows[rf.ids["dead-group-present"]].Current["group_class"])
	require.Equal(t, retGroupNone, rows[rf.ids["no-group-present"]].Current["group_class"])
	require.Equal(t, retGroupWithLive, rows[rf.ids["ambiguous"]].Current["group_class"])
}

// A no-target book is never given a target by the same-path holder heuristic;
// the heuristic's answer is reported as evidence only.
func TestRetiredVersions_SamePathHolderIsEvidenceNotATarget(t *testing.T) {
	rf := newRetiredFixture(t)
	p := rf.file("nogroup/x.mp3")
	rf.live("holder", "", p)
	rf.trashed("loser", "", 30, p)
	r := rf.plan()[rf.ids["loser"]]
	require.Equal(t, retCatNoTargetPresent, r.Class)
	require.Empty(t, r.Current["target_book_id"])
	require.Equal(t, rf.ids["holder"], r.Current["same_path_holder"])
	require.Equal(t, rf.ids["holder"], r.Current["shares_paths_with"])
}

// The plan through the framework: exact counts per category, and the guard
// pass's interaction with a would-retire row whose target sits under iTunes.
func TestRetiredVersions_RunPlanCounts(t *testing.T) {
	rf := newRetiredFixture(t)
	n := rf.seedEveryCategory()
	res, err := repairs.RunPlan(context.Background(), rf.f, nil, repairs.PlanDeps{Guard: rf.st}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, n, res.Total)

	wantClass := map[string]int{
		retCatWouldRetire:     2, // would-retire, merged-into
		retCatSamePath:        1,
		retCatITunes:          2, // a shell under iTunes, and a shell whose target is
		retCatTrashLabelled:   1,
		retCatTooRecent:       1,
		retCatAgeUnknown:      1,
		retCatListening:       1,
		retCatNoFiles:         1,
		retCatNoTargetPresent: 4,
		retCatNoTargetMissing: 2,
		retCatAmbiguous:       1,
	}
	require.Equal(t, wantClass, res.ByClass)

	// No framework guard kind appears: the fixer's own iTunes gate has already
	// labelled the target-in-iTunes row, so it is not a would_retire row that
	// the guard then skips.
	wantSkipped := map[string]int{
		retCatITunes:          2,
		retCatTooRecent:       1,
		retCatAgeUnknown:      1,
		retCatListening:       1,
		retCatTrashLabelled:   1,
		retCatNoFiles:         1,
		retCatNoTargetPresent: 4,
		retCatNoTargetMissing: 2,
		retCatAmbiguous:       1,
	}
	require.Equal(t, wantSkipped, res.SkippedByKind)
	require.Equal(t, 3, res.Applicable, "would-retire, merged-into and same-path are applicable")
	require.Equal(t, n, res.Applicable+sumInts(res.SkippedByKind))
}

func sumInts(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func snapshotLibrary(t *testing.T, st *database.PebbleStore) string {
	t.Helper()
	books, err := st.GetAllBooksCore(0, 0)
	require.NoError(t, err)
	files, err := st.GetAllBookFilesCore()
	require.NoError(t, err)
	out, err := json.Marshal(map[string]any{"books": books, "files": files})
	require.NoError(t, err)
	return string(out)
}

// Plan, re-plan and apply write nothing, and apply refuses every row.
func TestRetiredVersions_NoWritesAndApplyRefuses(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.seedEveryCategory()
	before := snapshotLibrary(t, rf.st)

	res, err := repairs.RunPlan(context.Background(), rf.f, nil, repairs.PlanDeps{Guard: rf.st}, &fakeReporter{})
	require.NoError(t, err)
	require.NotZero(t, res.Applicable)
	var ids []string
	for _, r := range res.Rows {
		if r.Applicable() {
			ids = append(ids, r.RowID)
		}
	}
	const opID = "op-retired-preview"
	w := repairs.NewWriter(rf.st, rf.st, rf.f.ID(), "bulk_update", "repairs-").WithJournal(rf.st, rf.st, opID).WithFieldStates(rf.st)
	sd := &countingStandDown{}
	// The engine refuses a plan-only fixer before it reads a row, takes the
	// stand-down or touches the writer.
	out, err := repairs.RunApply(context.Background(), rf.f, res, "plan-1", ids, false,
		repairs.ApplyDeps{Guard: rf.st, Writer: w, OpID: opID, StandDown: sd}, &fakeReporter{})
	require.ErrorIs(t, err, repairs.ErrPlanOnly)
	require.Zero(t, out.Requested)
	require.Zero(t, out.Applied)
	require.Zero(t, sd.acquires.Load(), "a fixer that writes nothing never parks the library scan")

	// Past the engine's refusal, Apply refuses every row itself: the wrapper
	// hides PlanOnly, so RunApply reaches the fixer.
	out, err = repairs.RunApply(context.Background(), plainFixer{rf.f}, res, "plan-1", ids, false,
		repairs.ApplyDeps{Guard: rf.st, Writer: w, OpID: opID, StandDown: &countingStandDown{}}, &fakeReporter{})
	require.NoError(t, err)
	require.Zero(t, out.Applied, "%v", out.ByOutcome)
	require.Equal(t, len(ids), out.Failed, "%v", out.ByOutcome)
	for _, rr := range out.Rows {
		require.Equal(t, repairs.OutcomeFailed, rr.Outcome)
		require.Contains(t, rr.Error, "not implemented until PR 7")
	}
	require.Zero(t, out.BookWrites)
	require.Zero(t, out.JournalRows)

	// Calling Apply directly refuses too.
	err = rf.f.Apply(context.Background(), nil, res.Rows[0])
	require.ErrorIs(t, err, errRetiredApplyNotBuilt)

	require.Equal(t, before, snapshotLibrary(t, rf.st), "preview must not change a book or a book_file row")
	files, err := rf.st.GetBookFiles(rf.ids["would-retire"])
	require.NoError(t, err)
	require.Len(t, files, 1, "the shell keeps its row")
}

// A re-plan reads fresh state through the same code and agrees with the plan.
func TestRetiredVersions_ReplanAgreesWithPlan(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.seedEveryCategory()
	rows := rf.plan()
	for id, planned := range rows {
		fresh, err := rf.f.Replan(context.Background(), nil, planned, &fakeReporter{})
		require.NoError(t, err, id)
		require.Equal(t, planned.Class, fresh.Class, id)
		require.Equal(t, planned.Fingerprint, fresh.Fingerprint, "%s: an unchanged book re-plans to the same fingerprint", id)
	}
	// A change after the plan changes the fingerprint: the target's primary
	// flag is taken away, so the group has no single primary any more.
	planned := rows[rf.ids["would-retire"]]
	p, err := rf.st.GetBookByID(rf.ids["g1-primary"])
	require.NoError(t, err)
	p.IsPrimaryVersion = nil
	_, err = rf.st.UpdateBook(p.ID, p)
	require.NoError(t, err)
	rf.liveNoFlag("g1-second", "g1")
	fresh, err := rf.f.Replan(context.Background(), nil, planned, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, retCatAmbiguous, fresh.Class)
	require.NotEqual(t, planned.Fingerprint, fresh.Fingerprint)

	// A book that left the trash re-plans as gone, not as an error.
	b, err := rf.st.GetBookByID(rf.ids["too-recent"])
	require.NoError(t, err)
	b.MarkedForDeletion, b.MarkedForDeletionAt = retiredPtr(false), nil
	_, err = rf.st.UpdateBook(b.ID, b)
	require.NoError(t, err)
	fresh, err = rf.f.Replan(context.Background(), nil, rows[rf.ids["too-recent"]], &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, retCatGone, fresh.Skipped)
}

// The age gate follows the min_age_days param: 14 by default, overridable,
// never negative.
func TestRetiredVersions_MinAgeParam(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.live("primary", "g", rf.file("g/a.mp3"))
	rf.trashed("thirteen", "g", 13, rf.file("g/13.mp3"))
	rf.trashed("fourteen", "g", 14, rf.file("g/14.mp3"))
	rows := rf.plan()
	require.Equal(t, retCatTooRecent, rows[rf.ids["thirteen"]].Class)
	require.Equal(t, retCatWouldRetire, rows[rf.ids["fourteen"]].Class, "exactly 14 days passes the gate")

	out, err := rf.f.Plan(context.Background(), json.RawMessage(`{"min_age_days":0}`), &fakeReporter{})
	require.NoError(t, err)
	for _, r := range out {
		require.Equal(t, retCatWouldRetire, r.Class, r.Title)
	}
	_, err = rf.f.Plan(context.Background(), json.RawMessage(`{"min_age_days":-1}`), &fakeReporter{})
	require.Error(t, err)
	_, err = rf.f.Plan(context.Background(), json.RawMessage(`{"min_age_days":"x"}`), &fakeReporter{})
	require.Error(t, err)
}

// Many more books than workers, sharing one group, one shared path and
// merged-into chains, with the ownership lookup, the group cache, the book
// cache and the target cache hit from every worker: run under -race.
func TestRetiredVersions_ConcurrentPlan(t *testing.T) {
	rf := newRetiredFixture(t)
	shared := rf.file("g/shared.mp3")
	primary := rf.live("primary", "g", rf.file("g/primary.mp3"))
	require.NoError(t, rf.st.CreateBookFile(&database.BookFile{BookID: primary, FilePath: shared, Format: "mp3"}))
	n := runtime.NumCPU()*6 + 7
	for i := range n {
		rf.trashed(fmt.Sprintf("loser-%d", i), "g", 30, rf.file(fmt.Sprintf("g/%d.mp3", i)))
	}
	// Every one of these shares a path with the target. The sharers and the
	// chains are fewer than the plain losers: they hit the same caches, and
	// each row costs a file and two writes.
	m := runtime.NumCPU() + 3
	for i := range m {
		rf.trashed(fmt.Sprintf("sp-%d", i), "g", 30, shared, rf.file(fmt.Sprintf("g/sp-%d.mp3", i)))
	}
	// Merged-into chains: shell -> trashed middle -> live primary.
	rf.trashed("mid", "g", 30, rf.file("g/mid.mp3"))
	rf.mergedInto("mid", "primary")
	for i := range m {
		name := fmt.Sprintf("chain-%d", i)
		rf.trashed(name, "g", 30, rf.file("g/"+name+".mp3"))
		rf.mergedInto(name, "mid")
	}
	rows := rf.plan()
	require.Len(t, rows, n+2*m+1)
	for _, r := range rows {
		require.Equal(t, rf.ids["primary"], r.Current["target_book_id"], r.Title)
		switch {
		case strings.HasPrefix(r.Title, "sp-"):
			require.Equal(t, retCatSamePath, r.Class, r.Title)
			require.Equal(t, "1", r.Current["rows_same_path"], r.Title)
		case strings.HasPrefix(r.Title, "chain-"), r.Title == "mid":
			require.Equal(t, retCatWouldRetire, r.Class, r.Title)
			require.Equal(t, retMethodMergedInto, r.Current["target_method"], r.Title)
		default:
			require.Equal(t, retCatWouldRetire, r.Class, r.Title)
		}
	}
}

// warmingDeps is the deps of a server whose memdb is still warming up: the
// complete who-holds-this-path lookup cannot answer.
type warmingDeps struct{ fakeDeps }

type warmingPaths struct{ FragmentRepairReader }

func (warmingPaths) BookFilesAtPath(string) ([]database.BookFile, error) {
	return nil, fmt.Errorf("%w: memdb is not serving", database.ErrBookFilesAtPathUnavailable)
}

func (d warmingDeps) FragmentRepairReader() FragmentRepairReader {
	return warmingPaths{d.fakeDeps.FragmentRepairReader()}
}

// An ownership lookup that cannot be answered fails the plan; it never turns
// a same-path row into a plain one.
func TestRetiredVersions_IncompleteOwnershipLookupFailsThePlan(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.live("primary", "g", rf.file("g/a.mp3"))
	rf.trashed("loser", "g", 30, rf.file("g/b.mp3"))
	f := newRetiredVersionsFixer(&Plugin{deps: warmingDeps{fakeDeps{store: rf.st}}})
	f.now = func() time.Time { return rf.now }
	_, err := f.Plan(context.Background(), nil, &fakeReporter{})
	require.ErrorIs(t, err, database.ErrOwnershipLookupIncomplete)
}

// The fixer is registered in the Repairs lane under its plan id.
func TestRetiredVersions_Registered(t *testing.T) {
	rf := newRetiredFixture(t)
	p := &Plugin{deps: fakeDeps{store: rf.st}}
	fx, ok := p.Repairs().Get("retired-versions")
	require.True(t, ok)
	require.Equal(t, retiredVersionsFixerID, fx.ID())
	require.True(t, repairs.IsPlanOnly(fx), "the preview has no Apply, so the lane must not offer one")
	require.True(t, repairs.SkipsScanStandDown(fx), "the preview writes nothing a scan writes")
}
