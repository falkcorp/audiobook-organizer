// file: internal/database/user_merge_test.go
// version: 1.0.0
// guid: 2f7b9c10-4e83-4a6d-b1f5-8c0d3e92a746
// last-edited: 2026-10-22

package database

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

func TestUpdateUser_RefusesToChangeMergeFields(t *testing.T) {
	s := newInviteTestStore(t)
	u := mustUser(t, s, "alice", "viewer", "active")

	u.MergedIntoUserID = "someone"
	if err := s.UpdateUser(u); !errors.Is(err, ErrUserProtectedField) {
		t.Fatalf("UpdateUser setting MergedIntoUserID = %v, want ErrUserProtectedField", err)
	}
	u.MergedIntoUserID = ""
	now := time.Now()
	u.MergedAt = &now
	if err := s.UpdateUser(u); !errors.Is(err, ErrUserProtectedField) {
		t.Fatalf("UpdateUser setting MergedAt = %v, want ErrUserProtectedField", err)
	}
	u.MergedAt = nil
	u.Roles = []string{"viewer", "editor"}
	if err := s.UpdateUser(u); err != nil {
		t.Fatalf("an ordinary change = %v, want nil", err)
	}
}

func TestRekeyUserKeyAndRewriteJSONField(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"ubs:B:book1", "ubs:A:book1"},
		{"upos:B:book1:seg", "upos:A:book1:seg"},
		{"bookmark:B:item:1.5", "bookmark:A:item:1.5"},
		{"sync_alias_use:B:al", "sync_alias_use:A:al"},
		{"sync_alias_use_seeded:B", "sync_alias_use_seeded:A"},
		{"pref:B:theme", "pref:A:theme"},
		{"playp:B:7", "playp:A:7"},
		{"playe:B:7:1", "playe:A:7:1"},
		{"stats:user:listen_seconds:B", "stats:user:listen_seconds:A"},
		{"idx:oauth:user:B:id1", "idx:oauth:user:A:id1"},
		{"idx:apikey:user:B:k1", "idx:apikey:user:A:k1"},
	} {
		if got, ok := RekeyUserKey(tc.in, "B", "A"); !ok || got != tc.want {
			t.Errorf("RekeyUserKey(%q) = %q, %v; want %q", tc.in, got, ok, tc.want)
		}
	}
	// A key of another user, and a user id that merely starts with B's, are not B's.
	for _, k := range []string{"ubs:C:book1", "ubs:BB:book1", "stats:user:listen_seconds:BB", "col:B"} {
		if got, ok := RekeyUserKey(k, "B", "A"); ok {
			t.Errorf("RekeyUserKey(%q) = %q, true; want not B's", k, got)
		}
	}

	out, old, err := RewriteJSONField([]byte(`{"user_id":"B","future_field":{"x":[1,2,3]},"n":12345678901234567890}`), "user_id", "A")
	if err != nil || old != "B" {
		t.Fatalf("RewriteJSONField = %v, old %q", err, old)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if string(m["user_id"]) != `"A"` || string(m["future_field"]) != `{"x":[1,2,3]}` || string(m["n"]) != `12345678901234567890` {
		t.Fatalf("rewritten = %s; an unknown field or a large number did not survive byte for byte", out)
	}
	if _, _, err := RewriteJSONField([]byte(`not json`), "user_id", "A"); err == nil {
		t.Fatal("RewriteJSONField accepted a value that is not a JSON object")
	}
}

