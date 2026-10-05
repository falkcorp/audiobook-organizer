// file: internal/plugins/maintenance/itunes_regroup_test.go
// version: 1.6.0
// guid: 6f7a8b9c-0d1e-2f3a-4b5c-6d7e8f9a0b1c
// last-edited: 2026-10-05

package maintenance

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// errExtIDStore wraps a Store and makes GetExternalIDsForBook error for one book
// ID, so a test can drive the delete guard's "read failed" branch.
type errExtIDStore struct {
	database.Store
	failID string
}

func (e *errExtIDStore) GetExternalIDsForBook(bookID string) ([]database.ExternalIDMapping, error) {
	if bookID == e.failID {
		return nil, fmt.Errorf("simulated ext-id read error")
	}
	return e.Store.GetExternalIDsForBook(bookID)
}

func regroupStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	s, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	// Memdb warmup publishes asynchronously; writes made before it publishes
	// are invisible to memdb-backed reads (see PebbleStore.WaitForWarmup).
	s.WaitForWarmup()
	t.Cleanup(func() { s.Close() })
	return s
}

func seedBook(t *testing.T, s *database.PebbleStore, title string) string {
	t.Helper()
	b, err := s.CreateBook(&database.Book{Title: title})
	if err != nil || b == nil {
		t.Fatalf("CreateBook(%q): %v", title, err)
	}
	return b.ID
}

func seedFilePID(t *testing.T, s *database.PebbleStore, bookID, pid string) {
	t.Helper()
	f := &database.BookFile{BookID: bookID, ITunesPersistentID: pid, FilePath: "/x/" + pid + ".m4b"}
	if err := s.CreateBookFile(f); err != nil {
		t.Fatalf("CreateBookFile(%s,%s): %v", bookID, pid, err)
	}
	if err := s.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "itunes", ExternalID: pid, BookID: bookID}); err != nil {
		t.Fatalf("CreateExternalIDMapping(%s): %v", pid, err)
	}
}

// End-to-end: two fragment books → one merged book; the emptied fragment is
// deleted; both PIDs and files land on the survivor; title is set.
func TestITunesRegroupApply_MergeAndDelete(t *testing.T) {
	s := regroupStore(t)
	b1 := seedBook(t, s, "Frag A")
	b2 := seedBook(t, s, "Frag B")
	seedFilePID(t, s, b1, "p1")
	seedFilePID(t, s, b2, "p2")

	p := &Plugin{}
	rep := &fakeReporter{}
	groups := []itunesservice.HealGroup{{Title: "Merged Book", PIDs: []string{"p1", "p2"}}}

	snap, err := p.buildRegroupSnapshot(context.Background(), s, rgRoot, rep)
	if err != nil {
		t.Fatalf("buildRegroupSnapshot: %v", err)
	}
	if len(snap.PIDLoc) != 2 || len(snap.Books) != 2 {
		t.Fatalf("snapshot pidloc=%d books=%d, want 2/2", len(snap.PIDLoc), len(snap.Books))
	}
	plan := itunesservice.PlanRegroup(groups, snap)
	if plan.Consolidated != 1 || len(plan.DeleteBooks) != 1 {
		t.Fatalf("plan consolidate=%d deletes=%d, want 1/1", plan.Consolidated, len(plan.DeleteBooks))
	}
	if _, err := p.applyRegroupPlan(context.Background(), s, plan, rgRoot, rep); err != nil {
		t.Fatalf("applyRegroupPlan: %v", err)
	}

	survivor := plan.Groups[0].Target
	loser := b1
	if survivor == b1 {
		loser = b2
	}
	files, _ := s.GetBookFiles(survivor)
	if len(files) != 2 {
		t.Fatalf("survivor has %d files, want 2", len(files))
	}
	for _, pid := range []string{"p1", "p2"} {
		if id, _ := s.GetBookByExternalID("itunes", pid); id != survivor {
			t.Errorf("PID %s -> %q, want survivor %q", pid, id, survivor)
		}
	}
	if b, _ := s.GetBookByID(loser); b != nil {
		t.Errorf("loser %s not deleted", loser)
	}
	if sb, _ := s.GetBookByID(survivor); sb == nil || sb.Title != "Merged Book" {
		t.Errorf("survivor title = %v, want 'Merged Book'", sb)
	}
}

