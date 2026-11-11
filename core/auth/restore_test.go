// file: core/auth/restore_test.go
// version: 1.0.0
// guid: 7f3a9c52-0e6d-4b81-a4c7-d2158e90b6f3
// last-edited: 2026-11-11

package auth_test

import (
	"context"
	"errors"
	"iter"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// The post-restore hook and the every-boot writes (05-authn §4.14, §4.6).

// restoreDeps is f's RestoreDeps: previous is the pre-swap store of a staged
// restore, nil for none.
func (f *fx) restoreDeps(files auth.StateFiles, previous auth.Store) auth.RestoreDeps {
	return auth.RestoreDeps{
		Store: f.st, Previous: previous, Revocations: f.jrnl, StateFiles: files, Clock: f.clock,
		NewBatch: f.st.NewBatch, Settings: f.liveSettings,
		RoleGrantsAdmin: func(ctx context.Context, name rbac.RoleName) (bool, error) {
			r, err := f.rb.Role(ctx, name)
			if errors.Is(err, rbac.ErrUnknownRole) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			return rbac.GrantsAdmin(r.Permissions()), nil
		},
	}
}

// covered is a RestoreContext whose journal covers the interval with lines.
func covered(lines ...journal.Revocation) journal.RestoreContext {
	return journal.RestoreContext{
		Kind: journal.KindArchive, Boundary: 7,
		Revocations: func() iter.Seq[journal.Revocation] { return slices.Values(lines) }(),
	}
}

// uncovered is a RestoreContext with no coverage (a replaced store).
func uncovered() journal.RestoreContext {
	return journal.RestoreContext{Kind: journal.KindExternal}
}

func userRef(id auth.UserID) entity.Ref {
	return entity.Ref{Module: "core", Kind: "user", ID: string(id)}
}
func keyRef(id string) entity.Ref { return entity.Ref{Module: "core", Kind: "apikey", ID: id} }

// importKey stores a key under an explicit ID, so a second store can hold the
// same key in another state.
func importKey(t *testing.T, f *fx, id string, owner auth.UserID, st auth.KeyStatus, scopes ...rbac.Permission) string {
	t.Helper()
	raw := "abk_" + id
	f.commit(func(b *storage.Batch) error {
		return f.st.ImportAPIKey(b, &auth.APIKey{
			ID: id, Owner: owner, Name: "utility belt " + id, Scopes: scopes, Status: st,
			CreatedAt: f.clock(), ExpiresAt: f.clock().Add(48 * time.Hour), Imported: true,
		}, auth.HashCredential(auth.Token(raw)))
	})
	return raw
}

func (f *fx) status(id string) auth.KeyStatus { return f.key(id).Status }

func (f *fx) mustPostRestore(rc journal.RestoreContext, previous auth.Store) auth.RestoreCounts {
	f.t.Helper()
	counts, err := auth.PostRestore(context.Background(), f.restoreDeps(nil, previous), rc)
	if err != nil {
		f.t.Fatalf("PostRestore: %v", err)
	}
	return counts
}

func (f *fx) linkIdentity(u *auth.User, provider, subject string) {
	f.t.Helper()
	f.commit(func(b *storage.Batch) error {
		return f.st.PutIdentity(b, &auth.ExternalIdentity{Provider: provider, Subject: subject, UserID: u.ID, Email: u.Email, CreatedAt: f.clock()})
	})
}

func (f *fx) hasIdentity(provider, subject string) bool {
	f.t.Helper()
	_, err := f.st.IdentityBySubject(context.Background(), provider, subject)
	if err != nil && !errors.Is(err, auth.ErrNotFound) {
		f.t.Fatal(err)
	}
	return err == nil
}

// TestPostRestore_RevokedKeyStaysRevoked (SEC-16): a restored active key the
// journal revoked is revoked again; a key the journal did not name stays
// active (coverage is complete); sessions and one-time tokens are deleted and
// every epoch bumped.
func TestPostRestore_RevokedKeyStaysRevoked(t *testing.T) {
	f := newFx(t)
	dick := f.user("dick-grayson", "gotham.detective")
	importKey(t, f, "key-redbird", dick.ID, auth.KeyActive, "gotham:patrol")
	importKey(t, f, "key-nightwing", dick.ID, auth.KeyActive, "gotham:patrol")
	sess, err := f.svc.IssueSession(context.Background(), dick, auth.OriginPassword, false, from("192.0.2.40"))
	if err != nil {
		t.Fatal(err)
	}
	before := dick.CredentialEpoch

	counts := f.mustPostRestore(covered(journal.Revocation{Kind: journal.APIKeyRevoked, Subject: keyRef("key-redbird")}), nil)

	if got := f.status("key-redbird"); got != auth.KeyRevoked || f.key("key-redbird").RevokedAt.IsZero() {
		t.Fatalf("the journaled key is %q", got)
	}
	if got := f.status("key-nightwing"); got != auth.KeyActive {
		t.Fatalf("an unjournaled key under full coverage is %q", got)
	}
	if _, err := f.st.SessionByToken(context.Background(), auth.Token(sess.Token)); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("a restored session survives: %v", err)
	}
	if got := f.reload(dick.ID).CredentialEpoch; got != before+1 {
		t.Fatalf("epoch %d, want %d", got, before+1)
	}
	if counts[auth.CountKeysRevoked] != 1 || counts[auth.CountSignInsDeleted] != 1 || counts[auth.CountEpochsBumped] != 1 {
		t.Fatalf("counts %v", counts)
	}
	if len(f.jrnl.lines) != 0 {
		t.Fatalf("a re-applied journal line was journaled again: %+v", f.jrnl.lines)
	}
}

