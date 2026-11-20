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

// plainAdmin is an admin session that is not the owner's Access sign-in.
var plainAdmin = func() config.Actor { a := adminActor; a.Owner = false; return a }()

// The five trust-root settings, with a value that differs from what ownerRig holds.
var ownerFields = []struct{ head, value, wrongType string }{
	{"owner_email", `"selina@wayne.example"`, `5`},
	{"cf_access_team_domain", `"wayne-tower"`, `5`},
	{"cf_access_aud", `"batcave"`, `5`},
	{"enable_auth", `false`, `"yes"`},
	{"oauth_allowed_emails", `"dick@wayne.example,jason@wayne.example"`, `5`},
}

// ownerRig is an Engine over a store that holds the five trust-root keys: sign-in
// required, a Cloudflare Access team and audience, an empty allow-list, and
// owner as the configured owner ("" for none).
func ownerRig(t *testing.T, owner string) (*Engine, *fakeAccess) {
	return ownerRigMode(t, owner, "required")
}

func ownerRigMode(t *testing.T, owner, mode string) (*Engine, *fakeAccess) {
	t.Helper()
	f := newFake()
	f.vals["core.log.level"] = json.RawMessage(`"warn"`)
	f.vals[ownerKey] = mustMarshal(owner)
	f.vals["modules.books.identity.cf_access_team_domain"] = json.RawMessage(`"arkham"`)
	f.vals["modules.books.identity.cf_access_aud"] = json.RawMessage(`"gotham-aud"`)
	f.vals["modules.books.cfaccess.allowed_emails"] = json.RawMessage(`""`)
	f.vals["core.auth.mode"] = mustMarshal(mode)
	f.vals[oidcProvidersKey] = json.RawMessage(`[]`)
	e := New(f, nil)
	if err := e.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	return e, f
}

func wantRefusal(t *testing.T, err error, msg string) {
	t.Helper()
	if !errors.Is(err, coreconfig.ErrForbidden) || !strings.Contains(err.Error(), msg) {
		t.Fatalf("want a refusal saying %q, got %v", msg, err)
	}
}

func wantOwnerRefusal(t *testing.T, err error) {
	t.Helper()
	wantRefusal(t, err, config.OwnerTrustRootUnsetMessage)
}

// refusedWithoutWriting runs u as a and wants the refusal msg, naming head, with
// nothing written.
func refusedWithoutWriting(t *testing.T, e *Engine, f *fakeAccess, a config.Actor, u config.Update, head, msg string) {
	t.Helper()
	gen, applies, mode := e.Generation(), len(f.applied), string(f.vals["core.auth.mode"])
	_, err := e.Update(t.Context(), a, u)
	wantRefusal(t, err, msg)
	// The fixed message lists all five names, so look for this one outside it.
	if !strings.Contains(strings.ReplaceAll(err.Error(), msg, ""), head) {
		t.Errorf("the refusal does not name %s: %v", head, err)
	}
	if len(f.applied) != applies || e.Generation() != gen || string(f.vals["core.auth.mode"]) != mode {
		t.Error("a refused update wrote")
	}
}

// TestOwnerTrustRoot_NoOwnerRefusesEveryCallerAndEveryField: with no owner
// configured, a change to any of the five owner trust-root settings is refused
// whoever asks, before anything is written and before the key is checked.
func TestOwnerTrustRoot_NoOwnerRefusesEveryCallerAndEveryField(t *testing.T) {
	for _, c := range ownerFields {
		for who, a := range map[string]config.Actor{"owner": adminActor, "admin": plainAdmin} {
			t.Run(c.head+"/"+who, func(t *testing.T) {
				e, f := ownerRig(t, "")
				refusedWithoutWriting(t, e, f, a, set(c.head, c.value), c.head, config.OwnerTrustRootUnsetMessage)
			})
		}
	}
}

// TestOwnerTrustRoot_OnlyAChangeIsRefused: sending a value back unchanged, an
// unset of what is already empty, and a setting outside the five are not this
// rule's.
func TestOwnerTrustRoot_OnlyAChangeIsRefused(t *testing.T) {
	e, _ := ownerRig(t, "")
	for name, u := range map[string]config.Update{
		"owner_email unchanged":     set("owner_email", `""`),
		"owner_email unset (empty)": {Unset: []string{"owner_email"}},
		"cf_access_aud unchanged":   set("cf_access_aud", `"gotham-aud"`),
		"enable_auth unchanged":     set("enable_auth", `true`),
		"allow-list unchanged":      set("oauth_allowed_emails", `""`),
		"another setting":           set("log_level", `"debug"`),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := e.Update(t.Context(), plainAdmin, u)
			if err != nil && strings.Contains(err.Error(), config.OwnerTrustRootUnsetMessage) {
				t.Fatalf("not a change, yet refused as one: %v", err)
			}
		})
	}
}

