// file: core/kernel/identity_holders.go
// version: 0.1.0
// guid: ee757dc5-1cd5-4baa-a38e-064911732c4a
// last-edited: 2026-11-20

package kernel

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// holderUsers is the part of auth's user store the Holders adapter reads:
// the scan, and the guard that stages an Expect on a user record in the
// caller's batch. *authstore.Store is the production implementation.
type holderUsers interface {
	ScanUsers(ctx context.Context) iter.Seq2[*auth.User, error]
	GuardUser(b *storage.Batch, u *auth.User) error
}

// authHolders is rbac.Holders over auth's user records (05-rbac §9,
// seams.d/F4-01.md): rbac's last-admin and role-in-use rules need the users
// who hold a role, which live in auth's store, and rbac never imports auth.
//
// Every user it reports is guarded in the role write's batch b
// (Store.GuardUser: an Expect on the record at the version scanned), so a
// user write that commits after this read, a demotion or a disable in auth
// among them, fails b's Commit with storage.ErrConflict and rbac's write
// retries on the new state. That is what keeps two concurrent writes, one in
// rbac and one in auth, from together leaving no active core:admin.
//
// Inactive holders are reported (Active false) and guarded too: rbac counts
// an admin "before" the change from the same read, and a re-enable landing
// between the read and the commit must fail the write, or a role edit could
// strip core:admin from a role the moment its holder became active. A record
// that does not decode is skipped, as auth's own last-admin rule skips it
// (auth refuses such a user, so it holds nothing); every other scan error
// fails the read. storage.ErrConflict from GuardUser (the user changed
// between the scan and the guard) is returned wrapped with %w, so rbac's
// retry sees it.
//
// The loop is sequential on purpose: it stages Expects into one batch,
// which is not safe for concurrent use, and it runs under rbac's writeMu
// (one role write at a time). It scans every user once per call; the user
// count is an install's accounts, not a library-scale collection.
type authHolders struct {
	users holderUsers
}

var _ rbac.Holders = authHolders{}

// newAuthHolders returns the adapter over users.
func newAuthHolders(users holderUsers) authHolders { return authHolders{users: users} }

// HoldersOf returns every user whose role list names any of roles, each
// guarded in b.
func (h authHolders) HoldersOf(ctx context.Context, b *storage.Batch, roles []rbac.RoleName) ([]rbac.Holder, error) {
	if b == nil {
		return nil, errors.New("kernel: identity: HoldersOf needs the role write's batch")
	}
	var out []rbac.Holder
	for u, err := range h.users.ScanUsers(ctx) {
		if errors.Is(err, auth.ErrUndecodableRecord) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("kernel: identity: scan users: %w", err)
		}
		if !slices.ContainsFunc(u.Roles, func(r rbac.RoleName) bool { return slices.Contains(roles, r) }) {
			continue
		}
		if err := h.users.GuardUser(b, u); err != nil {
			return nil, fmt.Errorf("kernel: identity: guard holder %s: %w", u.ID, err)
		}
		out = append(out, rbac.Holder{UserID: string(u.ID), Active: u.Status == auth.StatusActive})
	}
	return out, nil
}