// TestPostRestore_NoJournalKeysNeedConfirmation: with no coverage every
// restored active or disabled key needs confirmation; what the hook derives
// itself is journaled.
func TestPostRestore_NoJournalKeysNeedConfirmation(t *testing.T) {
	f := newFx(t)
	dick := f.user("dick-grayson", "gotham.detective")
	importKey(t, f, "key-active", dick.ID, auth.KeyActive, "gotham:patrol")
	importKey(t, f, "key-disabled", dick.ID, auth.KeyDisabled, "gotham:patrol")

	counts := f.mustPostRestore(uncovered(), nil)

	for _, id := range []string{"key-active", "key-disabled"} {
		if got := f.status(id); got != auth.KeyNeedsConfirmation {
			t.Errorf("%s is %q", id, got)
		}
	}
	if counts[auth.CountKeysUnconfirmed] != 2 {
		t.Fatalf("counts %v", counts)
	}
	kinds := map[journal.Kind]int{}
	for _, l := range f.jrnl.lines {
		kinds[l.Kind]++
	}
	if kinds[journal.APIKeyDisabled] != 2 {
		t.Fatalf("journaled %v, want two apikey.disabled", kinds)
	}
}

// TestRestoreNilCoverageKeepsRevokedTerminal (DATA4-13): a key restored as
// revoked stays revoked under the nil-coverage fallback, never
// needs_confirmation, so an admin cannot re-enable it.
func TestRestoreNilCoverageKeepsRevokedTerminal(t *testing.T) {
	f := newFx(t)
	dick := f.user("dick-grayson", "gotham.detective")
	importKey(t, f, "key-dead", dick.ID, auth.KeyRevoked, "gotham:patrol")
	f.mustPostRestore(uncovered(), nil)
	if got := f.status("key-dead"); got != auth.KeyRevoked {
		t.Fatalf("a revoked key is %q after a no-coverage restore", got)
	}
	admin := f.session(f.user("bruce-wayne", rbac.RoleAdmin))
	wantErr(t, "re-enabling a revoked key", f.svc.SetAPIKeyStatus(context.Background(), admin, "key-dead", auth.KeyActive), auth.ErrKeyRevoked)
}

