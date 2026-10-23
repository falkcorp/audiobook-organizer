// file: internal/usermerge/service_test.go
// version: 1.1.0
// guid: b17d4c90-3e62-4a85-9d0f-4c8a2e6b7153
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

const ownerAddr = "owner@example.com"

type fixture struct {
	t    *testing.T
	s    *database.PebbleStore
	svc  *Service
	a, b *database.User // survivor, absorbed
	t0   time.Time
}

func (f *fixture) put(key string, v any) {
	f.t.Helper()
	var data []byte
	if s, ok := v.(string); ok {
		data = []byte(s)
	} else {
		var err error
		if data, err = json.Marshal(v); err != nil {
			f.t.Fatal(err)
		}
	}
	if err := f.s.SetRaw(key, data); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) get(key string) string {
	f.t.Helper()
	v, err := f.s.GetRaw(key)
	if err != nil {
		f.t.Fatal(err)
	}
	return string(v)
}

// newFixture builds two admin accounts and gives each a row in every family the
// merge moves, including collisions. Addresses are example.com only.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	s, err := database.NewPebbleStoreInMemory("db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	a, err := s.CreateUser("keeper", "keeper@admins.example.com", "bcrypt", "hash-a", []string{"admin"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateUser("absorbed", "absorbed@admins.example.com", "bcrypt", "hash-b", []string{"admin", "editor"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, s: s, a: a, b: b, t0: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	svc, err := New(s)
	if err != nil {
		t.Fatal(err)
	}
	svc.ownerEmail = func() string { return ownerAddr }
	svc.backfillUserID = func() string { return "" }
	f.svc = svc
	f.seed()
	return f
}

func (f *fixture) state(user, book, status string, pct int, last time.Time) {
	f.put(database.UserBookStateKey(user, book), database.UserBookState{UserID: user, BookID: book, Status: status, ProgressPct: pct, LastActivityAt: last})
	f.put(database.UserBookStateStatusKey(user, status, book), "1")
}

// uposKey is the layout of one playback-position row (database.
// UserPositionBookPrefix plus the segment).
func uposKey(user, book, seg string) string {
	return database.UserPositionBookPrefix(user, book) + seg
}

func (f *fixture) pos(user, book, seg string, sec float64, at time.Time) {
	f.put(uposKey(user, book, seg), database.UserPosition{UserID: user, BookID: book, SegmentID: seg, PositionSeconds: sec, UpdatedAt: at})
}

