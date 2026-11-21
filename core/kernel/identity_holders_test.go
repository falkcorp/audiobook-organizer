// file: core/kernel/identity_holders_test.go
// version: 0.1.0
// guid: 77600215-32c1-46e3-9476-7a9fe7674def
// last-edited: 2026-11-20

package kernel

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"reflect"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// TestAuthHolders_ReportsHoldersOverTheIdentityStore: every user naming one
// of the roles is reported once with its Active flag, inactive ones included;
// a user holding none is not.
func TestAuthHolders_ReportsHoldersOverTheIdentityStore(t *testing.T) {
	f := newIDFx(t)
	f.putUser("bruce-wayne", auth.StatusActive, rbac.RoleAdmin)
	f.putUser("selina-kyle", auth.StatusDisabled, rbac.RoleAdmin, "custom.oracle")
	f.putUser("barbara-gordon", auth.StatusActive, "custom.oracle")
	f.putUser("alfred-pennyworth", auth.StatusActive, rbac.RoleViewer)
	b := f.roles.NewBatch()
	t.Cleanup(func() { _ = b.Close() })
	got, err := newAuthHolders(f.users).HoldersOf(context.Background(), b, []rbac.RoleName{rbac.RoleAdmin, "custom.oracle"})
	if err != nil {
		t.Fatal(err)
	}
	want := []rbac.Holder{
		{UserID: "u-barbara-gordon", Active: true},
		{UserID: "u-bruce-wayne", Active: true},
		{UserID: "u-selina-kyle", Active: false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("HoldersOf = %+v, want %+v", got, want)
	}
}

// TestAuthHolders_ReadsThroughTheBatch is the adapter's contract (05-rbac §9,
// seams.d/F4-01.md): a holder whose record changes after HoldersOf read it,
// active or not, fails the reading batch's Commit with storage.ErrConflict;
// a user who holds none of the roles is not guarded, so a change to it does
// not.
func TestAuthHolders_ReadsThroughTheBatch(t *testing.T) {
	for _, c := range []struct {
		name     string
		changed  auth.UserID
		conflict bool
	}{
		{"an active holder demoted", "u-bruce-wayne", true},
		{"an inactive holder re-enabled", "u-selina-kyle", true},
		{"a non-holder changed", "u-alfred-pennyworth", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newIDFx(t)
			f.putUser("bruce-wayne", auth.StatusActive, rbac.RoleAdmin)
			f.putUser("selina-kyle", auth.StatusDisabled, rbac.RoleAdmin)
			f.putUser("alfred-pennyworth", auth.StatusActive, rbac.RoleViewer)
			ctx := context.Background()
			b := f.roles.NewBatch()
			t.Cleanup(func() { _ = b.Close() })
			if _, err := newAuthHolders(f.users).HoldersOf(ctx, b, []rbac.RoleName{rbac.RoleAdmin}); err != nil {
				t.Fatal(err)
			}
			f.bump(c.changed, func(u *auth.User) {
				u.Roles = []rbac.RoleName{rbac.RoleViewer}
				u.Status = auth.StatusActive
			})
			err := b.Commit(ctx, storage.Sync)
			if c.conflict && !errors.Is(err, storage.ErrConflict) {
				t.Errorf("Commit after %s: %v, want storage.ErrConflict", c.name, err)
			}
			if !c.conflict && err != nil {
				t.Errorf("Commit after %s: %v, want it to land", c.name, err)
			}
		})
	}
}

// fakeUsers is holderUsers yielding a fixed scan and recording guards.
type fakeUsers struct {
	scan     []fakeRow
	guardErr error
	guarded  []auth.UserID
}

type fakeRow struct {
	u   *auth.User
	err error
}

func (f *fakeUsers) ScanUsers(context.Context) iter.Seq2[*auth.User, error] {
	return func(yield func(*auth.User, error) bool) {
		for _, r := range f.scan {
			if !yield(r.u, r.err) {
				return
			}
		}
	}
}

func (f *fakeUsers) GuardUser(_ *storage.Batch, u *auth.User) error {
	if f.guardErr != nil {
		return f.guardErr
	}
	f.guarded = append(f.guarded, u.ID)
	return nil
}

// TestAuthHolders_ScanErrors: an undecodable record is skipped (auth refuses
// that user, so it holds nothing), every other scan error fails the read,
// and a guard conflict stays storage.ErrConflict through the wrap, so rbac's
// write retries. Every reported holder was guarded; a nil batch is refused.
func TestAuthHolders_ScanErrors(t *testing.T) {
	ctx := context.Background()
	admin := []rbac.RoleName{rbac.RoleAdmin}
	bruce := &auth.User{ID: "u-bruce-wayne", Roles: admin, Status: auth.StatusActive, Version: 1}
	b := &storage.Batch{}

	fu := &fakeUsers{scan: []fakeRow{{err: fmt.Errorf("row 7: %w", auth.ErrUndecodableRecord)}, {u: bruce}}}
	got, err := newAuthHolders(fu).HoldersOf(ctx, b, admin)
	if err != nil || len(got) != 1 || got[0].UserID != "u-bruce-wayne" {
		t.Errorf("an undecodable row then Bruce: %+v, %v; want Bruce alone", got, err)
	}
	if !reflect.DeepEqual(fu.guarded, []auth.UserID{"u-bruce-wayne"}) {
		t.Errorf("guarded %v, want Bruce", fu.guarded)
	}

	broken := errors.New("pebble: the batcomputer is offline")
	fu = &fakeUsers{scan: []fakeRow{{u: bruce}, {err: broken}}}
	if got, err := newAuthHolders(fu).HoldersOf(ctx, b, admin); !errors.Is(err, broken) || got != nil {
		t.Errorf("a failing scan: %+v, %v; want the error and no holders", got, err)
	}

	fu = &fakeUsers{scan: []fakeRow{{u: bruce}}, guardErr: fmt.Errorf("%w: user moved", storage.ErrConflict)}
	if _, err := newAuthHolders(fu).HoldersOf(ctx, b, admin); !errors.Is(err, storage.ErrConflict) {
		t.Errorf("a guard conflict: %v, want storage.ErrConflict", err)
	}

	if _, err := newAuthHolders(&fakeUsers{scan: []fakeRow{{u: bruce}}}).HoldersOf(ctx, nil, admin); err == nil {
		t.Error("HoldersOf without a batch read the holders")
	}
}
