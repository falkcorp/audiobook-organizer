// file: internal/usermerge/undo_identity_test.go
// version: 0.1.0
// guid: 9d2e6b84-1f7a-4c53-8e0b-5a43d7c1f268
// last-edited: 2026-11-22

package usermerge

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/storage"
)

// F4-08 B10p: the undo of an identity-era merge over the fake core. The real
// core's own reversal is tested in core/auth (unmerge_test.go); what is pinned
// here is what books does around it: ONE batch for the inverse books rows, the
// journal and core's StageUserUnmerge, the refusals with their codes, the
// sweep_pending rule and the resume that clears it.

// unmergeIdentity is the sign-in id the fixture's receipt names. The fake core
// has no receipt of its own (its plan is empty), so armReceipt writes what a
// real core's plan would have put in the journal.
const unmergeIdentity = "gotham-sso/batman-subject-0001"

// mergedAndArmed applies the merge and arms the journal with the receipt, the
// way a real core's plan leaves it.
func (f *idFixture) mergedAndArmed() *Journal {
	f.t.Helper()
	p := f.plan()
	if _, err := f.svc.Apply(context.Background(), f.applyReq(p), nil); err != nil {
		f.t.Fatalf("Apply: %v", err)
	}
	j := f.journalOnDisk()
	j.Core.IdentityIDs = []string{unmergeIdentity}
	j.Core.APIKeyVersions = map[string]int64{"key-robin": 4}
	if err := f.svc.save(j); err != nil {
		f.t.Fatal(err)
	}
	return f.journalOnDisk()
}

// setPending writes the journal's sweep_pending marker.
func (f *idFixture) setPending(j *Journal, pending bool) *Journal {
	f.t.Helper()
	j.Core.SweepPending = pending
	if err := f.svc.save(j); err != nil {
		f.t.Fatal(err)
	}
	return f.journalOnDisk()
}

func (f *idFixture) undoReq(j *Journal) UndoRequest {
	return UndoRequest{JournalID: j.ID, ActorID: f.a.ID, Principal: f.pr}
}

// wantRefusal fails unless err is an UndoRefusal with code.
func wantRefusal(t *testing.T, err error, code string) {
	t.Helper()
	var ur *UndoRefusal
	if !errors.As(err, &ur) || !errors.Is(err, ErrUndoRefused) {
		t.Fatalf("err = %v, want an UndoRefusal %q", err, code)
	}
	if ur.Code != code {
		t.Fatalf("refusal code = %q, want %q (%v)", ur.Code, code, err)
	}
}

func TestUndoIdentity_OneBatchRestoresBooksAndCore(t *testing.T) {
	f := newIDFixture(t)
	before := f.snapshot()
	j := f.mergedAndArmed()
	if f.core.user(f.b.ID).MergedInto == "" {
		t.Fatal("the merge did not land")
	}

	undone, err := f.svc.Undo(context.Background(), f.undoReq(j), nil)
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if d := diff(before, f.snapshot()); len(d) != 0 {
		t.Errorf("the undo left the books rows different from before the merge: %v", d)
	}
	if got := f.core.user(f.b.ID); got.MergedInto != "" {
		t.Errorf("core's marker is still set: %+v", got)
	}
	if n := f.core.unstaged.Load(); n != 1 {
		t.Errorf("StageUserUnmerge ran %d times, want 1", n)
	}
	if n := f.core.unafter.Load(); n != 1 {
		t.Errorf("core's AfterCommit ran %d times, want 1", n)
	}
	if undone.Status != StatusUndone || undone.UndoneAt == nil {
		t.Errorf("returned journal = %+v", undone.Summary())
	}
	disk := f.journalOnDisk()
	if disk.Status != StatusUndone || disk.UndoneAt == nil || len(disk.Ops) != len(j.Ops) || disk.Core == nil {
		t.Errorf("journal on disk = %+v", disk.Summary())
	}

	// What books handed core is what the journal recorded.
	rec := f.core.lastRec
	if string(rec.Source) != f.b.ID || string(rec.Target) != f.a.ID ||
		rec.SourceVersionAfter != j.Core.SourceVersionBefore+1 ||
		rec.SourceStatusBefore != coreauth.StatusActive ||
		rec.SourceEmailBefore != "absorbed@admins.example.com" ||
		rec.EmailOutcome != coreauth.MergeEmailKept ||
		len(rec.RolesAdded) != 1 || rec.RolesAdded[0] != "editor" ||
		!slices.Equal(rec.Identities, []string{unmergeIdentity}) ||
		len(rec.APIKeys) != 1 || rec.APIKeys["key-robin"] != 4 {
		t.Errorf("the unmerge record = %+v", rec)
	}
	// The actor is the principal the request carried.
	if n := len(f.core.seen); n == 0 {
		t.Error("core never saw a principal")
	}
}

