// file: internal/auth/permissions_prop_test.go
// version: 1.0.0
// guid: 693012d9-3742-4acf-87c7-5570195e7dfc
//
// Property-based tests for the permission system (backlog item 4.5,
// task 7). Uses pgregory.net/rapid to verify invariants across random
// inputs:
//
//  1. Every element of All() passes IsKnown()
//  2. The admin role's permission set is a superset of every other
//     canonical role (viewer, editor)
//  3. Viewer permissions are a subset of editor permissions
//  4. Editor permissions are a subset of admin permissions
//  5. Round-tripping a permission set through WithPermissions /
//     PermissionsFromContext preserves the set exactly
//  6. Can(ctx, p) returns true iff p is in the set attached to ctx
//
// These tests must not modify production code — if a property uncovers
// a real bug, it is skipped with a note rather than patched here.
package auth

import (
	"context"
	"sort"
	"testing"

	"pgregory.net/rapid"
)

// permSet builds a map[Permission]struct{} from a slice for comparisons.
func permSet(perms []Permission) map[Permission]struct{} {
	out := make(map[Permission]struct{}, len(perms))
	for _, p := range perms {
		out[p] = struct{}{}
	}
	return out
}

// isSubset reports whether every element of sub is also in super.
func isSubset(sub, super []Permission) bool {
	s := permSet(super)
	for _, p := range sub {
		if _, ok := s[p]; !ok {
			return false
		}
	}
	return true
}

// genPermSubset draws a random subset of auth.All() by permuting the
// full permission list and taking the first N elements, where N is
// drawn from [0, len(All())]. Values are unique because All() itself
// has no duplicates.
func genPermSubset(t *rapid.T, label string) []Permission {
	all := All()
	n := rapid.IntRange(0, len(all)).Draw(t, label+"_n")
	perm := rapid.Permutation(all).Draw(t, label+"_perm")
	out := make([]Permission, 0, n)
	for i := range n {
		out = append(out, perm[i])
	}
	return out
}

// TestProp_AllReturnsKnownPermissions verifies that every permission
// returned by All() is recognized by IsKnown. This is a trivial
// invariant but it guards against the two lists drifting apart — a
// very real hazard because adding a permission requires editing both.
func TestProp_AllReturnsKnownPermissions(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		all := All()
		if len(all) == 0 {
			t.Fatal("All() returned empty slice")
		}
		// Pick a random index to focus on (rapid still reruns with
		// different draws, exercising every element over many trials).
		i := rapid.IntRange(0, len(all)-1).Draw(t, "i")
		if !IsKnown(all[i]) {
			t.Fatalf("All()[%d] = %q but IsKnown returned false", i, all[i])
		}
	})
}

// TestProp_AdminIsSupersetOfAllRoles verifies that the admin role's
// permission set contains every permission held by any other canonical
// role (viewer, editor). Admin must never be less privileged than a
// subordinate role.
func TestProp_AdminIsSupersetOfAllRoles(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		admin := adminPermissions()
		roles := map[string][]Permission{
			SeedRoleEditor: editorPermissions(),
			SeedRoleViewer: viewerPermissions(),
		}
		// Pick a role at random; rapid will cover both over runs.
		names := []string{SeedRoleEditor, SeedRoleViewer}
		name := rapid.SampledFrom(names).Draw(t, "role")
		perms := roles[name]
		if !isSubset(perms, admin) {
			t.Fatalf("admin is NOT a superset of %s: admin=%v role=%v", name, admin, perms)
		}
	})
}

// TestProp_ViewerSubsetOfEditor verifies that every viewer permission
// is also present in the editor role.
func TestProp_ViewerSubsetOfEditor(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		viewer := viewerPermissions()
		editor := editorPermissions()
		if len(viewer) == 0 {
			return
		}
		i := rapid.IntRange(0, len(viewer)-1).Draw(t, "i")
		p := viewer[i]
		if _, ok := permSet(editor)[p]; !ok {
			t.Fatalf("viewer permission %q not present in editor role", p)
		}
	})
}

// TestProp_EditorSubsetOfAdmin verifies that every editor permission
// is also present in the admin role.
func TestProp_EditorSubsetOfAdmin(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		editor := editorPermissions()
		admin := adminPermissions()
		if len(editor) == 0 {
			return
		}
		i := rapid.IntRange(0, len(editor)-1).Draw(t, "i")
		p := editor[i]
		if _, ok := permSet(admin)[p]; !ok {
			t.Fatalf("editor permission %q not present in admin role", p)
		}
	})
}

// TestProp_ContextRoundTrip verifies that attaching an arbitrary
// permission subset to a context and reading it back yields exactly
// the same set. Order is irrelevant — the context stores a set.
func TestProp_ContextRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		perms := genPermSubset(t, "perms")
		ctx := WithPermissions(context.Background(), perms)
		got := PermissionsFromContext(ctx)

		// WithPermissions short-circuits on empty input and attaches
		// nothing to the context. That's a documented behaviour, not a
		// bug, so treat it as a distinct case.
		if len(perms) == 0 {
			if got != nil {
				t.Fatalf("empty perms should not attach a set; got %v", got)
			}
			return
		}
		if got == nil {
			t.Fatalf("non-empty perms %v produced nil set from context", perms)
		}
		if len(got) != len(permSet(perms)) {
			t.Fatalf("set size mismatch: input=%d got=%d (input=%v got=%v)",
				len(perms), len(got), perms, keysOf(got))
		}
		for _, p := range perms {
			if _, ok := got[p]; !ok {
				t.Fatalf("perm %q missing from round-tripped set: %v", p, keysOf(got))
			}
		}
		// And the other direction — nothing extra.
		in := permSet(perms)
		for p := range got {
			if _, ok := in[p]; !ok {
				t.Fatalf("unexpected perm %q in round-tripped set", p)
			}
		}
	})
}

// TestProp_CanChecksMembership verifies that Can(ctx, p) returns true
// exactly when p is in the permission set attached to ctx, for an
// arbitrary subset of All().
func TestProp_CanChecksMembership(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		granted := genPermSubset(t, "granted")
		ctx := WithPermissions(context.Background(), granted)

		grantedSet := permSet(granted)
		// Walk every permission in All() and check Can matches set
		// membership. Covers both the positive case (granted perms) and
		// the negative case (perms not in the subset).
		for _, p := range All() {
			_, want := grantedSet[p]
			got := Can(ctx, p)
			if got != want {
				t.Fatalf("Can(ctx, %q) = %v, want %v; granted=%v", p, got, want, granted)
			}
		}
	})
}

// keysOf extracts the keys of a permission set as a sorted slice,
// used only for stable error messages.
func keysOf(m map[Permission]struct{}) []Permission {
	out := make([]Permission, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