func TestScanUserData_ReadsEveryFamilyForOneUserOnly(t *testing.T) {
	s := newInviteTestStore(t)
	a := mustUser(t, s, "keeper", "admin", "active")
	b := mustUser(t, s, "absorbed", "admin", "active")
	put := func(key string, v any) {
		t.Helper()
		var data []byte
		switch x := v.(type) {
		case string:
			data = []byte(x)
		default:
			data, _ = json.Marshal(x)
		}
		if err := s.db.Set([]byte(key), data, pebble.Sync); err != nil {
			t.Fatal(err)
		}
	}
	for _, u := range []*User{a, b} {
		put("ubs:"+u.ID+":book1", UserBookState{UserID: u.ID, BookID: "book1", Status: UserBookStatusInProgress})
		put("upos:"+u.ID+":book1:s1", UserPosition{UserID: u.ID, BookID: "book1", SegmentID: "s1"})
		put("bookmark:"+u.ID+":book1:10", map[string]any{"UserID": u.ID})
		put("sync_alias_use:"+u.ID+":alias1", map[string]any{})
		put("sync_alias_use_seeded:"+u.ID, "1")
		put("pref:"+u.ID+":k", UserPreferenceKV{UserID: u.ID, Key: "k"})
		put("playp:"+u.ID+":1", PlaybackProgress{UserID: u.ID, BookID: 1})
		put("playe:"+u.ID+":1:1", PlaybackEvent{UserID: u.ID, BookID: 1})
		put("stats:user:listen_seconds:"+u.ID, "5")
		put("oauthid:id-"+u.ID, OAuthIdentity{ID: "id-" + u.ID, Provider: "google", Subject: "sub-" + u.ID, UserID: u.ID})
		put("idx:oauth:user:"+u.ID+":id-"+u.ID, "1")
		put("apikey:k-"+u.ID, APIKey{ID: "k-" + u.ID, UserID: u.ID})
		put("idx:apikey:user:"+u.ID+":k-"+u.ID, "1")
		put("upl:pl-"+u.ID, UserPlaylist{ID: "pl-" + u.ID, CreatedByUserID: u.ID})
		put("col:c-"+u.ID, Collection{ID: "c-" + u.ID, CreatedByUserID: u.ID})
		put("invite:t-"+u.ID, Invite{Token: "t-" + u.ID, CreatedByUserID: u.ID})
		put("inviteallow:"+u.ID+"@example.com", InviteEmailGrant{UserID: u.ID})
	}
	put("upl:broken", "not json")

	d, err := s.ScanUserData(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{
		"states": len(d.BookStates), "positions": len(d.Positions), "bookmarks": len(d.Bookmarks),
		"aliases": len(d.SyncAliasUses), "seeded": len(d.SyncAliasSeeded), "prefs": len(d.Preferences),
		"playp": len(d.PlaybackProgress), "playe": len(d.PlaybackEvents), "stats": len(d.ListenStats),
		"oauth": len(d.OAuthIdentities), "apikeys": len(d.APIKeys), "playlists": len(d.Playlists),
		"collections": len(d.Collections), "invites": len(d.Invites), "grants": len(d.InviteGrants),
	}
	for name, n := range counts {
		if n != 1 {
			t.Errorf("%s = %d, want exactly B's one row", name, n)
		}
	}
	for _, rows := range [][]UserDataRow{d.BookStates, d.Positions, d.Bookmarks, d.OAuthIdentities, d.APIKeys, d.Playlists} {
		for _, r := range rows {
			if strings.Contains(r.Key, a.ID) || strings.Contains(string(r.Value), a.ID) {
				t.Errorf("row %s belongs to the other user", r.Key)
			}
		}
	}
	if !slices.ContainsFunc(d.Unreadable, func(s string) bool { return strings.HasPrefix(s, "upl:broken") }) {
		t.Errorf("Unreadable = %v, want the undecodable playlist named", d.Unreadable)
	}
	if _, err := s.ScanUserData("a:b"); err == nil {
		t.Error("ScanUserData accepted an id with a colon")
	}
}

func TestApplyUserMergeOps_ChecksBeforeImagesAndIsResumable(t *testing.T) {
	s := newInviteTestStore(t)
	set := func(k, v string) {
		t.Helper()
		if err := s.db.Set([]byte(k), []byte(v), pebble.Sync); err != nil {
			t.Fatal(err)
		}
	}
	set("pref:B:k", "old")
	ops := []UserMergeOp{
		{Key: "pref:A:k", Family: "pref", BeforeAbsent: true, After: []byte("old")},
		{Key: "pref:B:k", Family: "pref", Before: []byte("old"), Delete: true},
	}
	if err := s.ApplyUserMergeOps(ops, UserMergeJournalPrefix+"j1", []byte(`{"status":"applying"}`)); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.GetRaw("pref:A:k"); string(v) != "old" {
		t.Fatalf("pref:A:k = %q", v)
	}
	if v, _ := s.GetRaw("pref:B:k"); v != nil {
		t.Fatalf("pref:B:k = %q, want deleted", v)
	}
	if v, _ := s.GetRaw(UserMergeJournalPrefix + "j1"); string(v) != `{"status":"applying"}` {
		t.Fatalf("journal = %q, want it written in the same batch", v)
	}
	// The same chunk again (a resume after a crash between the commit and the cursor) is a no-op.
	if err := s.ApplyUserMergeOps(ops, "", nil); err != nil {
		t.Fatalf("re-applying an applied chunk = %v, want nil", err)
	}

	// A key that changed under the plan: nothing in the batch is written.
	set("pref:C:k", "planned")
	set("pref:D:k", "changed-by-someone")
	conflict := []UserMergeOp{
		{Key: "pref:E:k", Family: "pref", BeforeAbsent: true, After: []byte("x")},
		{Key: "pref:D:k", Family: "pref", Before: []byte("planned"), Delete: true},
	}
	err := s.ApplyUserMergeOps(conflict, UserMergeJournalPrefix+"j2", []byte("j"))
	var ce *UserMergeConflictError
	if !errors.Is(err, ErrUserMergeConflict) || !errors.As(err, &ce) || ce.Key != "pref:D:k" {
		t.Fatalf("err = %v, want a conflict on pref:D:k", err)
	}
	if v, _ := s.GetRaw("pref:E:k"); v != nil {
		t.Fatal("a conflicting batch wrote its other op")
	}
	if v, _ := s.GetRaw(UserMergeJournalPrefix + "j2"); v != nil {
		t.Fatal("a conflicting batch wrote the journal")
	}
}

// retirementFixture builds survivor A and absorbed B (locked) and the
// retirement a planner would build for them.
func retirementFixture(t *testing.T, s *PebbleStore) (a, b *User, r UserRetirement) {
	t.Helper()
	a = mustUser(t, s, "keeper", "admin", "active")
	b = mustUser(t, s, "absorbed", "editor", "active")
	if _, err := s.SetUserStatus(b.ID, "locked"); err != nil {
		t.Fatal(err)
	}
	a, _ = s.GetUserByID(a.ID)
	b, _ = s.GetUserByID(b.ID)
	r = UserRetirement{
		SurvivorID: a.ID, AbsorbedID: b.ID,
		SurvivorBefore: UserIdentityFields{Roles: a.Roles, Email: a.Email, EmailSource: a.EmailSource},
		SurvivorAfter:  UserIdentityFields{Roles: []string{"admin", "editor"}, Email: a.Email, EmailSource: EmailSourceAdmin},
		AbsorbedBefore: UserIdentityFields{Roles: b.Roles, Email: b.Email, EmailSource: b.EmailSource},
		AbsorbedAfter:  UserIdentityFields{Roles: b.Roles, MergedIntoUserID: a.ID},
		MergedAt:       time.Date(2026, 10, 22, 1, 2, 3, 0, time.UTC),
	}
	return a, b, r
}

func TestRetireMergedUser_RoundTripAndGuards(t *testing.T) {
	s := newInviteTestStore(t)
	a, b, r := retirementFixture(t, s)
	absorbedEmail := strings.ToLower(b.Email)

	if err := s.RetireMergedUser(r); err != nil {
		t.Fatal(err)
	}
	gotA, _ := s.GetUserByID(a.ID)
	gotB, _ := s.GetUserByID(b.ID)
	if !slices.Equal(gotA.Roles, []string{"admin", "editor"}) || gotA.EmailSource != EmailSourceAdmin {
		t.Errorf("survivor after retire = roles %v source %q", gotA.Roles, gotA.EmailSource)
	}
	if gotB.Email != "" || gotB.EmailSource != "" || gotB.MergedIntoUserID != a.ID || gotB.MergedAt == nil || gotB.Status != "locked" {
		t.Errorf("absorbed after retire = %+v", gotB)
	}
	if gotB.CredentialEpoch != b.CredentialEpoch || gotB.PasswordHash != b.PasswordHash || gotA.CredentialEpoch != a.CredentialEpoch {
		t.Error("retire changed a password or credential epoch")
	}
	if keyFor(t, s, strings.ToLower(a.Email)) != a.ID {
		t.Error("the survivor's verified-email key does not name the survivor")
	}
	if keyFor(t, s, absorbedEmail) != "" {
		t.Error("the absorbed account's verified-email key survived")
	}
	// Idempotent: a resumed merge retires again without error or change.
	if err := s.RetireMergedUser(r); err != nil {
		t.Fatalf("retiring twice = %v, want nil", err)
	}
	// The row cannot be re-activated through the whole-row write.
	gotB.Status = "active"
	if err := s.UpdateUser(gotB); !errors.Is(err, ErrUserProtectedField) {
		t.Fatalf("UpdateUser status on a merged row = %v", err)
	}

	// Undo: the fields go back and the merge stamp clears.
	conflicts, err := s.UnretireMergedUser(r)
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("UnretireMergedUser = %v, %v", conflicts, err)
	}
	backA, _ := s.GetUserByID(a.ID)
	backB, _ := s.GetUserByID(b.ID)
	if !slices.Equal(backA.Roles, a.Roles) || backA.EmailSource != a.EmailSource {
		t.Errorf("survivor after undo = roles %v source %q", backA.Roles, backA.EmailSource)
	}
	if backB.Email != b.Email || backB.EmailSource != b.EmailSource || backB.MergedIntoUserID != "" || backB.MergedAt != nil {
		t.Errorf("absorbed after undo = %+v", backB)
	}
	if keyFor(t, s, absorbedEmail) != b.ID {
		t.Error("the absorbed account's verified-email key was not restored")
	}
}