// TestOwnerTrustRoot_NullAndMalformedAreChanges: a value the write path would not
// take as given is refused as a change, never judged "unchanged". Decoding a JSON
// null or a wrong-typed value into the field's existing value leaves it in place,
// so the rule saw no change while the write path read something else: enable_auth
// null converts to "disabled".
func TestOwnerTrustRoot_NullAndMalformedAreChanges(t *testing.T) {
	for _, c := range ownerFields {
		for name, bad := range map[string]string{"null": `null`, "padded null": " null\n", "wrong type": c.wrongType, "truncated": `{`, "trailing": `"x" 1`} {
			t.Run(c.head+"/no owner/"+name, func(t *testing.T) {
				e, f := ownerRig(t, "")
				refusedWithoutWriting(t, e, f, adminActor, set(c.head, bad), c.head, config.OwnerTrustRootUnsetMessage)
			})
			t.Run(c.head+"/owner set, not the owner/"+name, func(t *testing.T) {
				e, f := ownerRig(t, "bruce@wayne.example")
				refusedWithoutWriting(t, e, f, plainAdmin, set(c.head, bad), c.head, ownerOnlyMessage)
			})
		}
	}
}

// TestOwnerTrustRoot_NullEnableAuthNeverTurnsSignInOff: even the owner's null for
// enable_auth is no boolean, and is refused before it can read as "disabled".
func TestOwnerTrustRoot_NullEnableAuthNeverTurnsSignInOff(t *testing.T) {
	e, f := ownerRig(t, "bruce@wayne.example")
	_, err := e.Update(t.Context(), adminActor, set("enable_auth", `null`))
	if !errors.Is(err, coreconfig.ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	if got := string(f.vals["core.auth.mode"]); got != `"required"` || len(f.applied) != 0 {
		t.Errorf("core.auth.mode = %s after a refused null", got)
	}
}

// TestOwnerTrustRoot_UnsetOfEnableAuthIsAChange: an unset restores core's default
// ("required"), not the Go zero value, so with sign-in disabled it is a change
// even though the zero value (false) is what is held.
func TestOwnerTrustRoot_UnsetOfEnableAuthIsAChange(t *testing.T) {
	e, f := ownerRigMode(t, "", "disabled")
	refusedWithoutWriting(t, e, f, adminActor, config.Update{Unset: []string{"enable_auth"}}, "enable_auth", config.OwnerTrustRootUnsetMessage)
	e, f = ownerRigMode(t, "bruce@wayne.example", "disabled")
	refusedWithoutWriting(t, e, f, plainAdmin, config.Update{Unset: []string{"enable_auth"}}, "enable_auth", ownerOnlyMessage)
}

// TestOwnerTrustRoot_AnUnsetThatClearsAValueIsAChange: clearing a trust-root
// setting that holds a value is as much a change as setting one.
func TestOwnerTrustRoot_AnUnsetThatClearsAValueIsAChange(t *testing.T) {
	e, f := ownerRig(t, "")
	refusedWithoutWriting(t, e, f, adminActor, config.Update{Unset: []string{"cf_access_aud"}}, "cf_access_aud", config.OwnerTrustRootUnsetMessage)
	if string(f.vals["modules.books.identity.cf_access_aud"]) != `"gotham-aud"` {
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

// TestOwnerTrustRoot_WithAnOwnerOnlyTheOwnerMayChangeAnyOfThem: once an owner is
// set, core's ProtectOwner tag covers only some of the five (core.auth.mode and
// the allow-list fold are untagged), so the Engine asks for the owner's proof on
// all of them: a second admin is refused on each, the owner is allowed on each.
func TestOwnerTrustRoot_WithAnOwnerOnlyTheOwnerMayChangeAnyOfThem(t *testing.T) {
	for _, c := range ownerFields {
		t.Run(c.head+"/second admin", func(t *testing.T) {
			e, f := ownerRig(t, "bruce@wayne.example")
			refusedWithoutWriting(t, e, f, plainAdmin, set(c.head, c.value), c.head, ownerOnlyMessage)
		})
		t.Run(c.head+"/the owner", func(t *testing.T) {
			e, f := ownerRig(t, "bruce@wayne.example")
			if _, err := e.Update(t.Context(), adminActor, set(c.head, c.value)); err != nil {
				t.Fatalf("the owner changing %s: %v", c.head, err)
			}
			if len(f.applied) == 0 && c.head != "oauth_allowed_emails" {
				t.Errorf("%s was not written", c.head)
			}
		})
	}
	t.Run("the owner hands ownership on", func(t *testing.T) {
		e, f := ownerRig(t, "bruce@wayne.example")
		if _, err := e.Update(t.Context(), adminActor, set("owner_email", `"dick@wayne.example"`)); err != nil {
			t.Fatal(err)
		}
		if got := string(f.vals[ownerKey]); got != `"dick@wayne.example"` {
			t.Errorf("owner = %s", got)
		}
	})
	t.Run("an unchanged value needs no proof", func(t *testing.T) {
		e, _ := ownerRig(t, "bruce@wayne.example")
		if _, err := e.Update(t.Context(), plainAdmin, set("enable_auth", `true`)); err != nil && strings.Contains(err.Error(), ownerOnlyMessage) {
			t.Fatalf("an unchanged value asked for the owner's proof: %v", err)
		}
	})
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
