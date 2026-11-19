// file: internal/books/cutover/rename_fold_test.go
// version: 1.0.1
// guid: 91c4e7b2-5d38-4a6e-b0f3-7e2a1d8c4f56
// last-edited: 2026-11-19

package cutover

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
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
	ghEntry = `{"id":"github","kind":"github","client_id":"gh-batman","allowed_emails":["bruce@batcave.example"],"default_role":"editor"}`
	goEntry = `{"id":"google","kind":"oidc","issuer":"https://accounts.google.com","client_id":"go-robin","allowed_emails":["bruce@batcave.example"],"default_role":"editor"}`
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
	wantJSON(t, got, `[`+ghEntry+`,{"id":"google","kind":"oidc","issuer":"https://accounts.google.com","client_id":"go-robin","allowed_emails":["bruce@batcave.example"],"default_role":"editor"}]`)
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
		`[{"id":"github","kind":"github","client_id":"gh-batman","allowed_emails":["bruce@batcave.example"],"default_role":"viewer"}]`)
	// Client ids with sign-in not on anywhere: nothing to fold.
	wantJSON(t, foldOK(t, vals(t, map[string]any{"oauth_github_client_id": "gh-batman"}), ``), `[]`)
}

func TestFoldOIDC_ClearsAllowListAndRole(t *testing.T) {
	prev := `[` + ghEntry + `,` + goEntry + `]`
	got := foldOK(t, vals(t, map[string]any{"oauth_allowed_emails": "", "oauth_default_role": ""}), prev)
	// The allow-list is cleared to an explicit empty list (HEAD: no OAuth logins),
	// never dropped; the role, which has no empty meaning, is removed.
	wantJSON(t, got, `[{"id":"github","kind":"github","client_id":"gh-batman","allowed_emails":[]},{"id":"google","kind":"oidc","issuer":"https://accounts.google.com","client_id":"go-robin","allowed_emails":[]}]`)
}