// The delete guard must refuse to delete a projected-empty book that still has a
// residual ext-id mapping (zero files ≠ zero mappings — the canary lesson).
func TestITunesRegroupApply_DeleteGuardSkipsResidualExtID(t *testing.T) {
	s := regroupStore(t)
	b1 := seedBook(t, s, "Frag A")
	b2 := seedBook(t, s, "Frag B")
	seedFilePID(t, s, b1, "p1")
	seedFilePID(t, s, b2, "p2")
	// Residual mapping on b2 that NO group claims (e.g. a PID no longer in the XML).
	if err := s.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "itunes", ExternalID: "resid", BookID: b2}); err != nil {
		t.Fatalf("seed residual: %v", err)
	}

	p := &Plugin{}
	rep := &fakeReporter{}
	groups := []itunesservice.HealGroup{{Title: "Merged Book", PIDs: []string{"p1", "p2"}}}
	snap, _ := p.buildRegroupSnapshot(context.Background(), s, rgRoot, rep)
	plan := itunesservice.PlanRegroup(groups, snap)

	survivor := plan.Groups[0].Target
	if survivor != b1 {
		t.Skipf("survivor was %s not b1 (tiebreak); residual-guard case needs b2 to be the loser", survivor)
	}
	if _, err := p.applyRegroupPlan(context.Background(), s, plan, rgRoot, rep); err != nil {
		t.Fatalf("applyRegroupPlan: %v", err)
	}
	// b2 still has the residual mapping → must NOT have been deleted.
	if b, _ := s.GetBookByID(b2); b == nil {
		t.Fatalf("b2 deleted despite residual ext-id (guard failed)")
	}
}

// The delete guard must fail CLOSED: if the ext-id (or file) read errors, the
// book's emptiness can't be proven, so it must be skipped rather than deleted.
// Without the fix, `exts, _ :=` swallowed the error → nil slice (len 0) → the
// guard passed and DeleteBook ran on an unverifiable book.
func TestITunesRegroupApply_DeleteGuardFailsClosedOnReadError(t *testing.T) {
	s := regroupStore(t)
	b1 := seedBook(t, s, "Frag A")
	b2 := seedBook(t, s, "Frag B")
	seedFilePID(t, s, b1, "p1")
	seedFilePID(t, s, b2, "p2")

	p := &Plugin{}
	rep := &fakeReporter{}
	groups := []itunesservice.HealGroup{{Title: "Merged Book", PIDs: []string{"p1", "p2"}}}
	snap, _ := p.buildRegroupSnapshot(context.Background(), s, rgRoot, rep)
	plan := itunesservice.PlanRegroup(groups, snap)

	survivor := plan.Groups[0].Target
	loser := b1
	if survivor == b1 {
		loser = b2
	}

	// Wrap so the delete-guard's ext-id read for the loser errors. The merge
	// phase does not call GetExternalIDsForBook, so only the guard is affected.
	wrapped := &errExtIDStore{Store: s, failID: loser}
	if _, err := p.applyRegroupPlan(context.Background(), wrapped, plan, rgRoot, rep); err != nil {
		t.Fatalf("applyRegroupPlan: %v", err)
	}

	// Fail-closed: the loser must survive because its emptiness could not be verified.
	if b, _ := s.GetBookByID(loser); b == nil {
		t.Fatalf("loser %s deleted despite ext-id read error (guard failed open)", loser)
	}
}

// #3764 follow-up item 5: a projected-empty book a user has listening state
// on is no longer stranded. Its state is carried to the regrouped book that
// took its files, then it is deleted.
func TestITunesRegroupApply_DeleteCarriesUserStateToTarget(t *testing.T) {
	s, u, plan, rep := regroupWithState(t)
	p := &Plugin{}
	if _, err := p.applyRegroupPlan(context.Background(), s, plan, rgRoot, rep); err != nil {
		t.Fatalf("applyRegroupPlan: %v", err)
	}
	loser, target := plan.DeleteBooks[0], plan.Groups[0].Target
	if b, _ := s.GetBookByID(loser); b != nil {
		t.Fatalf("book %s was kept; its state should have been carried and the book deleted", loser)
	}
	pos, err := s.ListUserPositionsForBook(u.ID, target)
	if err != nil || len(pos) == 0 {
		t.Fatalf("target %s has no position after the carry (err=%v)", target, err)
	}
	if rows, _ := s.ScanPrefix(merge.PendingUserStateRepairPrefix); len(rows) != 0 {
		t.Fatalf("pending repairs left: %d", len(rows))
	}
	if !strings.Contains(strings.Join(rep.logs, "\n"), "state-carried=1") {
		t.Fatalf("summary does not count the carry: %v", rep.logs)
	}
}