// TestRestore_PreSwapRevocationsWin (SEC5-10, SEC6-4, SEC7-8): whatever the
// journal holds, the restored state takes the more restrictive of itself and
// the pre-swap store: keys by rank, a user disabled or deleted there, a
// changed password hash, a link absent there.
func TestRestore_PreSwapRevocationsWin(t *testing.T) {
	for _, tc := range []struct {
		name string
		rc   func() journal.RestoreContext
		want map[string]auth.KeyStatus
	}{
		{"complete coverage over an unjournaled interval", func() journal.RestoreContext { return covered() },
			map[string]auth.KeyStatus{"k-revoked": auth.KeyRevoked, "k-disabled": auth.KeyDisabled, "k-unconfirmed": auth.KeyNeedsConfirmation, "k-same": auth.KeyActive}},
		{"nil coverage", uncovered,
			map[string]auth.KeyStatus{"k-revoked": auth.KeyRevoked, "k-disabled": auth.KeyNeedsConfirmation, "k-unconfirmed": auth.KeyNeedsConfirmation, "k-same": auth.KeyNeedsConfirmation}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			live, prev := newFx(t), newFx(t)
			for _, f := range []*fx{live, prev} {
				f.user("dick-grayson", "gotham.detective")
				f.user("tim-drake", "gotham.detective")
				f.user("jason-todd", "gotham.detective")
				f.user("stephanie-brown", "gotham.detective")
			}
			dick := live.reload("u-dick-grayson")
			for _, id := range []string{"k-revoked", "k-disabled", "k-unconfirmed", "k-same"} {
				importKey(t, live, id, dick.ID, auth.KeyActive, "gotham:patrol")
			}
			importKey(t, prev, "k-revoked", dick.ID, auth.KeyRevoked, "gotham:patrol")
			importKey(t, prev, "k-disabled", dick.ID, auth.KeyDisabled, "gotham:patrol")
			importKey(t, prev, "k-unconfirmed", dick.ID, auth.KeyNeedsConfirmation, "gotham:patrol")
			importKey(t, prev, "k-same", dick.ID, auth.KeyActive, "gotham:patrol")

			// tim: disabled in the pre-swap store; jason: new password there;
			// stephanie: gone there (deleted).
			prev.edit(prev.reload("u-tim-drake"), func(u *auth.User) { u.Status = auth.StatusDisabled })
			live.withPassword(live.reload("u-jason-todd"), "red-hood-1")
			prev.withPassword(prev.reload("u-jason-todd"), "red-hood-2")
			prev.commit(func(b *storage.Batch) error { return prev.st.DeleteUser(b, prev.reload("u-stephanie-brown")) })
			live.linkIdentity(dick, "google", "sub-dick")
			live.linkIdentity(dick, "github", "sub-dick-gh")
			prev.linkIdentity(dick, "google", "sub-dick")

			live.mustPostRestore(tc.rc(), prev.st)

			for id, want := range tc.want {
				if got := live.status(id); got != want {
					t.Errorf("%s is %q, want %q", id, got, want)
				}
			}
			if got := live.reload("u-tim-drake").Status; got != auth.StatusDisabled {
				t.Errorf("a user disabled pre-swap is %q", got)
			}
			if got := live.reload("u-stephanie-brown").Status; got != auth.StatusDisabled {
				t.Errorf("a user deleted pre-swap is %q", got)
			}
			if got := live.reload("u-dick-grayson").Status; got != auth.StatusActive {
				t.Errorf("an unchanged user is %q", got)
			}
			jason := live.reload("u-jason-todd")
			h, _ := live.st.PasswordHash(context.Background(), jason.ID)
			if h != "" || !jason.PasswordResetRequired || jason.HasPassword {
				t.Errorf("a changed password: hash %q reset %v", h, jason.PasswordResetRequired)
			}
			if !live.hasIdentity("google", "sub-dick") || live.hasIdentity("github", "sub-dick-gh") {
				t.Error("the link absent pre-swap survived, or the one present was removed")
			}
			// An unchanged password is not blanked.
			if h, _ := live.st.PasswordHash(context.Background(), dick.ID); h != "" {
				t.Errorf("an account without a password has %q", h)
			}
		})
	}
}

