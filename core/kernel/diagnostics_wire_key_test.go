// file: core/kernel/diagnostics_wire_key_test.go
// version: 1.0.0
// guid: c3f81e6a-2b49-47d0-a5e3-7d90b1f4c268
// last-edited: 2026-11-28

package kernel

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// F5-18 (i): a token actor's run is bound to the key that started it. Each
// cell starts from a live key whose viewer works, changes one thing about the
// key the way the world can after the run started, and asks ViewerFor again.

func dwKeyFixture(t *testing.T) (*dwKeys, *auth.APIKey, func(tasks.Actor) (func(string) bool, error)) {
	t.Helper()
	k := dwKey("key-batphone", "bruce", "core:system.manage", "core:metrics.read")
	table := &dwKeys{keys: []*auth.APIKey{k}}
	users := dwUsers{
		"bruce":  dwUser("bruce", auth.StatusActive, "admin"),
		"selina": dwUser("selina", auth.StatusActive, "admin"),
	}
	vf := diagnosticsViewerFor(users, table, newDWCompiler(t))
	// view is ViewerFor as a can(permission) function, nil when it refused.
	view := func(a tasks.Actor) (func(string) bool, error) {
		v, err := vf(t.Context(), a)
		if err != nil {
			return nil, err
		}
		return func(p string) bool { return v.Can(rbac.Permission(p)) }, nil
	}
	return table, k, view
}

var dwToken = tasks.Actor{UserID: "bruce", AuthMethod: "token", CredentialID: "key-batphone"}

// dwCompat is the same key's actor as a module compat surface (books.abs)
// stamps it: its own method name, the key's id.
var dwCompat = tasks.Actor{UserID: "bruce", AuthMethod: "books.abs", CredentialID: "key-batphone"}

// eachKeyActor runs fn once for a core API key's actor ("token") and once for
// a compat surface's actor over a key ("books.abs"). Both are key-bound, and
// the decision is made on the CredentialID, never on the method's name.
func eachKeyActor(t *testing.T, fn func(t *testing.T, a tasks.Actor)) {
	t.Helper()
	for _, a := range []tasks.Actor{dwToken, dwCompat} {
		t.Run(a.AuthMethod, func(t *testing.T) { fn(t, a) })
	}
}

// refused reports whether ViewerFor refused, and fails the test when it did so
// for any reason but errNoDiagnosticsViewer.
func refused(t *testing.T, what string, can func(string) bool, err error) bool {
	t.Helper()
	if err == nil {
		if can == nil {
			t.Fatalf("%s: no viewer and no error", what)
		}
		return false
	}
	if !errors.Is(err, errNoDiagnosticsViewer) {
		t.Errorf("%s: err = %v, want errNoDiagnosticsViewer", what, err)
	}
	// The refusal lands in the run's error and log: it names the reason, never
	// the credential.
	if strings.Contains(err.Error(), dwToken.CredentialID) {
		t.Errorf("%s: err = %q carries the credential id", what, err)
	}
	return true
}

func TestDiagnosticsViewerFor_TokenActorWithNoCredentialIDIsRefused(t *testing.T) {
	_, _, view := dwKeyFixture(t)
	for _, id := range []string{"", "key-unknown"} {
		a := dwToken
		a.CredentialID = id
		can, err := view(a)
		if !refused(t, "token actor with credential "+id, can, err) {
			t.Errorf("token actor with credential %q was admitted", id)
		}
		if id == "" && (err == nil || !strings.Contains(err.Error(), "no credential id")) {
			t.Errorf("err = %v, want the reason \"no credential id\"", err)
		}
	}
}

// With no CredentialID the method must be a known non-key one: "token" and
// every name nobody classified (a compat surface's, an identity extension's,
// the empty method) fail closed, and so does the user's own session id passed
// off as no key at all.
func TestDiagnosticsViewerFor_UnrecognisedMethodWithNoCredentialIDIsRefused(t *testing.T) {
	_, _, view := dwKeyFixture(t)
	for _, m := range []string{"token", "books.abs", "ext:cfaccess", "api_key", "Session", "SYSTEM", ""} {
		can, err := view(tasks.Actor{UserID: "bruce", AuthMethod: m})
		if !refused(t, "method "+m+" with no credential id", can, err) {
			t.Errorf("method %q with no credential id was admitted", m)
		}
	}
}

// A key-bound actor is never interactive, whatever method it claims: the
// credential id says it is a key, so the key's non-interactive checker applies.
func TestDiagnosticsViewerFor_KeyBoundActorIsNeverInteractive(t *testing.T) {
	_, k, view := dwKeyFixture(t)
	k.Scopes = []rbac.Permission{"core:system.manage", "core:backup.restore"}
	for _, m := range []string{"token", "books.abs", "session", "system", "auth_disabled"} {
		a := dwToken
		a.AuthMethod = m
		can, err := view(a)
		if refused(t, "key-bound "+m, can, err) {
			t.Fatalf("key-bound %s refused: %v", m, err)
		}
		if !can("core:system.manage") || can("core:backup.restore") {
			t.Errorf("key-bound %s: system.manage %v, backup.restore %v; want the key's non-Sensitive scope only", m, can("core:system.manage"), can("core:backup.restore"))
		}
	}
}

