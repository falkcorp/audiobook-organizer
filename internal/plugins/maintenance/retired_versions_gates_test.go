// file: internal/plugins/maintenance/retired_versions_gates_test.go
// version: 1.1.0
// guid: 5d0b7a96-3c41-4e8f-a2d7-91f6c8e03b54
// last-edited: 2026-10-22

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/decisions"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// countingStandDown is a real repairs.StandDown that counts its acquires.
type countingStandDown struct{ acquires atomic.Int32 }

func (c *countingStandDown) AcquireScanStandDown(context.Context, string, string) (func(), error) {
	c.acquires.Add(1)
	return func() {}, nil
}
func (c *countingStandDown) RenewScanStandDown(string) bool { return true }
func (c *countingStandDown) ScanStandDownValid(string) bool { return true }
func (c *countingStandDown) acquired() int32                { return c.acquires.Load() }

var _ repairs.StandDown = (*countingStandDown)(nil)

// addFile adds a book_file row with the given fields to a fixture book.
func (rf *retiredFixture) addFile(bookID string, f *database.BookFile) {
	rf.t.Helper()
	f.BookID = bookID
	if f.Format == "" {
		f.Format = "mp3"
	}
	require.NoError(rf.t, rf.st.CreateBookFile(f))
}

// plainFixer hides every optional capability of the fixer it wraps.
type plainFixer struct{ repairs.Fixer }

// reject records the owner's reject of joining two fixture books.
func (rf *retiredFixture) reject(a, b, family string) {
	rf.t.Helper()
	kv, ok := database.AsCapability[decisions.KV](rf.st)
	require.True(rf.t, ok)
	_, err := decisions.New(kv).Record(decisions.Input{
		OpKind: "dedup:book-candidate", EntityType: retiredEntityBook,
		EntityIDs: []string{rf.ids[a], rf.ids[b]},
		Decision:  decisions.DecisionReject, Source: decisions.SourceHuman, ActorUserID: "owner-user",
		ReasonCode: decisions.ReasonOther, Family: family,
	})
	require.NoError(rf.t, err)
}

// withDeps points the fixture's fixer at other deps.
func (rf *retiredFixture) withDeps(d ServerDeps) {
	rf.f = newRetiredVersionsFixer(&Plugin{deps: d})
	rf.f.now = func() time.Time { return rf.now }
}

type errVetoes struct{}

func (errVetoes) PairVetoed(string, string, string, string) (bool, error) {
	return false, errors.New("vetoes unavailable")
}

type errJournals struct{}

func (errJournals) ScanPrefix(string) ([]database.KVPair, error) {
	return nil, errors.New("journals unavailable")
}

// noVetoDeps is a server whose decisions store cannot be reached.
type noVetoDeps struct{ fakeDeps }

func (noVetoDeps) OwnerVetoes() OwnerVetoReader { return nil }

// stateErrStore fails every listening-state read.
type stateErrStore struct{ merge.UserStateRepairStore }

func (stateErrStore) GetUserBookState(string, string) (*database.UserBookState, error) {
	return nil, errors.New("state store down")
}

type stateErrDeps struct{ fakeDeps }

func (d stateErrDeps) MergeUserStateStore() merge.UserStateRepairStore {
	return stateErrStore{d.fakeDeps.MergeUserStateStore()}
}

func (rf *retiredFixture) user() string {
	rf.t.Helper()
	u, err := rf.st.CreateUser("reader", "reader@example.com", "argon2id", "x", []string{"user"}, "active")
	require.NoError(rf.t, err)
	return u.ID
}

