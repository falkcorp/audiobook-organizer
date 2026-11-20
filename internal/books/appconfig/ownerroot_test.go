// file: internal/books/appconfig/ownerroot_test.go
// version: 1.0.0
// guid: 9a3c5e17-2b84-4d60-8f19-e7c2b04a6d53
// last-edited: 2026-11-20

package appconfig

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	coreconfig "github.com/falkcorp/mediacore/config"
)

const ownerKey = "modules.books.identity.owner_email"

// ownerRig is an Engine over a store that registers the owner key, with owner
// as the configured owner ("" for none).
func ownerRig(t *testing.T, owner string) (*Engine, *fakeAccess) {
	t.Helper()
	f := newFake()
	f.vals["core.log.level"] = json.RawMessage(`"warn"`)
	if owner == "" {
		f.vals[ownerKey] = json.RawMessage(`""`)
	} else {
		raw, _ := json.Marshal(owner)
		f.vals[ownerKey] = raw
	}
	f.vals["modules.books.identity.cf_access_aud"] = json.RawMessage(`"gotham-aud"`)
	e := New(f, nil)
	if err := e.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	return e, f
}

func wantOwnerRefusal(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, coreconfig.ErrForbidden) || !strings.Contains(err.Error(), config.OwnerTrustRootUnsetMessage) {
		t.Fatalf("want the no-owner refusal, got %v", err)
	}
}

// TestOwnerTrustRoot_NoOwnerRefusesEveryCallerAndEveryField: with no owner
// configured, a change to any of the five owner trust-root settings is refused
// whoever asks, before anything is written and before the key is checked.
func TestOwnerTrustRoot_NoOwnerRefusesEveryCallerAndEveryField(t *testing.T) {
	nobody := config.Actor{Subject: "user:selina", Interactive: true, Can: func(string) bool { return false }}
	for _, c := range []struct {
		head, value string
	}{
		{"owner_email", `"bruce@wayne.example"`},
		{"cf_access_team_domain", `"arkham"`},
		{"cf_access_aud", `"batcave"`},
		{"enable_auth", `true`},
		{"oauth_allowed_emails", `"dick@wayne.example,jason@wayne.example"`},
	} {
		for who, a := range map[string]config.Actor{"admin": adminActor, "non-admin": nobody} {
			t.Run(c.head+"/"+who, func(t *testing.T) {
				e, f := ownerRig(t, "")
				gen, applies := e.Generation(), len(f.applied)
				_, err := e.Update(t.Context(), a, set(c.head, c.value))
				wantOwnerRefusal(t, err)
				if !strings.Contains(err.Error(), c.head) {
					t.Errorf("the refusal does not name %s: %v", c.head, err)
				}
				if len(f.applied) != applies || e.Generation() != gen {
					t.Error("a refused update wrote")
				}
			})
		}
	}
}

// TestOwnerTrustRoot_OnlyAChangeIsRefused: sending a value back unchanged, an
// unset of what is already empty, a value that does not decode, and a setting
// outside the five are not this rule's.
func TestOwnerTrustRoot_OnlyAChangeIsRefused(t *testing.T) {
	e, _ := ownerRig(t, "")
	for name, u := range map[string]config.Update{
		"owner_email unchanged":     set("owner_email", `""`),
		"owner_email unset (empty)": {Unset: []string{"owner_email"}},
		"enable_auth unchanged":     set("enable_auth", mustJSON(t, e.Snapshot().EnableAuth)),
		"owner_email not a string":  set("owner_email", `5`),
		"another setting":           set("log_level", `"debug"`),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := e.Update(t.Context(), adminActor, u)
			if err != nil && strings.Contains(err.Error(), config.OwnerTrustRootUnsetMessage) {
				t.Fatalf("not a change, yet refused as one: %v", err)
			}
		})
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestOwnerTrustRoot_AnUnsetThatClearsAValueIsAChange: clearing a trust-root
// setting that holds a value is as much a change as setting one.
func TestOwnerTrustRoot_AnUnsetThatClearsAValueIsAChange(t *testing.T) {
	e, f := ownerRig(t, "")
	applies := len(f.applied)
	_, err := e.Update(t.Context(), adminActor, config.Update{Unset: []string{"cf_access_aud"}})
	wantOwnerRefusal(t, err)
	if len(f.applied) != applies || string(f.vals["modules.books.identity.cf_access_aud"]) != `"gotham-aud"` {
		t.Error("a refused unset wrote")
	}
}

// TestOwnerTrustRoot_AMixedUpdateIsRefusedWhole: the rest of the update is not
// applied beside the refused field.
func TestOwnerTrustRoot_AMixedUpdateIsRefusedWhole(t *testing.T) {
	e, f := ownerRig(t, "")
	_, err := e.Update(t.Context(), adminActor, set("log_level", `"debug"`, "owner_email", `"bruce@wayne.example"`))
	wantOwnerRefusal(t, err)
	if string(f.vals["core.log.level"]) != `"warn"` || len(f.applied) != 0 {
		t.Errorf("log_level was applied beside a refused owner change: %s", f.vals["core.log.level"])
	}
}

// TestOwnerTrustRoot_WithAnOwnerCoreTakesOver: once an owner is set this rule
// stands aside; the owner's proof is core's ProtectOwner check, so an owner
// session may change the owner and a session without the proof is refused by
// core, not by this message.
func TestOwnerTrustRoot_WithAnOwnerCoreTakesOver(t *testing.T) {
	e, f := ownerRig(t, "bruce@wayne.example")
	if _, err := e.Update(t.Context(), adminActor, set("owner_email", `"dick@wayne.example"`)); err != nil {
		t.Fatalf("an owner session handing ownership on: %v", err)
	}
	if got := string(f.vals[ownerKey]); got != `"dick@wayne.example"` {
		t.Errorf("owner = %s", got)
	}
}

// TestOwnerTrustRoot_MutateIsNotTheAPI: the process's own writes were never
// subject to HEAD's rule.
func TestOwnerTrustRoot_MutateIsNotTheAPI(t *testing.T) {
	e, f := ownerRig(t, "")
	if err := e.Mutate(func(c *config.Config) { c.OwnerEmail = "alfred@wayne.example" }); err != nil {
		t.Fatal(err)
	}
	if got := string(f.vals[ownerKey]); got != `"alfred@wayne.example"` {
		t.Errorf("owner = %s", got)
	}
}
