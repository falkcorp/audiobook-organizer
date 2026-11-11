// file: core/auth/invites.go
// version: 1.2.0
// guid: 50c00e01-9428-42af-bc4e-b9e9aee83e64
// last-edited: 2026-11-11

package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"iter"
	"net/mail"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// Invites (05-authn §4.13; HEAD's handlers/user.go CreateInvite,
// DeleteInvite, ListInvites and AcceptInvite). An invite is a single-use
// mci_ token, stored as its SHA-256 (onetime/invite, Serialized), naming the
// account its creator chose (username, role, optional e-mail). Accepting it
// with a password creates that account and opens a delegated session
// (OriginInvite) in one batch. The method names are coined by F4-02
// (seams.d/F4-02.md): §3.2 declares none.
//
// An invite carries its creator's credential epoch: a password change,
// disable or sign-out-everywhere of the creator kills every invite it made
// (§4.12). Accepting re-runs rbac.CanAssign for the creator over the role
// (SEC3-2), so an invite whose creator was demoted, disabled or deleted
// yields nothing. The invitee's e-mail is stored unverified and grants no
// SSO admission: unlike HEAD, an invite never binds an address, so no
// uniqueness check guards it.
const (
	actionInviteCreate = "core.invite.create"
	actionInviteAccept = "core.invite.accept"
	refKindInvite      = "invite"
	reasonInviteDenied = "invite_invalid"
)

// inviteLink is the fragment link an invite token travels in.
func inviteLink(token string) string { return "/invite#token=" + token }

func inviteView(t *OneTimeToken) InviteView {
	return InviteView{
		ID: t.ID, Username: t.Username, Email: t.Email, Role: t.Role, CreatedBy: t.CreatedBy,
		CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt,
	}
}

// inviteEmail is HEAD's normalizeInviteEmail (handlers/invite_delivery.go):
// "" for none, else one plain address, normalised (NormalizeEmail).
func inviteEmail(raw string) (string, error) {
	if strings.ContainsAny(raw, "\r\n") {
		return "", fmt.Errorf("%w: an invite e-mail must be a single plain address", ErrBadInput)
	}
	e := NormalizeEmail(raw)
	if e == "" {
		return "", nil
	}
	if a, err := mail.ParseAddress(e); err != nil || a.Address != e || len(e) > 254 {
		return "", fmt.Errorf("%w: an invite e-mail must be a single plain address, like name@example.com", ErrBadInput)
	}
	return e, nil
}

// CreateInvite creates an invite (§4.13): core:users.manage through an
// interactive method and CanAssign over its role (05-rbac §4.7 rule 2). The
// username is trimmed and must be free: no user holds it and no open invite
// names it (ErrUserExists), both under FoldUsername. The TTL is in.TTL, or
// core.auth.invite.ttl when zero. It returns the fragment link, which holds
// the token and is the only place the token appears, and the invite's view;
// the audit record names the invite's public ID.
func (s *Service) CreateInvite(ctx context.Context, actor *Principal, in InviteSpec) (string, InviteView, error) {
	if err := s.mayManageUsers(actor, "create an invite"); err != nil {
		return "", InviteView{}, err
	}
	name := strings.TrimSpace(in.Username)
	role := rbac.RoleName(strings.TrimSpace(string(in.Role)))
	switch {
	case name == "":
		return "", InviteView{}, fmt.Errorf("%w: an invite needs a username", ErrBadInput)
	case role == "":
		return "", InviteView{}, fmt.Errorf("%w: an invite needs a role", ErrBadInput)
	case in.TTL < 0:
		return "", InviteView{}, fmt.Errorf("%w: a negative invite lifetime", ErrBadInput)
	}
	email, err := inviteEmail(in.Email)
	if err != nil {
		return "", InviteView{}, err
	}
	if err := s.d.RBAC.CanAssign(actor.Perms, []rbac.RoleName{role}); err != nil {
		return "", InviteView{}, fmt.Errorf("%w: %w", ErrScopeEscalation, err)
	}
	ttl := in.TTL
	if ttl == 0 {
		ttl = s.settings().InviteTTL
	}
	now := s.now().UTC()

	s.inviteMu.Lock()
	defer s.inviteMu.Unlock()
	if err := s.usernameFree(ctx, name, now); err != nil {
		return "", InviteView{}, err
	}
	var token string
	var view InviteView
	err = s.commitRetrying(ctx, func(b *storage.Batch) error {
		creator, err := s.d.Store.GetUser(ctx, actor.User.ID)
		if err != nil {
			return err
		}
		t := &OneTimeToken{
			Kind: OneTimeInvite, CreatedBy: creator.ID, Username: name, Email: email, Role: role,
			CredentialEpoch: creator.CredentialEpoch, CreatedAt: now, ExpiresAt: now.Add(ttl),
		}
		tok, err := s.d.Store.IssueOneTime(b, t)
		if err != nil {
			return err
		}
		token, view = string(tok), inviteView(t)
		return s.d.Audit.RecordIn(ctx, b, audit.Event{
			Action: actionInviteCreate, Outcome: audit.Success, Target: ref(refKindInvite, t.ID),
			Details: map[string]any{"username": name, "role": string(role)},
		})
	})
	if err != nil {
		return "", InviteView{}, err
	}
	return inviteLink(token), view, nil
}