func TestFoldOIDC_Lossy(t *testing.T) {
	// Spacing and empties in the allow-list are normalised on the way in.
	got := foldOK(t, vals(t, map[string]any{"oauth_enabled": true, "oauth_github_client_id": "gh-batman", "oauth_allowed_emails": " bruce@batcave.example , ,alfred@batcave.example"}), ``)
	wantJSON(t, got, `[{"id":"github","kind":"github","client_id":"gh-batman","allowed_emails":["bruce@batcave.example","alfred@batcave.example"]}]`)
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

// ---- roots ----

func rootsOK(t *testing.T, dir, aliases, prev string) json.RawMessage {
	t.Helper()
	got, err := FoldRoots(dir, json.RawMessage(aliases), json.RawMessage(prev))
	if err != nil {
		t.Fatalf("FoldRoots(%q, %s, %s): %v", dir, aliases, prev, err)
	}
	return got
}

func aliasJSON(t *testing.T, a ...headPathAlias) string {
	t.Helper()
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestFoldRoots_IsM6sEntry(t *testing.T) {
	archive := headPathAlias{Root: "/gotham/archive", Windows: "W:", UNC: `\\wayne\archive`, SMBURL: "smb://wayne/archive"}
	for _, c := range []struct {
		dir     string
		aliases []headPathAlias
	}{
		{"/gotham/archive", nil},
		{"/gotham/archive", []headPathAlias{archive}},
		{"/gotham/archive", []headPathAlias{{Root: "/gotham/archive/", Windows: "W:"}}},
	} {
		want := forwardRoots(t, c.dir, nil, c.aliases)
		wantJSON(t, rootsOK(t, c.dir, aliasJSON(t, c.aliases...), ``), string(want))
	}
	// With no root_dir there is nothing.
	wantJSON(t, rootsOK(t, "", ``, ``), `[]`)
	wantJSON(t, rootsOK(t, "  ", `null`, `null`), `[]`)
}

func TestFoldRoots_RoundTripsBothWays(t *testing.T) {
	archive := headPathAlias{Root: "/gotham/archive", Windows: "W:", UNC: `\\wayne\archive`, SMBURL: "smb://wayne/archive"}
	one := headPathAlias{Root: "/gotham/imports/one", Windows: "X:"}
	// Fold -> Unfold, onto a prev that already holds an import root.
	prev := forwardRoots(t, "", map[int]string{1: "/gotham/imports/one"}, nil)
	entries := rootsOK(t, "/gotham/archive", aliasJSON(t, archive, one), string(prev))
	dir, aliases, err := UnfoldRoots(entries)
	if err != nil || dir != "/gotham/archive" {
		t.Fatalf("UnfoldRoots = %q, %v", dir, err)
	}
	var got []headPathAlias
	if err := json.Unmarshal(aliases, &got); err != nil || !slices.Equal(got, []headPathAlias{archive, one}) {
		t.Errorf("aliases = %s, %v", aliases, err)
	}
	// Unfold -> Fold gives the entries back.
	for _, e := range []json.RawMessage{
		forwardRoots(t, "/gotham/archive", nil, nil),
		forwardRoots(t, "/gotham/archive", map[int]string{1: "/gotham/imports/one", 3: "/gotham/imports/three"}, []headPathAlias{archive, one}),
		forwardRoots(t, "", map[int]string{1: "/gotham/imports/one"}, []headPathAlias{one}),
	} {
		dir, aliases, err := UnfoldRoots(e)
		if err != nil {
			t.Fatal(err)
		}
		wantJSON(t, rootsOK(t, dir, string(aliases), string(e)), string(e))
	}
}

const (
	importOne = `{"id":"import-1","path":"/gotham/imports/one","role":"import","owner":"books","watch":"auto","sentinel":"bootstrap","min_free":-1}`
	dropbox   = `{"id":"dropbox-2","path":"/gotham/dropbox","role":"external","owner":"core","aliases":[{"windows":"V:"}]}`
	tunedBook = `{"id":"books-library","path":"/gotham/archive","role":"library","owner":"books","watch":"auto","min_free":5000,"quarantine":".failed","aliases":[{"windows":"W:"}]}`
)

func TestFoldRoots_MergesIntoPrev(t *testing.T) {
	prev := `[` + tunedBook + `,` + importOne + `,` + dropbox + `]`
	// A new root_dir changes the path and nothing the operator tuned; the aliases
	// are replaced by path_aliases, and the other roots are carried over, a
	// non-HEAD root's aliases included.
	got := rootsOK(t, "/gotham/cave", aliasJSON(t, headPathAlias{Root: "/gotham/cave", UNC: `\\wayne\cave`}), prev)
	wantJSON(t, got, `[{"id":"books-library","path":"/gotham/cave","role":"library","owner":"books","watch":"auto","min_free":5000,"quarantine":".failed","aliases":[{"unc":"\\\\wayne\\cave"}]},`+importOne+`,`+dropbox+`]`)
	// An alias goes to an import root when its root is that root's path.
	got = rootsOK(t, "/gotham/archive", aliasJSON(t, headPathAlias{Root: "/gotham/imports/one/", SMBURL: "smb://wayne/one"}), prev)
	wantJSON(t, got, `[{"id":"books-library","path":"/gotham/archive","role":"library","owner":"books","watch":"auto","min_free":5000,"quarantine":".failed"},
	  {"id":"import-1","path":"/gotham/imports/one","role":"import","owner":"books","watch":"auto","sentinel":"bootstrap","min_free":-1,"aliases":[{"smb_url":"smb://wayne/one"}]},`+dropbox+`]`)
	// A new books-library goes first.
	got = rootsOK(t, "/gotham/archive", ``, `[`+importOne+`]`)
	var list []map[string]any
	if err := json.Unmarshal(got, &list); err != nil || len(list) != 2 || list[0]["id"] != "books-library" || list[1]["id"] != "import-1" {
		t.Errorf("got %s, %v", got, err)
	}
}

func TestFoldRoots_RemovesTheBooksRoot(t *testing.T) {
	prev := `[` + tunedBook + `,` + importOne + `,` + dropbox + `]`
	wantJSON(t, rootsOK(t, "", ``, prev), `[`+importOne+`,`+dropbox+`]`)
	wantJSON(t, rootsOK(t, "", ``, `[`+importOne+`]`), `[`+importOne+`]`)
	// With no books root, an alias for it names no root.
	if _, err := FoldRoots("", json.RawMessage(aliasJSON(t, headPathAlias{Root: "/gotham/archive", Windows: "W:"})), json.RawMessage(prev)); !errors.Is(err, ErrNotRepresentable) {
		t.Errorf("alias of a removed root: %v", err)
	}
}

func TestFoldRoots_Refusals(t *testing.T) {
	prev := `[` + tunedBook + `,` + importOne + `,` + dropbox + `]`
	for _, c := range []struct {
		name, dir, aliases, prev string
		want                     error
	}{
		{"relative root_dir", "gotham/archive", ``, ``, ErrNotRepresentable},
		{"NUL in root_dir", "/gotham/\x00archive", ``, ``, ErrNotRepresentable},
		{"root_dir is another root", "/gotham/imports/one/", ``, prev, ErrNotRepresentable},
		{"root_dir is a native root", "/gotham/dropbox", ``, prev, ErrNotRepresentable},
		{"alias with no root", "/gotham/archive", `[{"windows":"W:"}]`, ``, ErrNotRepresentable},
		{"alias of no root", "/gotham/archive", `[{"root":"/gotham/elsewhere","windows":"W:"}]`, ``, ErrNotRepresentable},
		{"alias of a native root", "/gotham/archive", `[{"root":"/gotham/dropbox","windows":"W:"}]`, prev, ErrNotRepresentable},
		{"aliases not a list", "/gotham/archive", `{"root":"/x"}`, ``, ErrConvertInput},
		{"prev not a list", "/gotham/archive", ``, `{"id":"books-library"}`, ErrConvertInput},
		{"prev holds null", "/gotham/archive", ``, `[null]`, ErrConvertInput},
		{"two books-library entries", "/gotham/archive", ``, `[` + tunedBook + `,` + tunedBook + `]`, ErrConvertInput},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := FoldRoots(c.dir, json.RawMessage(c.aliases), json.RawMessage(c.prev))
			if !errors.Is(err, c.want) || got != nil {
				t.Errorf("FoldRoots = %s, %v; want %v", got, err, c.want)
			}
			if err != nil && strings.Contains(err.Error(), "gotham") {
				t.Errorf("the error carries a value: %v", err)
			}
		})
	}
}