// When the carry cannot fully land, the book is kept with every user's state
// on it, and the skip says why (no "(read error: <nil>)").
func TestITunesRegroupApply_DeleteKeepsBookWhenCarryFails(t *testing.T) {
	s, u, plan, rep := regroupWithState(t)
	loser, target := plan.DeleteBooks[0], plan.Groups[0].Target
	fs := &rgFailStateStore{PebbleStore: s, failOn: target}
	p := &Plugin{}
	if _, err := p.applyRegroupPlan(context.Background(), fs, plan, rgRoot, rep); err != nil {
		t.Fatalf("applyRegroupPlan: %v", err)
	}
	if b, _ := s.GetBookByID(loser); b == nil {
		t.Fatalf("book %s deleted though its users' state could not be carried", loser)
	}
	st, err := s.GetUserBookState(u.ID, loser)
	if err != nil || st == nil || st.ProgressPct != 30 {
		t.Fatalf("loser state = %+v (err=%v), want 30%% still on it", st, err)
	}
	logs := strings.Join(rep.logs, "\n")
	if !strings.Contains(logs, "could not be carried to "+target) || strings.Contains(logs, "<nil>") {
		t.Fatalf("skip message: %s", logs)
	}
}

// rgFailStateStore fails every user-state write onto one book.
type rgFailStateStore struct {
	*database.PebbleStore
	failOn string
}

func (s *rgFailStateStore) SetUserBookState(st *database.UserBookState) error {
	if st.BookID == s.failOn {
		return fmt.Errorf("injected SetUserBookState failure for %s", st.BookID)
	}
	return s.PebbleStore.SetUserBookState(st)
}

// regroupWithState plans two one-file fragments into one book, a user
// holding 30% (state and position) on each.
func regroupWithState(t *testing.T) (*database.PebbleStore, *database.User, itunesservice.RegroupPlan, *fakeReporter) {
	t.Helper()
	s := regroupStore(t)
	b1 := seedBook(t, s, "Frag A")
	b2 := seedBook(t, s, "Frag B")
	seedFilePID(t, s, b1, "p1")
	seedFilePID(t, s, b2, "p2")
	u, err := s.CreateUser("reader", "reader@example.com", "argon2id", "x", []string{"user"}, "active")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	for _, id := range []string{b1, b2} {
		if err := s.SetUserBookState(&database.UserBookState{UserID: u.ID, BookID: id, Status: database.UserBookStatusInProgress, ProgressPct: 30}); err != nil {
			t.Fatalf("SetUserBookState: %v", err)
		}
		if err := s.SetUserPosition(u.ID, id, "seg", 30); err != nil {
			t.Fatalf("SetUserPosition: %v", err)
		}
	}
	p := &Plugin{}
	rep := &fakeReporter{}
	groups := []itunesservice.HealGroup{{Title: "Merged Book", PIDs: []string{"p1", "p2"}}}
	snap, err := p.buildRegroupSnapshot(context.Background(), s, rgRoot, rep)
	if err != nil {
		t.Fatalf("buildRegroupSnapshot: %v", err)
	}
	plan := itunesservice.PlanRegroup(groups, snap)
	if len(plan.DeleteBooks) != 1 || plan.Groups[0].FreshBook {
		t.Fatalf("plan deletes=%d fresh=%v, want 1 delete onto an existing target", len(plan.DeleteBooks), plan.Groups[0].FreshBook)
	}
	return s, u, plan, rep
}

// #3769 review SF2: among the applied targets that took a deleted book's
// files, one ABS lists is preferred over one with more of its files; with
// none listed, the most files wins and the choice says it is not listed.
func TestRegroupStateTarget_PrefersListedTarget(t *testing.T) {
	plan := itunesservice.RegroupPlan{Groups: []itunesservice.GroupAction{
		{Moves: []itunesservice.FileMove{{From: "doomed"}, {From: "doomed"}, {From: "doomed"}}},
		{Moves: []itunesservice.FileMove{{From: "doomed"}}},
		{Moves: []itunesservice.FileMove{{From: "other"}}},
	}}
	targets := map[int]string{0: "hidden-big", 1: "listed-small", 2: "listed-unrelated"}
	isListed := func(id string) bool { return strings.HasPrefix(id, "listed") }

	got, listed := regroupStateTarget(plan, targets, "doomed", isListed)
	if got != "listed-small" || !listed {
		t.Fatalf("target = %q listed=%v, want listed-small (the listed one, not the one with more files)", got, listed)
	}
	got, listed = regroupStateTarget(plan, targets, "doomed", func(string) bool { return false })
	if got != "hidden-big" || listed {
		t.Fatalf("target = %q listed=%v, want hidden-big, not listed", got, listed)
	}
	if got, _ := regroupStateTarget(plan, targets, "nobody", isListed); got != "" {
		t.Fatalf("target for a book no group took files from = %q, want none", got)
	}
}