// usernameFree refuses name when a user holds it or an open (unexpired)
// invite names it, under FoldUsername. An undecodable invite cannot be ruled
// out and is skipped: the store's username index still refuses a duplicate
// account when the invite is accepted.
func (s *Service) usernameFree(ctx context.Context, name string, now time.Time) error {
	fold := FoldUsername(name)
	_, err := s.d.Store.UserByUsername(ctx, fold)
	switch {
	case err == nil:
		return fmt.Errorf("%w: username %q is taken", ErrUserExists, name)
	case !errors.Is(err, ErrNotFound):
		return fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
	}
	for t, err := range s.d.Store.ScanOneTime(ctx, OneTimeInvite) {
		if errors.Is(err, ErrUndecodableRecord) {
			continue
		}
		if err != nil {
			return err
		}
		if now.Before(t.ExpiresAt) && FoldUsername(t.Username) == fold {
			return fmt.Errorf("%w: an open invite names %q", ErrUserExists, name)
		}
	}
	return nil
}

// Invites lists the open invites (HEAD's ListInvites), never a token. An
// expired invite is not listed; an undecodable one is yielded as
// ErrUndecodableRecord for that record only. The route guards it
// (core:users.manage), as Users.
func (s *Service) Invites(ctx context.Context) iter.Seq2[InviteView, error] {
	now := s.now()
	return func(yield func(InviteView, error) bool) {
		for t, err := range s.d.Store.ScanOneTime(ctx, OneTimeInvite) {
			if err != nil {
				if !yield(InviteView{}, err) {
					return
				}
				continue
			}
			if !now.Before(t.ExpiresAt) {
				continue
			}
			if !yield(inviteView(t), nil) {
				return
			}
		}
	}
}

// DeleteInvite deletes the invite with public ID id (§4.13): core:users.manage
// through an interactive method; ErrNotFound when there is none. CoreActions
// names no invite deletion, so it is not audited (seams.d/F4-02.md).
func (s *Service) DeleteInvite(ctx context.Context, actor *Principal, id string) error {
	if err := s.mayManageUsers(actor, "delete an invite"); err != nil {
		return err
	}
	return s.commitRetrying(ctx, func(b *storage.Batch) error {
		return s.d.Store.DeleteOneTime(b, OneTimeInvite, id)
	})
}

// LookupInvite shows the open invite token names, without spending it, for
// the accept page (§4.13). It goes through the source's throttle (§4.9): a
// spent budget is refused first, and a token that does not parse or names
// nothing is charged (ErrInviteInvalid). An expired invite is
// ErrInviteInvalid, not charged.
func (s *Service) LookupInvite(ctx context.Context, token string, client ClientInfo) (InviteView, error) {
	t, err := s.openInvite(ctx, token, client)
	if err != nil {
		return InviteView{}, err
	}
	if !s.now().Before(t.ExpiresAt) {
		return InviteView{}, fmt.Errorf("%w: the invite expired", ErrInviteInvalid)
	}
	return inviteView(t), nil
}

