// file: core/auth/invalidation.go
// version: 0.1.0
// guid: 6e2a91c4-3f58-4d07-b8a1-9c45d0e7f312
// last-edited: 2026-11-21

package auth

import (
	"context"

	"github.com/falkcorp/mediacore/backup/journal"
)

// Every revocation publishes core.principal.invalidated.v1 {user_id,
// credential_id?, reason} after its batch commits (05-authn §4.12); realtime
// ends every open stream the event names (05-realtime §4.6), and the SPA
// treats that close as final. So an event names only credentials the write
// really revoked (F4-08 A15). What each revoking write publishes:
//
//   - API key revoked or disabled: the key (owner, key ID);
//   - one session revoked: that session (user, session public ID);
//   - user disabled (SetStatus), user deleted, merge source disabled
//     (StageUserMerge's after-commit step): the whole user, every credential;
//   - credential epoch bumped (sign-out everywhere, self-service password
//     change, a reset link issued, a reset link consumed): one event per
//     stored session of the user, minus the session the write keeps. Never
//     user-wide: API keys are not epoch-bound and survive the bump (§4.12),
//     and a user-wide event would end their streams and the kept session's
//     for good. An ext: principal's stream (epoch-bound, but named by no
//     stored record) ends on its next heartbeat, as before this event existed;
//   - nothing for a re-enable (user.enabled, its epoch bump included: the
//     disable already ended every stream), a key re-enabled, a role
//     unassigned (rights, not a credential: rbac's concern), or a restore's
//     re-applied lines (boot, before the bus freezes and before any stream).

// invalidation is one credential, or every credential of User when
// Credential is empty, that a revoking write revoked.
type invalidation struct {
	User       UserID
	Credential string // a session public ID or an API key ID; "" is the whole user
	Reason     journal.Kind
}

// invalidations collects one commit attempt's invalidations (commitInvalidating).
type invalidations struct {
	list []invalidation
}

// user records that every credential of u is revoked.
func (v *invalidations) user(u UserID, why journal.Kind) {
	v.list = append(v.list, invalidation{User: u, Reason: why})
}

// credential records that one credential of u is revoked. An empty cred
// records nothing: it would read as the whole user.
func (v *invalidations) credential(u UserID, cred string, why journal.Kind) {
	if cred == "" {
		return
	}
	v.list = append(v.list, invalidation{User: u, Credential: cred, Reason: why})
}

// invalidateSessions records one invalidation per stored session of u except keep (the
// session the write re-stamps; "" keeps none): an epoch bump's events. The
// scan reads the store, not the batch, so a session the same batch issues is
// never named. A scan error is logged and ends the recording: the write
// itself must not fail on what only speeds up the end of a stream, and the
// heartbeat still ends the rest.
func (s *Service) invalidateSessions(ctx context.Context, v *invalidations, u UserID, keep string, why journal.Kind) {
	for rec, err := range s.d.Store.ScanUserSessions(ctx, u) {
		if err != nil {
			if s.leftUndecodable("session", u, err) {
				continue
			}
			s.log.Warn("auth: the sessions an epoch bump revoked could not be listed; their streams end on the heartbeat", "user", string(u), "err", err)
			return
		}
		if rec.PublicID == keep {
			continue
		}
		v.credential(u, rec.PublicID, why)
	}
}

// publishInvalidations hands every recorded invalidation to
// Deps.OnInvalidated, in the order the write recorded them.
func (s *Service) publishInvalidations(ctx context.Context, v *invalidations) {
	if s.d.OnInvalidated == nil {
		return
	}
	for _, inv := range v.list {
		s.d.OnInvalidated(ctx, string(inv.User), inv.Credential, string(inv.Reason))
	}
}
