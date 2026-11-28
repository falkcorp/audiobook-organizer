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
	vf := diagnosticsViewerFor(users, table, newDWCompiler(t), dwNonKey())
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

// The exact reasons ViewerFor refuses with: each is the text after
// errNoDiagnosticsViewer's own, and a refusal test names the one it expects, so
// a mutant that refuses for a different reason (or for the right reason in the
// wrong branch) cannot pass on a substring.
const (
	reasonNoCredential  = "no credential id"
	reasonRevoked       = "credential revoked"
	reasonExpired       = "credential expired"
	reasonOtherUser     = "credential belongs to another user"
	reasonNoScope       = "credential has no scope"
	reasonUnregistered  = "method not registered and no credential id"
	reasonStoreRead     = "credential read: store unavailable"
	reasonNoActorUserID = "the actor names no user"
)

// refused reports whether ViewerFor refused. A refusal must be exactly
// errNoDiagnosticsViewer with the named reason; any other error text fails the
// test, as does a refusal that carries the credential id.
func refused(t *testing.T, what string, can func(string) bool, err error, reason string) bool {
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
	if want := errNoDiagnosticsViewer.Error() + ": " + reason; err.Error() != want {
		t.Errorf("%s: err = %q, want exactly %q", what, err.Error(), want)
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
	for _, c := range []struct{ id, reason string }{
		{"", reasonNoCredential},
		{"key-unknown", reasonRevoked}, // a key id the owner does not hold reads as gone
	} {
		a := dwToken
		a.CredentialID = c.id
		can, err := view(a)
		if !refused(t, "token actor with credential "+c.id, can, err, c.reason) {
			t.Errorf("token actor with credential %q was admitted", c.id)
		}
	}
}

// With no CredentialID the method must be a registered or core non-key one:
// "token", the old HEAD spellings and every name nobody registered fail closed.
// An unregistered compat or extension name, a prefix of a registered one, the
// empty method and a case variant of a core one are all refused.
func TestDiagnosticsViewerFor_UnrecognisedMethodWithNoCredentialIDIsRefused(t *testing.T) {
	_, _, view := dwKeyFixture(t)
	for _, c := range []struct{ method, reason string }{
		{"token", reasonNoCredential},
		{"api_key", reasonUnregistered}, // HEAD's spelling of a key method
		{"cf_access", reasonUnregistered},
		{"abs", reasonUnregistered},
		{"ext:unregistered", reasonUnregistered},
		{"ext:cfaccess-x", reasonUnregistered}, // a registered name with a suffix
		{"ext:cfacces", reasonUnregistered},    // a prefix of a registered name
		{"ext:", reasonUnregistered},           // the bare extension prefix
		{"books.", reasonUnregistered},
		{"books.abs2", reasonUnregistered},
		{"books.unregistered", reasonUnregistered},
		{"Session", reasonUnregistered},
		{"SYSTEM", reasonUnregistered},
		{"", reasonUnregistered},
	} {
		can, err := view(tasks.Actor{UserID: "bruce", AuthMethod: c.method})
		if !refused(t, "method "+c.method+" with no credential id", can, err, c.reason) {
			t.Errorf("method %q with no credential id was admitted", c.method)
		}
	}
}

// F1: a method registered with auth and carrying no credential id is admitted
// as it was at HEAD: the user's current roles, non-interactive, with no key
// lookup (the key table is made to fail, so any lookup would refuse). One cell
// per registered method.
func TestDiagnosticsViewerFor_RegisteredMethodWithNoCredentialIDIsAdmittedNonInteractive(t *testing.T) {
	for _, m := range []string{"ext:cfaccess", "books.abs", "books.batcomputer"} {
		t.Run(m, func(t *testing.T) {
			table, _, view := dwKeyFixture(t)
			table.keys, table.err = nil, errors.New("store unavailable")
			can, err := view(tasks.Actor{UserID: "bruce", AuthMethod: m})
			if refused(t, m, can, err, "") {
				t.Fatalf("registered method %s refused: %v", m, err)
			}
			if !can("core:system.manage") || !can("core:metrics.read") {
				t.Errorf("%s: want the user's ordinary roles", m)
			}
			if can("core:backup.restore") {
				t.Errorf("%s: admitted the Sensitive core:backup.restore, which only an interactive session holds", m)
			}
		})
	}
}

// F2: ext:cfaccess registers Interactive: true, and nonKeyMethods is built from
// that very spec, but the viewer is compiled non-interactive: HEAD's
// auth.Method.Interactive is true only for "session". So the identity
// extension's actor gets its ordinary roles and is refused a Sensitive scope.
func TestDiagnosticsViewerFor_ExtensionWithInteractiveSpecIsStillNonInteractive(t *testing.T) {
	_, _, view := dwKeyFixture(t)
	can, err := view(tasks.Actor{UserID: "bruce", AuthMethod: "ext:cfaccess"})
	if refused(t, "ext:cfaccess", can, err, "") {
		t.Fatalf("ext:cfaccess refused: %v", err)
	}
	if can("core:backup.restore") {
		t.Error("ext:cfaccess was admitted the Sensitive core:backup.restore: its Spec.Interactive was honoured")
	}
	if !can("core:system.manage") {
		t.Error("ext:cfaccess lost the user's ordinary roles")
	}
	// The same user's session does get it.
	can, err = view(tasks.Actor{UserID: "bruce", AuthMethod: "session"})
	if refused(t, "session", can, err, "") || !can("core:backup.restore") {
		t.Errorf("session: err %v; want core:backup.restore", err)
	}
}

// nonKeyMethods is an exact-match set of the core methods plus what is
// registered: a method missing from either list, a prefix and an empty spec
// name never match.
func TestNonKeyMethods_ExactMembership(t *testing.T) {
	in := nonKeyMethods(
		[]auth.Extension{{Spec: auth.MethodSpec{Method: "ext:cfaccess"}}, {Spec: auth.MethodSpec{Method: ""}}},
		[]auth.MethodSpec{{Method: "books.abs"}, {Method: ""}},
	)
	for _, m := range []string{"session", "session_delegated", "auth_disabled", "system", "ext:cfaccess", "books.abs"} {
		if !in(m) {
			t.Errorf("%q is not a member", m)
		}
	}
	for _, m := range []string{"", "token", "api_key", "ext:", "ext:cfaccess-x", "ext:cfacces", "books.", "books.abs2", "books.batcomputer", "ext:other", "Session"} {
		if in(m) {
			t.Errorf("%q is a member", m)
		}
	}
	// Drop one entry from each list and the method is gone.
	if nonKeyMethods(nil, []auth.MethodSpec{{Method: "books.abs"}})("ext:cfaccess") || nonKeyMethods([]auth.Extension{{Spec: auth.MethodSpec{Method: "ext:cfaccess"}}}, nil)("books.abs") {
		t.Error("a method outside the lists it was built from is a member")
	}
}

// The invariant: being in the non-key set never exempts a key-bound principal.
// books.batcomputer is a compat method this test registers (it is in dwNonKey's
// set) and a principal over a key under that name stamps the key's id, so its
// run is key-checked: revoke the key and the run is refused, expire it or narrow
// it and the viewer follows, though the method name is a member of the set.
func TestDiagnosticsViewerFor_NonKeyMembershipNeverExemptsAKeyBoundPrincipal(t *testing.T) {
	if !dwNonKey()("books.batcomputer") {
		t.Fatal("fixture: books.batcomputer is not in the non-key set")
	}
	table, k, view := dwKeyFixture(t)
	a := tasks.Actor{UserID: "bruce", AuthMethod: "books.batcomputer", CredentialID: "key-batphone"}
	if can, err := view(a); refused(t, "the live key", can, err, "") || !can("core:system.manage") || can("core:backup.restore") {
		t.Fatalf("live key under a registered method: err %v; want the key's scopes, non-interactive", err)
	}
	k.Scopes = []rbac.Permission{"core:metrics.read"}
	if can, err := view(a); refused(t, "narrowed", can, err, "") || can("core:system.manage") {
		t.Errorf("narrowed key under a registered method: err %v; want system.manage left out", err)
	}
	k.Scopes = []rbac.Permission{"core:system.manage"}
	k.Status = auth.KeyRevoked
	if can, err := view(a); !refused(t, "revoked", can, err, reasonRevoked) {
		t.Error("a revoked key was admitted because its method is in the non-key set")
	}
	k.Status = auth.KeyActive
	table.keys = nil
	if can, err := view(a); !refused(t, "gone", can, err, reasonRevoked) {
		t.Error("a deleted key was admitted because its method is in the non-key set")
	}
}

// A key-bound actor is never interactive, whatever method it claims: the
// credential id says it is a key, so the key's non-interactive checker applies.
func TestDiagnosticsViewerFor_KeyBoundActorIsNeverInteractive(t *testing.T) {
	_, k, view := dwKeyFixture(t)
	k.Scopes = []rbac.Permission{"core:system.manage", "core:backup.restore"}
	for _, m := range []string{"token", "books.abs", "session", "system", "auth_disabled", "ext:cfaccess"} {
		a := dwToken
		a.AuthMethod = m
		can, err := view(a)
		if refused(t, "key-bound "+m, can, err, "") {
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
		if can, err := view(a); refused(t, "the live key", can, err, "") {
			t.Fatalf("the live key's viewer is refused: %v", err)
		}
		for _, st := range []auth.KeyStatus{auth.KeyRevoked, auth.KeyDisabled, auth.KeyNeedsConfirmation} {
			k.Status = st
			can, err := view(a)
			if !refused(t, "key "+string(st), can, err, reasonRevoked) {
				t.Errorf("a %s key was admitted", st)
			}
		}
		k.Status = auth.KeyActive
		table.keys = nil // deleted outright
		if can, err := view(a); !refused(t, "a key that is gone", can, err, reasonRevoked) {
			t.Error("a key that is gone was admitted")
		}
		table.keys, table.err = []*auth.APIKey{k}, errors.New("store unavailable")
		if can, err := view(a); !refused(t, "an unreadable key table", can, err, reasonStoreRead) {
			t.Error("an unreadable key table admitted the run")
		}
		// A read error ahead of the live key still refuses: the key's state is not known.
		table.errThenKeys = true
		if can, err := view(a); !refused(t, "a read error before the live key", can, err, reasonStoreRead) {
			t.Error("a read error before the live key admitted the run")
		}
	})
}

func TestDiagnosticsViewerFor_KeyNarrowedAfterStartNarrowsTheViewer(t *testing.T) {
	eachKeyActor(t, func(t *testing.T, a tasks.Actor) {
		_, k, view := dwKeyFixture(t)
		can, err := view(a)
		if refused(t, "the live key", can, err, "") || !can("core:system.manage") || !can("core:metrics.read") {
			t.Fatalf("live key: err %v; want system.manage and metrics.read", err)
		}
		k.Scopes = []rbac.Permission{"core:metrics.read"} // narrowed after the run started
		can, err = view(a)
		if refused(t, "the narrowed key", can, err, "") {
			t.Fatalf("narrowed key refused: %v", err)
		}
		if can("core:system.manage") || !can("core:metrics.read") {
			t.Errorf("narrowed key: system.manage %v, metrics.read %v; want the lost section left out and the kept one in",
				can("core:system.manage"), can("core:metrics.read"))
		}
		k.Scopes = nil
		if can, err := view(a); !refused(t, "a key with no scope", can, err, reasonNoScope) {
			t.Error("a key with no scope was admitted: an empty list is every role permission to a checker")
		}
	})
}

func TestDiagnosticsViewerFor_ExpiredKeyIsRefused(t *testing.T) {
	eachKeyActor(t, func(t *testing.T, a tasks.Actor) {
		_, k, view := dwKeyFixture(t)
		k.ExpiresAt = time.Now().Add(-time.Minute)
		can, err := view(a)
		if !refused(t, "expired key", can, err, reasonExpired) {
			t.Fatal("an expired key was admitted")
		}
	})
}

func TestDiagnosticsViewerFor_KeyOwnedByAnotherUserIsRefused(t *testing.T) {
	_, k, view := dwKeyFixture(t)
	k.Owner = "selina" // the table answers for every owner, as a mis-scoped read would
	for _, a := range []tasks.Actor{dwToken, dwCompat} {
		if can, err := view(a); !refused(t, "another user's key via "+a.AuthMethod, can, err, reasonOtherUser) {
			t.Errorf("a key owned by another user was admitted to a %s actor", a.AuthMethod)
		}
	}
	// And the key's own owner is fine.
	owner := dwToken
	owner.UserID = "selina"
	if can, err := view(owner); refused(t, "the key's owner", can, err, "") {
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
		if refused(t, "method "+m, can, err, "") || !can("core:system.manage") || !can("core:metrics.read") {
			t.Errorf("%s actor: err %v; want the user's roles, unnarrowed", m, err)
		}
	}
}

// The actor that names no user is refused before any method or key logic.
func TestDiagnosticsViewerFor_NoUserIsRefusedWithItsReason(t *testing.T) {
	_, _, view := dwKeyFixture(t)
	can, err := view(tasks.Actor{AuthMethod: "token", CredentialID: "key-batphone"})
	if !refused(t, "no user", can, err, reasonNoActorUserID) {
		t.Error("an actor with no user was admitted")
	}
}
