// file: core/auth/authtest/storesuite.go
// version: 1.1.0
// guid: 8cf164e8-2a54-46de-8eed-b0912673891b
// last-edited: 2026-11-11

// Package authtest is the conformance kit of auth.Store and auth.Importer
// (05-authn §3.4, §10 TestNativeStore_RoundTrip): StoreSuite runs every case
// an implementation must pass, the importer's included. It is test support
// (03 R4): it may import any core package and no core non-test package
// imports it.
package authtest

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/auth/password"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// Fixture is one store under test: an empty store over a fresh keyspace.
type Fixture struct {
	// Store is the identity store.
	Store auth.Store
	// Importer imports into the same keyspace.
	Importer auth.Importer
	// NewBatch opens a batch every write of Store and Importer can join.
	NewBatch func() *storage.Batch
	// CorruptUser stores bytes that do not decode as a user at the row of id.
	CorruptUser func(t *testing.T, id auth.UserID)
	// Raw reads the stored bytes of the record a MergeRow names (kind
	// "apikey": key ID; "identity": "<provider>/<subject>"; "inviteallow": the
	// e-mail), nil when absent: what a merge preview hands StageMerge.
	Raw func(t *testing.T, kind, id string) []byte
}

// Harness builds fixtures. New is called once per case.
type Harness struct {
	New func(t *testing.T) Fixture
}

// StoreSuite runs the conformance cases of auth.Store and auth.Importer.
func StoreSuite(t *testing.T, h Harness) {
	t.Helper()
	if h.New == nil {
		t.Fatal("authtest: Harness.New is nil")
	}
	cases := []struct {
		name string
		run  func(t *testing.T, fx Fixture)
	}{
		{"UserRoundTrip", userRoundTrip},
		{"UserVersionRules", userVersionRules},
		{"UsernameUnique", usernameUnique},
		{"UsernameRenameMovesIndex", usernameRename},
		{"PasswordHashKeptByPutUser", passwordKept},
		{"DeleteUser", deleteUser},
		{"GuardUserConflictsOnChange", guardUser},
		{"UndecodableUserIsPerRecord", undecodableUser},
		{"SessionRoundTrip", sessionRoundTrip},
		{"SessionTokenNeverStoredInRecord", sessionNoToken},
		{"TouchSessionNeverRecreates", touchSessionNoRecreate},
		{"TouchSessionSkipsStaleRecords", touchSessionStale},
		{"TouchSessionLosesToDelete", touchSessionRace},
		{"RestampSession", restampSession},
		{"RevokeSignInsBefore", revokeSignIns},
		{"APIKeyRoundTrip", apiKeyRoundTrip},
		{"APIKeyRefusesEmptyOrWildcardScopes", apiKeyScopes},
		{"APIKeyNeedsStoredOwner", apiKeyOwner},
		{"APIKeyRevokedIsTerminal", apiKeyRevokedTerminal},
		{"TouchAPIKeyNeverUnrevokes", touchKeyNoUnrevoke},
		{"TouchAPIKeyLosesToRevoke", touchKeyRace},
		{"DeleteAPIKey", deleteAPIKey},
		{"OneTimeSingleUse", oneTimeSingleUse},
		{"OneTimeConcurrentTakesOneWins", oneTimeConcurrent},
		{"OneTimeLookupDoesNotSpend", oneTimeLookup},
		{"OneTimeDeleteByID", oneTimeDelete},
		{"IdentityRoundTrip", identityRoundTrip},
		{"IdentityRelinkMovesIndex", identityRelink},
		{"StageMergeMovesRecords", stageMerge},
		{"StageMergeStaleRowConflicts", stageMergeStale},
		{"ImportUserKeepsHashAndEpoch", importUser},
		{"ImportUserRejects", importUserRejects},
		{"ImportAPIKeyVerifiesUnderAppHash", importKey},
		{"ImportAPIKeyRevokedAtWins", importKeyRevokedAt},
		{"ImportAPIKeyRejects", importKeyRejects},
		{"ImportIdentityAndInviteAllow", importIdentityAllow},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.run(t, h.New(t)) })
	}
}

// stamp is a fixed UTC time without a monotonic reading, so a stored and a
// read record compare equal.
func stamp() time.Time { return time.Date(2026, 11, 10, 21, 0, 0, 0, time.UTC) }

// bg is the context of every read.
func bg() context.Context { return context.Background() }

// commit stages f into a fresh batch and commits it.
func commit(t *testing.T, fx Fixture, f func(b *storage.Batch) error) error {
	t.Helper()
	b := fx.NewBatch()
	if err := f(b); err != nil {
		closeBatch(t, b)
		return err
	}
	err := b.Commit(bg(), storage.Sync)
	closeBatch(t, b)
	return err
}

func closeBatch(t *testing.T, b *storage.Batch) {
	t.Helper()
	if err := b.Close(); err != nil {
		t.Errorf("close batch: %v", err)
	}
}

// must fails the test when err is not nil.
func must(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// errIs fails the test unless err wraps want.
func errIs(t *testing.T, what string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s = %v, want an error wrapping %v", what, err, want)
	}
}

// user is a stored-shape active user at version v.
func user(id, name string, v int64, roles ...rbac.RoleName) *auth.User {
	return &auth.User{
		ID: auth.UserID(id), Username: name, Email: name + "@gotham.example",
		DisplayName: name, Roles: roles, Status: auth.StatusActive,
		CreatedAt: stamp(), UpdatedAt: stamp(), Version: v,
	}
}

// putUser commits PutUser(u).
func putUser(t *testing.T, fx Fixture, u *auth.User) {
	t.Helper()
	must(t, "PutUser "+string(u.ID), commit(t, fx, func(b *storage.Batch) error { return fx.Store.PutUser(b, u) }))
}

func getUser(t *testing.T, fx Fixture, id auth.UserID) *auth.User {
	t.Helper()
	u, err := fx.Store.GetUser(bg(), id)
	must(t, "GetUser "+string(id), err)
	return u
}

// hashOf is a real argon2id hash of plain at a test cost.
func hashOf(t *testing.T, plain string) string {
	t.Helper()
	_, h, err := password.New(password.Options{Permits: 1, Time: 1, MemoryKiB: 8}).Hash(plain)
	must(t, "hash", err)
	return h
}

// key is a fresh active key request for owner.
func key(owner auth.UserID, scopes ...rbac.Permission) *auth.APIKey {
	return &auth.APIKey{
		Owner: owner, Name: "utility belt", Scopes: scopes, Status: auth.KeyActive,
		CreatedAt: stamp(), ExpiresAt: stamp().Add(90 * 24 * time.Hour),
	}
}

func issueKey(t *testing.T, fx Fixture, k *auth.APIKey) auth.Token {
	t.Helper()
	var raw auth.Token
	must(t, "IssueKey", commit(t, fx, func(b *storage.Batch) error {
		var err error
		raw, err = fx.Store.IssueKey(b, k)
		return err
	}))
	return raw
}

