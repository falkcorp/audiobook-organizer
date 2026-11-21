// file: internal/books/cutover/rename_fold_test.go
// version: 1.0.1
// guid: 91c4e7b2-5d38-4a6e-b0f3-7e2a1d8c4f56
// last-edited: 2026-11-19

package cutover

import (
	"encoding/json"
	"errors"
	"testing"
)

// vals encodes HEAD oauth_* values the way FoldOIDC takes them.
func vals(t *testing.T, kv map[string]any) map[string]json.RawMessage {
	t.Helper()
	out := map[string]json.RawMessage{}
	for k, v := range kv {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		out[k] = b
	}
	return out
}

func foldOK(t *testing.T, values map[string]json.RawMessage, prev string) json.RawMessage {
	t.Helper()
	got, err := FoldOIDC(values, json.RawMessage(prev))
	if err != nil {
		t.Fatalf("FoldOIDC(%v, %s): %v", rawKeys(values), prev, err)
	}
	return got
}

func wantJSON(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	if !jsonEq(t, string(got), want) {
		t.Errorf("got %s\nwant %s", got, want)
	}
}

const (
	ghEntry = `{"id":"github","kind":"github","issuer":"","client_id":"gh-batman","scopes":[],"allowed_emails":["bruce@batcave.example"],"allowed_domains":[],"link_existing_by_email":false,"default_role":"editor"}`
	goEntry = `{"id":"google","kind":"oidc","issuer":"https://accounts.google.com","client_id":"go-robin","scopes":[],"allowed_emails":["bruce@batcave.example"],"allowed_domains":[],"link_existing_by_email":false,"default_role":"editor"}`
)

// TestFoldOIDC_IsThePlannersFold: with no prev, the entries FoldOIDC returns are
// the ones planner.fold stages for the same values.
func TestFoldOIDC_IsThePlannersFold(t *testing.T) {
	for _, head := range []map[string]any{
		{"oauth_enabled": true, "oauth_github_client_id": "gh-batman", "oauth_google_client_id": "go-robin", "oauth_allowed_emails": "bruce@batcave.example,alfred@batcave.example", "oauth_default_role": "editor"},
		{"oauth_enabled": true, "oauth_github_client_id": "gh-batman"},
		{"oauth_enabled": true, "oauth_google_client_id": "go-robin", "oauth_default_role": "viewer"},
	} {
		want := forwardFold(t, head, false)
		if want == nil {
			t.Fatal("the planner staged nothing")
		}
		wantJSON(t, foldOK(t, vals(t, head), ``), string(want))
	}
}

func TestFoldOIDC_RoundTripsBothWays(t *testing.T) {
	// Fold -> Unfold.
	for _, head := range []map[string]any{
		{"oauth_enabled": true, "oauth_github_client_id": "gh-batman", "oauth_google_client_id": "go-robin", "oauth_allowed_emails": "bruce@batcave.example,alfred@batcave.example", "oauth_default_role": "editor"},
		{"oauth_enabled": true, "oauth_github_client_id": "gh-batman", "oauth_google_client_id": "", "oauth_allowed_emails": "", "oauth_default_role": "viewer"},
	} {
		entries := foldOK(t, vals(t, head), ``)
		got, err := UnfoldOIDC(entries)
		if err != nil {
			t.Fatal(err)
		}
		assertRawMap(t, got, vals(t, head))
	}
	// Unfold -> Fold, over entries the fold built, secret references and all.
	for _, head := range []map[string]any{
		{"oauth_enabled": true, "oauth_github_client_id": "gh-batman", "oauth_google_client_id": "go-robin", "oauth_allowed_emails": "bruce@batcave.example", "oauth_default_role": "editor"},
		{"oauth_enabled": true, "oauth_google_client_id": "go-robin"},
	} {
		entries := forwardFold(t, head, true)
		flat, err := UnfoldOIDC(entries)
		if err != nil {
			t.Fatal(err)
		}
		wantJSON(t, foldOK(t, flat, string(entries)), string(entries))
	}
}