func (f *fixture) seed() {
	a, b, t0 := f.a.ID, f.b.ID, f.t0
	// Listening state: a collision where B is newer, a B-only finished book, an A-only book.
	f.state(a, "book-both", database.UserBookStatusInProgress, 10, t0)
	f.pos(a, "book-both", "s1", 100, t0)
	f.state(b, "book-both", database.UserBookStatusInProgress, 60, t0.Add(time.Hour))
	f.pos(b, "book-both", "s9", 900, t0.Add(time.Hour))
	f.state(b, "book-only-b", database.UserBookStatusFinished, 100, t0)
	f.pos(b, "book-only-b", "s1", 5, t0)
	f.state(a, "book-only-a", database.UserBookStatusInProgress, 5, t0)
	f.pos(a, "book-only-a", "s1", 7, t0)
	// Bookmarks (the struct has no json tags: the field is UserID).
	f.put("bookmark:"+b+":item1:10", map[string]any{"UserID": b, "ItemID": "item1", "TimeSec": 10, "Title": "b-only"})
	f.put("bookmark:"+b+":item1:20", map[string]any{"UserID": b, "ItemID": "item1", "TimeSec": 20, "Title": "b-collides"})
	f.put("bookmark:"+a+":item1:20", map[string]any{"UserID": a, "ItemID": "item1", "TimeSec": 20, "Title": "a-wins"})
	// Sync aliases: B used one earlier than A did.
	f.put("sync_alias_use:"+a+":al1", map[string]any{"first_used_at": t0})
	f.put("sync_alias_use:"+b+":al1", map[string]any{"first_used_at": t0.Add(-time.Hour)})
	f.put("sync_alias_use:"+b+":al2", map[string]any{"first_used_at": t0})
	f.put("sync_alias_use_seeded:"+b, "1")
	// Preferences.
	f.put("pref:"+a+":theme", "{\"key\":\"theme\",\"value\":\"a\"}")
	f.put("pref:"+b+":theme", "{\"key\":\"theme\",\"value\":\"b\"}")
	f.put("pref:"+b+":speed", "{\"key\":\"speed\",\"value\":\"1.5\"}")
	// Playback progress: B newer on 1, A newer on 2, B only on 3.
	f.put("playp:"+a+":1", database.PlaybackProgress{UserID: a, BookID: 1, UpdatedAt: t0})
	f.put("playp:"+b+":1", database.PlaybackProgress{UserID: b, BookID: 1, UpdatedAt: t0.Add(time.Hour)})
	f.put("playp:"+a+":2", database.PlaybackProgress{UserID: a, BookID: 2, UpdatedAt: t0.Add(time.Hour)})
	f.put("playp:"+b+":2", database.PlaybackProgress{UserID: b, BookID: 2, UpdatedAt: t0})
	f.put("playp:"+b+":3", database.PlaybackProgress{UserID: b, BookID: 3, UpdatedAt: t0})
	// Playback events.
	f.put("playe:"+b+":1:111", database.PlaybackEvent{UserID: b, BookID: 1, CreatedAt: t0})
	f.put("playe:"+a+":1:222", database.PlaybackEvent{UserID: a, BookID: 1, CreatedAt: t0})
	f.put("playe:"+b+":1:222", database.PlaybackEvent{UserID: b, BookID: 1, CreatedAt: t0})
	// Listen counters.
	f.put("stats:user:listen_seconds:"+a, "100")
	f.put("stats:user:listen_seconds:"+b, "50")
	// Sign-ins: A has github, B has google carrying the owner's address.
	f.put("oauthid:idA", database.OAuthIdentity{ID: "idA", Provider: "github", Subject: "gh-12345", Email: "keeper@admins.example.com", UserID: a, CreatedAt: t0})
	f.put("idx:oauth:user:"+a+":idA", "1")
	f.put("oauthid:idB", database.OAuthIdentity{ID: "idB", Provider: "google", Subject: "google-67890", Email: ownerAddr, UserID: b, CreatedAt: t0})
	f.put("idx:oauth:user:"+b+":idB", "1")
	f.put("idx:oauth:sub:google:google-67890", "idB")
	f.put("apikey:kB", database.APIKey{ID: "kB", UserID: b, Name: "b key", TokenHash: "h", Status: "active", CreatedAt: t0})
	f.put("idx:apikey:user:"+b+":kB", "1")
	// Owner-field families.
	f.put("upl:plB", database.UserPlaylist{ID: "plB", Name: "b list", CreatedByUserID: b})
	f.put("col:colB", database.Collection{ID: "colB", Name: "b coll", CreatedByUserID: b})
	f.put("invite:tokB", database.Invite{Token: "tokB", Username: "newbie", CreatedByUserID: b})
	f.put("inviteallow:friend@example.com", database.InviteEmailGrant{UserID: b, CFAdded: true})
	// Another user's rows that must never be touched.
	f.put("upl:plOther", database.UserPlaylist{ID: "plOther", Name: "other", CreatedByUserID: "someone-else"})
}

func (f *fixture) planReq() PlanRequest {
	return PlanRequest{ActorID: f.a.ID, SurvivorID: f.a.ID, AbsorbedID: f.b.ID}
}

func (f *fixture) apply(ack bool) (*Journal, error) {
	f.t.Helper()
	plan, err := f.svc.BuildPlan(context.Background(), f.planReq())
	if err != nil {
		f.t.Fatal(err)
	}
	return f.svc.Apply(context.Background(), ApplyRequest{ActorID: f.a.ID, SurvivorID: f.a.ID, AbsorbedID: f.b.ID,
		Digest: plan.Digest, AcknowledgeOwnerIdentity: ack}, nil)
}

// snapshot is every key and value of the store.
func (f *fixture) snapshot() map[string]string {
	f.t.Helper()
	pairs, err := f.s.ScanPrefix("")
	if err != nil {
		f.t.Fatal(err)
	}
	m := make(map[string]string, len(pairs))
	for _, kv := range pairs {
		m[kv.Key] = string(kv.Value)
	}
	return m
}