func session(u auth.UserID, epoch int64) *auth.SessionRecord {
	return &auth.SessionRecord{
		UserID: u, Origin: auth.OriginPassword, CredentialEpoch: epoch,
		CreatedAt: stamp(), ExpiresAt: stamp().Add(24 * time.Hour), LastSeen: stamp(),
		ClientAddr: netip.MustParseAddr("192.0.2.10"), UserAgent: "batcomputer/1.0",
	}
}

func issueSession(t *testing.T, fx Fixture, s *auth.SessionRecord) auth.Token {
	t.Helper()
	var tok auth.Token
	must(t, "IssueSession", commit(t, fx, func(b *storage.Batch) error {
		var err error
		tok, err = fx.Store.IssueSession(b, s)
		return err
	}))
	return tok
}

// ---------------------------------------------------------------------------
// Users.

func userRoundTrip(t *testing.T, fx Fixture) {
	want := user("01JBRUCE", "Bruce", 1, rbac.RoleAdmin)
	want.EmailVerified = true
	want.CredentialEpoch = 7
	putUser(t, fx, want)
	if got := getUser(t, fx, want.ID); !reflect.DeepEqual(got, want) {
		t.Fatalf("GetUser = %+v, want %+v", got, want)
	}
	got, err := fx.Store.UserByUsername(bg(), auth.FoldUsername("BRUCE"))
	must(t, "UserByUsername", err)
	if got.ID != want.ID {
		t.Fatalf("UserByUsername(fold) = %s, want %s", got.ID, want.ID)
	}
	_, err = fx.Store.GetUser(bg(), "01JJOKER")
	errIs(t, "GetUser(absent)", err, auth.ErrNotFound)
	_, err = fx.Store.UserByUsername(bg(), "joker")
	errIs(t, "UserByUsername(absent)", err, auth.ErrNotFound)
	putUser(t, fx, user("01JALFRED", "alfred", 1))
	var ids []auth.UserID
	for u, err := range fx.Store.ScanUsers(bg()) {
		must(t, "ScanUsers", err)
		ids = append(ids, u.ID)
	}
	if want := []auth.UserID{"01JALFRED", "01JBRUCE"}; !slices.Equal(ids, want) {
		t.Fatalf("ScanUsers = %v, want %v in ID order (the username index is not a user)", ids, want)
	}
}

func userVersionRules(t *testing.T, fx Fixture) {
	err := commit(t, fx, func(b *storage.Batch) error { return fx.Store.PutUser(b, user("01JDICK", "dick", 2)) })
	errIs(t, "PutUser(v2 of an absent user)", err, storage.ErrConflict)
	putUser(t, fx, user("01JDICK", "dick", 1))
	err = commit(t, fx, func(b *storage.Batch) error { return fx.Store.PutUser(b, user("01JDICK", "dick", 1)) })
	errIs(t, "PutUser(v1 over a stored user)", err, storage.ErrConflict)
	err = commit(t, fx, func(b *storage.Batch) error { return fx.Store.PutUser(b, user("01JDICK", "dick", 3)) })
	errIs(t, "PutUser(skipping a version)", err, storage.ErrConflict)
	putUser(t, fx, user("01JDICK", "nightwing", 2))
	if got := getUser(t, fx, "01JDICK"); got.Version != 2 || got.Username != "nightwing" {
		t.Fatalf("after v2: %+v", got)
	}
	// Two writers both read v2; the second commit loses.
	b1, b2 := fx.NewBatch(), fx.NewBatch()
	defer closeBatch(t, b1)
	defer closeBatch(t, b2)
	must(t, "stage 1", fx.Store.PutUser(b1, user("01JDICK", "nightwing", 3)))
	must(t, "stage 2", fx.Store.PutUser(b2, user("01JDICK", "nightwing", 3)))
	must(t, "commit 1", b1.Commit(bg(), storage.Sync))
	errIs(t, "commit 2 of a stale read", b2.Commit(bg(), storage.Sync), storage.ErrConflict)
	bad := user("01JJASON", "jason", 1)
	bad.Status = "resurrected"
	err = commit(t, fx, func(b *storage.Batch) error { return fx.Store.PutUser(b, bad) })
	errIs(t, "PutUser(unknown status)", err, auth.ErrBadInput)
}

func usernameUnique(t *testing.T, fx Fixture) {
	putUser(t, fx, user("01JBARBARA", "Oracle", 1))
	err := commit(t, fx, func(b *storage.Batch) error { return fx.Store.PutUser(b, user("01JCASS", "oracle", 1)) })
	errIs(t, "PutUser(a username another user holds, other case)", err, auth.ErrUserExists)
}

func usernameRename(t *testing.T, fx Fixture) {
	putUser(t, fx, user("01JDICK", "robin", 1))
	putUser(t, fx, user("01JDICK", "nightwing", 2))
	_, err := fx.Store.UserByUsername(bg(), "robin")
	errIs(t, "UserByUsername(old name)", err, auth.ErrNotFound)
	// The freed name may be taken by someone else.
	putUser(t, fx, user("01JTIM", "robin", 1))
	got, err := fx.Store.UserByUsername(bg(), "robin")
	must(t, "UserByUsername(robin)", err)
	if got.ID != "01JTIM" {
		t.Fatalf("robin is %s, want 01JTIM", got.ID)
	}
}

func passwordKept(t *testing.T, fx Fixture) {
	u := user("01JSELINA", "selina", 1)
	h := hashOf(t, "nine lives")
	must(t, "SetPassword", commit(t, fx, func(b *storage.Batch) error { return fx.Store.SetPassword(b, u, h) }))
	if got := getUser(t, fx, u.ID); !got.HasPassword {
		t.Fatal("HasPassword is false after SetPassword")
	}
	putUser(t, fx, user("01JSELINA", "catwoman", 2))
	got, err := fx.Store.PasswordHash(bg(), u.ID)
	must(t, "PasswordHash", err)
	if got != h {
		t.Fatal("PutUser changed the stored password hash")
	}
	if u := getUser(t, fx, "01JSELINA"); !u.HasPassword {
		t.Fatal("HasPassword is false after PutUser kept the hash")
	}
	must(t, "SetPassword(clear)", commit(t, fx, func(b *storage.Batch) error {
		return fx.Store.SetPassword(b, user("01JSELINA", "catwoman", 3), "")
	}))
	if u := getUser(t, fx, "01JSELINA"); u.HasPassword {
		t.Fatal("HasPassword is true after the hash was cleared")
	}
	err = commit(t, fx, func(b *storage.Batch) error {
		return fx.Store.SetPassword(b, user("01JSELINA", "catwoman", 4), "plaintext-whip")
	})
	errIs(t, "SetPassword(not a PHC hash)", err, auth.ErrBadInput)
}