// TestUndoIdentity_RefusalAtStageTimeLeavesBooksAndJournalUntouched is the
// partial-reversal mutant's killer. Core's refusal that can only come while the
// batch is being staged (a key changed between the plan and the stage) arrives
// AFTER the inverse books ops are in the batch. One batch means nothing landed:
// the books rows, the journal bytes and core's marker are exactly as they were.
// Two batches (books first, core second) would have put the rows back and left
// core merged.
func TestUndoIdentity_RefusalAtStageTimeLeavesBooksAndJournalUntouched(t *testing.T) {
	f := newIDFixture(t)
	j := f.mergedAndArmed()
	before := f.snapshot()
	journalBefore := f.journalRows()[0].Value
	f.core.unmergeStageErr = &coreauth.MergeRefusal{Code: coreauth.RefusedKeyMoved, Detail: "a key the merge moved changed since"}

	_, err := f.svc.Undo(context.Background(), f.undoReq(j), nil)
	wantRefusal(t, err, coreauth.RefusedKeyMoved)
	if f.core.unstaged.Load() == 0 {
		t.Fatal("the refusal did not come from StageUserUnmerge: the test proves nothing")
	}
	after := f.snapshot()
	if d := diff(before, after); len(d) != 0 {
		t.Errorf("a refused undo changed books rows: %v", d)
	}
	for k, v := range before {
		if after[k] != v {
			t.Errorf("a refused undo changed %q", k)
		}
	}
	if len(after) != len(before) {
		t.Errorf("a refused undo changed the key count: %d -> %d", len(before), len(after))
	}
	if !bytes.Equal(f.journalRows()[0].Value, journalBefore) {
		t.Error("a refused undo rewrote the journal")
	}
	if got := f.core.user(f.b.ID); got.MergedInto == "" {
		t.Error("core's marker was removed by a refused undo")
	}
	if f.core.unafter.Load() != 0 {
		t.Error("AfterCommit ran for a refused undo")
	}
}

func TestUndoIdentity_CoreRefusalsAnswerWithTheirCodeAndWriteNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"a signed-in change to the source", &coreauth.MergeRefusal{Code: coreauth.RefusedSourceChanged, Detail: "x"}, coreauth.RefusedSourceChanged},
		{"a key moved on", &coreauth.MergeRefusal{Code: coreauth.RefusedKeyMoved, Detail: "x"}, coreauth.RefusedKeyMoved},
		{"a sign-in moved on", &coreauth.MergeRefusal{Code: coreauth.RefusedSignInMoved, Detail: "x"}, coreauth.RefusedSignInMoved},
		{"the address is claimed", &coreauth.MergeRefusal{Code: coreauth.RefusedEmailClaimed, Detail: "x"}, coreauth.RefusedEmailClaimed},
		{"never merged", &coreauth.MergeRefusal{Code: coreauth.RefusedNotMerged, Detail: "x"}, coreauth.RefusedNotMerged},
		{"the last admin", coreauth.ErrLastAdmin, CodeLastAdmin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newIDFixture(t)
			j := f.mergedAndArmed()
			before := f.snapshot()
			f.core.unmergePlanErr = tc.err
			_, err := f.svc.Undo(context.Background(), f.undoReq(j), nil)
			wantRefusal(t, err, tc.code)
			if f.core.unstaged.Load() != 0 {
				t.Error("a plan that refused was still staged")
			}
			for k, v := range f.snapshot() {
				if before[k] != v {
					t.Errorf("a refused undo changed %q", k)
				}
			}
			if f.core.user(f.b.ID).MergedInto == "" {
				t.Error("core's marker was removed")
			}
			// CheckUndo is the same answer on the request, before anything runs.
			wantRefusal(t, f.svc.CheckUndo(f.ctx(), j.ID), tc.code)
		})
	}
}