// mutable are the key families an apply or undo legitimately changes beyond the
// merge's own ops: the two user rows (status, epoch, UpdatedAt), the journal,
// and the absorbed account's revoked sessions.
func mutable(key string) bool {
	return strings.HasPrefix(key, "u:") || strings.HasPrefix(key, database.UserMergeJournalPrefix) ||
		strings.HasPrefix(key, "sess:") || strings.HasPrefix(key, "idx:sess:") || strings.HasPrefix(key, "idx:user:email:")
}

func diff(before, after map[string]string) (changed []string) {
	for k, v := range after {
		if mutable(k) {
			continue
		}
		if old, ok := before[k]; !ok || old != v {
			changed = append(changed, k)
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok && !mutable(k) {
			changed = append(changed, k)
		}
	}
	slices.Sort(changed)
	return changed
}

func TestBuildPlan_PreviewIsReadOnlyAndReportsTheOwnerIdentity(t *testing.T) {
	f := newFixture(t)
	before := f.snapshot()
	p1, err := f.svc.BuildPlan(context.Background(), f.planReq())
	if err != nil {
		t.Fatal(err)
	}
	p2, err := f.svc.BuildPlan(context.Background(), f.planReq())
	if err != nil {
		t.Fatal(err)
	}
	if after := f.snapshot(); len(diff(before, after)) != 0 || len(before) != len(after) {
		t.Fatalf("a preview wrote: %v", diff(before, after))
	}
	if !p1.Applicable() {
		t.Fatalf("refusals = %v", p1.Refusals)
	}
	if p1.Digest == "" || p1.Digest != p2.Digest {
		t.Errorf("digest %q vs %q: must be stable and non-empty", p1.Digest, p2.Digest)
	}
	if p1.OwnerIdentityHolder != HolderAbsorbed || !p1.OwnerIdentityOnAbsorbed {
		t.Errorf("owner holder = %q, onAbsorbed = %v; want the absorbed account (STOP condition)", p1.OwnerIdentityHolder, p1.OwnerIdentityOnAbsorbed)
	}
	if len(p1.AbsorbedIdentities) != 1 || !p1.AbsorbedIdentities[0].OwnerEmail || strings.Contains(p1.AbsorbedIdentities[0].Subject, "google-67890") {
		t.Errorf("absorbed identities = %+v: want one owner-flagged identity with a masked subject", p1.AbsorbedIdentities)
	}
	want := map[string]FamilyCount{
		FamBookState:        {Rows: 2, Conflicts: 1},
		FamBookmarks:        {Rows: 2, Conflicts: 1},
		FamSyncAliasUse:     {Rows: 2, Conflicts: 1},
		FamPreferences:      {Rows: 2, Conflicts: 1},
		FamPlaybackProgress: {Rows: 3, Conflicts: 2},
		FamPlaybackEvents:   {Rows: 2, Conflicts: 1},
		FamListenStats:      {Rows: 1, Conflicts: 1},
		FamOAuthIdentities:  {Rows: 1},
		FamAPIKeys:          {Rows: 1},
		FamPlaylists:        {Rows: 1},
		FamCollections:      {Rows: 1},
		FamInvites:          {Rows: 1},
		FamInviteGrants:     {Rows: 1},
	}
	for fam, w := range want {
		if got := p1.Counts[fam]; got != w {
			t.Errorf("counts[%s] = %+v, want %+v", fam, got, w)
		}
	}
	if !slices.Equal(p1.RolesGained, []string{"editor"}) {
		t.Errorf("roles gained = %v", p1.RolesGained)
	}
}

func TestApply_RefusesWithoutTheOwnerAcknowledgement(t *testing.T) {
	f := newFixture(t)
	before := f.snapshot()
	if _, err := f.apply(false); !errors.Is(err, ErrOwnerIdentityNotAcknowledged) {
		t.Fatalf("apply = %v, want ErrOwnerIdentityNotAcknowledged", err)
	}
	if after := f.snapshot(); len(diff(before, after)) != 0 {
		t.Errorf("a refused apply wrote %v", diff(before, after))
	}
	if u, _ := f.s.GetUserByID(f.b.ID); u.Status != "active" {
		t.Errorf("a refusal before the lock step left the absorbed account %s", u.Status)
	}
}

func TestApply_MovesEverythingAndLeavesNoLiveRowOfTheAbsorbedAccount(t *testing.T) {
	f := newFixture(t)
	sess, err := f.s.CreateSession(f.b.ID, "192.0.2.10", "test", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	j, err := f.apply(true)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if j.Status != StatusApplied || !j.Retired || j.Cursor != len(j.Ops) {
		t.Fatalf("journal = %+v", j.Summary())
	}
	a, b := f.a.ID, f.b.ID

	// Listening state.
	if got := f.get(database.UserBookStateKey(a, "book-both")); !strings.Contains(got, `"progress_pct":60`) {
		t.Errorf("book-both state = %s, want the newer absorbed progress", got)
	}
	if got := f.get(uposKey(a, "book-both", "s9")); got == "" || !strings.Contains(got, a) {
		t.Errorf("absorbed's newer position did not move, got %q", got)
	}
	if f.get(uposKey(a, "book-both", "s1")) != "" {
		t.Error("the survivor's older position row should have been replaced")
	}
	if got := f.get(database.UserBookStateKey(a, "book-only-b")); !strings.Contains(got, `"status":"finished"`) {
		t.Errorf("book-only-b = %s", got)
	}
	if f.get(database.UserBookStateStatusKey(a, database.UserBookStatusFinished, "book-only-b")) != "1" {
		t.Error("status index entry for the moved finished book is missing")
	}
	if got := f.get(database.UserBookStateKey(a, "book-only-a")); !strings.Contains(got, `"progress_pct":5`) {
		t.Errorf("an A-only book changed: %s", got)
	}
	// Other families.
	if got := f.get("bookmark:" + a + ":item1:20"); !strings.Contains(got, "a-wins") {
		t.Errorf("colliding bookmark = %s, want the survivor's", got)
	}
	if got := f.get("bookmark:" + a + ":item1:10"); !strings.Contains(got, `"UserID":"`+a+`"`) {
		t.Errorf("moved bookmark = %s", got)
	}
	if got := f.get("sync_alias_use:" + a + ":al1"); !strings.Contains(got, f.t0.Add(-time.Hour).Format(time.RFC3339)) {
		t.Errorf("sync alias = %s, want the earlier first use", got)
	}
	if got := f.get("pref:" + a + ":theme"); !strings.Contains(got, `"a"`) {
		t.Errorf("colliding pref = %s", got)
	}
	if got := f.get("playp:" + a + ":1"); !strings.Contains(got, f.t0.Add(time.Hour).Format(time.RFC3339)) {
		t.Errorf("playp 1 = %s, want the newer (absorbed) snapshot", got)
	}
	if got := f.get("playp:" + a + ":2"); !strings.Contains(got, f.t0.Add(time.Hour).Format(time.RFC3339)) {
		t.Errorf("playp 2 = %s, want the survivor's newer snapshot", got)
	}
	if got := f.get("stats:user:listen_seconds:" + a); got != "150" {
		t.Errorf("listen seconds = %q, want 150", got)
	}
	if got := f.get("oauthid:idB"); !strings.Contains(got, `"user_id":"`+a+`"`) || f.get("idx:oauth:user:"+a+":idB") != "1" {
		t.Errorf("identity not re-linked: %s", got)
	}
	if got := f.get("apikey:kB"); !strings.Contains(got, `"user_id":"`+a+`"`) || f.get("idx:apikey:user:"+a+":kB") != "1" {
		t.Errorf("api key not re-linked: %s", got)
	}
	for _, k := range []string{"upl:plB", "col:colB", "invite:tokB"} {
		if got := f.get(k); !strings.Contains(got, `"created_by_user_id":"`+a+`"`) {
			t.Errorf("%s = %s", k, got)
		}
	}
	if got := f.get("inviteallow:friend@example.com"); !strings.Contains(got, `"user_id":"`+a+`"`) || !strings.Contains(got, `"cf_added":true`) {
		t.Errorf("invite grant = %s (CFAdded must survive)", got)
	}
	if got := f.get("upl:plOther"); !strings.Contains(got, "someone-else") {
		t.Errorf("another user's playlist was touched: %s", got)
	}

	// The two accounts.
	ua, _ := f.s.GetUserByID(a)
	ub, _ := f.s.GetUserByID(b)
	if !slices.Contains(ua.Roles, "editor") || ua.Email != "keeper@admins.example.com" {
		t.Errorf("survivor = roles %v email %q", ua.Roles, ua.Email)
	}
	if ub.Status != "locked" || ub.MergedIntoUserID != a || ub.MergedAt == nil || ub.Email != "" || ub.EmailSource != "" {
		t.Errorf("absorbed = %+v", ub)
	}
	if got, _ := f.s.GetSession(sess.ID); got == nil || !got.Revoked {
		t.Errorf("the absorbed account's session was not revoked: %+v", got)
	}

	// Census guard: no live row anywhere may still name the absorbed account's id,
	// in its key or its value, except the history families below. A family a
	// future change adds a user id to fails here until it is handled or listed.
	for k, v := range f.snapshot() {
		if !strings.Contains(k, b) && !strings.Contains(v, b) {
			continue
		}
		if censusAllowed(k, b) {
			continue
		}
		t.Errorf("a live row still names the absorbed account: %s = %.120s", k, v)
	}
}

// censusAllowed lists the rows that keep the absorbed id on purpose: its own
// account row and username index, its revoked sessions, the merge journal, and
// history that records who did something.
func censusAllowed(key, absorbedID string) bool {
	switch {
	case key == "u:"+absorbedID,
		strings.HasPrefix(key, "idx:user:username:"),
		strings.HasPrefix(key, "sess:"), strings.HasPrefix(key, "idx:sess:user:"+absorbedID+":"),
		strings.HasPrefix(key, database.UserMergeJournalPrefix):
		return true
	}
	for _, p := range []string{"act:", "syslog:", "operation:", "opchange:", "opv2:", "opv3:", "file_prov:",
		"admin_debug_edit:", "candfb:", "merge:combine-journal:", "merge:survivor-reconcile:"} {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

func TestApply_RefusesADigestThatIsNotThePreviews(t *testing.T) {
	f := newFixture(t)
	plan, err := f.svc.BuildPlan(context.Background(), f.planReq())
	if err != nil {
		t.Fatal(err)
	}
	// Something the merge would write changes after the preview: the absorbed
	// account's own preference. (A row of the survivor's that the merge only keeps
	// is not part of what it writes, so it does not move the digest.)
	f.put("pref:"+f.b.ID+":speed", "{\"key\":\"speed\",\"value\":\"changed\"}")
	before := f.snapshot()
	_, err = f.svc.Apply(context.Background(), ApplyRequest{ActorID: f.a.ID, SurvivorID: f.a.ID, AbsorbedID: f.b.ID,
		Digest: plan.Digest, AcknowledgeOwnerIdentity: true}, nil)
	if !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("apply = %v, want ErrDigestMismatch", err)
	}
	if d := diff(before, f.snapshot()); len(d) != 0 {
		t.Errorf("a digest mismatch wrote %v", d)
	}
	if list, _ := f.svc.ListJournals(); len(list) != 0 {
		t.Errorf("a digest mismatch left a journal: %v", list)
	}
}

func TestUndo_RestoresEveryKeyByteForByte(t *testing.T) {
	f := newFixture(t)
	before := f.snapshot()
	ub0, _ := f.s.GetUserByID(f.b.ID)
	j, err := f.apply(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff(before, f.snapshot())) == 0 {
		t.Fatal("the apply changed nothing")
	}
	undone, err := f.svc.Undo(context.Background(), UndoRequest{JournalID: j.ID}, nil)
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if undone.Status != StatusUndone || len(undone.UndoConflicts) != 0 {
		t.Fatalf("undone = %+v", undone.Summary())
	}
	if d := diff(before, f.snapshot()); len(d) != 0 {
		t.Errorf("after undo these keys differ from before the merge: %v", d)
	}
	ua, _ := f.s.GetUserByID(f.a.ID)
	ub, _ := f.s.GetUserByID(f.b.ID)
	if slices.Contains(ua.Roles, "editor") || ua.Email != "keeper@admins.example.com" {
		t.Errorf("survivor after undo = roles %v email %q", ua.Roles, ua.Email)
	}
	if ub.Status != "active" || ub.MergedIntoUserID != "" || ub.MergedAt != nil || ub.Email != ub0.Email || ub.EmailSource != ub0.EmailSource {
		t.Errorf("absorbed after undo = %+v, was %+v", ub, ub0)
	}
	if ub.CredentialEpoch <= ub0.CredentialEpoch {
		t.Errorf("credential epoch %d did not go up from %d: sessions from before the merge must stay dead", ub.CredentialEpoch, ub0.CredentialEpoch)
	}
	// And it can be merged again.
	if _, err := f.apply(true); err != nil {
		t.Errorf("second apply after undo: %v", err)
	}
}

func TestUndo_LeavesAKeyWrittenAfterTheMergeAndSaysSo(t *testing.T) {
	f := newFixture(t)
	j, err := f.apply(true)
	if err != nil {
		t.Fatal(err)
	}
	// The survivor edits a preference the merge moved.
	moved := "pref:" + f.a.ID + ":speed"
	f.put(moved, "{\"key\":\"speed\",\"value\":\"2.0\"}")
	undone, err := f.svc.Undo(context.Background(), UndoRequest{JournalID: j.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(undone.UndoConflicts, func(s string) bool { return strings.HasPrefix(s, moved) }) {
		t.Fatalf("undo conflicts = %v, want %s named", undone.UndoConflicts, moved)
	}
	if got := f.get(moved); !strings.Contains(got, "2.0") {
		t.Errorf("undo overwrote the later write: %s", got)
	}
	if got := f.get("pref:" + f.b.ID + ":speed"); !strings.Contains(got, "1.5") {
		t.Errorf("the absorbed account's own row did not come back: %s", got)
	}
}

func TestUndo_KeepsAStatusAnAdminChangedAfterTheMerge(t *testing.T) {
	f := newFixture(t)
	j, err := f.apply(true)
	if err != nil {
		t.Fatal(err)
	}
	// An admin deactivates the merged-away account after the merge.
	if _, err := f.s.SetUserStatus(f.b.ID, "deactivated"); err != nil {
		t.Fatal(err)
	}
	undone, err := f.svc.Undo(context.Background(), UndoRequest{JournalID: j.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ub, _ := f.s.GetUserByID(f.b.ID); ub.Status != "deactivated" || ub.MergedIntoUserID != "" {
		t.Errorf("absorbed after undo = status %q merged_into %q; a status changed since the merge must be kept", ub.Status, ub.MergedIntoUserID)
	}
	if !slices.ContainsFunc(undone.UndoConflicts, func(c string) bool { return strings.Contains(c, "status") && strings.Contains(c, "deactivated") }) {
		t.Errorf("undo conflicts = %v, want the kept status named", undone.UndoConflicts)
	}
}

func TestUndo_EndsTheSurvivorsSessionsWhenSignInsMoveBack(t *testing.T) {
	f := newFixture(t)
	j, err := f.apply(true)
	if err != nil {
		t.Fatal(err)
	}
	// The survivor signs in after the merge, possibly through a re-linked identity.
	sess, err := f.s.CreateSession(f.a.ID, "192.0.2.20", "test", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	epoch := func() int64 { u, _ := f.s.GetUserByID(f.a.ID); return u.CredentialEpoch }
	before := epoch()
	if _, err := f.svc.Undo(context.Background(), UndoRequest{JournalID: j.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.s.GetSession(sess.ID); got == nil || !got.Revoked {
		t.Errorf("the survivor's web session outlived the credential rollback: %+v", got)
	}
	if epoch() <= before {
		t.Errorf("survivor epoch %d did not go up from %d, so its ABS sessions would survive", epoch(), before)
	}
}

func TestUndo_LeavesTheSurvivorsSessionsAloneWhenNoCredentialMoved(t *testing.T) {
	f := newFixture(t)
	// Take the sign-ins and keys out of the picture: nothing credential-shaped moves.
	for _, k := range []string{"oauthid:idB", "idx:oauth:user:" + f.b.ID + ":idB", "apikey:kB", "idx:apikey:user:" + f.b.ID + ":kB"} {
		if err := f.s.DeleteRaw(k); err != nil {
			t.Fatal(err)
		}
	}
	j, err := f.apply(true)
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := f.s.CreateSession(f.a.ID, "192.0.2.21", "test", time.Hour)
	if _, err := f.svc.Undo(context.Background(), UndoRequest{JournalID: j.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.s.GetSession(sess.ID); got == nil || got.Revoked {
		t.Errorf("an undo that moved no sign-in or key signed the survivor out: %+v", got)
	}
}

// failAfter makes the Nth ApplyUserMergeOps call fail, as a crash between chunks.
type failAfter struct {
	database.UserMerger
	mu    sync.Mutex
	calls int
	after int
}

func (m *failAfter) ApplyUserMergeOps(ops []database.UserMergeOp, key string, journal []byte) error {
	m.mu.Lock()
	m.calls++
	n := m.calls
	m.mu.Unlock()
	if n > m.after {
		return errors.New("simulated crash")
	}
	return m.UserMerger.ApplyUserMergeOps(ops, key, journal)
}

func TestResume_FinishesAnApplyThatStoppedBetweenChunks(t *testing.T) {
	old := chunkSize
	chunkSize = 5
	t.Cleanup(func() { chunkSize = old })
	f := newFixture(t)
	real := f.svc.merger
	f.svc.merger = &failAfter{UserMerger: real, after: 2}
	j, err := f.apply(true)
	if err == nil || j == nil || j.Status != StatusApplying || j.Cursor != 10 {
		t.Fatalf("apply = %v, journal %+v; want a stop at cursor 10 still applying", err, j)
	}
	if ub, _ := f.s.GetUserByID(f.b.ID); ub.MergedIntoUserID != "" {
		t.Error("the absorbed account must not be retired before every op is written")
	}
	f.svc.merger = real
	done, err := f.svc.Resume(context.Background(), j.ID, nil)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if done.Status != StatusApplied || !done.Retired {
		t.Fatalf("resumed journal = %+v", done.Summary())
	}
	for k, v := range f.snapshot() {
		if (strings.Contains(k, f.b.ID) || strings.Contains(v, f.b.ID)) && !censusAllowed(k, f.b.ID) {
			t.Errorf("after resume a live row still names the absorbed account: %s", k)
		}
	}
	// Resume refuses a merge that is not mid-apply.
	if _, err := f.svc.Resume(context.Background(), j.ID, nil); !errors.Is(err, ErrJournalState) {
		t.Errorf("resume of an applied merge = %v, want ErrJournalState", err)
	}
}

func TestBuildPlan_Refusals(t *testing.T) {
	f := newFixture(t)
	viewer, err := f.s.CreateUser("viewer", "viewer@users.example.com", "bcrypt", "h", []string{"viewer"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	refusal := func(name string, svc *Service, req PlanRequest, want string) {
		t.Helper()
		p, err := svc.BuildPlan(context.Background(), req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !slices.ContainsFunc(p.Refusals, func(r string) bool { return strings.Contains(r, want) }) {
			t.Errorf("%s: refusals = %v, want one containing %q", name, p.Refusals, want)
		}
	}
	refusal("self", f.svc, PlanRequest{ActorID: f.a.ID, SurvivorID: f.a.ID, AbsorbedID: f.a.ID}, "itself")
	refusal("actor is the absorbed account", f.svc, PlanRequest{ActorID: f.b.ID, SurvivorID: f.a.ID, AbsorbedID: f.b.ID}, "signed in as")
	refusal("actor not an admin", f.svc, PlanRequest{ActorID: viewer.ID, SurvivorID: f.a.ID, AbsorbedID: f.b.ID}, "active admin")
	refusal("survivor not an admin", f.svc, PlanRequest{ActorID: f.a.ID, SurvivorID: viewer.ID, AbsorbedID: f.b.ID}, "not an admin")
	refusal("missing absorbed", f.svc, PlanRequest{ActorID: f.a.ID, SurvivorID: f.a.ID, AbsorbedID: "nobody"}, "does not exist")
	pinned, _ := New(f.s)
	pinned.backfillUserID = func() string { return f.b.ID }
	refusal("pinned backfill user", pinned, f.planReq(), "abs_itunes_position_backfill_user_id")

	// An undecodable row of the absorbed account refuses.
	f.put("ubs:"+f.b.ID+":broken", "not json")
	refusal("undecodable state", f.svc, f.planReq(), "cannot read")

	// A queued operation naming the absorbed account refuses.
	if err := f.s.InsertOperationV2(database.OperationV2Row{ID: "op1", DefID: "maintenance.something", Status: "queued",
		Params: `{"user_id":"` + f.b.ID + `"}`, QueuedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	refusal("active op", f.svc, f.planReq(), "queued or running")
}

func TestBuildPlan_SurvivorWithNoSignInIsRefused(t *testing.T) {
	f := newFixture(t)
	if _, err := f.s.SetUserPassword(f.a.ID, "", "bcrypt", ""); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"oauthid:idA", "idx:oauth:user:" + f.a.ID + ":idA", "oauthid:idB", "idx:oauth:user:" + f.b.ID + ":idB"} {
		if err := f.s.DeleteRaw(k); err != nil {
			t.Fatal(err)
		}
	}
	p, err := f.svc.BuildPlan(context.Background(), f.planReq())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(p.Refusals, func(r string) bool { return strings.Contains(r, "no way to sign in") }) {
		t.Errorf("refusals = %v, want the no-sign-in refusal", p.Refusals)
	}
}

func TestApply_RacingAWriterOfTheSurvivorsListeningState(t *testing.T) {
	f := newFixture(t)
	plan, err := f.svc.BuildPlan(context.Background(), f.planReq())
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			// What an ABS or web writer does: the per-(user, book) stripe around the write.
			unlock := database.LockUserBookState(f.a.ID, "book-both")
			_ = f.s.SetUserPositionAt(f.a.ID, "book-both", "s1", float64(i), f.t0.Add(time.Duration(i)*time.Second))
			unlock()
		}
	}()
	_, err = f.svc.Apply(context.Background(), ApplyRequest{ActorID: f.a.ID, SurvivorID: f.a.ID, AbsorbedID: f.b.ID,
		Digest: plan.Digest, AcknowledgeOwnerIdentity: true}, nil)
	close(stop)
	wg.Wait()
	// A write that lands between preview and apply changes what the plan covers, so
	// ErrDigestMismatch is correct; any other error, or a torn merge, is not.
	if err != nil && !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("apply = %v", err)
	}
	if err == nil {
		for k, v := range f.snapshot() {
			if (strings.Contains(k, f.b.ID) || strings.Contains(v, f.b.ID)) && !censusAllowed(k, f.b.ID) {
				t.Errorf("a live row still names the absorbed account: %s", k)
			}
		}
	}
}

func TestFootprint(t *testing.T) {
	f := newFixture(t)
	fp, err := f.svc.Footprint(f.b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fp.Counts[FamBookState] != 2 || fp.Counts[FamPositions] != 2 || fp.Counts[FamBookmarks] != 2 ||
		fp.Counts[FamOAuthIdentities] != 1 || fp.Counts[FamAPIKeys] != 1 || fp.Counts[FamPlaylists] != 1 {
		t.Errorf("counts = %v", fp.Counts)
	}
	if len(fp.Identities) != 1 || !fp.Identities[0].OwnerEmail || fp.Identities[0].Provider != "google" {
		t.Errorf("identities = %+v", fp.Identities)
	}
	if _, err := f.svc.Footprint("nobody"); err == nil {
		t.Error("footprint of a missing user returned no error")
	}
}

func TestGrants_AreSingleUseBoundAndExpire(t *testing.T) {
	g := NewGrants()
	now := time.Now()
	g.now = func() time.Time { return now }
	base := Grant{ActorID: "admin1", Action: ActionApply, SurvivorID: "a", AbsorbedID: "b", Digest: "d"}
	tok, err := g.Issue(base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Redeem(tok, "admin1", base); err != nil {
		t.Fatalf("redeem = %v", err)
	}
	if _, err := g.Redeem(tok, "admin1", base); err == nil {
		t.Error("a grant redeemed twice")
	}
	for name, mutate := range map[string]func(g *Grant, actor *string){
		"other actor":  func(_ *Grant, actor *string) { *actor = "admin2" },
		"other digest": func(w *Grant, _ *string) { w.Digest = "x" },
		"other target": func(w *Grant, _ *string) { w.AbsorbedID = "c" },
		"other action": func(w *Grant, _ *string) { w.Action = ActionUndo },
		"ack differs":  func(w *Grant, _ *string) { w.AcknowledgeOwnerIdentity = true },
	} {
		tok, _ := g.Issue(base)
		want, actor := base, "admin1"
		mutate(&want, &actor)
		if _, err := g.Redeem(tok, actor, want); err == nil {
			t.Errorf("%s: redeem succeeded", name)
		}
		if _, ok := g.Take(tok); ok {
			t.Errorf("%s: a refused redeem must still consume the grant", name)
		}
	}
	tok, _ = g.Issue(base)
	now = now.Add(GrantTTL + time.Second)
	if _, err := g.Redeem(tok, "admin1", base); err == nil {
		t.Error("an expired grant was honoured")
	}
	if _, err := g.Issue(Grant{ActorID: "x", Action: ActionApply}); err == nil {
		t.Error("an apply grant without accounts or digest was issued")
	}
	if _, err := g.Issue(Grant{Action: ActionUndo, JournalID: "j"}); err == nil {
		t.Error("a grant without an actor was issued")
	}
}