func deleteUser(t *testing.T, fx Fixture) {
	u := user("01JJASON", "redhood", 1)
	putUser(t, fx, u)
	err := commit(t, fx, func(b *storage.Batch) error { return fx.Store.DeleteUser(b, user("01JJASON", "redhood", 2)) })
	errIs(t, "DeleteUser(wrong version)", err, storage.ErrConflict)
	must(t, "DeleteUser", commit(t, fx, func(b *storage.Batch) error { return fx.Store.DeleteUser(b, u) }))
	_, err = fx.Store.GetUser(bg(), u.ID)
	errIs(t, "GetUser(deleted)", err, auth.ErrNotFound)
	_, err = fx.Store.UserByUsername(bg(), "redhood")
	errIs(t, "UserByUsername(deleted)", err, auth.ErrNotFound)
	putUser(t, fx, user("01JJASON2", "redhood", 1)) // the name is free again
}

func guardUser(t *testing.T, fx Fixture) {
	admin := user("01JBRUCE", "bruce", 1, rbac.RoleAdmin)
	putUser(t, fx, admin)
	putUser(t, fx, user("01JDICK", "dick", 1, rbac.RoleAdmin))
	// A demotion of dick guards bruce (the other admin) at v1; bruce is
	// demoted first, so dick's demotion must not commit.
	b := fx.NewBatch()
	defer closeBatch(t, b)
	must(t, "GuardUser", fx.Store.GuardUser(b, admin))
	must(t, "PutUser(dick)", fx.Store.PutUser(b, user("01JDICK", "dick", 2)))
	putUser(t, fx, user("01JBRUCE", "bruce", 2))
	errIs(t, "commit guarded by a changed user", b.Commit(bg(), storage.Sync), storage.ErrConflict)
	if got := getUser(t, fx, "01JDICK"); !slices.Contains(got.Roles, rbac.RoleAdmin) {
		t.Fatal("the guarded write committed")
	}
	err := commit(t, fx, func(b *storage.Batch) error { return fx.Store.GuardUser(b, admin) })
	errIs(t, "GuardUser(stale version)", err, storage.ErrConflict)
}

func undecodableUser(t *testing.T, fx Fixture) {
	putUser(t, fx, user("01JALFRED", "alfred", 1))
	putUser(t, fx, user("01JZSASZ", "zsasz", 1))
	fx.CorruptUser(t, "01JZSASZ")
	_, err := fx.Store.GetUser(bg(), "01JZSASZ")
	errIs(t, "GetUser(corrupt)", err, auth.ErrUndecodableRecord)
	var good, bad int
	for u, err := range fx.Store.ScanUsers(bg()) {
		if errors.Is(err, auth.ErrUndecodableRecord) {
			bad++
			continue
		}
		must(t, "ScanUsers", err)
		if u.ID == "01JALFRED" {
			good++
		}
	}
	if good != 1 || bad != 1 {
		t.Fatalf("ScanUsers: %d good, %d undecodable; want 1 and 1 (the scan goes on)", good, bad)
	}
}

// ---------------------------------------------------------------------------
// Sessions.

func sessionRoundTrip(t *testing.T, fx Fixture) {
	putUser(t, fx, user("01JBRUCE", "bruce", 1))
	s := session("01JBRUCE", 0)
	tok := issueSession(t, fx, s)
	if err := auth.ParseCredential(auth.KindSession, string(tok)); err != nil {
		t.Fatalf("IssueSession token %q: %v", tok[:4], err)
	}
	if s.PublicID == "" || s.Version != 1 {
		t.Fatalf("IssueSession left PublicID %q, Version %d", s.PublicID, s.Version)
	}
	got, err := fx.Store.SessionByToken(bg(), tok)
	must(t, "SessionByToken", err)
	if !reflect.DeepEqual(got, s) {
		t.Fatalf("SessionByToken = %+v, want %+v", got, s)
	}
	got, err = fx.Store.SessionByPublicID(bg(), s.PublicID)
	must(t, "SessionByPublicID", err)
	if got.PublicID != s.PublicID {
		t.Fatalf("SessionByPublicID = %s", got.PublicID)
	}
	other := issueSession(t, fx, session("01JBRUCE", 0))
	var n int
	for r, err := range fx.Store.ScanUserSessions(bg(), "01JBRUCE") {
		must(t, "ScanUserSessions", err)
		if r.UserID != "01JBRUCE" {
			t.Fatalf("ScanUserSessions yielded user %s", r.UserID)
		}
		n++
	}
	if n != 2 {
		t.Fatalf("ScanUserSessions = %d sessions, want 2", n)
	}
	must(t, "DeleteSession", commit(t, fx, func(b *storage.Batch) error { return fx.Store.DeleteSession(b, s) }))
	_, err = fx.Store.SessionByToken(bg(), tok)
	errIs(t, "SessionByToken(deleted)", err, auth.ErrNotFound)
	_, err = fx.Store.SessionByPublicID(bg(), s.PublicID)
	errIs(t, "SessionByPublicID(deleted)", err, auth.ErrNotFound)
	if _, err := fx.Store.SessionByToken(bg(), other); err != nil {
		t.Fatalf("the other session went with it: %v", err)
	}
	b := fx.NewBatch()
	defer closeBatch(t, b)
	must(t, "DeleteSession(absent)", fx.Store.DeleteSession(b, s))
	if n := b.Count(); n != 0 {
		t.Fatalf("DeleteSession(absent) staged %d writes", n)
	}
	_, err = fx.Store.SessionByToken(bg(), "mcs_not-a-session")
	errIs(t, "SessionByToken(unknown)", err, auth.ErrNotFound)
}

func sessionNoToken(t *testing.T, fx Fixture) {
	putUser(t, fx, user("01JBRUCE", "bruce", 1))
	tok := issueSession(t, fx, session("01JBRUCE", 0))
	secret := tok[4:47]
	for r, err := range fx.Store.ScanUserSessions(bg(), "01JBRUCE") {
		must(t, "ScanUserSessions", err)
		if strings.Contains(stringsOf(r), string(secret)) {
			t.Fatal("a session record carries its token")
		}
	}
}

// stringsOf flattens every string field of v (one level) for a containment
// check.
func stringsOf(v any) string {
	rv := reflect.Indirect(reflect.ValueOf(v))
	var sb strings.Builder
	for _, f := range rv.Fields() {
		if f.Kind() == reflect.String {
			sb.WriteString(f.String())
			sb.WriteByte('|')
		}
	}
	return sb.String()
}

func touchSessionNoRecreate(t *testing.T, fx Fixture) {
	putUser(t, fx, user("01JBRUCE", "bruce", 1))
	s := session("01JBRUCE", 0)
	tok := issueSession(t, fx, s)
	at := stamp().Add(time.Hour)
	must(t, "TouchSession", commit(t, fx, func(b *storage.Batch) error { return fx.Store.TouchSession(b, s.PublicID, at) }))
	got, err := fx.Store.SessionByToken(bg(), tok)
	must(t, "SessionByToken", err)
	if !got.LastSeen.Equal(at) {
		t.Fatalf("LastSeen = %v, want %v", got.LastSeen, at)
	}
	must(t, "DeleteSession", commit(t, fx, func(b *storage.Batch) error { return fx.Store.DeleteSession(b, s) }))
	b := fx.NewBatch()
	defer closeBatch(t, b)
	must(t, "TouchSession(deleted)", fx.Store.TouchSession(b, s.PublicID, at.Add(time.Minute)))
	if n := b.Count(); n != 0 {
		t.Fatalf("TouchSession of a deleted session staged %d writes", n)
	}
}