// TestPostRestore_UnchangedPasswordIsKept: a pre-swap hash equal to the
// restored one blanks nothing.
func TestPostRestore_UnchangedPasswordIsKept(t *testing.T) {
	live, prev := newFx(t), newFx(t)
	live.withPassword(live.user("alfred-pennyworth", "gotham.detective"), "earl-grey-1")
	_, h, err := live.hasher.Hash("earl-grey-1")
	if err != nil {
		t.Fatal(err)
	}
	prev.withHash(prev.user("alfred-pennyworth", "gotham.detective"), h)
	stored, _ := live.st.PasswordHash(context.Background(), "u-alfred-pennyworth")
	prev.withHash(prev.reload("u-alfred-pennyworth"), stored)
	live.mustPostRestore(covered(), prev.st)
	if h, _ := live.st.PasswordHash(context.Background(), "u-alfred-pennyworth"); h != stored {
		t.Fatal("an unchanged password was blanked")
	}
	if live.reload("u-alfred-pennyworth").PasswordResetRequired {
		t.Fatal("reset required for an unchanged password")
	}
}

// TestPostRestoreSurvivesRestart (SEC2-3, DATA2-4): after a restore and a
// restart, the re-revoked key, a restored session cookie and a restored
// invite are refused and their rows are gone.
func TestPostRestoreSurvivesRestart(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	admin := f.user("bruce-wayne", rbac.RoleAdmin)
	dick := f.user("dick-grayson", "gotham.detective")
	raw := importKey(t, f, "key-redbird", dick.ID, auth.KeyActive, "gotham:patrol")
	sess, err := f.svc.IssueSession(ctx, dick, auth.OriginPassword, false, from("192.0.2.41"))
	if err != nil {
		t.Fatal(err)
	}
	inviteTok, _ := f.invite(f.session(admin), detectiveInvite("damian-wayne"))

	f.mustPostRestore(covered(journal.Revocation{Kind: journal.APIKeyRevoked, Subject: keyRef("key-redbird")}), nil)
	f.rebuild()

	_, got := serve(t, f.svc, request(raw, ""))
	if got.p != nil || !got.failed {
		t.Fatalf("the re-revoked key authenticated: %+v", got)
	}
	_, got = serve(t, f.svc, request("", sess.Token))
	if got.p != nil || !got.failed {
		t.Fatalf("the restored session cookie authenticated: %+v", got)
	}
	if _, err := f.st.SessionByPublicID(ctx, sess.PublicID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("the session row survives: %v", err)
	}
	_, _, err = f.accept(inviteTok, "robin-the-boy-wonder-1")
	wantErr(t, "the restored invite", err, auth.ErrInviteInvalid)
	for kind := range map[auth.OneTimeKind]bool{auth.OneTimeInvite: true, auth.OneTimeTempLogin: true, auth.OneTimeBootstrap: true} {
		for tk, err := range f.st.ScanOneTime(ctx, kind) {
			if err != nil || tk != nil {
				t.Fatalf("a %v token row survives: %v %v", kind, tk, err)
			}
		}
	}
}

// TestRestore_PostArchivePasswordChangeNotReverted (SEC2-11): a journaled
// password change blanks the restored hash and requires a reset, so the old
// password no longer signs in; a journaled unlink removes the re-appeared
// link.
func TestRestore_PostArchivePasswordChangeNotReverted(t *testing.T) {
	f := newFx(t)
	tim := f.withPassword(f.user("tim-drake", "gotham.detective"), "red-robin-1")
	f.linkIdentity(tim, "google", "sub-tim")
	f.linkIdentity(tim, "github", "sub-tim-gh")

	f.mustPostRestore(covered(
		journal.Revocation{Kind: journal.PasswordChanged, Subject: userRef(tim.ID), Epoch: tim.CredentialEpoch + 3},
		journal.Revocation{Kind: journal.IdentityUnlinked, Subject: userRef(tim.ID), Detail: []string{"google"}},
	), nil)
	f.rebuild()

	got := f.reload(tim.ID)
	if got.HasPassword || !got.PasswordResetRequired || got.CredentialEpoch < tim.CredentialEpoch+3 {
		t.Fatalf("user after the restore: %+v", got)
	}
	_, err := f.login("tim-drake", "red-robin-1", from("192.0.2.42"))
	if !errors.Is(err, auth.ErrResetRequired) && !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("the old password after a journaled change: %v", err)
	}
	if f.hasIdentity("google", "sub-tim") || !f.hasIdentity("github", "sub-tim-gh") {
		t.Fatal("the journaled unlink removed the wrong link")
	}
}

