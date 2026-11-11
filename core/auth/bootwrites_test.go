// file: core/auth/bootwrites_test.go
// version: 1.0.0
// guid: 2d9b6f84-a317-4c5e-b0e2-8f61c3a7d905
// last-edited: 2026-11-11

package auth_test

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

func (f *fx) bootWrites(files auth.StateFiles) error {
	f.t.Helper()
	return auth.BootWrites(context.Background(), f.restoreDeps(files, nil))
}

func (f *fx) mustBootWrites(files auth.StateFiles) {
	f.t.Helper()
	if err := f.bootWrites(files); err != nil {
		f.t.Fatalf("BootWrites: %v", err)
	}
}

func (f *fx) startupKeys() []*auth.APIKey {
	f.t.Helper()
	var out []*auth.APIKey
	for k, err := range f.st.ScanAPIKeys(context.Background(), "") {
		if err != nil {
			f.t.Fatal(err)
		}
		if k.Name == auth.ReadOnlyKeyName {
			out = append(out, k)
		}
	}
	return out
}

func (f *fx) liveStartupKey() *auth.APIKey {
	f.t.Helper()
	var live *auth.APIKey
	for _, k := range f.startupKeys() {
		if k.Status == auth.KeyActive {
			if live != nil {
				f.t.Fatal("two live start-up keys")
			}
			live = k
		}
	}
	return live
}

// TestBootWrites_SeedsSystemUserOnce: BootWrites seeds _system, with no
// password and the system status, and a second run changes nothing.
func TestBootWrites_SeedsSystemUserOnce(t *testing.T) {
	f := newFx(t)
	f.mustBootWrites(nil)
	u := f.reload(auth.SystemUserID)
	if u.Status != auth.StatusSystem || u.HasPassword || u.Username != "_system" {
		t.Fatalf("system user: %+v", u)
	}
	f.mustBootWrites(nil)
	if again := f.reload(auth.SystemUserID); again.Version != u.Version {
		t.Fatalf("a second boot rewrote the system user: %d -> %d", u.Version, again.Version)
	}
	if h, _ := f.st.PasswordHash(context.Background(), auth.SystemUserID); h != "" {
		t.Fatal("the system user has a password")
	}
}

// TestBootWrites_MintsRedeemableBootstrapToken: the token is written 0600 to
// the state filesystem, redeems through RedeemBootstrap, a restart replaces it
// (the old value is dead) and a restart never leaves two live tokens.
func TestBootWrites_MintsRedeemableBootstrapToken(t *testing.T) {
	f, files := bootFx(t)
	ctx := context.Background()
	f.mustBootWrites(files)

	first := files.file(auth.BootstrapTokenLabel, auth.BootstrapTokenFile)
	if first == "" {
		t.Fatal("no bootstrap token file written")
	}
	if perm := files.perms[auth.BootstrapTokenLabel+"/"+auth.BootstrapTokenFile]; perm != fs.FileMode(0o600) {
		t.Fatalf("token file mode %o", perm)
	}
	if !f.tokenStored(first) {
		t.Fatal("the written token is not stored")
	}

	f.advance(time.Minute)
	f.mustBootWrites(files)
	second := files.file(auth.BootstrapTokenLabel, auth.BootstrapTokenFile)
	if second == first || f.tokenStored(first) || !f.tokenStored(second) {
		t.Fatal("a restart did not replace the bootstrap token")
	}
	live := 0
	for tk, err := range f.st.ScanOneTime(ctx, auth.OneTimeBootstrap) {
		if err != nil {
			t.Fatal(err)
		}
		if tk != nil {
			live++
			if want := f.clock().Add(auth.DefaultBootstrapTokenTTL); !tk.ExpiresAt.Equal(want) {
				t.Fatalf("expiry %v, want %v", tk.ExpiresAt, want)
			}
		}
	}
	if live != 1 {
		t.Fatalf("%d live bootstrap tokens", live)
	}

	if _, err := f.svc.RedeemBootstrap(ctx, first, from("192.0.2.60")); err == nil {
		t.Fatal("the replaced token redeemed")
	}
	res, err := f.svc.RedeemBootstrap(ctx, second, from("192.0.2.60"))
	if err != nil || res == nil {
		t.Fatalf("the minted token did not redeem: %v", err)
	}
}