func touchSessionStale(t *testing.T, fx Fixture) {
	u := user("01JBRUCE", "bruce", 1)
	u.CredentialEpoch = 1
	putUser(t, fx, u)
	stale := session("01JBRUCE", 0) // epoch 0 < the user's 1
	issueSession(t, fx, stale)
	live := session("01JBRUCE", 1)
	issueSession(t, fx, live)
	for name, c := range map[string]struct {
		id string
		at time.Time
	}{
		"epoch-stale": {stale.PublicID, stamp().Add(time.Hour)},
		"expired":     {live.PublicID, live.ExpiresAt.Add(time.Second)},
		"not later":   {live.PublicID, stamp()},
	} {
		b := fx.NewBatch()
		must(t, "TouchSession "+name, fx.Store.TouchSession(b, c.id, c.at))
		if n := b.Count(); n != 0 {
			t.Errorf("TouchSession(%s) staged %d writes", name, n)
		}
		closeBatch(t, b)
	}
	putUser(t, fx, func() *auth.User {
		d := user("01JBRUCE", "bruce", 2)
		d.CredentialEpoch = 1
		d.Status = auth.StatusDisabled
		return d
	}())
	b := fx.NewBatch()
	defer closeBatch(t, b)
	must(t, "TouchSession(disabled user)", fx.Store.TouchSession(b, live.PublicID, stamp().Add(time.Hour)))
	if n := b.Count(); n != 0 {
		t.Fatalf("TouchSession of a disabled user's session staged %d writes", n)
	}
}

func touchSessionRace(t *testing.T, fx Fixture) {
	putUser(t, fx, user("01JBRUCE", "bruce", 1))
	s := session("01JBRUCE", 0)
	tok := issueSession(t, fx, s)
	touch := fx.NewBatch()
	defer closeBatch(t, touch)
	must(t, "TouchSession", fx.Store.TouchSession(touch, s.PublicID, stamp().Add(time.Hour)))
	must(t, "DeleteSession", commit(t, fx, func(b *storage.Batch) error { return fx.Store.DeleteSession(b, s) }))
	errIs(t, "touch committed after a logout", touch.Commit(bg(), storage.Sync), storage.ErrConflict)
	_, err := fx.Store.SessionByToken(bg(), tok)
	errIs(t, "SessionByToken after the lost touch", err, auth.ErrNotFound)
}

func restampSession(t *testing.T, fx Fixture) {
	putUser(t, fx, user("01JBRUCE", "bruce", 1))
	s := session("01JBRUCE", 0)
	tok := issueSession(t, fx, s)
	must(t, "RestampSession", commit(t, fx, func(b *storage.Batch) error { return fx.Store.RestampSession(b, s, 5) }))
	got, err := fx.Store.SessionByToken(bg(), tok)
	must(t, "SessionByToken", err)
	if got.CredentialEpoch != 5 || got.Version != 2 {
		t.Fatalf("after RestampSession: epoch %d, version %d", got.CredentialEpoch, got.Version)
	}
}

func revokeSignIns(t *testing.T, fx Fixture) {
	putUser(t, fx, user("01JBRUCE", "bruce", 1))
	old := session("01JBRUCE", 0)
	oldTok := issueSession(t, fx, old)
	fresh := session("01JBRUCE", 0)
	fresh.CreatedAt = stamp().Add(2 * time.Hour)
	freshTok := issueSession(t, fx, fresh)
	var inv auth.Token
	must(t, "IssueOneTime", commit(t, fx, func(b *storage.Batch) error {
		var err error
		inv, err = fx.Store.IssueOneTime(b, &auth.OneTimeToken{Kind: auth.OneTimeInvite, Email: "robin@gotham.example", CreatedAt: stamp(), ExpiresAt: stamp().Add(time.Hour)})
		return err
	}))
	var n int
	must(t, "RevokeSignInsBefore", commit(t, fx, func(b *storage.Batch) error {
		var err error
		n, err = fx.Store.RevokeSignInsBefore(b, stamp().Add(time.Hour))
		return err
	}))
	if n != 2 {
		t.Fatalf("RevokeSignInsBefore = %d, want 2 (one session, one invite)", n)
	}
	_, err := fx.Store.SessionByToken(bg(), oldTok)
	errIs(t, "old session", err, auth.ErrNotFound)
	if _, err := fx.Store.SessionByToken(bg(), freshTok); err != nil {
		t.Fatalf("a session created after t was revoked: %v", err)
	}
	_, err = fx.Store.LookupOneTime(bg(), auth.OneTimeInvite, inv)
	errIs(t, "old invite", err, auth.ErrNotFound)
	must(t, "RevokeSignInsBefore(zero)", commit(t, fx, func(b *storage.Batch) error {
		_, err := fx.Store.RevokeSignInsBefore(b, time.Time{})
		return err
	}))
	_, err = fx.Store.SessionByToken(bg(), freshTok)
	errIs(t, "every session after a zero t", err, auth.ErrNotFound)
}

// ---------------------------------------------------------------------------
// API keys.

func apiKeyRoundTrip(t *testing.T, fx Fixture) {
	putUser(t, fx, user("01JLUCIUS", "lucius", 1))
	k := key("01JLUCIUS", "gotham:patrol", "gotham:signal")
	raw := issueKey(t, fx, k)
	if err := auth.ParseCredential(auth.KindKey, string(raw)); err != nil {
		t.Fatalf("IssueKey key: %v", err)
	}
	if k.ID == "" || k.Version != 1 || k.HashPrefix != auth.HashHex(raw)[:8] || k.Imported {
		t.Fatalf("IssueKey left %+v", k)
	}
	got, err := fx.Store.APIKeyByToken(bg(), raw)
	must(t, "APIKeyByToken", err)
	if !reflect.DeepEqual(got, k) {
		t.Fatalf("APIKeyByToken = %+v, want %+v", got, k)
	}
	byID, err := fx.Store.APIKeyByID(bg(), k.ID)
	must(t, "APIKeyByID", err)
	if !reflect.DeepEqual(byID, k) {
		t.Fatalf("APIKeyByID = %+v", byID)
	}
	putUser(t, fx, user("01JFOX", "fox", 1))
	issueKey(t, fx, key("01JFOX", "gotham:patrol"))
	var mine, all int
	for k, err := range fx.Store.ScanAPIKeys(bg(), "01JLUCIUS") {
		must(t, "ScanAPIKeys(owner)", err)
		if k.Owner != "01JLUCIUS" {
			t.Fatalf("ScanAPIKeys(owner) yielded %s's key", k.Owner)
		}
		mine++
	}
	for _, err := range fx.Store.ScanAPIKeys(bg(), "") {
		must(t, "ScanAPIKeys(all)", err)
		all++
	}
	if mine != 1 || all != 2 {
		t.Fatalf("ScanAPIKeys: %d of the owner's, %d in all; want 1 and 2", mine, all)
	}
	_, err = fx.Store.APIKeyByToken(bg(), "mck_unknown")
	errIs(t, "APIKeyByToken(unknown)", err, auth.ErrNotFound)
	// PutAPIKey changes status, expiry and name only.
	next := *k
	next.Version = 2
	next.Status = auth.KeyDisabled
	next.Name = "bat-grapple"
	next.Scopes = []rbac.Permission{"core:admin"}
	must(t, "PutAPIKey", commit(t, fx, func(b *storage.Batch) error { return fx.Store.PutAPIKey(b, &next) }))
	got, err = fx.Store.APIKeyByID(bg(), k.ID)
	must(t, "APIKeyByID", err)
	if got.Status != auth.KeyDisabled || got.Name != "bat-grapple" || !slices.Equal(got.Scopes, k.Scopes) {
		t.Fatalf("after PutAPIKey: %+v (scopes are not writable)", got)
	}
	err = commit(t, fx, func(b *storage.Batch) error { return fx.Store.PutAPIKey(b, &next) })
	errIs(t, "PutAPIKey(stale version)", err, storage.ErrConflict)
}