// An owner veto on the pair holds the row, whichever family recorded it, and
// it outranks every gate after it.
func TestRetiredVersions_OwnerVetoGate(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.live("primary", "g", rf.file("g/primary.mp3"))
	rf.live("other", "", rf.file("o/other.mp3"))
	rf.trashed("work", "g", 30, rf.file("g/work.mp3"))
	rf.trashed("copy", "g", 30, rf.file("g/copy.mp3"))
	rf.trashed("recent", "g", 3, rf.file("g/recent.mp3"))
	rf.trashed("free", "g", 30, rf.file("g/free.mp3"))
	rf.trashed("wrong-pair", "g", 30, rf.file("g/wrong.mp3"))
	rf.reject("primary", "work", decisions.FamilySameWork) // the reverse of the planned order
	rf.reject("copy", "primary", decisions.FamilySameCopy)
	rf.reject("recent", "primary", decisions.FamilySameWork)
	rf.reject("wrong-pair", "other", decisions.FamilySameWork) // a veto on another pair

	rows := rf.plan()
	for name, fam := range map[string]string{"work": decisions.FamilySameWork, "copy": decisions.FamilySameCopy, "recent": decisions.FamilySameWork} {
		r := rows[rf.ids[name]]
		require.Equal(t, retCatVetoed, r.Class, "%s: %s", name, r.Reason)
		require.Equal(t, retCatVetoed, r.Skipped, name)
		require.False(t, r.Applicable(), name)
		require.Equal(t, fam, r.Current["owner_veto_family"], name)
	}
	require.Equal(t, retCatWouldRetire, rows[rf.ids["free"]].Class)
	require.Equal(t, retCatWouldRetire, rows[rf.ids["wrong-pair"]].Class, "a veto names a pair, not a book")
	require.Empty(t, rows[rf.ids["free"]].Current["owner_veto_family"])
}

// A veto read that fails is an error row, never a would-retire row; a server
// that cannot read vetoes at all refuses to plan; and a no-target row never
// needs the veto or the journal read.
func TestRetiredVersions_GuardReadsFailClosed(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.live("primary", "g", rf.file("g/primary.mp3"))
	rf.trashed("targeted", "g", 30, rf.file("g/a.mp3"))
	rf.trashed("orphan", "", 30, rf.file("o/a.mp3"))

	rf.withDeps(fakeDeps{store: rf.st, vetoes: errVetoes{}})
	rows := rf.plan()
	require.Equal(t, retCatError, rows[rf.ids["targeted"]].Class)
	require.Contains(t, rows[rf.ids["targeted"]].Reason, "vetoes unavailable")
	require.Equal(t, retCatNoTargetPresent, rows[rf.ids["orphan"]].Class, "no target, no veto lookup")

	// Every row's category depends on the combine journals, so an unreadable
	// journal fails the plan instead of producing a plan of error rows.
	rf.withDeps(fakeDeps{store: rf.st, journals: errJournals{}})
	_, err := rf.f.Plan(context.Background(), nil, &fakeReporter{})
	require.ErrorIs(t, err, errRetiredUndoRead)
	_, err = rf.f.Replan(context.Background(), nil, repairs.Row{RowID: rf.ids["orphan"]}, &fakeReporter{})
	require.ErrorIs(t, err, errRetiredUndoRead)

	rf.withDeps(noVetoDeps{fakeDeps{store: rf.st}})
	_, err = rf.f.Plan(context.Background(), nil, &fakeReporter{})
	require.ErrorContains(t, err, "refusing to plan")
	_, err = rf.f.Replan(context.Background(), nil, repairs.Row{RowID: rf.ids["targeted"]}, &fakeReporter{})
	require.ErrorContains(t, err, "refusing to plan")
}

// A book an unreversed combine journal names as absorbed is held; an undone
// journal releases it.
func TestRetiredVersions_UndoWindowGate(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.live("primary", "g", rf.file("g/primary.mp3"))
	rf.trashed("absorbed", "g", 30, rf.file("g/a.mp3"))
	rf.trashed("released", "g", 30, rf.file("g/b.mp3"))
	rf.trashed("plain", "g", 30, rf.file("g/c.mp3"))
	rf.trashed("recent", "g", 3, rf.file("g/d.mp3"))
	journal := func(id, status string, names ...string) {
		j := &merge.CombineJournal{ID: id, Status: status, SurvivorID: rf.ids["primary"]}
		for _, n := range names {
			j.Absorbed = append(j.Absorbed, merge.CombineAbsorbed{BookID: rf.ids[n]})
		}
		require.NoError(t, merge.WriteCombineJournal(rf.st, j))
	}
	journal("J-live", merge.CombineJournalApplied, "absorbed", "recent")
	journal("J-undone", merge.CombineJournalUndone, "released")

	rows := rf.plan()
	for _, n := range []string{"absorbed", "recent"} {
		r := rows[rf.ids[n]]
		require.Equal(t, retCatUndoWindow, r.Class, "%s: %s", n, r.Reason)
		require.Equal(t, retCatUndoWindow, r.Skipped, n)
		require.Contains(t, r.Reason, "J-live", n)
	}
	require.Equal(t, retCatWouldRetire, rows[rf.ids["released"]].Class)
	require.Equal(t, retCatWouldRetire, rows[rf.ids["plain"]].Class)
}