// TestBootWrites_AuthNewWritesNothing (DATA4-10): constructing the service
// stores no system user, token or key; only BootWrites does.
func TestBootWrites_AuthNewWritesNothing(t *testing.T) {
	f, files := bootFx(t)
	f.setSettings(auth.Settings{ReadOnlyKey: true})
	f.rebuild()
	_ = f.serviceWith(f.st, files)
	if len(files.written) != 0 {
		t.Fatalf("auth.New wrote files: %v", files.written)
	}
	if _, err := f.st.GetUser(context.Background(), auth.SystemUserID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("auth.New seeded the system user: %v", err)
	}
	for tk, err := range f.st.ScanOneTime(context.Background(), auth.OneTimeBootstrap) {
		if err != nil || tk != nil {
			t.Fatalf("auth.New stored a bootstrap token: %v %v", tk, err)
		}
	}
	if len(f.startupKeys()) != 0 {
		t.Fatal("auth.New minted a start-up key")
	}
}

// TestBootWrites_BootstrapDisabledOrNoFiles: no token is minted when
// bootstrap is switched off or when there is no state filesystem to carry it.
func TestBootWrites_BootstrapDisabledOrNoFiles(t *testing.T) {
	f, files := bootFx(t)
	f.setSettings(auth.Settings{BootstrapEnabled: boolp(false)})
	f.mustBootWrites(files)
	if got := files.file(auth.BootstrapTokenLabel, auth.BootstrapTokenFile); got != "" {
		t.Fatalf("a token was written with bootstrap disabled: %q", got)
	}

	g := newFx(t)
	g.mustBootWrites(nil)
	for tk, err := range g.st.ScanOneTime(context.Background(), auth.OneTimeBootstrap) {
		if err != nil || tk != nil {
			t.Fatalf("a token was stored with no state filesystem: %v %v", tk, err)
		}
	}
}

// TestBootWrites_TokenFileFailureIsNotFatal: an unwritable file is logged;
// BootWrites still succeeds and the key and user writes happen.
func TestBootWrites_TokenFileFailureIsNotFatal(t *testing.T) {
	f, files := bootFx(t)
	files.failWrite = errors.New("disk full")
	f.mustBootWrites(files)
	if _, err := f.st.GetUser(context.Background(), auth.SystemUserID); err != nil {
		t.Fatalf("the system user was not seeded: %v", err)
	}
}

// TestBootWrites_ReadOnlyKey: the start-up key has the default scope, belongs
// to the first active admin, lives 24 h, is written 0600, and the next boot
// replaces it (the old one is revoked, never two live).
func TestBootWrites_ReadOnlyKey(t *testing.T) {
	f, files := bootFx(t)
	f.user("harvey-bullock", "gotham.detective")
	f.user("lucius-fox", rbac.RoleAdmin)
	f.user("bruce-wayne", rbac.RoleAdmin)
	f.setSettings(auth.Settings{ReadOnlyKey: true})
	f.mustBootWrites(files)

	k := f.liveStartupKey()
	if k == nil {
		t.Fatal("no start-up key minted")
	}
	if k.Owner != "u-bruce-wayne" {
		t.Fatalf("owner %q, want the first admin by ID", k.Owner)
	}
	if !slices.Equal(k.Scopes, []rbac.Permission{"core:system.view"}) {
		t.Fatalf("scopes %v", k.Scopes)
	}
	if want := f.clock().Add(auth.ReadOnlyKeyTTL); !k.ExpiresAt.Equal(want) {
		t.Fatalf("expiry %v, want %v", k.ExpiresAt, want)
	}
	raw := files.file(auth.ReadOnlyKeyLabel, auth.ReadOnlyKeyFile)
	if raw == "" || files.perms[auth.ReadOnlyKeyLabel+"/"+auth.ReadOnlyKeyFile] != fs.FileMode(0o600) {
		t.Fatalf("key file %q mode %o", raw, files.perms[auth.ReadOnlyKeyLabel+"/"+auth.ReadOnlyKeyFile])
	}
	f.rebuild()
	if _, got := serve(t, f.svc, request(raw, "")); got.p == nil {
		t.Fatalf("the start-up key did not authenticate: %+v", got)
	}

	f.advance(time.Hour)
	f.mustBootWrites(files)
	next := f.liveStartupKey()
	if next == nil || next.ID == k.ID {
		t.Fatal("the next boot did not mint a new key")
	}
	if got := f.status(k.ID); got != auth.KeyRevoked {
		t.Fatalf("the previous start-up key is %q", got)
	}
	if len(f.startupKeys()) != 2 {
		t.Fatalf("%d start-up keys", len(f.startupKeys()))
	}
	if len(f.jrnl.lines) == 0 {
		t.Fatal("the replaced key's revocation was not journaled")
	}
}