func apiKeyScopes(t *testing.T, fx Fixture) {
	putUser(t, fx, user("01JLUCIUS", "lucius", 1))
	for name, scopes := range map[string][]rbac.Permission{
		"nil":       nil,
		"empty":     {},
		"wildcard":  {"gotham:*"},
		"sub-wild":  {"gotham:patrol.*"},
		"malformed": {"Gotham:Patrol"},
	} {
		err := commit(t, fx, func(b *storage.Batch) error {
			_, err := fx.Store.IssueKey(b, key("01JLUCIUS", scopes...))
			return err
		})
		if !errors.Is(err, auth.ErrBadInput) {
			t.Errorf("IssueKey(%s scopes) = %v, want ErrBadInput", name, err)
		}
	}
	err := commit(t, fx, func(b *storage.Batch) error {
		k := key("01JLUCIUS", "gotham:patrol")
		k.ExpiresAt = time.Time{}
		_, err := fx.Store.IssueKey(b, k)
		return err
	})
	errIs(t, "IssueKey(no expiry)", err, auth.ErrBadInput)
}

func apiKeyOwner(t *testing.T, fx Fixture) {
	err := commit(t, fx, func(b *storage.Batch) error {
		_, err := fx.Store.IssueKey(b, key("01JGHOST", "gotham:patrol"))
		return err
	})
	errIs(t, "IssueKey(owner not stored)", err, auth.ErrBadInput)
	// The owner is deleted between staging and commit: the key loses.
	u := user("01JHARVEY", "harvey", 1)
	putUser(t, fx, u)
	b := fx.NewBatch()
	defer closeBatch(t, b)
	_, err = fx.Store.IssueKey(b, key("01JHARVEY", "gotham:patrol"))
	must(t, "IssueKey", err)
	must(t, "DeleteUser", commit(t, fx, func(b *storage.Batch) error { return fx.Store.DeleteUser(b, u) }))
	errIs(t, "key for a deleted owner", b.Commit(bg(), storage.Sync), storage.ErrConflict)
}

func revoke(t *testing.T, fx Fixture, id string) {
	t.Helper()
	cur, err := fx.Store.APIKeyByID(bg(), id)
	must(t, "APIKeyByID", err)
	next := *cur
	next.Version++
	next.Status = auth.KeyRevoked
	next.RevokedAt = stamp().Add(time.Minute)
	must(t, "revoke", commit(t, fx, func(b *storage.Batch) error { return fx.Store.PutAPIKey(b, &next) }))
}

func apiKeyRevokedTerminal(t *testing.T, fx Fixture) {
	putUser(t, fx, user("01JLUCIUS", "lucius", 1))
	k := key("01JLUCIUS", "gotham:patrol")
	issueKey(t, fx, k)
	revoke(t, fx, k.ID)
	cur, err := fx.Store.APIKeyByID(bg(), k.ID)
	must(t, "APIKeyByID", err)
	if cur.Status != auth.KeyRevoked || cur.RevokedAt.IsZero() {
		t.Fatalf("after revoke: %+v", cur)
	}
	for _, st := range []auth.KeyStatus{auth.KeyActive, auth.KeyDisabled, auth.KeyNeedsConfirmation} {
		next := *cur
		next.Version++
		next.Status = st
		next.RevokedAt = time.Time{}
		err := commit(t, fx, func(b *storage.Batch) error { return fx.Store.PutAPIKey(b, &next) })
		if !errors.Is(err, auth.ErrKeyRevoked) {
			t.Errorf("PutAPIKey(revoked -> %s) = %v, want ErrKeyRevoked", st, err)
		}
	}
	next := *cur
	next.Version++
	next.RevokedAt = time.Time{}
	err = commit(t, fx, func(b *storage.Batch) error { return fx.Store.PutAPIKey(b, &next) })
	errIs(t, "PutAPIKey(clearing revoked_at)", err, auth.ErrKeyRevoked)
}

func touchKeyNoUnrevoke(t *testing.T, fx Fixture) {
	putUser(t, fx, user("01JLUCIUS", "lucius", 1))
	k := key("01JLUCIUS", "gotham:patrol")
	issueKey(t, fx, k)
	ip := netip.MustParseAddr("192.0.2.20")
	must(t, "TouchAPIKey", commit(t, fx, func(b *storage.Batch) error {
		return fx.Store.TouchAPIKey(b, k.ID, stamp().Add(time.Minute), ip)
	}))
	got, err := fx.Store.APIKeyByID(bg(), k.ID)
	must(t, "APIKeyByID", err)
	if got.UseCount != 1 || got.LastUsedIP != ip || got.Status != auth.KeyActive || got.Version != 1 {
		t.Fatalf("after TouchAPIKey: %+v", got)
	}
	revoke(t, fx, k.ID)
	b := fx.NewBatch()
	defer closeBatch(t, b)
	must(t, "TouchAPIKey(revoked)", fx.Store.TouchAPIKey(b, k.ID, stamp().Add(time.Hour), ip))
	if n := b.Count(); n != 0 {
		t.Fatalf("TouchAPIKey of a revoked key staged %d writes", n)
	}
}

func touchKeyRace(t *testing.T, fx Fixture) {
	putUser(t, fx, user("01JLUCIUS", "lucius", 1))
	k := key("01JLUCIUS", "gotham:patrol")
	issueKey(t, fx, k)
	touch := fx.NewBatch()
	defer closeBatch(t, touch)
	must(t, "TouchAPIKey", fx.Store.TouchAPIKey(touch, k.ID, stamp().Add(time.Minute), netip.MustParseAddr("192.0.2.30")))
	revoke(t, fx, k.ID)
	errIs(t, "touch committed after a revoke", touch.Commit(bg(), storage.Sync), storage.ErrConflict)
	got, err := fx.Store.APIKeyByID(bg(), k.ID)
	must(t, "APIKeyByID", err)
	if got.Status != auth.KeyRevoked {
		t.Fatalf("a touch read before the revoke un-revoked the key: %s", got.Status)
	}
}

