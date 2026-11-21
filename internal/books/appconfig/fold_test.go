// file: internal/books/appconfig/fold_test.go
// version: 1.0.0
// guid: 6f2a9d17-3c84-4b60-a5e1-b8d4c0e73f19
// last-edited: 2026-11-19

package appconfig

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	coreconfig "github.com/falkcorp/mediacore/config"
)

func foldEngine(t *testing.T, providers, roots string) (*Engine, *fakeAccess) {
	t.Helper()
	f := newFake()
	f.vals[oidcProvidersKey] = json.RawMessage(providers)
	f.vals[libraryRootsKey] = json.RawMessage(roots)
	f.regSecret["core.auth.oidc.github.client_secret"] = true
	// An owner is configured: with none, HEAD refuses a change to the sign-in
	// allow-list through the API (ownerroot.go), and these tests change it.
	f.vals[ownerKey] = json.RawMessage(`"alfred@wayne.example"`)
	e := New(f, nil)
	if err := e.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	return e, f
}

func set(kv ...string) config.Update {
	u := config.Update{Set: map[string]json.RawMessage{}}
	for i := 0; i < len(kv); i += 2 {
		u.Set[kv[i]] = json.RawMessage(kv[i+1])
	}
	return u
}

func providersOf(t *testing.T, f *fakeAccess) []map[string]any {
	t.Helper()
	var out []map[string]any
	if err := json.Unmarshal(f.vals[oidcProvidersKey], &out); err != nil {
		t.Fatalf("providers = %s: %v", f.vals[oidcProvidersKey], err)
	}
	return out
}

// TestFold_WritesBuildTheProviderList: oauth_* writes become one write of the
// provider list; the client id lands on its provider, setting the secret adds the
// entry's reference to it, and the response names the HEAD fields.
func TestFold_WritesBuildTheProviderList(t *testing.T) {
	e, f := foldEngine(t, `[]`, `[]`)
	res, err := e.Update(t.Context(), adminActor, set("oauth_enabled", `true`, "oauth_github_client_id", `"gh-id"`, "oauth_allowed_emails", `"bruce@example.test"`))
	if err != nil {
		t.Fatal(err)
	}
	got := providersOf(t, f)
	if len(got) != 1 || got[0]["id"] != "github" || got[0]["client_id"] != "gh-id" || got[0]["client_secret"] != nil {
		t.Fatalf("providers = %v", got)
	}
	if !slices.Equal(got[0]["allowed_emails"].([]any), []any{"bruce@example.test"}) {
		t.Fatalf("allowed_emails = %v", got[0]["allowed_emails"])
	}
	for _, h := range []string{"oauth_enabled", "oauth_github_client_id", "oauth_allowed_emails"} {
		if !slices.Contains(res.Applied, h) {
			t.Errorf("Applied = %v, missing %s", res.Applied, h)
		}
	}
	if string(f.vals["modules.books.cfaccess.allowed_emails"]) != `"bruce@example.test"` {
		t.Errorf("the Cloudflare Access allow-list key was not written: %s", f.vals["modules.books.cfaccess.allowed_emails"])
	}
	if !e.Snapshot().OAuthEnabled || e.Snapshot().OAuthGithubClientID != "gh-id" {
		t.Fatalf("projection = %+v", e.Snapshot().OAuthGithubClientID)
	}
	// The secret arrives: the entry now references it, in the same Apply.
	if _, err := e.Update(t.Context(), adminActor, set("oauth_github_client_secret", `"riddler"`)); err != nil {
		t.Fatal(err)
	}
	ref, _ := providersOf(t, f)[0]["client_secret"].(map[string]any)
	if ref["secret"] != "core.auth.oidc.github.client_secret" || f.secrets["core.auth.oidc.github.client_secret"] != "riddler" {
		t.Fatalf("entry %v, secret %q", providersOf(t, f)[0], f.secrets["core.auth.oidc.github.client_secret"])
	}
}

