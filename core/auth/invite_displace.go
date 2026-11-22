// file: core/auth/invite_displace.go
// version: 0.1.0
// guid: d41e7a63-0b95-4c2f-8e17-5a6f3b92c0d8
// last-edited: 2026-11-22

package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/falkcorp/mediacore/storage"
)

// inviteBindsEmail reports whether accepting t binds its address to the new
// account as that account's own, the way HEAD's accept did for an invitation
// emailed to that address by an admin (ConsumeInvite, planInviteAcceptLocked).
// An accept that binds takes the address from the accounts that only typed it
// in (displaceUnverifiedHolders), in its own batch; one that does not touches
// no other account, because it claims nothing: the invitee's address is
// stored unverified and shadows no one.
//
// It is false for every invite today: core's invite carries no "emailed to
// this address" fact. F4-08 B10t makes it the invite's BindEmailOnAccept flag,
// and the displacement below is already wired to it, in the accept's own
// commit, so that change is this function and the account's EmailVerified.
func inviteBindsEmail(*OneTimeToken) bool { return false }

// displaceUnverifiedHolders is HEAD's displacement of unverified holders, for
// an accept that binds email to the account newUser (ConsumeInvite's
// clearEmailFromHolders over unprovenHoldersLocked): every account other
// than newUser that holds email, by SameEmail, WITHOUT its address verified
// loses it (the address is cleared and the version bumped), staged in b so
// the accept and the displacement commit together or not at all. It returns
// the ids of the accounts it cleared, in store order.
//
// An account that holds the address VERIFIED refuses the bind (ErrEmailInUse,
// HEAD's ErrInviteEmailTaken), as does a record it cannot decode, which
// cannot be ruled out as a verified holder: nothing is staged for either.
// Holding the invite token proves control of the inbox only where the invite
// was emailed to it, so the caller binds only then (inviteBindsEmail).
//
// The caller holds the account e-mail lock (emailMu), so no verified claim to
// the address lands between this read and the commit, and re-runs the call on
// every attempt: the clears are derived from the stored records and so
// conflict the commit (storage.ErrConflict) when a holder changed first.
func (s *Service) displaceUnverifiedHolders(ctx context.Context, b *storage.Batch, email string, newUser UserID) ([]UserID, error) {
	now := s.now().UTC()
	var holders []*User
	for u, err := range s.d.Store.ScanUsers(ctx) {
		if errors.Is(err, ErrUndecodableRecord) {
			return nil, fmt.Errorf("%w: a user record could not be read, so a verified holder of the address cannot be ruled out", ErrEmailInUse)
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
		}
		if u.ID == newUser || !SameEmail(u.Email, email) {
			continue
		}
		if u.EmailVerified {
			return nil, fmt.Errorf("%w: another account holds that address verified", ErrEmailInUse)
		}
		holders = append(holders, u)
	}
	displaced := make([]UserID, 0, len(holders))
	for _, u := range holders {
		up := *u
		up.Email, up.EmailVerified = "", false
		up.Version, up.UpdatedAt = u.Version+1, now
		if err := s.d.Store.PutUser(b, &up); err != nil {
			return nil, err
		}
		displaced = append(displaced, u.ID)
	}
	return displaced, nil
}

// warnDisplaced logs, after the accept committed, that it took its address
// from unverified accounts (HEAD's recordAcceptDisplacement warning). Ids
// only: the address is not logged.
func (s *Service) warnDisplaced(ctx context.Context, invite string, newUser UserID, displaced []UserID) {
	if len(displaced) == 0 {
		return
	}
	ids := make([]string, len(displaced))
	for i, d := range displaced {
		ids[i] = string(d)
	}
	s.log.LogAttrs(ctx, slog.LevelWarn, "auth: an accepted invite took its address back from unverified account(s)",
		slog.String("invite", invite), slog.String("new_user", string(newUser)), slog.Any("displaced_user_ids", ids))
}