// The undo window outranks every other category: a combine-absorbed shell is
// usually labelled deleted, may have no group, and may own no file rows any
// more, and each of those must still read as "undo possible", not as a book a
// purge may take.
func TestRetiredVersions_UndoWindowOutranksEveryOtherCategory(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.live("primary", "g", rf.file("g/primary.mp3"))
	rf.trashed("labelled", "g", 30, rf.file("g/a.mp3"))
	b, err := rf.st.GetBookByID(rf.ids["labelled"])
	require.NoError(t, err)
	b.LibraryState = retiredPtr("deleted")
	_, err = rf.st.UpdateBook(b.ID, b)
	require.NoError(t, err)
	rf.trashed("no-files", "g", 30)
	rf.trashed("no-group", "", 30, rf.file("o/a.mp3"))
	rf.trashed("no-group-missing", "", 30, rf.absent("m.mp3"))
	rf.trashed("free-orphan", "", 30, rf.file("o/free.mp3"))
	j := &merge.CombineJournal{ID: "J-all", Status: merge.CombineJournalApplied, SurvivorID: rf.ids["primary"]}
	for _, n := range []string{"labelled", "no-files", "no-group", "no-group-missing"} {
		j.Absorbed = append(j.Absorbed, merge.CombineAbsorbed{BookID: rf.ids[n]})
	}
	require.NoError(t, merge.WriteCombineJournal(rf.st, j))

	rows := rf.plan()
	for _, n := range []string{"labelled", "no-files", "no-group", "no-group-missing"} {
		require.Equal(t, retCatUndoWindow, rows[rf.ids[n]].Class, "%s: %s", n, rows[rf.ids[n]].Reason)
		require.Equal(t, retCatUndoWindow, rows[rf.ids[n]].Skipped, n)
	}
	require.Equal(t, retCatNoTargetPresent, rows[rf.ids["free-orphan"]].Class, "a book no journal names is unaffected")
}

// EACCES, a timeout and ENOTDIR on a no-target row: only "does not exist" and
// "not a directory" mean missing; everything else is present and counted.
func TestRetiredVersions_StatErrorsAreUnknownNotMissing(t *testing.T) {
	rf := newRetiredFixture(t)
	notDir := rf.file("nd/real.mp3") // a regular file, so "<file>/child" is ENOTDIR
	rf.trashed("denied-all", "", 30, filepath.Join(rf.root, "denied", "a.mp3"), filepath.Join(rf.root, "denied", "b.mp3"))
	rf.trashed("denied-one", "", 30, filepath.Join(rf.root, "denied", "c.mp3"), rf.absent("x.mp3"))
	rf.trashed("not-a-dir", "", 30, filepath.Join(notDir, "child.mp3"))
	rf.trashed("clean-missing", "", 30, rf.absent("y.mp3"))
	rf.f.stat = func(p string) (os.FileInfo, error) {
		if strings.Contains(p, string(filepath.Separator)+"denied"+string(filepath.Separator)) {
			return nil, &fs.PathError{Op: "stat", Path: p, Err: syscall.EACCES}
		}
		return os.Stat(p)
	}
	rows := rf.plan()

	all := rows[rf.ids["denied-all"]]
	require.Equal(t, retCatNoTargetPresent, all.Class, "permission denied is not missing: %s", all.Reason)
	require.Equal(t, "2", all.Current["rows_stat_unknown"])
	require.Equal(t, "0", all.Current["rows_absent_on_disk"])
	require.Len(t, all.Evidence, 1)
	require.Contains(t, all.Evidence[0], "2 file(s) could not be checked")

	one := rows[rf.ids["denied-one"]]
	require.Equal(t, retCatNoTargetPresent, one.Class)
	require.Equal(t, "1", one.Current["rows_stat_unknown"])
	require.Equal(t, "1", one.Current["rows_absent_on_disk"])

	nd := rows[rf.ids["not-a-dir"]]
	require.Equal(t, retCatNoTargetMissing, nd.Class, "ENOTDIR is a path that is not there: %s", nd.Reason)
	require.Empty(t, nd.Current["rows_stat_unknown"])

	clean := rows[rf.ids["clean-missing"]]
	require.Equal(t, retCatNoTargetMissing, clean.Class)
	_, has := clean.Current["rows_stat_unknown"]
	require.False(t, has, "a clean row carries no unknown count")
	require.Empty(t, clean.Evidence)
}