// TestFold_PartialWriteKeepsAccessPolicy: writing one client id must not drop the
// allow-list or the role the entries carry (it would let anyone with the IdP in).
func TestFold_PartialWriteKeepsAccessPolicy(t *testing.T) {
	e, f := foldEngine(t, `[{"id":"github","kind":"github","client_id":"a","allowed_emails":["bruce@example.test"],"default_role":"viewer","note":"keep"}]`, `[]`)
	if _, err := e.Update(t.Context(), adminActor, set("oauth_google_client_id", `"goog"`)); err != nil {
		t.Fatal(err)
	}
	got := providersOf(t, f)
	if len(got) != 2 {
		t.Fatalf("providers = %v", got)
	}
	for _, p := range got {
		if !slices.Equal(p["allowed_emails"].([]any), []any{"bruce@example.test"}) || p["default_role"] != "viewer" {
			t.Fatalf("access policy lost on %v", p)
		}
	}
	if got[0]["note"] != "keep" {
		t.Fatalf("an entry's own field was dropped: %v", got[0])
	}
	// An explicit empty allow-list means nobody, written out.
	if _, err := e.Update(t.Context(), adminActor, set("oauth_allowed_emails", `""`)); err != nil {
		t.Fatal(err)
	}
	for _, p := range providersOf(t, f) {
		if l, ok := p["allowed_emails"].([]any); !ok || len(l) != 0 {
			t.Fatalf("an empty allow-list became %v", p["allowed_emails"])
		}
	}
}

// TestFold_ConflictingProvidersAreAFieldError: github and google entries that
// disagree on the role cannot be merged into by flat names; the Update fails with
// an invalid-value error that names the field and writes nothing.
func TestFold_ConflictingProvidersAreAFieldError(t *testing.T) {
	e, f := foldEngine(t, `[{"id":"github","client_id":"a","default_role":"viewer"},{"id":"google","client_id":"b","default_role":"admin"}]`, `[]`)
	before := string(f.vals[oidcProvidersKey])
	_, err := e.Update(t.Context(), adminActor, set("oauth_github_client_id", `"c"`))
	if !errors.Is(err, coreconfig.ErrInvalid) || !strings.Contains(err.Error(), "oauth_github_client_id") || !strings.Contains(err.Error(), "role") {
		t.Fatalf("err = %v, want ErrInvalid naming oauth_github_client_id and the role conflict", err)
	}
	if string(f.vals[oidcProvidersKey]) != before || len(f.applied) != 0 {
		t.Fatal("a refused fold wrote")
	}
}

// TestFold_RootsWrites: root_dir becomes the books-library root, path_aliases
// attach to roots by path, a relative root is refused, and "" removes the root.
func TestFold_RootsWrites(t *testing.T) {
	e, f := foldEngine(t, `[]`, `[]`)
	if _, err := e.Update(t.Context(), adminActor, set("root_dir", `"/srv/library"`, "path_aliases", `[{"root":"/srv/library","windows":"Z:\\\\lib","unc":"","smb_url":""}]`)); err != nil {
		t.Fatal(err)
	}
	if e.Snapshot().RootDir != "/srv/library" || len(e.Snapshot().PathAliases) != 1 || e.Snapshot().PathAliases[0].Root != "/srv/library" {
		t.Fatalf("projection root=%q aliases=%v", e.Snapshot().RootDir, e.Snapshot().PathAliases)
	}
	// Changing only the root keeps the aliases of the root that stays: here the
	// root moves, so its aliases (keyed by the old path) are replaced by none.
	if _, err := e.Update(t.Context(), adminActor, set("path_aliases", `[]`)); err != nil {
		t.Fatal(err)
	}
	if len(e.Snapshot().PathAliases) != 0 || e.Snapshot().RootDir != "/srv/library" {
		t.Fatalf("after clearing aliases: root=%q aliases=%v", e.Snapshot().RootDir, e.Snapshot().PathAliases)
	}
	if _, err := e.Update(t.Context(), adminActor, set("root_dir", `"relative/dir"`)); !errors.Is(err, coreconfig.ErrInvalid) {
		t.Fatalf("a relative root_dir: %v", err)
	}
	if _, err := e.Update(t.Context(), adminActor, set("root_dir", `""`)); err != nil {
		t.Fatal(err)
	}
	if e.Snapshot().RootDir != "" || strings.Contains(string(f.vals[libraryRootsKey]), "books-library") {
		t.Fatalf("root_dir \"\" left %s", f.vals[libraryRootsKey])
	}
}

