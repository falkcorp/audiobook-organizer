// file: internal/books/identity_declarations_test.go
// version: 0.1.0
// guid: 2c7f81d4-0e93-4a56-b1d8-96a3e5f40c72
// last-edited: 2026-11-21

package books

import (
	"slices"
	"strings"
	"testing"

	headauth "github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// Dark: with the identity predicate off books declares nothing to rbac, so a boot
// without the flip is what it was. AuthRefresh is the one declaration that is not
// gated, because the handle it asks for exists only on an identity boot.
func TestManifest_IdentityDeclarationsAreDarkWhileThePredicateIsOff(t *testing.T) {
	mf := (&booksModule{}).Manifest()
	if len(mf.Permissions) != 0 || len(mf.Roles) != 0 || len(mf.ImportedRoleIDs) != 0 {
		t.Fatalf("predicate off: permissions %d, roles %d, imported role IDs %v; want none", len(mf.Permissions), len(mf.Roles), mf.ImportedRoleIDs)
	}
}

func TestManifest_IdentityDeclarationsFollowThePredicate(t *testing.T) {
	defer SetIdentityFlipForTest(true)()
	mf := (&booksModule{}).Manifest()
	if len(mf.Permissions) == 0 || len(mf.Roles) != 1 || !slices.Equal(mf.ImportedRoleIDs, []rbac.RoleName{"fp-worker"}) {
		t.Fatalf("predicate on: permissions %d, roles %d, imported role IDs %v", len(mf.Permissions), len(mf.Roles), mf.ImportedRoleIDs)
	}
}

// identityCatalog is the catalog the kernel builds for the boot: core's scopes and
// books' (earlyCatalog), which M3 is validated against.
func identityCatalog(t *testing.T) *rbac.Catalog {
	t.Helper()
	cat, err := rbac.NewCatalog(append(rbac.CoreScopes(), booksPermissions()...)...)
	if err != nil {
		t.Fatalf("core's scopes and books' do not make a catalog: %v", err)
	}
	return cat
}

// Every scope an atom, a seed set or the ambient list names exists in the catalog,
// and everything books declares is in its own namespace.
func TestBooksPermissions_CoverEveryScopeTheCutoverNames(t *testing.T) {
	cat := identityCatalog(t)
	for _, spec := range booksPermissions() {
		if !strings.HasPrefix(string(spec.Name), "books:") {
			t.Errorf("books declares %s outside its namespace", spec.Name)
		}
	}
	var named []rbac.Permission
	for _, a := range AtomTable() {
		named = append(named, a.Scopes...)
	}
	named = append(named, scopeReading, scopeAdminView, scopeAdminDebug)
	for role := range headSeedAtoms {
		named = append(named, roleBooksScopes(role)...)
	}
	for _, r := range booksRoles() {
		named = append(named, r.Permissions...)
	}
	for _, s := range named {
		if _, ok := cat.Lookup(s); !ok {
			t.Errorf("%s is named by the atom table, a seed set or the ambient list and is in no catalog", s)
		}
	}
}

// The default grants are HEAD's seed, read through the atom table: the golden is
// spelled with HEAD's own permission constants, so a seed that changes there
// fails here until the declaration follows (05-rbac §4.8's table).
func TestBooksPermissions_DefaultGrantsAreHEADsSeed(t *testing.T) {
	want := map[rbac.RoleName][]rbac.Permission{
		// editorPermissions: library.view, edit_metadata, delete, organize, scan.trigger,
		// playlists.create, requests.create, requests.approve; plus books:reading.
		"editor": {"books:view", "books:edit", "books:delete", "books:organize", "books:scan", "books:playlists.create", "books:request", "books:request.approve", scopeReading},
		// viewerPermissions: library.view, requests.create; plus books:reading.
		"viewer": {"books:view", "books:request", scopeReading},
	}
	// The atoms the golden rests on are HEAD's constants.
	if got := headSeedAtoms["editor"]; !slices.Equal(got, []string{
		headauth.PermLibraryView, headauth.PermLibraryEditMetadata, headauth.PermLibraryDelete, headauth.PermLibraryOrganize,
		headauth.PermScanTrigger, headauth.PermPlaylistsCreate, headauth.PermRequestsCreate, headauth.PermRequestsApprove,
	}) {
		t.Errorf("editor atoms %v are not HEAD's editor seed", got)
	}
	if got := headSeedAtoms["viewer"]; !slices.Equal(got, []string{headauth.PermLibraryView, headauth.PermRequestsCreate}) {
		t.Errorf("viewer atoms %v are not HEAD's viewer seed", got)
	}
	got := map[rbac.RoleName][]rbac.Permission{}
	for _, spec := range booksPermissions() {
		for _, role := range spec.DefaultGrants {
			got[role] = append(got[role], spec.Name)
		}
	}
	for role, w := range want {
		g := got[role]
		slices.Sort(g)
		w = slices.Clone(w)
		slices.Sort(w)
		if !slices.Equal(g, w) {
			t.Errorf("%s is granted %v by default, want %v", role, g, w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("default grants reach roles %v, want only editor and viewer", got)
	}
}

// core's declarer refuses a default grant of a restricted scope (05-rbac §4.9), so
// none is declared; the Confining scope is held by the fp-worker role alone.
func TestBooksPermissions_RestrictedScopesAreNeverDefaultGrants(t *testing.T) {
	for _, spec := range booksPermissions() {
		restricted := spec.Sensitive || spec.AuthRequired || spec.Confining ||
			strings.HasSuffix(string(spec.Name), ":settings.manage") || strings.HasSuffix(string(spec.Name), ":integrations.manage")
		if restricted && len(spec.DefaultGrants) != 0 {
			t.Errorf("%s is restricted and a default grant of %v", spec.Name, spec.DefaultGrants)
		}
	}
	by := map[rbac.Permission]rbac.PermissionSpec{}
	for _, spec := range booksPermissions() {
		by[spec.Name] = spec
	}
	if s := by[scopeFingerprintWorker]; !s.Confining {
		t.Errorf("%s is not Confining: %+v", s.Name, s)
	}
	if s := by[scopeAdminDebug]; !s.Sensitive || !s.AuthRequired {
		t.Errorf("%s is not Sensitive and AuthRequired: %+v", s.Name, s)
	}
	if s := by["books:view"]; s.Class != rbac.ClassView {
		t.Errorf("books:view is not ClassView")
	}
	roles := booksRoles()
	if len(roles) != 1 || roles[0].Name != "fp-worker" || !slices.Equal(roles[0].Permissions, []rbac.Permission{scopeFingerprintWorker}) {
		t.Errorf("seed roles = %+v, want fp-worker holding books:fingerprint.worker only", roles)
	}
}
