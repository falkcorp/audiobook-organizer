// file: internal/books/identity_flip_test.go
// version: 0.1.0
// guid: 8c2e6b40-1a97-4d53-b6e8-3f0d7a95c214
// last-edited: 2026-11-21

package books

import (
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m3"
	"github.com/falkcorp/mediacore/storage"
)

// TestIdentityFlip_OnForGood: since B10g the identity predicate is on with nothing
// turning it on, so a plain test binary, and the shipped one, runs ABO under core
// identity. Each consumer of the predicate is asked directly, so a predicate that
// read false again fails here by name, not only through the boots behind it.
func TestIdentityFlip_OnForGood(t *testing.T) {
	if !identityOn() {
		t.Fatal("the identity predicate is off: ABO would serve under HEAD's own middleware over the tables M3 drains")
	}
	if flipShapeOptions().Identity == nil {
		t.Fatal("flipShapeOptions does not ask for the identity flip")
	}
	if mf := (&booksModule{}).Manifest(); len(mf.Permissions) == 0 || len(mf.Roles) == 0 {
		t.Fatalf("the manifest declares %d permissions and %d roles: the identity declarations are off", len(mf.Permissions), len(mf.Roles))
	}
}

// TestIdentityFlip_TheManifestCarriesTheIdentityLevel: the manifest's storage
// declaration is the flipped one: books.level-2 at the next contiguous version
// raising Spec.Level to BooksIdentityLevel, every identity family M3 drains
// retired, and HEAD's two bootstrap rows declared as retired exact keys.
func TestIdentityFlip_TheManifestCarriesTheIdentityLevel(t *testing.T) {
	on := flipOn().Manifest().Storage
	lvl, ok := versionOf(on.Migrations, cutover.BooksIdentityLevelStepID)
	if !ok {
		t.Fatalf("no %s in the manifest", cutover.BooksIdentityLevelStepID)
	}
	if lvl.Version != booksLevelStepVersion+1 || lvl.RaisesLevel != cutover.BooksIdentityLevel || on.Level != cutover.BooksIdentityLevel {
		t.Errorf("level-2 = %+v, Spec.Level %d; want version %d raising to %d", lvl, on.Level, booksLevelStepVersion+1, cutover.BooksIdentityLevel)
	}
	retiredPrefix := map[string]bool{}
	retiredKey := map[string]bool{}
	for _, c := range on.Claims {
		if c.Mode != storage.ClaimRetired {
			continue
		}
		if c.Prefix != "" {
			retiredPrefix[c.Prefix] = true
		}
		if c.Key != "" {
			retiredKey[c.Key] = true
		}
	}
	for _, p := range m3.SourcePrefixes() {
		if !retiredPrefix[p] {
			t.Errorf("the identity family %q M3 drains is not a retired claim", p)
		}
	}
	for _, k := range m3.SourceKeys() {
		if !retiredKey[k] {
			t.Errorf("HEAD's bootstrap row %q is not a retired claim", k)
		}
	}
}

// TestIdentityFlip_StandaloneOpensFollowThePredicate: the op-runner child's
// store and the restore stager's verify open the store through StandaloneSpec.
// It must carry books.level-2 too, or those opens refuse the store the serve
// boot raised as written by a newer build.
func TestIdentityFlip_StandaloneOpensFollowThePredicate(t *testing.T) {
	base := flipOn().Manifest().Storage
	spec := StandaloneSpec(base)
	if spec.Level != cutover.BooksIdentityLevel {
		t.Errorf("StandaloneSpec level = %d, want %d", spec.Level, cutover.BooksIdentityLevel)
	}
	if !slices.ContainsFunc(spec.Migrations, func(m storage.Migration) bool { return m.Name == cutover.BooksIdentityLevelStepID }) {
		t.Errorf("StandaloneSpec carries no %s", cutover.BooksIdentityLevelStepID)
	}
}