// A denial or a failure of core is not a refusal with a code: it is returned as
// it is (the handler maps ErrDenied to 403 and the rest to 500).
func TestUndoIdentity_ADenialOrAFailureIsNotARefusal(t *testing.T) {
	f := newIDFixture(t)
	j := f.mergedAndArmed()
	f.core.unmergePlanErr = coreauth.ErrDenied
	_, err := f.svc.Undo(context.Background(), f.undoReq(j), nil)
	if !errors.Is(err, coreauth.ErrDenied) || errors.Is(err, ErrUndoRefused) {
		t.Errorf("a denial came back as %v", err)
	}
	boom := errors.New("core is down")
	f.core.unmergePlanErr = boom
	_, err = f.svc.Undo(context.Background(), f.undoReq(j), nil)
	if !errors.Is(err, boom) || errors.Is(err, ErrUndoRefused) {
		t.Errorf("a failure came back as %v", err)
	}
}

func TestUndoIdentity_SweepPendingRefuses(t *testing.T) {
	f := newIDFixture(t)
	j := f.setPending(f.mergedAndArmed(), true)
	before := f.snapshot()

	_, err := f.svc.Undo(context.Background(), f.undoReq(j), nil)
	wantRefusal(t, err, CodeSweepPending)
	wantRefusal(t, f.svc.CheckUndo(f.ctx(), j.ID), CodeSweepPending)
	if n := f.core.unplanned.Load(); n != 0 {
		t.Errorf("core was asked to plan an undo of a merge whose sweep is pending (%d times)", n)
	}
	for k, v := range f.snapshot() {
		if before[k] != v {
			t.Errorf("a refused undo changed %q", k)
		}
	}
	if f.core.user(f.b.ID).MergedInto == "" {
		t.Error("core's marker was removed")
	}
}

// A journal written before the identity flip has no core half: it stays
// permanent, whatever its status, and core is never asked.
func TestUndoIdentity_APreFlipJournalStaysPermanent(t *testing.T) {
	f := newIDFixture(t)
	j := f.mergedAndArmed()
	j.Core = nil
	if err := f.svc.save(j); err != nil {
		t.Fatal(err)
	}
	before := f.snapshot()
	_, err := f.svc.Undo(context.Background(), f.undoReq(j), nil)
	if !errors.Is(err, ErrMergePermanent) {
		t.Fatalf("Undo = %v, want ErrMergePermanent", err)
	}
	if err := f.svc.CheckUndo(f.ctx(), j.ID); !errors.Is(err, ErrMergePermanent) {
		t.Errorf("CheckUndo = %v", err)
	}
	if err := f.svc.CheckAction(ActionUndo, j.ID); !errors.Is(err, ErrMergePermanent) {
		t.Errorf("CheckAction = %v", err)
	}
	if f.core.unplanned.Load()+f.core.unstaged.Load() != 0 {
		t.Error("core was asked about a pre-flip journal")
	}
	for k, v := range f.snapshot() {
		if before[k] != v {
			t.Errorf("changed %q", k)
		}
	}
}

// A journal that does not record which sign-ins and keys moved cannot say which
// of the survivor's credentials are the merge's: it is refused, never guessed.
func TestUndoIdentity_AJournalWithoutTheReceiptIsRefused(t *testing.T) {
	f := newIDFixture(t)
	p := f.plan()
	if _, err := f.svc.Apply(context.Background(), f.applyReq(p), nil); err != nil {
		t.Fatal(err)
	}
	j := f.journalOnDisk() // the fake core's plan carries no receipt
	_, err := f.svc.Undo(context.Background(), f.undoReq(j), nil)
	wantRefusal(t, err, CodeReceiptMissing)
	if f.core.unplanned.Load() != 0 {
		t.Error("core was asked to plan from a journal with no receipt")
	}
}