func TestDiagnosticsViewerFor_KeyRevokedAfterStartIsRefused(t *testing.T) {
	eachKeyActor(t, func(t *testing.T, a tasks.Actor) {
		table, k, view := dwKeyFixture(t)
		if can, err := view(a); refused(t, "the live key", can, err) {
			t.Fatalf("the live key's viewer is refused: %v", err)
		}
		for _, st := range []auth.KeyStatus{auth.KeyRevoked, auth.KeyDisabled, auth.KeyNeedsConfirmation} {
			k.Status = st
			can, err := view(a)
			if !refused(t, "key "+string(st), can, err) {
				t.Errorf("a %s key was admitted", st)
			}
			if st == auth.KeyRevoked && (err == nil || !strings.Contains(err.Error(), "credential revoked")) {
				t.Errorf("revoked key: err = %v, want the reason \"credential revoked\"", err)
			}
		}
		k.Status = auth.KeyActive
		table.keys = nil // deleted outright
		if can, err := view(a); !refused(t, "a key that is gone", can, err) {
			t.Error("a key that is gone was admitted")
		}
		table.keys, table.err = []*auth.APIKey{k}, errors.New("store unavailable")
		if can, err := view(a); !refused(t, "an unreadable key table", can, err) {
			t.Error("an unreadable key table admitted the run")
		}
		// A read error ahead of the live key still refuses: the key's state is not known.
		table.errThenKeys = true
		if can, err := view(a); !refused(t, "a read error before the live key", can, err) {
			t.Error("a read error before the live key admitted the run")
		}
	})
}

func TestDiagnosticsViewerFor_KeyNarrowedAfterStartNarrowsTheViewer(t *testing.T) {
	eachKeyActor(t, func(t *testing.T, a tasks.Actor) {
		_, k, view := dwKeyFixture(t)
		can, err := view(a)
		if refused(t, "the live key", can, err) || !can("core:system.manage") || !can("core:metrics.read") {
			t.Fatalf("live key: err %v; want system.manage and metrics.read", err)
		}
		k.Scopes = []rbac.Permission{"core:metrics.read"} // narrowed after the run started
		can, err = view(a)
		if refused(t, "the narrowed key", can, err) {
			t.Fatalf("narrowed key refused: %v", err)
		}
		if can("core:system.manage") || !can("core:metrics.read") {
			t.Errorf("narrowed key: system.manage %v, metrics.read %v; want the lost section left out and the kept one in",
				can("core:system.manage"), can("core:metrics.read"))
		}
		k.Scopes = nil
		if can, err := view(a); !refused(t, "a key with no scope", can, err) {
			t.Error("a key with no scope was admitted: an empty list is every role permission to a checker")
		}
	})
}

func TestDiagnosticsViewerFor_ExpiredKeyIsRefused(t *testing.T) {
	eachKeyActor(t, func(t *testing.T, a tasks.Actor) {
		_, k, view := dwKeyFixture(t)
		k.ExpiresAt = time.Now().Add(-time.Minute)
		can, err := view(a)
		if !refused(t, "expired key", can, err) {
			t.Fatal("an expired key was admitted")
		}
		if !strings.Contains(err.Error(), "credential expired") {
			t.Errorf("err = %v, want the reason \"credential expired\"", err)
		}
	})
}

func TestDiagnosticsViewerFor_KeyOwnedByAnotherUserIsRefused(t *testing.T) {
	_, k, view := dwKeyFixture(t)
	k.Owner = "selina" // the table answers for every owner, as a mis-scoped read would
	for _, a := range []tasks.Actor{dwToken, dwCompat} {
		if can, err := view(a); !refused(t, "another user's key via "+a.AuthMethod, can, err) {
			t.Errorf("a key owned by another user was admitted to a %s actor", a.AuthMethod)
		}
	}
	// And the key's own owner is fine.
	owner := dwToken
	owner.UserID = "selina"
	if can, err := view(owner); refused(t, "the key's owner", can, err) {
		t.Errorf("the key's owner is refused: %v", err)
	}
}

// A session or system actor carries no CredentialID (actorOf stamps one only
// for a key) and takes no key lookup and no liveness check: a run keeps going
// after its starter signs out. The key table is made to fail so that any lookup
// would refuse.
func TestDiagnosticsViewerFor_SessionActorIsUnchanged(t *testing.T) {
	table, _, view := dwKeyFixture(t)
	table.keys, table.err = nil, errors.New("store unavailable")
	for _, m := range []string{"session", "session_delegated", "auth_disabled", "system"} {
		can, err := view(tasks.Actor{UserID: "bruce", AuthMethod: m})
		if refused(t, "method "+m, can, err) || !can("core:system.manage") || !can("core:metrics.read") {
			t.Errorf("%s actor: err %v; want the user's roles, unnarrowed", m, err)
		}
	}
}