// hangingStat makes every stat of a path containing "hang" block until the
// returned release is closed.
func (rf *retiredFixture) hangingStat(t *testing.T) (release func()) {
	t.Helper()
	ch := make(chan struct{})
	var once atomic.Bool
	release = func() {
		if once.CompareAndSwap(false, true) {
			close(ch)
		}
	}
	t.Cleanup(release)
	rf.f.stat = func(p string) (os.FileInfo, error) {
		if strings.Contains(p, "hang") {
			<-ch
			return nil, fs.ErrNotExist
		}
		return os.Stat(p)
	}
	rf.f.statTimeout = 20 * time.Millisecond
	return release
}

func setStatCap(t *testing.T, n int64) {
	t.Helper()
	old := retiredStatMaxAbandoned
	retiredStatMaxAbandoned = n
	t.Cleanup(func() { retiredStatMaxAbandoned = old })
}

// A stat that outlives its bound counts the file as present and unknown, and
// feeds the process-wide gauge until it finally returns.
func TestRetiredVersions_StatTimeoutIsUnknownAndGauged(t *testing.T) {
	rf := newRetiredFixture(t)
	release := rf.hangingStat(t)
	setStatCap(t, 100)
	rf.trashed("hung", "", 30, filepath.Join(rf.root, "hang", "a.mp3"))
	rf.trashed("fine", "", 30, rf.absent("b.mp3"))
	// Stats abandoned by an earlier test decrement the process-wide gauge when
	// their cleanup releases them; start from a quiet gauge.
	require.Eventually(t, func() bool { return retiredStatsAbandoned.Load() == 0 }, 5*time.Second, 5*time.Millisecond)
	base := retiredStatsAbandoned.Load()

	rows := rf.plan()
	hung := rows[rf.ids["hung"]]
	require.Equal(t, retCatNoTargetPresent, hung.Class, "a hung stat is not a missing file: %s", hung.Reason)
	require.Equal(t, "1", hung.Current["rows_stat_unknown"])
	require.Equal(t, retCatNoTargetMissing, rows[rf.ids["fine"]].Class)
	require.Greater(t, retiredStatsAbandoned.Load(), base, "the abandoned stat is on the process-wide gauge")

	release()
	require.Eventually(t, func() bool { return retiredStatsAbandoned.Load() <= base }, 5*time.Second, 5*time.Millisecond,
		"the gauge falls when the abandoned stat returns")
}

// Past the per-run cap of hung stats the plan fails rather than stacking more
// blocked threads; a re-plan fails the same way.
func TestRetiredVersions_HungMountFailsThePlan(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.hangingStat(t)
	setStatCap(t, 2)
	for i := range 12 {
		rf.trashed("hung-"+string(rune('a'+i)), "", 30, filepath.Join(rf.root, "hang", string(rune('a'+i))+".mp3"))
	}
	_, err := rf.f.Plan(context.Background(), nil, &fakeReporter{})
	require.ErrorIs(t, err, errRetiredStatCap)

	setStatCap(t, 1)
	_, err = rf.f.Replan(context.Background(), nil, repairs.Row{RowID: rf.ids["hung-a"]}, &fakeReporter{})
	require.ErrorIs(t, err, errRetiredStatCap)
}

// The context is checked before every stat: a canceled run stats nothing, and a
// run canceled mid-row stops at the next file.
func TestRetiredVersions_StatChecksContext(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.live("primary", "g", rf.file("g/a.mp3"))
	run, err := rf.f.newRun(nil)
	require.NoError(t, err)
	files := []database.BookFileCore{{FilePath: "/x/1"}, {FilePath: "/x/2"}, {FilePath: "/x/3"}}

	var calls atomic.Int32
	run.stat = func(string) (os.FileInfo, error) { calls.Add(1); return nil, fs.ErrNotExist }
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = run.countAbsent(canceled, files)
	require.ErrorIs(t, err, context.Canceled)
	time.Sleep(50 * time.Millisecond) // a stat started anyway runs in its own goroutine; let it land
	require.Zero(t, calls.Load(), "a canceled run does not stat")

	ctx, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	calls.Store(0)
	run.stat = func(string) (os.FileInfo, error) { calls.Add(1); cancel2(); return nil, fs.ErrNotExist }
	_, _, err = run.countAbsent(ctx, files)
	require.ErrorIs(t, err, context.Canceled)
	time.Sleep(50 * time.Millisecond)
	require.EqualValues(t, 1, calls.Load(), "cancelation stops the row at the next file")
}