// TestFold_MutateReachesTheFolds: a Mutate of oauth_* and root_dir fields is one
// Update through the same paths.
func TestFold_MutateReachesTheFolds(t *testing.T) {
	e, f := foldEngine(t, `[]`, `[]`)
	if err := e.Mutate(func(c *config.Config) {
		c.OAuthEnabled, c.OAuthGoogleClientID, c.RootDir = true, "goog", "/srv/library"
	}); err != nil {
		t.Fatal(err)
	}
	if got := providersOf(t, f); len(got) != 1 || got[0]["id"] != "google" {
		t.Fatalf("providers = %v", got)
	}
	if e.Snapshot().RootDir != "/srv/library" {
		t.Fatalf("root_dir = %q", e.Snapshot().RootDir)
	}
}

var editorActor = config.Actor{Subject: "user:selina", Interactive: true, Can: func(string) bool { return false }}

// TestFold_NeedsAdmin: the oauth_* and root fields decide who may sign in and
// where the library lives, so an actor without the admin permission is refused
// before anything is written.
func TestFold_NeedsAdmin(t *testing.T) {
	for _, kv := range [][2]string{
		{"oauth_github_client_id", `"x"`}, {"oauth_enabled", `true`}, {"oauth_allowed_emails", `"x@example.test"`},
		{"root_dir", `"/srv/library"`}, {"path_aliases", `[]`},
	} {
		e, f := foldEngine(t, `[]`, `[]`)
		_, err := e.Update(t.Context(), editorActor, set(kv[0], kv[1]))
		if !errors.Is(err, coreconfig.ErrForbidden) || len(f.applied) != 0 {
			t.Errorf("%s by an editor: err=%v applied=%d", kv[0], err, len(f.applied))
		}
		if _, err := e.Update(t.Context(), editorActor, config.Update{Unset: []string{kv[0]}}); !errors.Is(err, coreconfig.ErrForbidden) {
			t.Errorf("unsetting %s by an editor: %v", kv[0], err)
		}
	}
}

// TestFold_ViewShowsTheFoldedFields: the fields inside the lists are listed by
// their HEAD names with their values and the admin flag.
func TestFold_ViewShowsTheFoldedFields(t *testing.T) {
	e, _ := foldEngine(t, `[{"id":"github","kind":"github","client_id":"gh-id","allowed_emails":["bruce@example.test"],"default_role":"editor"}]`,
		`[{"id":"books-library","path":"/srv/library","kind":"directory"}]`)
	byName := map[string]config.KeyView{}
	for _, k := range e.View().Keys {
		byName[k.Name] = k
	}
	for name, want := range map[string]string{
		"oauth_enabled": `true`, "oauth_github_client_id": `"gh-id"`, "oauth_default_role": `"editor"`,
		"oauth_allowed_emails": `"bruce@example.test"`, "root_dir": `"/srv/library"`,
	} {
		k, ok := byName[name]
		if !ok || string(k.Value) != want || !k.Admin {
			t.Errorf("%s = %+v (listed %v), want value %s, admin", name, k, ok, want)
		}
	}
}