func TestFoldOIDC_MergesIntoPrev(t *testing.T) {
	// An entry that stays keeps its own fields; the values change what they name.
	prev := `[{"id":"github","kind":"github","client_id":"gh-batman","client_secret":{"secret":"core.auth.oidc.github.client_secret"},"scopes":["read:user"],"default_role":"editor"},
	  {"id":"wayne-sso","kind":"oidc","client_id":"wsso"}]`
	got := foldOK(t, vals(t, map[string]any{"oauth_github_client_id": "gh-nightwing", "oauth_allowed_emails": "dick@batcave.example"}), prev)
	wantJSON(t, got, `[{"id":"github","kind":"github","client_id":"gh-nightwing","client_secret":{"secret":"core.auth.oidc.github.client_secret"},"scopes":["read:user"],"default_role":"editor","allowed_emails":["dick@batcave.example"]},
	  {"id":"wayne-sso","kind":"oidc","client_id":"wsso"}]`)
}

func TestFoldOIDC_AddsAProvider(t *testing.T) {
	// A new provider is the fold's entry, with no secret reference (the secret is
	// set by name and the caller adds the reference); the other is untouched.
	prev := `[` + ghEntry + `]`
	got := foldOK(t, vals(t, map[string]any{"oauth_google_client_id": "go-robin"}), prev)
	wantJSON(t, got, `[`+ghEntry+`,{"id":"google","kind":"oidc","issuer":"https://accounts.google.com","client_id":"go-robin","scopes":[],"allowed_domains":[],"link_existing_by_email":false,"allowed_emails":["bruce@batcave.example"],"default_role":"editor"}]`)
}

func TestFoldOIDC_RemovesAProvider(t *testing.T) {
	prev := `[` + ghEntry + `,` + goEntry + `]`
	wantJSON(t, foldOK(t, vals(t, map[string]any{"oauth_google_client_id": ""}), prev), `[`+ghEntry+`]`)
	wantJSON(t, foldOK(t, vals(t, map[string]any{"oauth_github_client_id": ""}), prev), `[`+goEntry+`]`)
	// Both removed: nothing is left to carry the allow-list, so the list is empty.
	wantJSON(t, foldOK(t, vals(t, map[string]any{"oauth_github_client_id": "", "oauth_google_client_id": ""}), prev), `[]`)
}

func TestFoldOIDC_SignInOffRemovesHEADProvidersOnly(t *testing.T) {
	prev := `[` + ghEntry + `,{"id":"wayne-sso","kind":"oidc","client_id":"wsso"},` + goEntry + `]`
	wantJSON(t, foldOK(t, vals(t, map[string]any{"oauth_enabled": false}), prev), `[{"id":"wayne-sso","kind":"oidc","client_id":"wsso"}]`)
	wantJSON(t, foldOK(t, vals(t, map[string]any{"oauth_enabled": false}), ``), `[]`)
	wantJSON(t, foldOK(t, vals(t, map[string]any{}), `null`), `[]`)
}

func TestFoldOIDC_AbsentNamesComeFromPrev(t *testing.T) {
	prev := `[` + ghEntry + `]`
	// Nothing named: prev is rebuilt as it is.
	wantJSON(t, foldOK(t, vals(t, map[string]any{}), prev), prev)
	// Only the role named: the id, the allow-list and sign-in stay.
	wantJSON(t, foldOK(t, vals(t, map[string]any{"oauth_default_role": "viewer"}), prev),
		`[{"id":"github","kind":"github","issuer":"","client_id":"gh-batman","scopes":[],"allowed_domains":[],"link_existing_by_email":false,"allowed_emails":["bruce@batcave.example"],"default_role":"viewer"}]`)
	// Client ids with sign-in not on anywhere: nothing to fold.
	wantJSON(t, foldOK(t, vals(t, map[string]any{"oauth_github_client_id": "gh-batman"}), ``), `[]`)
}

func TestFoldOIDC_ClearsAllowListAndRole(t *testing.T) {
	prev := `[` + ghEntry + `,` + goEntry + `]`
	got := foldOK(t, vals(t, map[string]any{"oauth_allowed_emails": "", "oauth_default_role": ""}), prev)
	// The allow-list is cleared to an explicit empty list (HEAD: no OAuth logins),
	// never dropped; the role, which has no empty meaning, is removed.
	wantJSON(t, got, `[{"id":"github","kind":"github","issuer":"","client_id":"gh-batman","scopes":[],"allowed_domains":[],"link_existing_by_email":false,"allowed_emails":[]},{"id":"google","kind":"oidc","issuer":"https://accounts.google.com","client_id":"go-robin","scopes":[],"allowed_domains":[],"link_existing_by_email":false,"allowed_emails":[]}]`)
}

