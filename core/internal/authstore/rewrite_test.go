// file: core/internal/authstore/rewrite_test.go
// version: 1.0.0
// guid: 91c4e7a2-5b38-4d06-a8f3-2e7d0b6c1f59
// last-edited: 2026-11-11

package authstore

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// seedUser commits an active user at version 1.
func seedUser(t *testing.T, st *Store, id string) {
	t.Helper()
	b := st.NewBatch()
	defer func() { _ = b.Close() }()
	if err := st.PutUser(b, &auth.User{ID: auth.UserID(id), Username: id, Status: auth.StatusActive, Version: 1}); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(context.Background(), storage.Sync); err != nil {
		t.Fatal(err)
	}
}

// TestStore_SecondWriteToARecordInOneBatchIsRefused: NewBatch's contract is one
// write per record, because a write is derived from the committed bytes. The
// second write of one batch would replace the first unseen (a user's disable
// lost to the role change staged after it), so it fails loudly instead.
func TestStore_SecondWriteToARecordInOneBatchIsRefused(t *testing.T) {
	st := newStore(t)
	seedUser(t, st, "u-oracle")
	ctx := context.Background()
	read := func() *auth.User {
		u, err := st.GetUser(ctx, "u-oracle")
		if err != nil {
			t.Fatal(err)
		}
		return u
	}

	t.Run("a put after a put", func(t *testing.T) {
		b := st.NewBatch()
		defer func() { _ = b.Close() }()
		disabled := *read()
		disabled.Status, disabled.Version = auth.StatusDisabled, disabled.Version+1
		if err := st.PutUser(b, &disabled); err != nil {
			t.Fatal(err)
		}
		roles := *read()
		roles.Roles, roles.Version = []rbac.RoleName{"viewer"}, roles.Version+1
		if err := st.PutUser(b, &roles); !errors.Is(err, ErrDoubleWrite) {
			t.Fatalf("the second PutUser of one user in one batch: err = %v, want ErrDoubleWrite", err)
		}
		// The caller abandons a batch whose stage failed: nothing was committed.
		if u := read(); u.Status != auth.StatusActive || u.Version != 1 {
			t.Errorf("user = %q at version %d: the refused batch changed it", u.Status, u.Version)
		}
	})

	t.Run("a delete after a put", func(t *testing.T) {
		b := st.NewBatch()
		defer func() { _ = b.Close() }()
		cur := *read()
		put := cur
		put.DisplayName, put.Version = "Oracle", cur.Version+1
		if err := st.PutUser(b, &put); err != nil {
			t.Fatal(err)
		}
		if err := st.DeleteUser(b, &cur); !errors.Is(err, ErrDoubleWrite) {
			t.Fatalf("DeleteUser after PutUser in one batch: err = %v, want ErrDoubleWrite", err)
		}
	})

	t.Run("two records, and the same record in two batches, are fine", func(t *testing.T) {
		seedUser(t, st, "u-robin")
		for _, id := range []auth.UserID{"u-oracle", "u-robin"} {
			b := st.NewBatch()
			u, err := st.GetUser(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			u.DisplayName, u.Version = "renamed", u.Version+1
			if err := st.PutUser(b, u); err != nil {
				t.Fatalf("PutUser %s: %v", id, err)
			}
			if err := b.Commit(ctx, storage.Sync); err != nil {
				t.Fatalf("commit %s: %v", id, err)
			}
			_ = b.Close()
		}
	})
}