func deleteAPIKey(t *testing.T, fx Fixture) {
	putUser(t, fx, user("01JLUCIUS", "lucius", 1))
	k := key("01JLUCIUS", "gotham:patrol")
	raw := issueKey(t, fx, k)
	must(t, "DeleteAPIKey", commit(t, fx, func(b *storage.Batch) error { return fx.Store.DeleteAPIKey(b, k) }))
	_, err := fx.Store.APIKeyByToken(bg(), raw)
	errIs(t, "APIKeyByToken(deleted)", err, auth.ErrNotFound)
	for range fx.Store.ScanAPIKeys(bg(), "01JLUCIUS") {
		t.Fatal("ScanAPIKeys(owner) yields a deleted key")
	}
}

// ---------------------------------------------------------------------------
// One-time tokens.

func issueOneTime(t *testing.T, fx Fixture, tok *auth.OneTimeToken) auth.Token {
	t.Helper()
	var raw auth.Token
	must(t, "IssueOneTime", commit(t, fx, func(b *storage.Batch) error {
		var err error
		raw, err = fx.Store.IssueOneTime(b, tok)
		return err
	}))
	return raw
}

// oneTime sets every field, so a round trip proves the store keeps each one.
func oneTime(kind auth.OneTimeKind) *auth.OneTimeToken {
	return &auth.OneTimeToken{
		Kind: kind, UserID: "u-robin", CreatedBy: "u-batman", Username: "robin", Email: "robin@gotham.example", Role: rbac.RoleViewer,
		CredentialEpoch: 3, CreatorEpoch: 5, Reset: true, ClearsRestriction: true, CreatedAt: stamp(), ExpiresAt: stamp().Add(time.Hour),
	}
}

func oneTimeSingleUse(t *testing.T, fx Fixture) {
	for kind, prefix := range map[auth.OneTimeKind]auth.CredentialKind{
		auth.OneTimeInvite: auth.KindInvite, auth.OneTimeTempLogin: auth.KindTempLogin, auth.OneTimeBootstrap: auth.KindBootstrap,
	} {
		tok := oneTime(kind)
		raw := issueOneTime(t, fx, tok)
		if err := auth.ParseCredential(prefix, string(raw)); err != nil {
			t.Fatalf("IssueOneTime(%s) token: %v", kind, err)
		}
		if tok.ID == "" {
			t.Fatalf("IssueOneTime(%s) left no ID", kind)
		}
		var got *auth.OneTimeToken
		must(t, "TakeOneTime", commit(t, fx, func(b *storage.Batch) error {
			var err error
			got, err = fx.Store.TakeOneTime(b, kind, raw)
			return err
		}))
		if !reflect.DeepEqual(got, tok) {
			t.Fatalf("TakeOneTime(%s) = %+v, want %+v", kind, got, tok)
		}
		err := commit(t, fx, func(b *storage.Batch) error {
			_, err := fx.Store.TakeOneTime(b, kind, raw)
			return err
		})
		errIs(t, "second TakeOneTime("+string(kind)+")", err, auth.ErrNotFound)
	}
	raw := issueOneTime(t, fx, oneTime(auth.OneTimeInvite))
	err := commit(t, fx, func(b *storage.Batch) error {
		_, err := fx.Store.TakeOneTime(b, auth.OneTimeTempLogin, raw)
		return err
	})
	errIs(t, "TakeOneTime(an invite as a temp-login)", err, auth.ErrNotFound)
	err = commit(t, fx, func(b *storage.Batch) error {
		_, err := fx.Store.IssueOneTime(b, &auth.OneTimeToken{Kind: "pass_key", ExpiresAt: stamp()})
		return err
	})
	errIs(t, "IssueOneTime(unknown kind)", err, auth.ErrBadInput)
}

func oneTimeConcurrent(t *testing.T, fx Fixture) {
	raw := issueOneTime(t, fx, oneTime(auth.OneTimeBootstrap))
	b1, b2 := fx.NewBatch(), fx.NewBatch()
	defer closeBatch(t, b1)
	defer closeBatch(t, b2)
	_, err := fx.Store.TakeOneTime(b1, auth.OneTimeBootstrap, raw)
	must(t, "take 1", err)
	_, err = fx.Store.TakeOneTime(b2, auth.OneTimeBootstrap, raw)
	must(t, "take 2", err)
	must(t, "commit 1", b1.Commit(bg(), storage.Sync))
	errIs(t, "commit 2 of a spent token", b2.Commit(bg(), storage.Sync), storage.ErrConflict)
}

func oneTimeLookup(t *testing.T, fx Fixture) {
	raw := issueOneTime(t, fx, oneTime(auth.OneTimeInvite))
	for range 2 {
		got, err := fx.Store.LookupOneTime(bg(), auth.OneTimeInvite, raw)
		must(t, "LookupOneTime", err)
		if got.Email != "robin@gotham.example" {
			t.Fatalf("LookupOneTime = %+v", got)
		}
	}
	var n int
	for tok, err := range fx.Store.ScanOneTime(bg(), auth.OneTimeInvite) {
		must(t, "ScanOneTime", err)
		if tok.Kind != auth.OneTimeInvite {
			t.Fatalf("ScanOneTime(invite) yielded %s", tok.Kind)
		}
		n++
	}
	if n != 1 {
		t.Fatalf("ScanOneTime = %d, want 1", n)
	}
}

func oneTimeDelete(t *testing.T, fx Fixture) {
	tok := oneTime(auth.OneTimeInvite)
	raw := issueOneTime(t, fx, tok)
	must(t, "DeleteOneTime", commit(t, fx, func(b *storage.Batch) error {
		return fx.Store.DeleteOneTime(b, auth.OneTimeInvite, tok.ID)
	}))
	_, err := fx.Store.LookupOneTime(bg(), auth.OneTimeInvite, raw)
	errIs(t, "LookupOneTime(deleted)", err, auth.ErrNotFound)
	err = commit(t, fx, func(b *storage.Batch) error { return fx.Store.DeleteOneTime(b, auth.OneTimeInvite, tok.ID) })
	errIs(t, "DeleteOneTime(absent)", err, auth.ErrNotFound)
}

// ---------------------------------------------------------------------------
// Identities.

func identity(user auth.UserID) *auth.ExternalIdentity {
	return &auth.ExternalIdentity{Provider: "wayne-sso", Subject: "sub-1939", UserID: user, Email: "bruce@gotham.example", CreatedAt: stamp()}
}