// TestFold_SecretOnlyKeepsAnExistingReference: setting the client secret adds the
// reference only to an entry that has none; one that names another secret keeps it.
func TestFold_SecretOnlyKeepsAnExistingReference(t *testing.T) {
	e, f := foldEngine(t, `[{"id":"github","kind":"github","client_id":"a","client_secret":{"secret":"integrations.custom.gh"}}]`, `[]`)
	if _, err := e.Update(t.Context(), adminActor, set("oauth_github_client_secret", `"riddler"`)); err != nil {
		t.Fatal(err)
	}
	ref, _ := providersOf(t, f)[0]["client_secret"].(map[string]any)
	if ref["secret"] != "integrations.custom.gh" {
		t.Fatalf("the reference was rewritten: %v", ref)
	}
}

// TestFold_UnsetRemovesWhatItNames: unsetting a client id drops that provider's
// entry (an entry with no client id is not a provider); unsetting the role
// leaves the entries.
func TestFold_UnsetRemovesWhatItNames(t *testing.T) {
	e, f := foldEngine(t, `[{"id":"github","kind":"github","client_id":"a","default_role":"editor"},{"id":"google","kind":"oidc","client_id":"b","default_role":"editor"}]`, `[]`)
	if _, err := e.Update(t.Context(), adminActor, config.Update{Unset: []string{"oauth_github_client_id"}}); err != nil {
		t.Fatal(err)
	}
	got := providersOf(t, f)
	if len(got) != 1 || got[0]["id"] != "google" {
		t.Fatalf("providers after unsetting github's id = %v", got)
	}
}

// TestFold_NoOpReportsTheCurrentGeneration: re-sending the folded values already
// stored is a 200 at the current generation that applies nothing.
func TestFold_NoOpReportsTheCurrentGeneration(t *testing.T) {
	e, f := foldEngine(t, `[]`, `[]`)
	u := set("oauth_enabled", `true`, "oauth_github_client_id", `"gh-id"`, "root_dir", `"/srv/library"`)
	if _, err := e.Update(t.Context(), adminActor, u); err != nil {
		t.Fatal(err)
	}
	gen := f.Generation()
	if gen == 0 {
		t.Fatal("the first write did not move the generation")
	}
	res, err := e.Update(t.Context(), adminActor, u)
	if err != nil {
		t.Fatal(err)
	}
	if res.Generation != gen || len(res.Applied) != 0 || f.Generation() != gen {
		t.Fatalf("a repeated fold write: %+v (store at %d, was %d)", res, f.Generation(), gen)
	}
}

// TestFold_ViewReportsRestartPendingOfTheList: a folded field (oauth_*, root_dir,
// path_aliases) is restart-pending when the list it lives in is.
func TestFold_ViewReportsRestartPendingOfTheList(t *testing.T) {
	e, f := foldEngine(t, `[{"id":"github","kind":"github","client_id":"a"}]`, `[]`)
	f.pending = map[coreconfig.Path]bool{oidcProvidersKey: true, libraryRootsKey: true}
	if e.byHead["oauth_enabled"].kind != kindFold {
		t.Fatal("oauth_enabled is not a fold row")
	}
	for _, k := range e.View().Keys {
		if want := e.byHead[k.Name].kind == kindFold || e.byHead[k.Name].kind == kindRoots; k.RestartPending != want {
			t.Errorf("%s RestartPending = %v, want %v", k.Name, k.RestartPending, want)
		}
	}
}

// TestFold_StoredSecretIsReferencedByANewEntry: a client secret stored earlier is
// referenced by the entry a later client-id write creates.
func TestFold_StoredSecretIsReferencedByANewEntry(t *testing.T) {
	e, f := foldEngine(t, `[]`, `[]`)
	f.secrets["core.auth.oidc.github.client_secret"] = "riddler"
	if _, err := e.Update(t.Context(), adminActor, set("oauth_enabled", `true`, "oauth_github_client_id", `"gh-id"`)); err != nil {
		t.Fatal(err)
	}
	ref, _ := providersOf(t, f)[0]["client_secret"].(map[string]any)
	if ref["secret"] != "core.auth.oidc.github.client_secret" {
		t.Fatalf("entry %v does not reference the stored secret", providersOf(t, f)[0])
	}
}