func TestFoldOIDC_Lossy(t *testing.T) {
	// Spacing and empties in the allow-list are normalised on the way in.
	got := foldOK(t, vals(t, map[string]any{"oauth_enabled": true, "oauth_github_client_id": "gh-batman", "oauth_allowed_emails": " bruce@batcave.example , ,alfred@batcave.example"}), ``)
	wantJSON(t, got, `[{"id":"github","kind":"github","issuer":"","client_id":"gh-batman","scopes":[],"allowed_domains":[],"link_existing_by_email":false,"allowed_emails":["bruce@batcave.example","alfred@batcave.example"]}]`)
}

func TestFoldOIDC_Refusals(t *testing.T) {
	for _, c := range []struct {
		name string
		v    map[string]any
		prev string
	}{
		{"not a fold name", map[string]any{"oauth_redirect_base_url": "https://batcave.example"}, ``},
		{"enabled not a boolean", map[string]any{"oauth_enabled": "yes"}, ``},
		{"id not a string", map[string]any{"oauth_github_client_id": true}, ``},
		{"a number", map[string]any{"oauth_default_role": 3}, ``},
		{"null", map[string]any{"oauth_default_role": nil}, ``},
		{"prev not a list", map[string]any{}, `{"id":"github"}`},
		{"prev holds a non-object", map[string]any{}, `[1]`},
		{"prev holds null", map[string]any{}, `[null]`},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := FoldOIDC(vals(t, c.v), json.RawMessage(c.prev))
			if !errors.Is(err, ErrConvertInput) || got != nil {
				t.Errorf("FoldOIDC = %s, %v; want ErrConvertInput", got, err)
			}
		})
	}
	if _, err := FoldOIDC(map[string]json.RawMessage{"oauth_enabled": json.RawMessage(`{`)}, nil); !errors.Is(err, ErrConvertInput) {
		t.Errorf("malformed value: %v", err)
	}
}

// The access policy is never dropped by a write that does not name it.
func TestFoldOIDC_PartialWriteKeepsPolicy(t *testing.T) {
	prev := `[` + ghEntry + `,` + goEntry + `]`
	// Only a client id: both the allow-list and the role stay.
	got := foldOK(t, vals(t, map[string]any{"oauth_github_client_id": "gh-nightwing"}), prev)
	wantJSON(t, got, `[{"id":"github","kind":"github","issuer":"","client_id":"gh-nightwing","scopes":[],"allowed_domains":[],"link_existing_by_email":false,"allowed_emails":["bruce@batcave.example"],"default_role":"editor"},`+goEntry+`]`)
	// Only sign-in on: nothing changes.
	wantJSON(t, foldOK(t, vals(t, map[string]any{"oauth_enabled": true}), prev), prev)
	// Only the allow-list named: the role stays.
	got = foldOK(t, vals(t, map[string]any{"oauth_allowed_emails": "alfred@batcave.example"}), prev)
	wantJSON(t, got, `[{"id":"github","kind":"github","issuer":"","client_id":"gh-batman","scopes":[],"allowed_domains":[],"link_existing_by_email":false,"allowed_emails":["alfred@batcave.example"],"default_role":"editor"},{"id":"google","kind":"oidc","issuer":"https://accounts.google.com","client_id":"go-robin","scopes":[],"allowed_domains":[],"link_existing_by_email":false,"allowed_emails":["alfred@batcave.example"],"default_role":"editor"}]`)
	// Only the role named: the allow-list stays.
	got = foldOK(t, vals(t, map[string]any{"oauth_default_role": "viewer"}), prev)
	wantJSON(t, got, `[{"id":"github","kind":"github","issuer":"","client_id":"gh-batman","scopes":[],"allowed_domains":[],"link_existing_by_email":false,"allowed_emails":["bruce@batcave.example"],"default_role":"viewer"},{"id":"google","kind":"oidc","issuer":"https://accounts.google.com","client_id":"go-robin","scopes":[],"allowed_domains":[],"link_existing_by_email":false,"allowed_emails":["bruce@batcave.example"],"default_role":"viewer"}]`)
	// A provider added by a partial write takes the allow-list and role prev holds.
	got = foldOK(t, vals(t, map[string]any{"oauth_google_client_id": "go-robin"}), `[`+ghEntry+`]`)
	wantJSON(t, got, `[`+ghEntry+`,`+goEntry+`]`)
}