// A listening-state read that fails holds the row as unknown, never as free.
func TestRetiredVersions_ListeningStateReadErrorIsUnknown(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.live("primary", "g", rf.file("g/primary.mp3"))
	rf.trashed("victim", "g", 30, rf.file("g/a.mp3"))
	rf.user()
	rf.withDeps(stateErrDeps{fakeDeps{store: rf.st}})
	r := rf.plan()[rf.ids["victim"]]
	require.Equal(t, retCatListeningUnknown, r.Class, r.Reason)
	require.Contains(t, r.Reason, "state store down")
	require.False(t, r.Applicable())
}

// Two trashed books at one path share nothing live; a book labelled into the
// trash without the deletion flag is not a live holder either. A live holder is.
func TestRetiredVersions_PathOwnerMustBeLive(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.live("primary", "g", rf.file("g/primary.mp3"))
	p := rf.file("g/shared.mp3")
	rf.trashed("shell-a", "g", 30, p, rf.file("g/a.mp3"))
	rf.trashed("shell-b", "g", 30, p)
	q := rf.file("g/labelled.mp3")
	rf.add("labelled-holder", &database.Book{Title: "labelled-holder", FilePath: "/lib/lh", LibraryState: retiredPtr("deleted")}, q)
	rf.trashed("shell-c", "g", 30, q)
	r := rf.file("g/live.mp3")
	rf.live("live-holder", "", r)
	rf.trashed("shell-d", "g", 30, r)

	rows := rf.plan()
	for _, n := range []string{"shell-a", "shell-b", "shell-c"} {
		require.Equal(t, retCatWouldRetire, rows[rf.ids[n]].Class, "%s: %s", n, rows[rf.ids[n]].Reason)
		_, has := rows[rf.ids[n]].Current["rows_same_path"]
		require.False(t, has, n)
	}
	d := rows[rf.ids["shell-d"]]
	require.Equal(t, retCatSamePath, d.Class)
	require.Equal(t, rf.ids["live-holder"], d.Current["shares_paths_with"])
	require.Equal(t, repairs.RiskReview, d.Risk, "the live holder is not the target")
}

// A book labelled into the trash is not live, in a group, in a merged-into
// chain, or when asked directly.
func TestRetiredVersions_LabelledTrashIsNotLive(t *testing.T) {
	rf := newRetiredFixture(t)
	labelled := func(name, group string) string {
		b := &database.Book{Title: name, FilePath: "/lib/" + name, LibraryState: retiredPtr("deleted")}
		if group != "" {
			b.VersionGroupID = retiredPtr(group)
		}
		return rf.add(name, b, rf.file("lab/"+name+".mp3"))
	}
	// The only other member of the group is labelled, not flagged.
	labelled("lab-member", "gl")
	rf.trashed("lonely", "gl", 30, rf.file("gl/own.mp3"))
	// A chain shell -> labelled -> live primary skips the labelled hop.
	rf.live("primary", "gc", rf.file("gc/primary.mp3"))
	labelled("lab-hop", "")
	rf.mergedInto("lab-hop", "primary")
	rf.trashed("chained", "gc", 30, rf.file("gc/own.mp3"))
	rf.mergedInto("chained", "lab-hop")

	rows := rf.plan()
	lonely := rows[rf.ids["lonely"]]
	require.Equal(t, retGroupWithoutLive, lonely.Current["group_class"])
	require.Equal(t, retCatNoTargetPresent, lonely.Class, lonely.Reason)
	chained := rows[rf.ids["chained"]]
	require.Equal(t, rf.ids["primary"], chained.Current["target_book_id"], "the chain runs past the labelled book")
	require.Equal(t, retMethodMergedInto, chained.Current["target_method"])

	run, err := rf.f.newRun(nil)
	require.NoError(t, err)
	for name, want := range map[string]bool{"primary": true, "lab-member": false, "lab-hop": false, "lonely": false} {
		got, err := run.isLive(rf.ids[name])
		require.NoError(t, err)
		require.Equal(t, want, got, name)
	}
}