func TestUndoIdentity_OnlyAnAppliedMergeIsUndone(t *testing.T) {
	f := newIDFixture(t)
	j := f.mergedAndArmed()
	if _, err := f.svc.Undo(context.Background(), f.undoReq(j), nil); err != nil {
		t.Fatal(err)
	}
	before := f.snapshot()
	_, err := f.svc.Undo(context.Background(), f.undoReq(j), nil)
	if !errors.Is(err, ErrJournalState) {
		t.Errorf("a second undo = %v, want ErrJournalState", err)
	}
	for k, v := range f.snapshot() {
		if before[k] != v {
			t.Errorf("a refused undo changed %q", k)
		}
	}
}

func TestUndoIdentity_NeedsTheActingAdminsPrincipal(t *testing.T) {
	f := newIDFixture(t)
	j := f.mergedAndArmed()
	before := f.snapshot()
	for name, req := range map[string]UndoRequest{
		"no principal":   {JournalID: j.ID, ActorID: f.a.ID},
		"no actor":       {JournalID: j.ID, Principal: f.pr},
		"another actor":  {JournalID: j.ID, ActorID: f.b.ID, Principal: f.pr},
		"neither at all": {JournalID: j.ID},
	} {
		if _, err := f.svc.Undo(context.Background(), req, nil); !errors.Is(err, ErrNoPrincipal) {
			t.Errorf("%s: Undo = %v, want ErrNoPrincipal", name, err)
		}
	}
	if f.core.unplanned.Load() != 0 {
		t.Error("core was asked to plan for an undo with no principal of its own")
	}
	for k, v := range f.snapshot() {
		if before[k] != v {
			t.Errorf("changed %q", k)
		}
	}
}

