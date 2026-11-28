// file: core/internal/taskhttp/actor_credential_test.go
// version: 1.0.0
// guid: 8d4a71c3-62be-4e90-b5f7-0a39c1e86d25
// last-edited: 2026-11-28

package taskhttp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// TestActorOf_StampsCredentialID is F5-18 (i): the actor a principal's runs are
// stamped with carries a CredentialID for a key-bound principal and for no
// other. A core API key and a compat surface's principal over a key both stamp
// the key's id (from KeyRef, never from an editable field); a session, a
// delegated session, an extension, a compat principal built over no key and the
// auth-disabled principal stamp none, whatever their own CredentialID says.
func TestActorOf_StampsCredentialID(t *testing.T) {
	r := newRealAuth(t)
	ctx := context.Background()
	lucius := r.user("lucius", "batcave.keeper")

	keyed := r.key(lucius, "batcave:run")
	keyID := keyed.CredentialID

	compat, err := r.svc.RegisterCompat("arkham", auth.MethodSpec{Method: gateWay})
	if err != nil {
		t.Fatalf("RegisterCompat: %v", err)
	}
	raw, k, err := r.svc.CreateAPIKey(ctx, r.as(lucius, auth.MethodSession), auth.KeySpec{Name: "gate pass", Scopes: []rbac.Permission{"batcave:run"}})
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	over, err := compat.AuthenticateKey(ctx, raw, auth.ClientInfo{})
	if err != nil {
		t.Fatalf("AuthenticateKey: %v", err)
	}
	if over.Method != gateWay {
		t.Fatalf("compat principal method = %q, want %q", over.Method, gateWay)
	}
	// A holder editing the exported field must not move what the actor says.
	over.CredentialID = "not-the-key"

	// A compat surface's principal for a user it resolved itself (an ABS JWT):
	// built over no key, so not key-bound.
	surface, err := compat.PrincipalFor(ctx, lucius, nil, lucius.CredentialEpoch, auth.ClientInfo{})
	if err != nil {
		t.Fatalf("compat PrincipalFor: %v", err)
	}
	surface.CredentialID = "not-a-key"

	session := r.as(lucius, auth.MethodSession)
	session.CredentialID = "sess-batgirl"
	delegated := r.as(lucius, auth.MethodSessionDelegated)
	delegated.CredentialID = "sess-oracle"
	extension := r.as(lucius, extBadge)
	extension.CredentialID = "badge-7"

	for _, c := range []struct {
		name       string
		p          *auth.Principal
		wantMethod string
		wantID     string
	}{
		{"api key", keyed, "token", keyID},
		{"compat principal over a key", over, string(gateWay), k.ID},
		{"compat principal over no key", surface, string(gateWay), ""},
		{"session", session, "session", ""},
		{"delegated session", delegated, "session_delegated", ""},
		{"extension method", extension, string(extBadge), ""},
		{"auth disabled", &auth.Principal{Method: auth.MethodAuthDisabled, CredentialID: "not-an-id"}, string(auth.MethodAuthDisabled), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := actorOf(c.p)
			if got.CredentialID != c.wantID || got.AuthMethod != c.wantMethod {
				t.Errorf("actorOf = {method %q, credential %q}, want {%q, %q}", got.AuthMethod, got.CredentialID, c.wantMethod, c.wantID)
			}
			if c.p.User != nil && got.UserID != string(c.p.User.ID) {
				t.Errorf("UserID = %q, want %q", got.UserID, c.p.User.ID)
			}
		})
	}
	if over.CredentialID == k.ID {
		t.Fatal("the compat case did not edit CredentialID")
	}
}

// An actor's credential id is internal: it is on the stored run and never on
// the wire form of a run (the REST view and the stream).
func TestActorPayload_CarriesNoCredentialID(t *testing.T) {
	a := tasks.Actor{UserID: "u-lucius", AuthMethod: "token", CredentialID: "key-batphone"}
	raw, err := json.Marshal(actorView(a))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "key-batphone") || strings.Contains(strings.ToLower(string(raw)), "credential") {
		t.Errorf("actor wire form = %s, carries the credential id", raw)
	}
	run := tasks.NewRunPayload(tasks.RunView{ID: "run-1", Actor: a})
	if raw, err = json.Marshal(run); err != nil || strings.Contains(string(raw), "key-batphone") {
		t.Errorf("run wire form = %s (%v), carries the credential id", raw, err)
	}
}
