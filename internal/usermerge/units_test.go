// file: internal/usermerge/units_test.go
// version: 1.1.0
// guid: 4e8b1c63-d2a7-4905-b1f4-6a09c3e7d582
// last-edited: 2026-10-23

package usermerge

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// assertIndexMatchesRow fails unless (user, book)'s status index holds exactly
// the entry for the status of the ubs: row that is there (or none, with no row).
func assertIndexMatchesRow(t *testing.T, f *fixture, user, book string) {
	t.Helper()
	status := ""
	if raw := f.get(database.UserBookStateKey(user, book)); raw != "" {
		var st database.UserBookState
		if err := json.Unmarshal([]byte(raw), &st); err != nil {
			t.Fatalf("ubs row: %v", err)
		}
		status = st.Status
	}
	for _, st := range []string{database.UserBookStatusUnstarted, database.UserBookStatusInProgress, database.UserBookStatusFinished, database.UserBookStatusAbandoned} {
		has := f.get(database.UserBookStateStatusKey(user, st, book)) != ""
		if want := st == status; has != want {
			t.Errorf("status index %s for %s/%s present=%v, want %v (row status %q)", st, user, book, has, want, status)
		}
	}
}

// An API key is rewritten by the middleware on every request. A key used after
// the merge goes back to the absorbed account with its use counters, and its
// index entries go with it: the row and the index never disagree.
func TestUndo_KeyUsedAfterTheMergeGoesBackWithItsCounters(t *testing.T) {
	f := newFixture(t)
	j, err := f.apply(true)
	if err != nil {
		t.Fatal(err)
	}
	a, b := f.a.ID, f.b.ID
	usedAt := time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)
	for range 2 {
		if err := f.s.TouchAPIKeyLastUsed("kB", usedAt, "192.0.2.1"); err != nil {
			t.Fatal(err)
		}
	}
	undone, err := f.svc.Undo(context.Background(), UndoRequest{JournalID: j.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(undone.UndoConflicts) != 0 {
		t.Errorf("a key that was only used is not a conflict, got %v", undone.UndoConflicts)
	}
	k, err := f.s.GetAPIKey("kB")
	if err != nil || k == nil {
		t.Fatalf("key: %v %v", k, err)
	}
	if k.UserID != b {
		t.Errorf("key owner = %s, want the absorbed account %s", k.UserID, b)
	}
	if k.UseCount != 2 || k.LastUsedAt == nil || !k.LastUsedAt.Equal(usedAt) || k.LastUsedIP != "192.0.2.1" {
		t.Errorf("key counters = %d %v %q, want the uses since the merge kept", k.UseCount, k.LastUsedAt, k.LastUsedIP)
	}
	if f.get("idx:apikey:user:"+b+":kB") != "1" || f.get("idx:apikey:user:"+a+":kB") != "" {
		t.Errorf("index: B=%q A=%q, want B only", f.get("idx:apikey:user:"+b+":kB"), f.get("idx:apikey:user:"+a+":kB"))
	}
	keysA, _ := f.s.ListAPIKeysForUser(a)
	keysB, _ := f.s.ListAPIKeysForUser(b)
	if len(keysA) != 0 || len(keysB) != 1 {
		t.Errorf("ListAPIKeysForUser: A=%d B=%d, want 0 and 1", len(keysA), len(keysB))
	}
}

// A key that was used between the preview and the apply does not change the
// digest: it covers the owner the merge decides, not the use counters.
func TestApply_KeyUsedBetweenPreviewAndApplyKeepsTheDigest(t *testing.T) {
	f := newFixture(t)
	plan, err := f.svc.BuildPlan(context.Background(), f.planReq())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.TouchAPIKeyLastUsed("kB", time.Now(), "192.0.2.2"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Apply(context.Background(), ApplyRequest{ActorID: f.a.ID, SurvivorID: f.a.ID, AbsorbedID: f.b.ID,
		Digest: plan.Digest, AcknowledgeOwnerIdentity: true}, nil); err != nil {
		t.Fatalf("apply after a key use = %v, want it to go through", err)
	}
	k, _ := f.s.GetAPIKey("kB")
	if k.UserID != f.a.ID || k.UseCount != 1 {
		t.Errorf("key = owner %s, uses %d; want the survivor and the use kept", k.UserID, k.UseCount)
	}
}

// A book the survivor kept listening to after the merge is left whole: its row,
// its status index and its positions stay together, and the absorbed account's
// rows for that book are not put back next to a combined row.
func TestUndo_BookListenedAfterTheMergeIsLeftWhole(t *testing.T) {
	book := "book-only-b"
	t.Run("same status", func(t *testing.T) {
		f := newFixture(t)
		j, err := f.apply(true)
		if err != nil {
			t.Fatal(err)
		}
		a, b := f.a.ID, f.b.ID
		if err := f.s.SetUserBookState(&database.UserBookState{UserID: a, BookID: book,
			Status: database.UserBookStatusFinished, ProgressPct: 100, LastActivityAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		undone, err := f.svc.Undo(context.Background(), UndoRequest{JournalID: j.ID}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if n := countContaining(undone.UndoConflicts, "book:"+book); n != 1 {
			t.Errorf("conflicts naming the book = %d in %v, want exactly 1 (one report for the whole group)", n, undone.UndoConflicts)
		}
		assertIndexMatchesRow(t, f, a, book)
		if f.get(database.UserBookStateKey(a, book)) == "" {
			t.Error("the survivor's row for the book is gone")
		}
		if f.get(uposKey(a, book, "s1")) == "" {
			t.Error("the survivor's position row was reverted next to a kept state row")
		}
		for _, k := range append([]string{database.UserBookStateKey(b, book), uposKey(b, book, "s1")},
			database.UserBookStateStatusKeys(b, book)...) {
			if f.get(k) != "" {
				t.Errorf("%s came back although its group was left as it is", k)
			}
		}
	})
	t.Run("status changed", func(t *testing.T) {
		f := newFixture(t)
		j, err := f.apply(true)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.s.SetUserBookState(&database.UserBookState{UserID: f.a.ID, BookID: book,
			Status: database.UserBookStatusInProgress, ProgressPct: 40, LastActivityAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.Undo(context.Background(), UndoRequest{JournalID: j.ID}, nil); err != nil {
			t.Fatal(err)
		}
		assertIndexMatchesRow(t, f, f.a.ID, book)
		if !strings.Contains(f.get(database.UserBookStateKey(f.a.ID, book)), `"status":"in_progress"`) {
			t.Errorf("the survivor's later state was overwritten: %s", f.get(database.UserBookStateKey(f.a.ID, book)))
		}
	})
	t.Run("an index entry that already disagrees with its row is rebuilt", func(t *testing.T) {
		f := newFixture(t)
		j, err := f.apply(true)
		if err != nil {
			t.Fatal(err)
		}
		a := f.a.ID
		if err := f.s.SetUserBookState(&database.UserBookState{UserID: a, BookID: book,
			Status: database.UserBookStatusFinished, ProgressPct: 100, LastActivityAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		// The split the old per-key undo produced: the row is there, its entry is not,
		// and a stray entry for another status is.
		if err := f.s.DeleteRaw(database.UserBookStateStatusKey(a, database.UserBookStatusFinished, book)); err != nil {
			t.Fatal(err)
		}
		f.put(database.UserBookStateStatusKey(a, database.UserBookStatusAbandoned, book), "1")
		if _, err := f.svc.Undo(context.Background(), UndoRequest{JournalID: j.ID}, nil); err != nil {
			t.Fatal(err)
		}
		assertIndexMatchesRow(t, f, a, book)
	})
	t.Run("an untouched book still goes back whole", func(t *testing.T) {
		f := newFixture(t)
		before := f.snapshot()
		j, err := f.apply(true)
		if err != nil {
			t.Fatal(err)
		}
		// A different book is edited; this one is not.
		if err := f.s.SetUserBookState(&database.UserBookState{UserID: f.a.ID, BookID: "book-only-a",
			Status: database.UserBookStatusFinished, ProgressPct: 100, LastActivityAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		undone, err := f.svc.Undo(context.Background(), UndoRequest{JournalID: j.ID}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(undone.UndoConflicts) != 0 {
			t.Errorf("conflicts = %v, want none (the edit was to a book the merge did not touch)", undone.UndoConflicts)
		}
		for _, k := range []string{database.UserBookStateKey(f.b.ID, book), database.UserBookStateKey(f.b.ID, "book-both")} {
			if f.get(k) == "" || f.get(k) != before[k] {
				t.Errorf("%s did not come back as it was", k)
			}
		}
		assertIndexMatchesRow(t, f, f.b.ID, book)
		assertIndexMatchesRow(t, f, f.a.ID, "book-both")
	})
}

func countContaining(list []string, sub string) int {
	n := 0
	for _, s := range list {
		if strings.Contains(s, sub) {
			n++
		}
	}
	return n
}

// An invite grant is keyed by an email address and an invite by its token. A
// storage conflict reaches operation rows (readable by anyone with
// library.view), so the error says the family and the op's position and never
// the key; the admin-only journal has the key.
func TestApply_ConflictErrorLeavesTheKeyOutOfTheErrorAndInTheJournal(t *testing.T) {
	f := newFixture(t)
	real := f.svc.merger
	f.svc.merger = &racingWriter{UserMerger: real, before: func() {
		// Somebody re-owns the grant after the locked re-plan and before the batch.
		f.put("inviteallow:friend@example.com", database.InviteEmailGrant{UserID: "someone-else"})
	}}
	j, err := f.apply(true)
	if err == nil || !errors.Is(err, database.ErrUserMergeConflict) {
		t.Fatalf("apply = %v, want a storage conflict", err)
	}
	for _, leak := range []string{"@", "inviteallow", "friend", "invite:", "tokB"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("the conflict error exposes %q: %v", leak, err)
		}
	}
	if !strings.Contains(err.Error(), FamInviteGrants) {
		t.Errorf("the error should name the family %q: %v", FamInviteGrants, err)
	}
	if j == nil || !strings.Contains(j.Error, "inviteallow:friend@example.com") {
		t.Errorf("the admin-only journal should record the key, got %q", func() string {
			if j == nil {
				return ""
			}
			return j.Error
		}())
	}
	// What an operation row gets of the journal never carries it.
	if raw, _ := json.Marshal(j.OpResult()); strings.Contains(string(raw), "friend") {
		t.Errorf("the op result exposes the key: %s", raw)
	}
}

type racingWriter struct {
	database.UserMerger
	once   sync.Once
	before func()
}

func (m *racingWriter) ApplyUserMergeOps(ops []database.UserMergeOp, key string, journal []byte) error {
	m.once.Do(m.before)
	return m.UserMerger.ApplyUserMergeOps(ops, key, journal)
}

// A resume plans the writes that are left again: a row the first plan compared
// against changed while the run was stopped, and the resume still finishes
// (before, its frozen before-image failed it forever).
func TestResume_PlansTheRemainderAgainWhenARowChangedWhileStopped(t *testing.T) {
	old := chunkSize
	chunkSize = 1
	t.Cleanup(func() { chunkSize = old })
	f := newFixture(t)
	real := f.svc.merger
	f.svc.merger = &failAfter{UserMerger: real, after: 1}
	j, err := f.apply(true)
	if err == nil || j == nil || j.Status != StatusApplying || j.Cursor < 1 || j.Cursor >= len(j.Ops) {
		t.Fatalf("apply = %v, journal %+v; want one chunk written and a stop", err, j)
	}
	// The survivor listened meanwhile: the listen-seconds counter the frozen set op
	// expects (100) is now 130.
	f.put("stats:user:listen_seconds:"+f.a.ID, "130")
	f.svc.merger = real
	done, err := f.svc.Resume(context.Background(), j.ID, nil)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if done.Status != StatusApplied || !done.Retired {
		t.Fatalf("resumed journal = %+v", done.Summary())
	}
	if got := f.get("stats:user:listen_seconds:" + f.a.ID); got != "180" {
		t.Errorf("listen seconds = %q, want 130 + 50 (counted once)", got)
	}
	if !slices.ContainsFunc(done.Warnings, func(w string) bool { return strings.Contains(w, "planned the") }) {
		t.Errorf("warnings = %v, want the re-plan noted", done.Warnings)
	}
	for k, v := range f.snapshot() {
		if (strings.Contains(k, f.b.ID) || strings.Contains(v, f.b.ID)) && !censusAllowed(k, f.b.ID) {
			t.Errorf("after resume a live row still names the absorbed account: %s", k)
		}
	}
	// And the whole merge still undoes cleanly.
	if undone, err := f.svc.Undo(context.Background(), UndoRequest{JournalID: j.ID}, nil); err != nil || undone.Status != StatusUndone {
		t.Fatalf("undo after a re-planned resume = %v, %v", undone, err)
	}
}

// With no owner_email configured the plan cannot say the owner's sign-in is not
// on the account being merged away, so it asks for the acknowledgement instead
// of waving the merge through.
func TestBuildPlan_NoOwnerEmailFailsClosedOnALinkedSignIn(t *testing.T) {
	f := newFixture(t)
	f.svc.ownerEmail = func() string { return "" }
	plan, err := f.svc.BuildPlan(context.Background(), f.planReq())
	if err != nil {
		t.Fatal(err)
	}
	if plan.OwnerIdentityHolder != HolderNoOwner || !plan.OwnerIdentityAckRequired {
		t.Fatalf("holder %q ack=%v, want no_owner_configured and the acknowledgement required", plan.OwnerIdentityHolder, plan.OwnerIdentityAckRequired)
	}
	before := f.snapshot()
	if _, err := f.apply(false); !errors.Is(err, ErrOwnerIdentityNotAcknowledged) {
		t.Fatalf("apply without the acknowledgement = %v, want ErrOwnerIdentityNotAcknowledged", err)
	}
	if d := diff(before, f.snapshot()); len(d) != 0 {
		t.Errorf("a refused apply wrote %v", d)
	}
	if _, err := f.apply(true); err != nil {
		t.Errorf("apply with the acknowledgement = %v", err)
	}

	t.Run("nothing linked on the absorbed account needs no acknowledgement", func(t *testing.T) {
		f := newFixture(t)
		f.svc.ownerEmail = func() string { return "" }
		for _, k := range []string{"oauthid:idB", "idx:oauth:user:" + f.b.ID + ":idB"} {
			if err := f.s.DeleteRaw(k); err != nil {
				t.Fatal(err)
			}
		}
		plan, err := f.svc.BuildPlan(context.Background(), f.planReq())
		if err != nil {
			t.Fatal(err)
		}
		if plan.OwnerIdentityAckRequired {
			t.Error("an absorbed account with no linked sign-in cannot hold the owner's; no acknowledgement is needed")
		}
	})
	t.Run("a sign-in with no address cannot be checked even when the owner is configured", func(t *testing.T) {
		f := newFixture(t)
		f.put("oauthid:idB", database.OAuthIdentity{ID: "idB", Provider: "google", Subject: "google-67890", UserID: f.b.ID, CreatedAt: f.t0})
		plan, err := f.svc.BuildPlan(context.Background(), f.planReq())
		if err != nil {
			t.Fatal(err)
		}
		if plan.OwnerIdentityHolder == HolderAbsorbed || !plan.OwnerIdentityAckRequired {
			t.Errorf("holder %q ack=%v, want the acknowledgement required for a sign-in that cannot be matched", plan.OwnerIdentityHolder, plan.OwnerIdentityAckRequired)
		}
	})
}

// setEmail writes a user's address and its source directly (and the verified-
// email key for a trusted one), the way the store leaves them.
func (f *fixture) setEmail(id, email, source string) {
	f.t.Helper()
	u, err := f.s.GetUserByID(id)
	if err != nil || u == nil {
		f.t.Fatalf("user %s: %v", id, err)
	}
	if old := strings.ToLower(u.Email); old != "" {
		_ = f.s.DeleteRaw("idx:user:email:" + old)
	}
	u.Email, u.EmailSource = email, source
	f.put("u:"+id, u)
	if database.EmailSourceIsTrusted(source) {
		f.put("idx:user:email:"+strings.ToLower(email), id)
	}
}

// When both accounts hold an address and only the merged-away one proved it,
// the survivor keeps it AND it becomes verified: otherwise nobody would hold it
// verified and the owner's next SSO login with it would create a new account.
func TestBuildPlan_SurvivorTakesTheProofOfAnAddressTheAbsorbedAccountVerified(t *testing.T) {
	const addr = "shared@admins.example.com"
	f := newFixture(t)
	f.setEmail(f.a.ID, addr, database.EmailSourceSelf)
	f.setEmail(f.b.ID, addr, database.EmailSourceAdmin)
	plan, err := f.svc.BuildPlan(context.Background(), f.planReq())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.EmailOutcome, "verified") || strings.Contains(plan.EmailOutcome, "WARNING") {
		t.Errorf("email outcome = %q, want the address to become verified", plan.EmailOutcome)
	}
	j, err := f.apply(true)
	if err != nil {
		t.Fatal(err)
	}
	ua, _ := f.s.GetUserByID(f.a.ID)
	if ua.Email != addr || ua.EmailSource != database.EmailSourceAdmin {
		t.Errorf("survivor = %q (%s), want the address with source admin", ua.Email, ua.EmailSource)
	}
	if got := f.get("idx:user:email:" + addr); got != f.a.ID {
		t.Errorf("verified-email key = %q, want the survivor %s", got, f.a.ID)
	}
	if _, err := f.svc.Undo(context.Background(), UndoRequest{JournalID: j.ID}, nil); err != nil {
		t.Fatal(err)
	}
	ua, _ = f.s.GetUserByID(f.a.ID)
	ub, _ := f.s.GetUserByID(f.b.ID)
	if ua.EmailSource != database.EmailSourceSelf || ub.EmailSource != database.EmailSourceAdmin || ub.Email != addr {
		t.Errorf("after undo survivor source %q, absorbed %q/%q; want self and admin/%s", ua.EmailSource, ub.EmailSource, ub.Email, addr)
	}
	if got := f.get("idx:user:email:" + addr); got != f.b.ID {
		t.Errorf("after undo the verified-email key = %q, want the absorbed account %s again", got, f.b.ID)
	}

	t.Run("a merge never proves an address nobody proved", func(t *testing.T) {
		f := newFixture(t)
		f.setEmail(f.a.ID, addr, database.EmailSourceSelf)
		f.setEmail(f.b.ID, addr, database.EmailSourceSelf)
		if _, err := f.apply(true); err != nil {
			t.Fatal(err)
		}
		if ua, _ := f.s.GetUserByID(f.a.ID); ua.EmailSource != database.EmailSourceSelf {
			t.Errorf("survivor source = %q, want it left self", ua.EmailSource)
		}
	})
	t.Run("a different verified address is released and the preview warns", func(t *testing.T) {
		f := newFixture(t)
		f.setEmail(f.a.ID, "keeper@admins.example.com", database.EmailSourceSelf)
		f.setEmail(f.b.ID, addr, database.EmailSourceAdmin)
		plan, err := f.svc.BuildPlan(context.Background(), f.planReq())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(plan.EmailOutcome, "WARNING") || !strings.Contains(plan.EmailOutcome, "no account holds it") {
			t.Errorf("email outcome = %q, want a warning that nobody will hold the verified address", plan.EmailOutcome)
		}
		if strings.Contains(plan.EmailOutcome, addr) {
			t.Errorf("the outcome text quotes the address: %q", plan.EmailOutcome)
		}
	})
}

// failOnDelete stops the first batch that deletes key, as a restart or a
// cancelled run does.
type failOnDelete struct {
	database.UserMerger
	key   string
	armed bool
}

func (m *failOnDelete) ApplyUserMergeOps(ops []database.UserMergeOp, key string, journal []byte) error {
	if m.armed {
		for _, o := range ops {
			if o.Key == m.key && o.Delete {
				return errors.New("simulated stop")
			}
		}
	}
	return m.UserMerger.ApplyUserMergeOps(ops, key, journal)
}

// The listen-seconds sum is the one write that is not idempotent. With the sum
// and the delete of the counter it came from in different batches, a stop
// between them made a resume add the same seconds again.
func TestResume_ListenSecondsStoppedBetweenSumAndDeleteAreNotCountedTwice(t *testing.T) {
	old := chunkSize
	chunkSize = 1
	t.Cleanup(func() { chunkSize = old })
	f := newFixture(t)
	real := f.svc.merger
	aKey, bKey := "stats:user:listen_seconds:"+f.a.ID, "stats:user:listen_seconds:"+f.b.ID
	f.svc.merger = &failOnDelete{UserMerger: real, key: bKey, armed: true}
	j, err := f.apply(true)
	if err == nil {
		t.Fatal("the run was meant to stop on the batch that deletes the counter")
	}
	if a, b := f.get(aKey), f.get(bKey); (a == "150") != (b == "") {
		t.Fatalf("after the stop A=%q B=%q: the sum and the delete must be in one batch or neither", a, b)
	}
	f.svc.merger = real
	if _, err := f.svc.Resume(context.Background(), j.ID, nil); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got := f.get(aKey); got != "150" {
		t.Errorf("listen seconds after resume = %q, want 150 (50 must be added once)", got)
	}
	if got := f.get(bKey); got != "" {
		t.Errorf("the absorbed counter is still there: %q", got)
	}
}

// The listen-stat sum and its delete sit in one unit in the plan.
func TestBuildPlan_ListenSecondsSumAndDeleteShareAUnit(t *testing.T) {
	f := newFixture(t)
	plan, err := f.svc.BuildPlan(context.Background(), f.planReq())
	if err != nil {
		t.Fatal(err)
	}
	units := map[string]int{}
	for _, op := range plan.ops {
		if op.Family == FamListenStats {
			if op.Unit == "" {
				t.Fatalf("a listen-stats op has no unit")
			}
			units[op.Unit]++
		}
	}
	if len(units) != 1 {
		t.Fatalf("listen-stats units = %v, want exactly one", units)
	}
	for u, n := range units {
		if n != 2 {
			t.Errorf("unit %s holds %d ops, want the delete and the sum", u, n)
		}
	}
}

// Playlists, collections, invites and invite grants are re-owned by an
// owner-field op: an edit made between the preview and the apply is neither
// lost nor a reason to refuse, and undo puts the owner back without touching it.
func TestApply_OwnerFieldFamiliesKeepAnEditMadeAfterThePreview(t *testing.T) {
	for _, tc := range []struct {
		name, key, family, edit, from, to string
	}{
		{"playlist", "upl:plB", FamPlaylists, `"b list"`, `"b list"`, `"renamed after preview"`},
		{"collection", "col:colB", FamCollections, `"b coll"`, `"b coll"`, `"renamed after preview"`},
		{"invite", "invite:tokB", FamInvites, `"newbie"`, `"newbie"`, `"renamed-after-preview"`},
		{"invite grant", "inviteallow:friend@example.com", FamInviteGrants, `"cf_added":true`, `"cf_added":true`, `"cf_added":false`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			plan, err := f.svc.BuildPlan(context.Background(), f.planReq())
			if err != nil {
				t.Fatal(err)
			}
			var op *database.UserMergeOp
			for i := range plan.ops {
				if plan.ops[i].Key == tc.key {
					op = &plan.ops[i]
				}
			}
			if op == nil || !op.IsOwnerOp() || op.Unit != "" {
				t.Fatalf("op for the %s = %+v, want a unit-less owner-field op", tc.name, op)
			}
			before := f.get(tc.key)
			if !strings.Contains(before, tc.from) {
				t.Fatalf("row %s lacks %s", before, tc.from)
			}
			edited := strings.Replace(before, tc.from, tc.to, 1)
			if err := f.s.SetRaw(tc.key, []byte(edited)); err != nil {
				t.Fatal(err)
			}
			j, err := f.svc.Apply(context.Background(), ApplyRequest{ActorID: f.a.ID, SurvivorID: f.a.ID, AbsorbedID: f.b.ID,
				Digest: plan.Digest, AcknowledgeOwnerIdentity: true}, nil)
			if err != nil {
				t.Fatalf("apply after an edit = %v, want it to go through", err)
			}
			got := f.get(tc.key)
			if !strings.Contains(got, tc.to) || !strings.Contains(got, f.a.ID) || strings.Contains(got, f.b.ID) {
				t.Errorf("row after apply = %s, want the edit kept and the owner moved", got)
			}
			if _, err := f.svc.Undo(context.Background(), UndoRequest{JournalID: j.ID}, nil); err != nil {
				t.Fatal(err)
			}
			if got := f.get(tc.key); got != edited {
				t.Errorf("row after undo = %s, want %s", got, edited)
			}
		})
	}
}

// A row whose owner field cannot be rewritten without guessing is refused in the
// preview, not left for the apply to stop on.
func TestBuildPlan_RefusesAnOwnerRowWithARepeatedOwnerField(t *testing.T) {
	f := newFixture(t)
	if err := f.s.SetRaw("upl:plDup", []byte(`{"ID":"plDup","created_by_user_id":"someone-else","created_by_user_id":"`+f.b.ID+`"}`)); err != nil {
		t.Fatal(err)
	}
	plan, err := f.svc.BuildPlan(context.Background(), f.planReq())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Applicable() {
		t.Fatal("a plan with a repeated owner field was applicable")
	}
	if n := countContaining(plan.Refusals, "more than once"); n != 1 {
		t.Errorf("refusals = %v, want one naming the repeated field", plan.Refusals)
	}
	for _, r := range plan.Refusals {
		if strings.Contains(r, "plDup") {
			t.Errorf("refusal quotes the key: %q", r)
		}
	}
}

// When the survivor's state row for a skipped book cannot be decoded, the status
// index cannot be rebuilt from it, and undo says so rather than skipping quietly.
func TestUndo_UnreadableBookStateRowIsReportedNotSkippedQuietly(t *testing.T) {
	f := newFixture(t)
	j, err := f.apply(true)
	if err != nil {
		t.Fatal(err)
	}
	book := "book-only-b"
	if err := f.s.SetRaw(database.UserBookStateKey(f.a.ID, book), []byte(`{not json`)); err != nil {
		t.Fatal(err)
	}
	undone, err := f.svc.Undo(context.Background(), UndoRequest{JournalID: j.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := countContaining(undone.UndoConflicts, "cannot be read"); n != 1 {
		t.Errorf("conflicts = %v, want exactly one saying the state row cannot be read", undone.UndoConflicts)
	}
	if got := f.get(database.UserBookStateKey(f.a.ID, book)); got != `{not json` {
		t.Errorf("the unreadable row was changed: %q", got)
	}
}