// The iTunes gate sees a path that only a file row's iTunes path spells, and
// the book's own iTunes path, on the shell and on the target.
func TestRetiredVersions_ITunesPathOnlyRows(t *testing.T) {
	rf := newRetiredFixture(t)
	const frozen = "/data/books/itunes/Author/Title.m4b"
	rf.live("primary", "g", rf.file("g/primary.mp3"))
	shell := rf.trashed("shell-row", "g", 30)
	rf.addFile(shell, &database.BookFile{FilePath: rf.file("g/own.mp3"), ITunesPath: frozen})
	bookLevel := rf.trashed("shell-book", "g", 30, rf.file("g/b.mp3"))
	b, err := rf.st.GetBookByID(bookLevel)
	require.NoError(t, err)
	b.ITunesPath = retiredPtr(frozen)
	_, err = rf.st.UpdateBook(b.ID, b)
	require.NoError(t, err)

	rf.live("t-primary", "g2", rf.file("g2/primary.mp3"))
	tp := rf.ids["t-primary"]
	rf.addFile(tp, &database.BookFile{FilePath: rf.file("g2/second.mp3"), ITunesPath: frozen})
	rf.trashed("shell-target", "g2", 30, rf.file("g2/own.mp3"))

	got := rf.plan()
	for _, n := range []string{"shell-row", "shell-book"} {
		require.Equal(t, retCatITunes, got[rf.ids[n]].Class, "%s: %s", n, got[rf.ids[n]].Reason)
		require.Equal(t, "shell", got[rf.ids[n]].Current["itunes_side"], n)
	}
	st := got[rf.ids["shell-target"]]
	require.Equal(t, retCatITunes, st.Class, st.Reason)
	require.Equal(t, "target", st.Current["itunes_side"])
	require.Equal(t, "1", got[rf.ids["shell-row"]].Current["rows_itunes"])
}

// A library symlink into books/itunes/** is under it, on either side, even
// though neither path spells it.
func TestRetiredVersions_ITunesThroughSymlinks(t *testing.T) {
	rf := newRetiredFixture(t)
	real := rf.file("library/books/itunes/Author/real.mp3")
	link := func(name string) string {
		l := filepath.Join(rf.root, "links", name)
		require.NoError(t, os.MkdirAll(filepath.Dir(l), 0o755))
		require.NoError(t, os.Symlink(real, l))
		return l
	}
	rf.live("primary", "g", rf.file("g/primary.mp3"))
	rf.trashed("shell-link", "g", 30, link("shell.mp3"))
	rf.live("t-primary", "g2", link("target.mp3"))
	rf.trashed("target-link", "g2", 30, rf.file("g2/own.mp3"))

	rows := rf.plan()
	sl := rows[rf.ids["shell-link"]]
	require.Equal(t, retCatITunes, sl.Class, sl.Reason)
	require.Equal(t, "shell", sl.Current["itunes_side"])
	_, lexical := sl.Current["rows_itunes"]
	require.False(t, lexical, "the lexical count does not see the link, which is why the gate must resolve it")
	tl := rows[rf.ids["target-link"]]
	require.Equal(t, retCatITunes, tl.Class, tl.Reason)
	require.Equal(t, "target", tl.Current["itunes_side"])
}

// The iTunes class outranks the age and listening gates.
func TestRetiredVersions_ITunesOutranksAgeAndListening(t *testing.T) {
	rf := newRetiredFixture(t)
	const frozen = "/data/books/itunes/Author/Title.m4b"
	rf.live("primary", "g", rf.file("g/primary.mp3"))
	listened := rf.trashed("itunes-listened", "g", 30, frozen)
	u := rf.user()
	require.NoError(t, rf.st.SetUserBookState(&database.UserBookState{UserID: u, BookID: listened,
		Status: database.UserBookStatusInProgress, ProgressPct: 40}))
	rf.trashed("itunes-recent", "g", 2, frozen+".2")
	rows := rf.plan()
	require.Equal(t, retCatITunes, rows[listened].Class, rows[listened].Reason)
	require.Equal(t, retCatITunes, rows[rf.ids["itunes-recent"]].Class)
}