// #3769 review SF2: a carry onto a target ABS does not list still happens --
// the state is not dropped -- and is reported: counted in the APPLIED line,
// named in a warning and returned for the run's final status. A listed
// target is not reported.
func TestITunesRegroupApply_CarryToUnlistedTargetIsReported(t *testing.T) {
	for _, makeListed := range []bool{false, true} {
		s, u, plan, rep := regroupWithState(t)
		loser, target := plan.DeleteBooks[0], plan.Groups[0].Target
		if makeListed {
			if _, err := s.ModifyBook(target, func(b *database.Book) error {
				st, primary := "organized", true
				b.LibraryState, b.IsPrimaryVersion = &st, &primary
				return nil
			}); err != nil {
				t.Fatalf("ModifyBook: %v", err)
			}
		}
		got, err := (&Plugin{}).applyRegroupPlan(context.Background(), s, plan, rgRoot, rep)
		if err != nil {
			t.Fatalf("applyRegroupPlan: %v", err)
		}
		if pos, _ := s.ListUserPositionsForBook(u.ID, target); len(pos) == 0 {
			t.Fatalf("listed=%v: the state was not carried to %s", makeListed, target)
		}
		logs := strings.Join(rep.logs, "\n")
		if makeListed {
			if len(got.StateCarriedUnlisted) != 0 || !strings.Contains(logs, "state-carried-unlisted=0") {
				t.Fatalf("listed target reported as unlisted: %v\n%s", got.StateCarriedUnlisted, logs)
			}
			continue
		}
		if len(got.StateCarriedUnlisted) != 1 || got.StateCarriedUnlisted[0] != loser {
			t.Fatalf("StateCarriedUnlisted = %v, want [%s]", got.StateCarriedUnlisted, loser)
		}
		if !strings.Contains(logs, "state-carried-unlisted=1") || !strings.Contains(logs, "first ids: "+loser) {
			t.Fatalf("unlisted carry not reported:\n%s", logs)
		}
	}
}

// rgLateWriteStore writes a reader's position onto book `on` right after the
// apply's first state probe read it, as a client listening between the probe
// and the delete would.
type rgLateWriteStore struct {
	*database.PebbleStore
	on, user string
	once     sync.Once
}

func (s *rgLateWriteStore) ListUserPositionsForBook(userID, bookID string) ([]database.UserPosition, error) {
	pos, err := s.PebbleStore.ListUserPositionsForBook(userID, bookID)
	if bookID == s.on && userID == s.user {
		s.once.Do(func() { err = s.PebbleStore.SetUserPosition(userID, bookID, "seg", 77) })
	}
	return pos, err
}

// #3769 review NIT a: state that lands on a projected-empty book after the
// probe said it had none is caught by the re-check made under the merge lock
// right before the delete; the book and the new position are kept.
func TestITunesRegroupApply_StateAfterProbeRefusesDelete(t *testing.T) {
	s := regroupStore(t)
	b1 := seedBook(t, s, "Frag A")
	b2 := seedBook(t, s, "Frag B")
	seedFilePID(t, s, b1, "p1")
	seedFilePID(t, s, b2, "p2")
	u, err := s.CreateUser("reader", "reader@example.com", "argon2id", "x", []string{"user"}, "active")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	p := &Plugin{}
	rep := &fakeReporter{}
	snap, err := p.buildRegroupSnapshot(context.Background(), s, rgRoot, rep)
	if err != nil {
		t.Fatalf("buildRegroupSnapshot: %v", err)
	}
	plan := itunesservice.PlanRegroup([]itunesservice.HealGroup{{Title: "Merged Book", PIDs: []string{"p1", "p2"}}}, snap)
	if len(plan.DeleteBooks) != 1 {
		t.Fatalf("plan deletes=%d, want 1", len(plan.DeleteBooks))
	}
	loser := plan.DeleteBooks[0]
	ls := &rgLateWriteStore{PebbleStore: s, on: loser, user: u.ID}
	if _, err := p.applyRegroupPlan(context.Background(), ls, plan, rgRoot, rep); err != nil {
		t.Fatalf("applyRegroupPlan: %v", err)
	}
	if b, _ := s.GetBookByID(loser); b == nil {
		t.Fatalf("book %s deleted though a position landed on it before the delete", loser)
	}
	pos, _ := s.ListUserPositionsForBook(u.ID, loser)
	if len(pos) != 1 || pos[0].PositionSeconds != 77 {
		t.Fatalf("positions on %s = %+v, want the late write kept", loser, pos)
	}
	if logs := strings.Join(rep.logs, "\n"); !strings.Contains(logs, "state-reappeared=1") {
		t.Fatalf("refusal not counted:\n%s", logs)
	}
}