// A row the survivor wrote after the merge is left as it is and reported, as
// HEAD's undo does, and core's half still commits in the same batch.
func TestUndoIdentity_ABooksRowWrittenSinceIsLeftAndReported(t *testing.T) {
	f := newIDFixture(t)
	j := f.mergedAndArmed()
	moved := "pref:" + f.a.ID + ":speed"
	f.put(moved, "{\"key\":\"speed\",\"value\":\"2.0\"}")

	undone, err := f.svc.Undo(context.Background(), f.undoReq(j), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.get(moved); !bytes.Contains([]byte(got), []byte("2.0")) {
		t.Errorf("the undo overwrote a later write: %s", got)
	}
	if !slices.ContainsFunc(undone.UndoConflicts, func(s string) bool { return len(s) >= len(moved) && s[:len(moved)] == moved }) {
		t.Errorf("undo conflicts = %v, want %s named", undone.UndoConflicts, moved)
	}
	if got := f.journalOnDisk(); len(got.UndoConflicts) != len(undone.UndoConflicts) || got.Status != StatusUndone {
		t.Errorf("the journal on disk does not carry the conflicts: %+v", got.Summary())
	}
	if f.core.user(f.b.ID).MergedInto != "" {
		t.Error("core's half did not commit beside the conflicts")
	}
}

// A row that moved between classifying and committing is a conflict, not a
// failure: the undo classifies again, up to undoRetries times.
func TestUndoIdentity_ARaceIsRetriedAndThenReported(t *testing.T) {
	f := newIDFixture(t)
	j := f.mergedAndArmed()
	real := f.svc.idMerger
	var calls atomic.Int32
	f.svc.idMerger = decorated{IdentityUserMerger: real, apply: func(ops []database.UserMergeOp, key string, journal []byte, stage func(*storage.Batch) error) error {
		if calls.Add(1) == 1 {
			return database.ErrUserMergeConflict
		}
		return real.ApplyUserMergeIdentity(ops, key, journal, stage)
	}}
	if _, err := f.svc.Undo(context.Background(), f.undoReq(j), nil); err != nil {
		t.Fatalf("a single race failed the undo: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("commit attempts = %d, want 2", calls.Load())
	}

	// Always racing: the undo gives up with a code, having written nothing.
	g := newIDFixture(t)
	jg := g.mergedAndArmed()
	before := g.snapshot()
	g.svc.idMerger = decorated{IdentityUserMerger: g.svc.idMerger, apply: func([]database.UserMergeOp, string, []byte, func(*storage.Batch) error) error {
		return database.ErrUserMergeConflict
	}}
	_, err := g.svc.Undo(context.Background(), g.undoReq(jg), nil)
	wantRefusal(t, err, CodeUndoConflict)
	for k, v := range g.snapshot() {
		if before[k] != v {
			t.Errorf("a given-up undo changed %q", k)
		}
	}
}

func TestUndoIdentity_TooLargeAnswersBeforeAnythingIsStaged(t *testing.T) {
	f := newIDFixture(t)
	j := f.mergedAndArmed()
	before := f.snapshot()
	f.svc.batchCap = 1
	_, err := f.svc.Undo(context.Background(), f.undoReq(j), nil)
	if !errors.Is(err, ErrMergeTooLarge) {
		t.Fatalf("Undo = %v, want ErrMergeTooLarge", err)
	}
	if f.core.unstaged.Load() != 0 {
		t.Error("an undo too large to commit was still staged")
	}
	for k, v := range f.snapshot() {
		if before[k] != v {
			t.Errorf("changed %q", k)
		}
	}
}

// Resume under identity: the one thing left to resume is the straggler sweep.

func TestResumeIdentity_SweepsStragglersAndClearsTheMarker(t *testing.T) {
	f := newIDFixture(t)
	j := f.setPending(f.mergedAndArmed(), true)
	late := "pref:" + f.b.ID + ":late"
	f.put(late, "{\"key\":\"late\",\"value\":\"1\"}")
	plansBefore := f.core.planned.Load()

	got, err := f.svc.Resume(context.Background(), j.ID, nil)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if f.get(late) != "" || f.get("pref:"+f.a.ID+":late") == "" {
		t.Error("the straggler row did not move: a resume that skips the sweep leaves it behind")
	}
	if got.Core == nil || got.Core.SweepPending {
		t.Errorf("returned journal still pending: %+v", got.Core)
	}
	disk := f.journalOnDisk()
	if disk.Core.SweepPending || disk.Sweeps == 0 || len(disk.Ops) <= len(j.Ops) || disk.Status != StatusApplied {
		t.Errorf("journal on disk = %+v, ops %d (was %d)", disk.Summary(), len(disk.Ops), len(j.Ops))
	}
	if n := f.core.planned.Load(); n != plansBefore {
		t.Errorf("a sweep resume planned the merge in core again (%d plans, was %d)", n, plansBefore)
	}
}

// The marker is cleared only by a sweep that ran clean: a sweep that fails again
// fails loudly and leaves it set.
func TestResumeIdentity_AFailingSweepStaysLoudAndPending(t *testing.T) {
	f := newIDFixture(t)
	j := f.setPending(f.mergedAndArmed(), true)
	f.put("pref:"+f.b.ID+":late", "{\"key\":\"late\",\"value\":\"1\"}")
	fail := &atomic.Bool{}
	fail.Store(true)
	f.svc.merger = failingMerger{UserMerger: f.svc.merger, fail: fail}

	_, err := f.svc.Resume(context.Background(), j.ID, nil)
	var se *SweepError
	if !errors.As(err, &se) || !errors.Is(err, ErrSweepIncomplete) {
		t.Fatalf("Resume = %v, want a *SweepError", err)
	}
	if !f.journalOnDisk().Core.SweepPending {
		t.Error("a failed sweep cleared the marker")
	}
	if f.get("pref:"+f.b.ID+":late") == "" {
		t.Error("the straggler moved although its write failed")
	}
}

// Every other identity-era Resume stays merge_permanent and touches nothing.
func TestResumeIdentity_EveryOtherJournalStaysPermanent(t *testing.T) {
	f := newIDFixture(t)
	j := f.mergedAndArmed() // applied, sweep clean
	before := f.snapshot()
	if _, err := f.svc.Resume(context.Background(), j.ID, nil); !errors.Is(err, ErrMergePermanent) {
		t.Errorf("Resume of a clean applied merge = %v", err)
	}
	if err := f.svc.CheckAction(ActionResume, j.ID); !errors.Is(err, ErrMergePermanent) {
		t.Errorf("CheckAction(resume) = %v", err)
	}
	// Pending, but not applied (an undone merge has nothing to sweep).
	f.setPending(j, true)
	if _, err := f.svc.Undo(context.Background(), f.undoReq(j), nil); err == nil {
		t.Fatal("an undo of a pending merge succeeded")
	}
	for k, v := range f.snapshot() {
		if k == journalKey(j.ID) {
			continue
		}
		if before[k] != v {
			t.Errorf("changed %q", k)
		}
	}
}

func TestResumeIdentity_ANoCoreJournalIsPermanentAndOnlyRead(t *testing.T) {
	f := newIDFixture(t)
	store := &touchedStore{journalRows: journalRows{rows: journalRow("j1", StatusApplying)}}
	svc := &Service{store: store, identityFlipped: f.svc.identityFlipped, core: f.svc.core, idMerger: f.svc.idMerger}
	if _, err := svc.Resume(context.Background(), "j1", nil); !errors.Is(err, ErrMergePermanent) {
		t.Fatalf("Resume = %v", err)
	}
	if n := store.calls.Load(); n != 1 {
		t.Errorf("Resume touched the store %d times, want the one journal read", n)
	}
}

// An undo is refused while the sweep is pending and works after the resume
// cleared it; the straggler the sweep moved is put back with the rest.
func TestUndoIdentity_WorksAfterTheResumeClearedThePendingSweep(t *testing.T) {
	f := newIDFixture(t)
	before := f.snapshot()
	j := f.setPending(f.mergedAndArmed(), true)
	late := "pref:" + f.b.ID + ":late"
	f.put(late, "{\"key\":\"late\",\"value\":\"1\"}")
	before[late] = f.get(late)

	_, err := f.svc.Undo(context.Background(), f.undoReq(j), nil)
	wantRefusal(t, err, CodeSweepPending)

	if _, err := f.svc.Resume(context.Background(), j.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Undo(context.Background(), f.undoReq(f.journalOnDisk()), nil); err != nil {
		t.Fatalf("Undo after the resume: %v", err)
	}
	if d := diff(before, f.snapshot()); len(d) != 0 {
		t.Errorf("rows differ from before the merge (straggler included): %v", d)
	}
	if f.core.user(f.b.ID).MergedInto != "" {
		t.Error("core still merged")
	}
}

func TestCheckMergeAction_IdentityEraJournals(t *testing.T) {
	mk := func(core string) *Service {
		raw := `{"id":"j1","status":"applied","survivor_id":"bruce","absorbed_id":"dick"` + core + `}`
		return permanentSvc(map[string][]byte{database.UserMergeJournalPrefix + "j1": []byte(raw)})
	}
	clean := mk(`,"core":{"era":"identity"}`)
	pending := mk(`,"core":{"era":"identity","sweep_pending":true}`)
	preflip := mk(``)
	for _, tc := range []struct {
		name   string
		svc    *Service
		action Action
		want   error
	}{
		{"undo of a clean identity journal", clean, ActionUndo, nil},
		{"resume of a clean identity journal", clean, ActionResume, ErrMergePermanent},
		{"undo of a pending identity journal", pending, ActionUndo, nil},
		{"resume of a pending identity journal", pending, ActionResume, nil},
		{"undo of a pre-flip journal", preflip, ActionUndo, ErrMergePermanent},
		{"resume of a pre-flip journal", preflip, ActionResume, ErrMergePermanent},
	} {
		got := tc.svc.CheckMergeAction(tc.action, "j1", true)
		if !errors.Is(got, tc.want) || (tc.want == nil) != (got == nil) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}