// Plan logs what its no-target rows knew but did not use.
func TestRetiredVersions_SummaryLogsNoTargetEvidence(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.live("primary", "g", rf.file("g/primary.mp3"))
	rf.trashed("merged-only", "", 30, rf.file("m/a.mp3"))
	rf.mergedInto("merged-only", "primary")
	p := rf.file("h/p.mp3")
	rf.live("holder", "", p)
	rf.trashed("held", "", 30, p)
	rep := &fakeReporter{}
	_, err := rf.f.Plan(context.Background(), nil, rep)
	require.NoError(t, err)
	rep.mu.Lock()
	defer rep.mu.Unlock()
	var found bool
	for _, l := range rep.logs {
		if strings.Contains(l, "2 trashed book(s); 2 have no target, of which 1 have a merged-into chain ending at a live book and 1 have a live book at every path") {
			found = true
		}
	}
	require.True(t, found, "summary line missing: %v", rep.logs)
}

// RunApply takes a scan stand-down for a fixer that did not opt out and none
// for this one; the control proves the assertion can fail.
func TestRetiredVersions_NoScanStandDown(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.live("primary", "g", rf.file("g/primary.mp3"))
	rf.trashed("loser", "g", 30, rf.file("g/a.mp3"))
	res, err := repairs.RunPlan(context.Background(), rf.f, nil, repairs.PlanDeps{Guard: rf.st}, &fakeReporter{})
	require.NoError(t, err)
	ids := []string{rf.ids["loser"]}
	apply := func(f repairs.Fixer) int32 {
		sd := &countingStandDown{}
		const opID = "op-standdown"
		w := repairs.NewWriter(rf.st, rf.st, f.ID(), "bulk_update", "repairs-").WithJournal(rf.st, rf.st, opID).WithFieldStates(rf.st)
		_, err := repairs.RunApply(context.Background(), f, res, "plan-1", ids, false,
			repairs.ApplyDeps{Guard: rf.st, Writer: w, OpID: opID, StandDown: sd}, &fakeReporter{})
		require.NoError(t, err)
		return sd.acquired()
	}
	require.Zero(t, apply(rf.f))
	require.Positive(t, apply(plainFixer{rf.f}), "without the opt-out the run does take the stand-down")
}

func TestRetiredVersions_DescriptionNamesItsLimits(t *testing.T) {
	rf := newRetiredFixture(t)
	d := rf.f.Description()
	for _, want := range []string{"min_age_days", "rule 5", "owner veto", "undone", "no Apply"} {
		require.Contains(t, d, want)
	}
}

// One stored row per trashed book, tens of thousands of them in one
// operation's result: keep each row small, and say how small it is.
func TestRetiredVersions_StoredRowSize(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.live("primary", "g", rf.file("g/primary.mp3"))
	for i := range 30 {
		rf.trashed("would-"+string(rune('a'+i%26))+string(rune('a'+i/26)), "g", 30, rf.file("g/w"+string(rune('a'+i%26))+string(rune('a'+i/26))+".mp3"))
	}
	for i := range 30 {
		rf.trashed("orphan-"+string(rune('a'+i%26))+string(rune('a'+i/26)), "", 30, rf.absent("o"+string(rune('a'+i%26))+string(rune('a'+i/26))+".mp3"))
	}
	rows := rf.plan()
	total, largest := 0, 0
	for _, r := range rows {
		b, err := json.Marshal(r)
		require.NoError(t, err)
		total += len(b)
		largest = max(largest, len(b))
		if r.Class == retCatWouldRetire {
			for _, k := range []string{"rows_same_path", "rows_itunes", "itunes_pid_rows", "shares_paths_with", "rows_missing_flag", "library_state"} {
				_, has := r.Current[k]
				require.False(t, has, "a zero or empty %s is not stored", k)
			}
		}
	}
	avg := total / len(rows)
	t.Logf("stored plan rows: %d rows, average %d bytes, largest %d bytes; 51,000 rows would be about %.1f MB",
		len(rows), avg, largest, float64(avg)*51000/1e6)
	require.Less(t, avg, 900, "a plan row grew; 51,000 of them are one operation's result")
}
