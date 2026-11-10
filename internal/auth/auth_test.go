// file: internal/auth/auth_test.go
// version: 1.0.1
// guid: 4e8c1a2d-5b9f-4a70-b6d3-8c2e1f0a9b57

package auth

import (
	"context"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

func TestIsKnown(t *testing.T) {
	if !IsKnown(PermLibraryView) {
		t.Errorf("PermLibraryView should be known")
	}
	if IsKnown("not.a.permission") {
		t.Errorf("unknown permission reported as known")
	}
}

func TestAll_HasNoDuplicates(t *testing.T) {
	seen := make(map[Permission]struct{})
	for _, p := range All() {
		if _, ok := seen[p]; ok {
			t.Errorf("duplicate permission in All(): %q", p)
		}
		seen[p] = struct{}{}
		if !IsKnown(p) {
			t.Errorf("All() returned %q which IsKnown says is unknown — keep the two lists in sync", p)
		}
	}
}

func TestWithUser_Roundtrip(t *testing.T) {
	u := &database.User{ID: "01HX", Username: "alice"}
	ctx := WithUser(context.Background(), u)
	got, ok := UserFromContext(ctx)
	if !ok {
		t.Fatal("UserFromContext !ok")
	}
	if got.ID != "01HX" {
		t.Errorf("ID = %q, want 01HX", got.ID)
	}
}

func TestUserFromContext_Unset(t *testing.T) {
	if _, ok := UserFromContext(context.Background()); ok {
		t.Error("UserFromContext should be !ok on empty context")
	}
	if _, ok := UserFromContext(context.TODO()); ok {
		t.Error("UserFromContext should be !ok on TODO context")
	}
}

func TestCan(t *testing.T) {
	ctx := WithPermissions(context.Background(), []Permission{PermLibraryView, PermScanTrigger})

	if !Can(ctx, PermLibraryView) {
		t.Error("should allow PermLibraryView")
	}
	if !Can(ctx, PermScanTrigger) {
		t.Error("should allow PermScanTrigger")
	}
	if Can(ctx, PermUsersManage) {
		t.Error("should NOT allow PermUsersManage")
	}
}

func TestCan_Unauthenticated(t *testing.T) {
	// No permissions attached → every Can call returns false.
	if Can(context.Background(), PermLibraryView) {
		t.Error("unauthenticated Can should always be false")
	}
	if Can(context.TODO(), PermLibraryView) {
		t.Error("TODO context Can should always be false")
	}
}

func TestWithPermissions_EmptyIsNoop(t *testing.T) {
	ctx := WithPermissions(context.Background(), nil)
	if PermissionsFromContext(ctx) != nil {
		t.Error("empty/nil perms should result in no permission set on context")
	}
}