func TestFoldRoots_AliasesListIsWhole(t *testing.T) {
	// path_aliases is HEAD's whole list: a root it no longer mentions loses its
	// aliases.
	prev := `[` + tunedBook + `,{"id":"import-1","path":"/gotham/imports/one","aliases":[{"windows":"X:"}]}]`
	got := rootsOK(t, "/gotham/archive", aliasJSON(t, headPathAlias{Root: "/gotham/imports/one", Windows: "Y:"}), prev)
	wantJSON(t, got, `[{"id":"books-library","path":"/gotham/archive","role":"library","owner":"books","watch":"auto","min_free":5000,"quarantine":".failed"},
	  {"id":"import-1","path":"/gotham/imports/one","aliases":[{"windows":"Y:"}]}]`)
}

func TestFoldRoots_AliasOrderWithinARoot(t *testing.T) {
	a := headPathAlias{Root: "/gotham/archive", Windows: "W:"}
	b := headPathAlias{Root: "/gotham/archive", SMBURL: "smb://wayne/archive"}
	got := rootsOK(t, "/gotham/archive", aliasJSON(t, b, a), ``)
	var list []struct {
		Aliases []struct {
			Windows string `json:"windows"`
			SMBURL  string `json:"smb_url"`
		} `json:"aliases"`
	}
	if err := json.Unmarshal(got, &list); err != nil || len(list) != 1 || len(list[0].Aliases) != 2 || list[0].Aliases[0].SMBURL == "" || list[0].Aliases[1].Windows == "" {
		t.Errorf("got %s, %v", got, err)
	}
}