func TestRetireMergedUser_RefusesWhatChangedOrIsUnsafe(t *testing.T) {
	t.Run("absorbed account not locked", func(t *testing.T) {
		s := newInviteTestStore(t)
		_, b, r := retirementFixture(t, s)
		if _, err := s.SetUserStatus(b.ID, "active"); err != nil {
			t.Fatal(err)
		}
		if err := s.RetireMergedUser(r); !errors.Is(err, ErrUserMergeRetireRefused) {
			t.Fatalf("err = %v, want a refusal", err)
		}
	})
	t.Run("survivor edited since the plan", func(t *testing.T) {
		s := newInviteTestStore(t)
		a, _, r := retirementFixture(t, s)
		a.Roles = []string{"admin", "viewer"}
		if err := s.UpdateUser(a); err != nil {
			t.Fatal(err)
		}
		if err := s.RetireMergedUser(r); !errors.Is(err, ErrUserMergeRetireRefused) {
			t.Fatalf("err = %v, want a refusal", err)
		}
	})
	t.Run("a third account holds the survivor's final address proven", func(t *testing.T) {
		s := newInviteTestStore(t)
		_, _, r := retirementFixture(t, s)
		r.SurvivorAfter.Email = "elsewhere@example.com"
		third := mustUser(t, s, "third", "viewer", "active")
		if _, err := s.SetUserEmailAsAdmin(third.ID, "elsewhere@example.com"); err != nil {
			t.Fatal(err)
		}
		if err := s.RetireMergedUser(r); !errors.Is(err, ErrUserEmailTaken) {
			t.Fatalf("err = %v, want ErrUserEmailTaken", err)
		}
	})
	t.Run("same account twice", func(t *testing.T) {
		s := newInviteTestStore(t)
		if err := s.RetireMergedUser(UserRetirement{SurvivorID: "x", AbsorbedID: "x"}); !errors.Is(err, ErrUserMergeRetireRefused) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestUnretireMergedUser_ListsFieldsChangedSinceInsteadOfOverwriting(t *testing.T) {
	s := newInviteTestStore(t)
	a, b, r := retirementFixture(t, s)
	if err := s.RetireMergedUser(r); err != nil {
		t.Fatal(err)
	}
	// The survivor's roles changed after the merge.
	cur, _ := s.GetUserByID(a.ID)
	cur.Roles = []string{"admin", "editor", "auditor"}
	if err := s.UpdateUser(cur); err != nil {
		t.Fatal(err)
	}
	conflicts, err := s.UnretireMergedUser(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 1 || !strings.Contains(conflicts[0], "roles") {
		t.Fatalf("conflicts = %v, want the roles named", conflicts)
	}
	got, _ := s.GetUserByID(a.ID)
	if !slices.Equal(got.Roles, []string{"admin", "editor", "auditor"}) {
		t.Errorf("survivor roles = %v, want the later edit kept", got.Roles)
	}
	if back, _ := s.GetUserByID(b.ID); back.MergedIntoUserID != "" {
		t.Error("the merge stamp must clear even when a field conflicts")
	}
}
