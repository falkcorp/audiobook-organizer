// file: internal/books/taskmanage/taskmanage_test.go
// version: 0.1.0
// guid: 8d2f4a90-6c13-4e7b-b5a8-1f0e93c7d204
// last-edited: 2026-11-22

package taskmanage_test

import (
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/taskmanage"
	"github.com/falkcorp/mediacore/rbac"
)

// settingsManage is 05-rbac §6.1's settings.manage row, spelled out here so a
// change to the helper cannot also change what the test expects.
var settingsManage = []rbac.Permission{
	"core:settings.manage", "books:settings.manage", "core:tasks.manage",
	"core:system.view", "core:system.manage", "core:library.manage", "core:library.browse",
	"core:integrations.manage",
}

// TestRequirement_IsTheEightScopeAllOfOfSettingsManage: a books kind's Manage is
// HEAD's settings.manage reach, no more and no less, as an AllOf (holding seven
// of the eight is not enough).
func TestRequirement_IsTheEightScopeAllOfOfSettingsManage(t *testing.T) {
	r := taskmanage.Requirement()
	if r.Kind() != rbac.KindAllOf {
		t.Fatalf("kind = %v, want AllOf", r.Kind())
	}
	if got := r.Permissions(); !slices.Equal(got, settingsManage) {
		t.Fatalf("scopes = %v, want %v", got, settingsManage)
	}
	if got := taskmanage.Scopes(); !slices.Equal(got, settingsManage) {
		t.Fatalf("Scopes() = %v, want %v", got, settingsManage)
	}
	for i := range settingsManage {
		rest := slices.Concat(settingsManage[:i], settingsManage[i+1:])
		if holds(rest, r) {
			t.Errorf("a caller without %s satisfies the requirement", settingsManage[i])
		}
	}
	if !holds(settingsManage, r) {
		t.Error("a caller holding all eight does not satisfy the requirement")
	}
}

// TestScopes_ReturnsAFreshSlice: a caller that edits the result cannot change
// another kind's Manage.
func TestScopes_ReturnsAFreshSlice(t *testing.T) {
	a := taskmanage.Scopes()
	a[0] = "gotham:poisoned"
	if got := taskmanage.Scopes(); got[0] != settingsManage[0] {
		t.Fatalf("Scopes() shares its backing array: first scope %q", got[0])
	}
}

// holds reports whether a caller with exactly perms satisfies the AllOf r.
func holds(perms []rbac.Permission, r rbac.Requirement) bool {
	for _, p := range r.Permissions() {
		if !slices.Contains(perms, p) {
			return false
		}
	}
	return true
}