// TestRevocationCommittedAfterCheckpointReapplied (DATA2-7): a journaled user
// disable and a journaled user delete are re-applied: the disabled user
// cannot sign in and keeps the journaled epoch; the deleted user's keys,
// links and invites go with it.
func TestRevocationCommittedAfterCheckpointReapplied(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	admin := f.user("bruce-wayne", rbac.RoleAdmin)
	harvey := f.withPassword(f.user("harvey-dent", "gotham.detective"), "two-face-1")
	jonathan := f.user("jonathan-crane", "gotham.detective")
	importKey(t, f, "key-scarecrow", jonathan.ID, auth.KeyActive, "gotham:patrol")
	f.linkIdentity(jonathan, "google", "sub-jc")
	f.invite(f.session(admin), detectiveInvite("arnold-wesker"))

	f.mustPostRestore(covered(
		journal.Revocation{Kind: journal.UserDisabled, Subject: userRef(harvey.ID), Epoch: harvey.CredentialEpoch + 5},
		journal.Revocation{Kind: journal.UserDeleted, Subject: userRef(jonathan.ID)},
	), nil)

	h := f.reload(harvey.ID)
	if h.Status != auth.StatusDisabled || h.CredentialEpoch < harvey.CredentialEpoch+5 {
		t.Fatalf("the disabled user: %+v", h)
	}
	if _, err := f.st.GetUser(ctx, jonathan.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("the deleted user survives: %v", err)
	}
	if _, err := f.st.APIKeyByID(ctx, "key-scarecrow"); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("the deleted user's key survives: %v", err)
	}
	if f.hasIdentity("google", "sub-jc") {
		t.Fatal("the deleted user's link survives")
	}
	if got := f.reload(admin.ID).Status; got != auth.StatusActive {
		t.Fatalf("an unnamed admin is %q", got)
	}
	for tk, err := range f.st.ScanOneTime(ctx, auth.OneTimeInvite) {
		if err != nil || tk != nil {
			t.Fatalf("an invite row survives a restore: %v %v", tk, err)
		}
	}
}

// TestPostRestore_SessionRevocationReapplied: a journaled session revocation
// deletes that session, and is counted.
func TestPostRestore_SessionRevocationReapplied(t *testing.T) {
	f := newFx(t)
	dick := f.user("dick-grayson", "gotham.detective")
	sess, err := f.svc.IssueSession(context.Background(), dick, auth.OriginPassword, false, from("192.0.2.43"))
	if err != nil {
		t.Fatal(err)
	}
	counts := f.mustPostRestore(covered(journal.Revocation{
		Kind: journal.SessionRevoked, Subject: entity.Ref{Module: "core", Kind: "session", ID: sess.PublicID},
	}), nil)
	if counts[auth.CountSessionsRevoked] != 1 {
		t.Fatalf("counts %v", counts)
	}
	if _, err := f.st.SessionByPublicID(context.Background(), sess.PublicID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("session after revoke: %v", err)
	}
}

// TestPostRestore_IsIdempotent: a re-run (a crash before "applied") leaves
// every restriction in place and fails nothing; only the epochs move again.
func TestPostRestore_IsIdempotent(t *testing.T) {
	f := newFx(t)
	dick := f.user("dick-grayson", "gotham.detective")
	importKey(t, f, "key-redbird", dick.ID, auth.KeyActive, "gotham:patrol")
	rc := uncovered()
	f.mustPostRestore(rc, nil)
	first := f.key("key-redbird")
	f.mustPostRestore(rc, nil)
	if got := f.key("key-redbird"); got.Status != auth.KeyNeedsConfirmation {
		t.Fatalf("key after a second run: %q", got.Status)
	}
	_ = first
	revs := covered(journal.Revocation{Kind: journal.UserDeleted, Subject: userRef("u-ghost")})
	f.mustPostRestore(revs, nil) // an already-gone or never-stored user is not an error
}