func identityRoundTrip(t *testing.T, fx Fixture) {
	putUser(t, fx, user("01JBRUCE", "bruce", 1))
	id := identity("01JBRUCE")
	must(t, "PutIdentity", commit(t, fx, func(b *storage.Batch) error { return fx.Store.PutIdentity(b, id) }))
	got, err := fx.Store.IdentityBySubject(bg(), "wayne-sso", "sub-1939")
	must(t, "IdentityBySubject", err)
	if !reflect.DeepEqual(got, id) {
		t.Fatalf("IdentityBySubject = %+v, want %+v", got, id)
	}
	var n int
	for got, err := range fx.Store.ScanIdentities(bg(), "01JBRUCE") {
		must(t, "ScanIdentities", err)
		if got.UserID != "01JBRUCE" {
			t.Fatalf("ScanIdentities(user) yielded %s", got.UserID)
		}
		n++
	}
	if n != 1 {
		t.Fatalf("ScanIdentities(user) = %d, want 1", n)
	}
	err = commit(t, fx, func(b *storage.Batch) error { return fx.Store.PutIdentity(b, identity("01JGHOST")) })
	errIs(t, "PutIdentity(user not stored)", err, auth.ErrBadInput)
	must(t, "DeleteIdentity", commit(t, fx, func(b *storage.Batch) error { return fx.Store.DeleteIdentity(b, id) }))
	_, err = fx.Store.IdentityBySubject(bg(), "wayne-sso", "sub-1939")
	errIs(t, "IdentityBySubject(deleted)", err, auth.ErrNotFound)
	for range fx.Store.ScanIdentities(bg(), "01JBRUCE") {
		t.Fatal("ScanIdentities(user) yields a deleted link")
	}
}

func identityRelink(t *testing.T, fx Fixture) {
	putUser(t, fx, user("01JBRUCE", "bruce", 1))
	putUser(t, fx, user("01JTERRY", "terry", 1))
	must(t, "PutIdentity", commit(t, fx, func(b *storage.Batch) error { return fx.Store.PutIdentity(b, identity("01JBRUCE")) }))
	must(t, "relink", commit(t, fx, func(b *storage.Batch) error { return fx.Store.PutIdentity(b, identity("01JTERRY")) }))
	for range fx.Store.ScanIdentities(bg(), "01JBRUCE") {
		t.Fatal("the old user still lists the relinked identity")
	}
	var n int
	for _, err := range fx.Store.ScanIdentities(bg(), "01JTERRY") {
		must(t, "ScanIdentities", err)
		n++
	}
	if n != 1 {
		t.Fatalf("the new user lists %d identities, want 1", n)
	}
}

// ---------------------------------------------------------------------------
// Merge.

func mergeFixture(t *testing.T, fx Fixture) (keyID string) {
	putUser(t, fx, user("01JDICK", "dick", 1))
	putUser(t, fx, user("01JNIGHTWING", "nightwing", 1))
	k := key("01JDICK", "gotham:patrol")
	issueKey(t, fx, k)
	id := identity("01JDICK")
	must(t, "PutIdentity", commit(t, fx, func(b *storage.Batch) error { return fx.Store.PutIdentity(b, id) }))
	return k.ID
}

func stageMerge(t *testing.T, fx Fixture) {
	keyID := mergeFixture(t, fx)
	plan := auth.UserMergePlan{Source: "01JDICK", Target: "01JNIGHTWING", Rows: []auth.MergeRow{
		{Kind: auth.MergeAPIKey, ID: keyID, Seen: fx.Raw(t, auth.MergeAPIKey, keyID)},
		{Kind: auth.MergeIdentity, ID: "wayne-sso/sub-1939", Seen: fx.Raw(t, auth.MergeIdentity, "wayne-sso/sub-1939")},
	}}
	must(t, "StageMerge", commit(t, fx, func(b *storage.Batch) error { return fx.Store.StageMerge(b, plan) }))
	k, err := fx.Store.APIKeyByID(bg(), keyID)
	must(t, "APIKeyByID", err)
	if k.Owner != "01JNIGHTWING" {
		t.Fatalf("merged key owner = %s", k.Owner)
	}
	for range fx.Store.ScanAPIKeys(bg(), "01JDICK") {
		t.Fatal("the source still lists the merged key")
	}
	id, err := fx.Store.IdentityBySubject(bg(), "wayne-sso", "sub-1939")
	must(t, "IdentityBySubject", err)
	if id.UserID != "01JNIGHTWING" {
		t.Fatalf("merged identity user = %s", id.UserID)
	}
	err = commit(t, fx, func(b *storage.Batch) error {
		return fx.Store.StageMerge(b, auth.UserMergePlan{Source: "01JDICK", Target: "01JDICK"})
	})
	errIs(t, "StageMerge(source = target)", err, auth.ErrBadInput)
}

func stageMergeStale(t *testing.T, fx Fixture) {
	keyID := mergeFixture(t, fx)
	seen := fx.Raw(t, auth.MergeAPIKey, keyID)
	plan := auth.UserMergePlan{Source: "01JDICK", Target: "01JNIGHTWING", Rows: []auth.MergeRow{{Kind: auth.MergeAPIKey, ID: keyID, Seen: seen}}}
	b := fx.NewBatch()
	defer closeBatch(t, b)
	must(t, "StageMerge", fx.Store.StageMerge(b, plan))
	revoke(t, fx, keyID) // the key changes after the preview read it
	errIs(t, "merge of a changed key", b.Commit(bg(), storage.Sync), storage.ErrConflict)
	k, err := fx.Store.APIKeyByID(bg(), keyID)
	must(t, "APIKeyByID", err)
	if k.Owner != "01JDICK" || k.Status != auth.KeyRevoked {
		t.Fatalf("after the lost merge: %+v", k)
	}
}

// ---------------------------------------------------------------------------
// Importer.

func importOne(t *testing.T, fx Fixture, f func(b *storage.Batch) error) error {
	t.Helper()
	return commit(t, fx, f)
}

func importUser(t *testing.T, fx Fixture) {
	u := user("01JGORDON", "Gordon", 4, rbac.RoleEditor)
	u.CredentialEpoch = 12
	h := hashOf(t, "commissioner")
	must(t, "ImportUser", importOne(t, fx, func(b *storage.Batch) error { return fx.Importer.ImportUser(b, u, h) }))
	got := getUser(t, fx, u.ID)
	if got.CredentialEpoch != 12 || got.Version != 4 || !got.HasPassword || got.Username != "Gordon" {
		t.Fatalf("imported user = %+v", got)
	}
	stored, err := fx.Store.PasswordHash(bg(), u.ID)
	must(t, "PasswordHash", err)
	if stored != h {
		t.Fatal("ImportUser changed the password hash")
	}
	if _, err := fx.Store.UserByUsername(bg(), "gordon"); err != nil {
		t.Fatalf("UserByUsername(imported, folded): %v", err)
	}
	// An account without a password and a bcrypt one import as they are.
	must(t, "ImportUser(no password)", importOne(t, fx, func(b *storage.Batch) error {
		return fx.Importer.ImportUser(b, user("01JMONTOYA", "montoya", 1), "")
	}))
	must(t, "ImportUser(bcrypt)", importOne(t, fx, func(b *storage.Batch) error {
		return fx.Importer.ImportUser(b, user("01JBULLOCK", "bullock", 1), "$2a$10$abcdefghijklmnopqrstuuJ8x5B1yZk7eD2Gm3e1t0Qq0a8b9c0dK")
	}))
}