// openInvite is the throttled read of the invite token names: ErrThrottled,
// ErrInviteInvalid (charged when the token is malformed or unknown), or the
// store's error.
func (s *Service) openInvite(ctx context.Context, token string, client ClientInfo) (*OneTimeToken, error) {
	source := client.sourceKey()
	th := s.loginThrottle()
	if th.SourceBlocked(source) {
		return nil, s.throttledOn(surfaceInvite, LoginRetryAfter)
	}
	t, err := s.lookupOneTime(ctx, OneTimeInvite, KindInvite, token)
	if err != nil {
		if chargeable(err) {
			th.RecordFailure("", source)
		}
		if errors.Is(err, ErrInvalidCredentials) {
			return nil, fmt.Errorf("%w: %w", ErrInviteInvalid, err)
		}
		return nil, err
	}
	return t, nil
}

// AcceptInvite spends an invite and creates its account (§4.13): the
// username and role the creator chose, the e-mail unverified, in.Password
// (at least core.auth.password.min_length) as its password, and a delegated
// session (OriginInvite, core.auth.session.remember_ttl) in the same batch.
// The throttle applies as in LookupInvite; a malformed request, a short
// password and a busy hasher leave the invite unspent.
//
// A stored invite is spent whatever the answer, and each refusal is
// ErrInviteInvalid, audited core.invite.accept in the batch that deletes it:
// expired; its creator gone, not active, or at another credential epoch than
// at issue; the creator no longer able to assign the role (SEC3-2, Outcome
// denied); or the username taken since it was created (wrapping
// ErrUserExists).
func (s *Service) AcceptInvite(ctx context.Context, in AcceptInviteInput) (*User, *IssuedSession, error) {
	if _, err := s.openInvite(ctx, in.Token, in.Client); err != nil {
		return nil, nil, err
	}
	if in.Password == "" {
		return nil, nil, fmt.Errorf("%w: a password is required", ErrBadInput)
	}
	if err := s.checkNewPassword(in.Password); err != nil {
		return nil, nil, err
	}
	hash, err := s.hashNew(ctx, in.Password)
	if err != nil {
		return nil, nil, err
	}
	now := s.now().UTC()
	id, err := newULID(now, rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	var user *User
	var out *IssuedSession
	var refusal error
	err = s.commitRetrying(ctx, func(b *storage.Batch) error {
		refusal = nil
		t, err := s.d.Store.TakeOneTime(b, OneTimeInvite, Token(in.Token)) // parsed by openInvite
		if errors.Is(err, ErrNotFound) {
			return fmt.Errorf("%w: the invite was spent concurrently", ErrInviteInvalid)
		}
		if err != nil {
			return err
		}
		// refuse commits the take alone, with its audit record: a refused
		// invite is spent.
		refuse := func(denied error) error {
			refusal = denied
			outcome := audit.Failure
			if errors.Is(denied, errCannotAssign) || errors.Is(denied, errCreatorGone) {
				outcome = audit.Denied
			}
			return s.d.Audit.RecordIn(ctx, b, audit.Event{
				Action: actionInviteAccept, Outcome: outcome, Reason: reasonInviteDenied, Target: ref(refKindInvite, t.ID),
				Actor:   audit.Actor{ClientAddr: addrString(in.Client.Addr), UserAgent: in.Client.UserAgent},
				Details: map[string]any{"why": denied.Error()},
			})
		}
		creator, denied := s.inviteRefusal(ctx, t, now)
		if denied != nil {
			if !errors.Is(denied, ErrInviteInvalid) {
				return denied
			}
			return refuse(denied)
		}
		if err := s.d.Store.GuardUser(b, creator); err != nil {
			return err
		}
		u := &User{
			ID: UserID(id), Username: t.Username, Email: t.Email, HasPassword: true,
			Roles: []rbac.RoleName{t.Role}, Status: StatusActive, CreatedAt: now, UpdatedAt: now, Version: 1,
		}
		if err := s.d.Store.SetPassword(b, u, hash); err != nil {
			if !errors.Is(err, ErrUserExists) {
				return err
			}
			// Taken after inviteRefusal read it (a concurrent accept of
			// this invite, or another account). The store staged nothing
			// for the user; if the invite is gone too, committing the take
			// conflicts and the retry finds it spent.
			return refuse(fmt.Errorf("%w: %w", ErrInviteInvalid, err))
		}
		rec, issued, err := s.stageSession(b, u, OriginInvite, true, in.Client, now)
		if err != nil {
			return err
		}
		user, out = u, issued
		return s.d.Audit.RecordIn(ctx, b, audit.Event{
			Action: actionInviteAccept, Outcome: audit.Success, Target: ref(refKindUser, id),
			Actor: audit.Actor{
				UserID: id, Username: u.Username, Method: string(MethodSessionDelegated), CredentialID: rec.PublicID,
				ClientAddr: addrString(in.Client.Addr), UserAgent: in.Client.UserAgent,
			},
			Details: map[string]any{"invite": t.ID, "role": string(t.Role), "created_by": string(t.CreatedBy)},
		})
	})
	if err != nil {
		return nil, nil, err
	}
	if refusal != nil {
		return nil, nil, refusal
	}
	s.hasUsers.Store(true)
	return user, out, nil
}

// The SEC3-2 refusals, audited with Outcome denied (§4.13): the invite's
// creator no longer exists, or may no longer assign its role.
var (
	errCreatorGone  = errors.New("the invite's creator no longer exists")
	errCannotAssign = errors.New("the invite's creator may no longer assign its role")
)

// inviteRefusal is why accepting t, as just taken, yields nothing (wrapping
// ErrInviteInvalid), a store failure (not wrapping it), or, when it may, the
// creator row it was judged against, which the caller guards in the accept
// batch so a concurrent demotion or disable of the creator conflicts the
// commit instead of racing it (Store.GuardUser).
func (s *Service) inviteRefusal(ctx context.Context, t *OneTimeToken, now time.Time) (*User, error) {
	if !now.Before(t.ExpiresAt) {
		return nil, fmt.Errorf("%w: the invite expired", ErrInviteInvalid)
	}
	creator, err := s.d.Store.GetUser(ctx, t.CreatedBy)
	switch {
	case errors.Is(err, ErrNotFound):
		return nil, fmt.Errorf("%w: %w", ErrInviteInvalid, errCreatorGone)
	case err != nil:
		return nil, err
	case creator.Status != StatusActive:
		return nil, fmt.Errorf("%w: its creator is %s", ErrInviteInvalid, creator.Status)
	case creator.CredentialEpoch != t.CredentialEpoch:
		return nil, fmt.Errorf("%w: its creator's credentials changed since it was made", ErrInviteInvalid)
	}
	// The creator's epoch does not cover a demotion: role changes bump no
	// epoch (§4.12), so this re-check is what voids an invite whose creator
	// lost the right to make it. Creating one needs core:users.manage
	// (mayManageUsers), and CanAssign alone does not ask for it: a creator
	// moved to a role that still holds every scope of t.Role would pass.
	checker := s.d.RBAC.Compile(rbac.CheckerInput{Roles: creator.Roles, Interactive: true})
	if !checker.Has(permUsersManage) {
		return nil, fmt.Errorf("%w: %w: its creator no longer holds %s", ErrInviteInvalid, errCannotAssign, permUsersManage)
	}
	if err := s.d.RBAC.CanAssign(checker, []rbac.RoleName{t.Role}); err != nil {
		return nil, fmt.Errorf("%w: %w (%s): %w", ErrInviteInvalid, errCannotAssign, t.Role, err)
	}
	_, err = s.d.Store.UserByUsername(ctx, FoldUsername(t.Username))
	switch {
	case err == nil:
		return nil, fmt.Errorf("%w: %w: username %q is taken", ErrInviteInvalid, ErrUserExists, t.Username)
	case !errors.Is(err, ErrNotFound):
		return nil, err
	}
	return creator, nil
}
