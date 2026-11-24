// file: core/auth/account_footprint.go
// version: 0.1.0
// guid: f3ddc90a-8e58-4ece-b023-1b6f833918a2
// last-edited: 2026-11-22

package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/rbac"
)

// AccountFootprint is the masked view of what hangs off one account that an
// admin screen shows beside it (F4-08 B10fp): its linked sign-ins, how many API
// keys and invites it holds, and how many of its sessions stand. It carries no
// credential: no key (the id, the prefix of its hash, the scopes), no token, no
// session id, no raw subject. Every field is either a count or a value an admin
// may read in the sign-in list.
//
// TestAccountFootprint_FieldSetIsPinned pins the whole field set of this type
// and of IdentityLink, so a field added to either is judged before it ships.
type AccountFootprint struct {
	// Identities are the account's linked sign-ins, ordered by provider and then
	// by subject, so two reads of unchanged links are equal.
	Identities []IdentityLink
	// APIKeys is how many API keys the account owns, whatever their status.
	APIKeys int
	// Invites is how many stored invites the account created.
	Invites int
	// InviteGrants is 1 when the invite allow-list holds the account's e-mail
	// address and 0 when it does not or the account has none.
	InviteGrants int
	// ActiveSessions is how many sessions the account holds that have not
	// expired and were opened at its current credential epoch (a session an
	// epoch bump has outdated is rejected at sign-in, so it is not open).
	ActiveSessions int
	// Unreadable names, by kind only, the records the read could not decode. A
	// record's id is not given: the name is a fixed phrase.
	Unreadable []string
}

// IdentityLink is one linked sign-in with its subject masked.
type IdentityLink struct {
	Provider string
	// SubjectMask is the subject with all but its first and last two characters
	// hidden (MaskSubject). The subject itself is never returned.
	SubjectMask string
	Email       string
	LinkedAt    time.Time
}

// MaskSubject hides a sign-in subject for display: a subject of four bytes or
// fewer is one bullet per byte, any other keeps its first two and last two
// bytes around an ellipsis. It is HEAD's mask (usermerge.maskSubject), kept
// byte for byte so the footprint reads the same before and after the flip.
func MaskSubject(s string) string {
	if len(s) <= 4 {
		return strings.Repeat("•", len(s))
	}
	return s[:2] + "…" + s[len(s)-2:]
}

// AdminAccountFootprint is AccountAdmin.AdminAccountFootprint: the masked
// footprint of one account, for the principal bound on ctx under the legacy
// surface's rule for it (mayReadFootprint). ErrNotFound when the account does
// not exist. It reads and writes nothing else, and the result is built from
// counts and masked values only (AccountFootprint).
func (s *Service) AdminAccountFootprint(ctx context.Context, id UserID) (AccountFootprint, error) {
	if err := ctxFootprintReader(ctx, "read an account's footprint"); err != nil {
		return AccountFootprint{}, err
	}
	u, err := s.d.Store.GetUser(ctx, id)
	if err != nil {
		return AccountFootprint{}, err
	}
	fp := AccountFootprint{Identities: []IdentityLink{}, Unreadable: []string{}}
	unreadable := func(kind string, err error) (skip bool, fatal error) {
		if errors.Is(err, ErrUndecodableRecord) {
			fp.Unreadable = append(fp.Unreadable, kind+" (undecodable)")
			return true, nil
		}
		return false, err
	}
	type linked struct {
		subject string
		link    IdentityLink
	}
	var links []linked
	for ident, err := range s.d.Store.ScanIdentities(ctx, id) {
		if err != nil {
			if skip, fatal := unreadable("sign-in", err); !skip {
				return AccountFootprint{}, fatal
			}
			continue
		}
		links = append(links, linked{ident.Subject, IdentityLink{
			Provider: ident.Provider, SubjectMask: MaskSubject(ident.Subject), Email: ident.Email, LinkedAt: ident.CreatedAt}})
	}
	slices.SortFunc(links, func(a, b linked) int {
		if c := strings.Compare(a.link.Provider, b.link.Provider); c != 0 {
			return c
		}
		return strings.Compare(a.subject, b.subject)
	})
	for _, l := range links {
		fp.Identities = append(fp.Identities, l.link)
	}
	for _, err := range s.d.Store.ScanAPIKeys(ctx, id) {
		if err != nil {
			if skip, fatal := unreadable("API key", err); !skip {
				return AccountFootprint{}, fatal
			}
			continue
		}
		fp.APIKeys++
	}
	for t, err := range s.d.Store.ScanOneTime(ctx, OneTimeInvite) {
		if err != nil {
			if skip, fatal := unreadable("invite", err); !skip {
				return AccountFootprint{}, fatal
			}
			continue
		}
		if t.CreatedBy == id {
			fp.Invites++
		}
	}
	if fold := NormalizeEmail(u.Email); fold != "" {
		ok, err := s.d.Store.InviteAllowed(ctx, fold)
		if err != nil {
			return AccountFootprint{}, fmt.Errorf("read the invite allow-list: %w", err)
		}
		if ok {
			fp.InviteGrants = 1
		}
	}
	now := s.now()
	for rec, err := range s.d.Store.ScanUserSessions(ctx, id) {
		if err != nil {
			if skip, fatal := unreadable("session", err); !skip {
				return AccountFootprint{}, fatal
			}
			continue
		}
		if now.Before(rec.ExpiresAt) && rec.CredentialEpoch == u.CredentialEpoch {
			fp.ActiveSessions++
		}
	}
	return fp, nil
}

// ctxFootprintReader is the check of AdminAccountFootprint: the principal bound
// on ctx, or ErrDenied.
func ctxFootprintReader(ctx context.Context, what string) error {
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return fmt.Errorf("%w: no principal is bound on the request", ErrDenied)
	}
	return mayReadFootprint(p, what)
}

// mayReadFootprint is the legacy surface's rule for the footprint read (F4-08
// B10fp): HEAD's GET /api/v1/users/:id/footprint asked for the admin role and,
// of an API key, that it may act as admin, and nothing about the method. So the
// rule is the principal's own: a user, holding core:users.manage and
// core:admin, through any method (a key that holds both included). It is
// mayManageUsers without its interactive leg and with core:admin, and without a
// dominance check, which an actor holding core:admin always passes. The
// auth-disabled principal is refused, as HEAD refused it (its handler needs a
// user). It is reachable only through AdminAccountFootprint, whose result
// holds counts and masked values and no credential (AccountFootprint).
func mayReadFootprint(p *Principal, what string) error {
	if p == nil || p.User == nil || p.User.ID == "" || p.Perms == nil {
		return fmt.Errorf("%w: no authenticated actor", ErrDenied)
	}
	if !p.Perms.Has(permUsersManage) {
		return fmt.Errorf("%w: %s is needed to %s", ErrDenied, permUsersManage, what)
	}
	if !p.Perms.Has(rbac.CoreAdmin) {
		return fmt.Errorf("%w: %s needs %s", ErrDenied, what, rbac.CoreAdmin)
	}
	return nil
}