// TestPostRestore_RequiresItsDeps: a missing collaborator is an error, never a
// silent skip of the journal.
func TestPostRestore_RequiresItsDeps(t *testing.T) {
	f := newFx(t)
	for name, mut := range map[string]func(*auth.RestoreDeps){
		"store":       func(d *auth.RestoreDeps) { d.Store = nil },
		"batch":       func(d *auth.RestoreDeps) { d.NewBatch = nil },
		"revocations": func(d *auth.RestoreDeps) { d.Revocations = nil },
	} {
		d := f.restoreDeps(nil, nil)
		mut(&d)
		if _, err := auth.PostRestore(context.Background(), d, uncovered()); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// TestRestrictedLastAdmin_SSORefused (SEC3-15, SEC4-12): the restricted
// account's password, keys, SSO link and every other door are closed: hash
// blanked with a reset required, keys needs_confirmation (a revoked one stays
// revoked), links deleted, SignInRestricted so an extension assertion or a
// compat call is refused as password sign-in is. The restrictions are
// journaled.
func TestRestrictedLastAdmin_SSORefused(t *testing.T) {
	f, c := compatFx(t)
	ctx := context.Background()
	bruce := f.withPassword(f.user("bruce-wayne", rbac.RoleAdmin), "i-am-the-night-1")
	importKey(t, f, "key-cowl", bruce.ID, auth.KeyActive, "core:admin")
	importKey(t, f, "key-old", bruce.ID, auth.KeyRevoked, "core:admin")
	f.linkIdentity(bruce, "google", "sub-bruce")
	f.jrnl.lines = nil

	counts, err := auth.RestrictLastAdmin(ctx, f.restoreDeps(nil, nil), bruce.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := f.reload(bruce.ID)
	if !got.SignInRestricted || !got.PasswordResetRequired || got.HasPassword {
		t.Fatalf("restricted user: %+v", got)
	}
	if f.status("key-cowl") != auth.KeyNeedsConfirmation || f.status("key-old") != auth.KeyRevoked {
		t.Fatalf("keys: %q, %q", f.status("key-cowl"), f.status("key-old"))
	}
	if f.hasIdentity("google", "sub-bruce") {
		t.Fatal("the SSO link survives")
	}
	if counts[auth.CountUsersRestricted] != 1 || counts[auth.CountIdentitiesUnlinked] != 1 || counts[auth.CountKeysUnconfirmed] != 1 {
		t.Fatalf("counts %v", counts)
	}
	kinds := map[journal.Kind]int{}
	for _, l := range f.jrnl.lines {
		kinds[l.Kind]++
	}
	if kinds[journal.PasswordChanged] != 1 || kinds[journal.IdentityUnlinked] != 1 || kinds[journal.APIKeyDisabled] != 1 {
		t.Fatalf("journaled %v", kinds)
	}

	_, err = f.login("bruce-wayne", "i-am-the-night-1", from("192.0.2.44"))
	wantErr(t, "password sign-in", err, auth.ErrInvalidCredentials)
	_, err = c.AuthenticateKey(ctx, "abk_key-cowl", from("192.0.2.44"))
	if err == nil {
		t.Fatal("the unconfirmed key authenticated")
	}
	_, err = c.PrincipalFor(ctx, got, nil, got.CredentialEpoch, from("192.0.2.44"))
	wantErr(t, "an extension or compat principal", err, auth.ErrResetRequired)
	_, err = f.svc.PrincipalFor(ctx, got, auth.MethodSession, nil, got.CredentialEpoch)
	wantErr(t, "a session principal", err, auth.ErrResetRequired)

	// Idempotent; and the system user cannot be restricted.
	if _, err := auth.RestrictLastAdmin(ctx, f.restoreDeps(nil, nil), bruce.ID); err != nil {
		t.Fatalf("a second restriction: %v", err)
	}
	if _, err := auth.RestrictLastAdmin(ctx, f.restoreDeps(nil, nil), "u-ghost"); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("an unknown user: %v", err)
	}
}