// The access policy is never dropped by a write that does not name it.
func TestFoldOIDC_PartialWriteKeepsPolicy(t *testing.T) {
	prev := `[` + ghEntry + `,` + goEntry + `]`
	// Only a client id: both the allow-list and the role stay.
	got := foldOK(t, vals(t, map[string]any{"oauth_github_client_id": "gh-nightwing"}), prev)
	wantJSON(t, got, `[{"id":"github","kind":"github","client_id":"gh-nightwing","allowed_emails":["bruce@batcave.example"],"default_role":"editor"},`+goEntry+`]`)
	// Only sign-in on: nothing changes.
	wantJSON(t, foldOK(t, vals(t, map[string]any{"oauth_enabled": true}), prev), prev)
	// Only the allow-list named: the role stays.
	got = foldOK(t, vals(t, map[string]any{"oauth_allowed_emails": "alfred@batcave.example"}), prev)
	wantJSON(t, got, `[{"id":"github","kind":"github","client_id":"gh-batman","allowed_emails":["alfred@batcave.example"],"default_role":"editor"},{"id":"google","kind":"oidc","issuer":"https://accounts.google.com","client_id":"go-robin","allowed_emails":["alfred@batcave.example"],"default_role":"editor"}]`)
	// Only the role named: the allow-list stays.
	got = foldOK(t, vals(t, map[string]any{"oauth_default_role": "viewer"}), prev)
	wantJSON(t, got, `[{"id":"github","kind":"github","client_id":"gh-batman","allowed_emails":["bruce@batcave.example"],"default_role":"viewer"},{"id":"google","kind":"oidc","issuer":"https://accounts.google.com","client_id":"go-robin","allowed_emails":["bruce@batcave.example"],"default_role":"viewer"}]`)
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
	wantJSON(t, got, `[{"id":"github","kind":"github","client_id":"gh-batman","allowed_emails":[]}]`)
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

// No aliases at all means keep; any JSON list is HEAD's whole list.
func TestFoldRoots_AbsentAliasesKeepPrev(t *testing.T) {
	prev := `[` + tunedBook + `,` + importOne + `,` + dropbox + `]`
	for _, none := range []string{``} {
		got, err := FoldRoots("/gotham/archive", json.RawMessage(none), json.RawMessage(prev))
		if err != nil {
			t.Fatal(err)
		}
		wantJSON(t, got, prev)
	}
	// An explicit empty list or null clears the HEAD-derived roots' aliases only.
	for _, clear := range []string{`[]`, `null`} {
		got := rootsOK(t, "/gotham/archive", clear, prev)
		wantJSON(t, got, `[{"id":"books-library","path":"/gotham/archive","role":"library","owner":"books","watch":"auto","min_free":5000,"quarantine":".failed"},`+importOne+`,`+dropbox+`]`)
	}
}

// A root whose id or path is not a string is a corrupt prev, an error.
func TestFoldRoots_CorruptEntryFieldsAreErrors(t *testing.T) {
	for _, prev := range []string{
		`[{"id":3,"path":"/gotham/archive"}]`,
		`[{"id":"books-library","path":["/gotham/archive"]}]`,
		`[{"id":"import-1","path":null,"role":"import"},{"id":"x","path":7}]`,
	} {
		if got, err := FoldRoots("/gotham/archive", nil, json.RawMessage(prev)); !errors.Is(err, ErrConvertInput) || got != nil {
			t.Errorf("FoldRoots(prev %s) = %s, %v; want ErrConvertInput", prev, got, err)
		}
	}
}

func TestFoldOIDC_NonStringIDIsAnError(t *testing.T) {
	if got, err := FoldOIDC(vals(t, map[string]any{}), json.RawMessage(`[{"id":7,"client_id":"a"}]`)); !errors.Is(err, ErrConvertInput) || got != nil {
		t.Errorf("= %s, %v", got, err)
	}
}