// An explicit empty allow-list means no OAuth logins, as HEAD does, and is
// written out so the meaning never rests on an absent key.
func TestFoldOIDC_EmptyAllowListKeepsHEADMeaning(t *testing.T) {
	got := foldOK(t, vals(t, map[string]any{"oauth_allowed_emails": ""}), `[`+ghEntry+`]`)
	var list []struct {
		Emails *[]string `json:"allowed_emails"`
	}
	if err := json.Unmarshal(got, &list); err != nil || len(list) != 1 || list[0].Emails == nil || len(*list[0].Emails) != 0 {
		t.Fatalf("got %s, %v; want allowed_emails written as []", got, err)
	}
	// Spaces and commas alone are empty too.
	got = foldOK(t, vals(t, map[string]any{"oauth_allowed_emails": " , "}), `[`+ghEntry+`]`)
	if err := json.Unmarshal(got, &list); err != nil || list[0].Emails == nil || len(*list[0].Emails) != 0 {
		t.Errorf("got %s, %v", got, err)
	}
	// A new entry carries the empty list too.
	got = foldOK(t, vals(t, map[string]any{"oauth_enabled": true, "oauth_github_client_id": "gh-batman", "oauth_allowed_emails": ""}), ``)
	wantJSON(t, got, `[{"id":"github","kind":"github","issuer":"","client_id":"gh-batman","scopes":[],"allowed_domains":[],"link_existing_by_email":false,"allowed_emails":[]}]`)
	// The round trip keeps the meaning: Unfold reads "" back.
	flat, err := UnfoldOIDC(got)
	if err != nil || string(flat["oauth_allowed_emails"]) != `""` {
		t.Errorf("unfold = %v, %v", flat, err)
	}
}

// A prev the flat names cannot be read from is an error, never a guess.
func TestFoldOIDC_CorruptPrevIsAnError(t *testing.T) {
	disagree := `[{"id":"github","kind":"github","client_id":"a","allowed_emails":["x@batcave.example"]},{"id":"google","kind":"oidc","client_id":"b","allowed_emails":["y@batcave.example"]}]`
	for _, values := range []map[string]any{
		{"oauth_github_client_id": "a"},
		{"oauth_allowed_emails": "z@batcave.example"},
		{},
	} {
		got, err := FoldOIDC(vals(t, values), json.RawMessage(disagree))
		if err == nil || got != nil {
			t.Errorf("FoldOIDC(%v) = %s, %v; want an error", values, got, err)
		}
		if !errors.Is(err, ErrNotRepresentable) {
			t.Errorf("FoldOIDC(%v): %v; want ErrNotRepresentable", values, err)
		}
	}
	// A client id that is not a string, or a role that is not, in prev.
	for _, prev := range []string{
		`[{"id":"github","client_id":3}]`,
		`[{"id":"github","client_id":"a","allowed_emails":"x"}]`,
		`[{"id":"github","client_id":"a","default_role":4}]`,
	} {
		if got, err := FoldOIDC(vals(t, map[string]any{}), json.RawMessage(prev)); err == nil || got != nil {
			t.Errorf("FoldOIDC(prev %s) = %s, %v; want an error", prev, got, err)
		}
	}
}

func TestFoldOIDC_NonStringIDIsAnError(t *testing.T) {
	if got, err := FoldOIDC(vals(t, map[string]any{}), json.RawMessage(`[{"id":7,"client_id":"a"}]`)); !errors.Is(err, ErrConvertInput) || got != nil {
		t.Errorf("= %s, %v", got, err)
	}
}

// A partial write leaves the allow-list and the role as prev spells them,
// byte for byte, even where the fold would spell them differently.
func TestFoldOIDC_PartialWriteLeavesPolicyUntouched(t *testing.T) {
	prev := `[{"id":"github","kind":"github","issuer":"","client_id":"gh-batman","scopes":[],"allowed_domains":[],"link_existing_by_email":false,"allowed_emails":[" Bruce@batcave.example "],"default_role":" editor "}]`
	got := foldOK(t, vals(t, map[string]any{"oauth_github_client_id": "gh-nightwing"}), prev)
	wantJSON(t, got, `[{"id":"github","kind":"github","issuer":"","client_id":"gh-nightwing","scopes":[],"allowed_domains":[],"link_existing_by_email":false,"allowed_emails":[" Bruce@batcave.example "],"default_role":" editor "}]`)
}