// TestBootWrites_ReadOnlyKeyDisabledRevokesLeftover: with the key switched
// off, a leftover start-up key is revoked and none minted.
func TestBootWrites_ReadOnlyKeyDisabledRevokesLeftover(t *testing.T) {
	f, files := bootFx(t)
	f.user("bruce-wayne", rbac.RoleAdmin)
	f.setSettings(auth.Settings{ReadOnlyKey: true})
	f.mustBootWrites(files)
	old := f.liveStartupKey()
	f.setSettings(auth.Settings{})
	f.mustBootWrites(files)
	if f.liveStartupKey() != nil || f.status(old.ID) != auth.KeyRevoked {
		t.Fatal("a leftover start-up key survives with the key disabled")
	}
}

// TestBootWrites_ReadOnlyKeyOwner: no admin yet means no key; an admin that
// is SignInRestricted is skipped; the key never goes to a non-admin.
func TestBootWrites_ReadOnlyKeyOwner(t *testing.T) {
	f, files := bootFx(t)
	f.user("harvey-bullock", "gotham.detective")
	f.setSettings(auth.Settings{ReadOnlyKey: true})
	f.mustBootWrites(files)
	if f.liveStartupKey() != nil {
		t.Fatal("a key was minted with no admin")
	}
	if got := files.file(auth.ReadOnlyKeyLabel, auth.ReadOnlyKeyFile); got != "" {
		t.Fatalf("a key file was written with no key: %q", got)
	}

	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	f.edit(bruce, func(u *auth.User) { u.SignInRestricted = true })
	f.mustBootWrites(files)
	if f.liveStartupKey() != nil {
		t.Fatal("a restricted admin owns the start-up key")
	}

	f.user("lucius-fox", rbac.RoleAdmin)
	f.mustBootWrites(files)
	if k := f.liveStartupKey(); k == nil || k.Owner != "u-lucius-fox" {
		t.Fatalf("start-up key %+v, want it owned by the unrestricted admin", k)
	}
}

// TestBootWrites_ReadOnlyKeyScopes: configured scopes are used; an empty list
// falls back to the default; an unparseable scope and core:admin are refused
// with ErrBadInput and nothing is minted.
func TestBootWrites_ReadOnlyKeyScopes(t *testing.T) {
	f, files := bootFx(t)
	f.user("bruce-wayne", rbac.RoleAdmin)

	f.setSettings(auth.Settings{ReadOnlyKey: true, ReadOnlyKeyScopes: []rbac.Permission{"gotham:patrol", "core:system.view"}})
	f.mustBootWrites(files)
	if k := f.liveStartupKey(); k == nil || !slices.Equal(k.Scopes, []rbac.Permission{"gotham:patrol", "core:system.view"}) {
		t.Fatalf("configured scopes not used: %+v", k)
	}

	for name, scopes := range map[string][]rbac.Permission{
		"admin":      {"core:admin"},
		"garbage":    {"not a scope"},
		"admin+safe": {"core:system.view", "core:admin"},
	} {
		g, gfiles := bootFx(t)
		g.user("bruce-wayne", rbac.RoleAdmin)
		g.setSettings(auth.Settings{ReadOnlyKey: true, ReadOnlyKeyScopes: scopes})
		err := g.bootWrites(gfiles)
		if !errors.Is(err, auth.ErrBadInput) {
			t.Errorf("%s: err = %v, want ErrBadInput", name, err)
		}
		if g.liveStartupKey() != nil {
			t.Errorf("%s: a key was minted", name)
		}
	}
}

// TestBootWrites_StoresOnlyHashes: the key row does not carry its raw value;
// it is found only by presenting the token.
func TestBootWrites_StoresOnlyHashes(t *testing.T) {
	f, files := bootFx(t)
	f.user("bruce-wayne", rbac.RoleAdmin)
	f.setSettings(auth.Settings{ReadOnlyKey: true})
	f.mustBootWrites(files)
	raw := files.file(auth.ReadOnlyKeyLabel, auth.ReadOnlyKeyFile)
	k := f.liveStartupKey()
	if strings.Contains(k.ID, raw) || strings.Contains(k.Name, raw) {
		t.Fatal("the key row carries its raw value")
	}
	if _, err := f.st.APIKeyByToken(context.Background(), auth.Token(raw)); err != nil {
		t.Fatalf("the key is not stored under its hash: %v", err)
	}
}

// TestBootWrites_RequiresRevocations: no journal wired is an error, never a
// silent skip.
func TestBootWrites_RequiresRevocations(t *testing.T) {
	f := newFx(t)
	d := f.restoreDeps(nil, nil)
	d.Revocations = nil
	if err := auth.BootWrites(context.Background(), d); err == nil {
		t.Fatal("no error without revocations")
	}
}