func importUserRejects(t *testing.T, fx Fixture) {
	putUser(t, fx, user("01JBRUCE", "bruce", 1))
	badStatus := user("01JX1", "x1", 1)
	badStatus.Status = "inactive"
	for name, c := range map[string]struct {
		u    *auth.User
		hash string
	}{
		"no ID":          {user("", "nameless", 1), ""},
		"no username":    {user("01JX2", " ", 1), ""},
		"unknown status": {badStatus, ""},
		"plain hash":     {user("01JX3", "x3", 1), "hunter2"},
		"already stored": {user("01JBRUCE", "bruce2", 1), ""},
		"name taken":     {user("01JX4", "BRUCE", 1), ""},
	} {
		b := fx.NewBatch()
		err := fx.Importer.ImportUser(b, c.u, c.hash)
		if !errors.Is(err, auth.ErrImportRejected) {
			t.Errorf("ImportUser(%s) = %v, want ErrImportRejected", name, err)
		}
		if n := b.Count(); n != 0 {
			t.Errorf("ImportUser(%s) staged %d writes", name, n)
		}
		closeBatch(t, b)
	}
}

func importKey(t *testing.T, fx Fixture) {
	putUser(t, fx, user("01JLUCIUS", "lucius", 1))
	const raw = "abk_wayne-enterprises-applied-sciences-0001"
	k := &auth.APIKey{ID: "01JKEY1", Owner: "01JLUCIUS", Name: "cowl", Scopes: []rbac.Permission{"gotham:patrol"},
		Status: auth.KeyActive, CreatedAt: stamp(), ExpiresAt: stamp().Add(time.Hour), UseCount: 41}
	must(t, "ImportAPIKey", importOne(t, fx, func(b *storage.Batch) error {
		return fx.Importer.ImportAPIKey(b, k, auth.HashCredential(raw))
	}))
	got, err := fx.Store.APIKeyByToken(bg(), raw)
	must(t, "APIKeyByToken(imported abk_)", err)
	if got.ID != "01JKEY1" || !got.Imported || got.UseCount != 41 || got.HashPrefix != auth.HashHex(raw)[:8] {
		t.Fatalf("imported key = %+v", got)
	}
}

func importKeyRevokedAt(t *testing.T, fx Fixture) {
	putUser(t, fx, user("01JLUCIUS", "lucius", 1))
	k := &auth.APIKey{ID: "01JKEY2", Owner: "01JLUCIUS", Scopes: []rbac.Permission{"gotham:patrol"},
		Status: auth.KeyActive, RevokedAt: stamp(), ExpiresAt: stamp().Add(time.Hour)}
	must(t, "ImportAPIKey", importOne(t, fx, func(b *storage.Batch) error {
		return fx.Importer.ImportAPIKey(b, k, auth.HashCredential("abk_revoked"))
	}))
	got, err := fx.Store.APIKeyByID(bg(), "01JKEY2")
	must(t, "APIKeyByID", err)
	if got.Status != auth.KeyRevoked {
		t.Fatalf("a key imported with revoked_at set has status %s, want revoked", got.Status)
	}
}

func importKeyRejects(t *testing.T, fx Fixture) {
	putUser(t, fx, user("01JLUCIUS", "lucius", 1))
	base := func() *auth.APIKey {
		return &auth.APIKey{ID: "01JKEY3", Owner: "01JLUCIUS", Scopes: []rbac.Permission{"gotham:patrol"},
			Status: auth.KeyActive, ExpiresAt: stamp().Add(time.Hour)}
	}
	cases := map[string]func(k *auth.APIKey){
		"no ID":          func(k *auth.APIKey) { k.ID = "" },
		"nil scopes":     func(k *auth.APIKey) { k.Scopes = nil },
		"empty scopes":   func(k *auth.APIKey) { k.Scopes = []rbac.Permission{} },
		"wildcard":       func(k *auth.APIKey) { k.Scopes = []rbac.Permission{"core:*"} },
		"unknown status": func(k *auth.APIKey) { k.Status = "lost" },
		"no expiry":      func(k *auth.APIKey) { k.ExpiresAt = time.Time{} },
		"no owner":       func(k *auth.APIKey) { k.Owner = "01JGHOST" },
	}
	for name, mutate := range cases {
		k := base()
		mutate(k)
		b := fx.NewBatch()
		err := fx.Importer.ImportAPIKey(b, k, auth.HashCredential(auth.Token("abk_"+name)))
		if !errors.Is(err, auth.ErrImportRejected) {
			t.Errorf("ImportAPIKey(%s) = %v, want ErrImportRejected", name, err)
		}
		if n := b.Count(); n != 0 {
			t.Errorf("ImportAPIKey(%s) staged %d writes", name, n)
		}
		closeBatch(t, b)
	}
	b := fx.NewBatch()
	defer closeBatch(t, b)
	errIs(t, "ImportAPIKey(zero hash)", fx.Importer.ImportAPIKey(b, base(), [32]byte{}), auth.ErrImportRejected)
}

func importIdentityAllow(t *testing.T, fx Fixture) {
	putUser(t, fx, user("01JBRUCE", "bruce", 1))
	must(t, "ImportIdentity", importOne(t, fx, func(b *storage.Batch) error { return fx.Importer.ImportIdentity(b, identity("01JBRUCE")) }))
	if _, err := fx.Store.IdentityBySubject(bg(), "wayne-sso", "sub-1939"); err != nil {
		t.Fatalf("IdentityBySubject(imported): %v", err)
	}
	err := importOne(t, fx, func(b *storage.Batch) error { return fx.Importer.ImportIdentity(b, identity("01JBRUCE")) })
	errIs(t, "ImportIdentity(already stored)", err, auth.ErrImportRejected)
	err = importOne(t, fx, func(b *storage.Batch) error { return fx.Importer.ImportIdentity(b, identity("01JGHOST")) })
	errIs(t, "ImportIdentity(user not imported)", err, auth.ErrImportRejected)
	must(t, "ImportInviteAllow", importOne(t, fx, func(b *storage.Batch) error {
		return fx.Importer.ImportInviteAllow(b, " Alfred.Pennyworth@Gotham.Example ")
	}))
	ok, err := fx.Store.InviteAllowed(bg(), "alfred.pennyworth@gotham.example")
	must(t, "InviteAllowed", err)
	if !ok {
		t.Fatal("an imported allow-list e-mail is not allowed under its folded form")
	}
	ok, err = fx.Store.InviteAllowed(bg(), "joker@arkham.example")
	must(t, "InviteAllowed(absent)", err)
	if ok {
		t.Fatal("an e-mail never allowed is allowed")
	}
	must(t, "ImportInviteAllow(again)", importOne(t, fx, func(b *storage.Batch) error {
		return fx.Importer.ImportInviteAllow(b, "alfred.pennyworth@gotham.example")
	}))
}
