// file: core/internal/authhttp/dto.go
// version: 1.0.0
// guid: b1d46f8e-5a07-4c93-8e2b-d07c9a3e1f52
// last-edited: 2026-11-10

package authhttp

import (
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
)

// Status is GET /auth/status: what the sign-in page needs before it knows the
// caller. It says nothing about any account.
type Status struct {
	HasUsers      bool `json:"has_users"`
	AuthDisabled  bool `json:"auth_disabled"`
	SetupRequired bool `json:"setup_required"`
}

// User is an account as the identity routes show it: never a hash, an epoch or
// a credential.
type User struct {
	ID                    string    `json:"id"`
	Username              string    `json:"username"`
	Email                 string    `json:"email,omitempty"`
	EmailVerified         bool      `json:"email_verified"`
	DisplayName           string    `json:"display_name,omitempty"`
	Roles                 []string  `json:"roles"`
	Status                string    `json:"status"`
	HasPassword           bool      `json:"has_password"`
	PasswordResetRequired bool      `json:"password_reset_required"`
	SignInRestricted      bool      `json:"sign_in_restricted"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

func userBody(u *auth.User) User {
	roles := make([]string, 0, len(u.Roles))
	for _, r := range u.Roles {
		roles = append(roles, string(r))
	}
	return User{
		ID: string(u.ID), Username: u.Username, Email: u.Email, EmailVerified: u.EmailVerified, DisplayName: u.DisplayName,
		Roles: roles, Status: string(u.Status), HasPassword: u.HasPassword, PasswordResetRequired: u.PasswordResetRequired,
		SignInRestricted: u.SignInRestricted, CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt,
	}
}

// Me is GET /me: the caller, the scopes its checker holds, and how it signed in.
type Me struct {
	User          User     `json:"user"`
	Permissions   []string `json:"permissions"`
	Method        string   `json:"method"`
	OwnerVerified bool     `json:"owner_verified"`
}

// LoginRequest is POST /auth/login.
type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Remember bool   `json:"remember,omitempty"`
}

// SessionOpened is what a sign-in, a temp-login consumption or an invite
// acceptance answers: when the session expires and its public ID. The token is
// in the cookie and nowhere else.
type SessionOpened struct {
	PublicID  string    `json:"public_id"`
	ExpiresAt time.Time `json:"expires_at"`
	User      *User     `json:"user,omitempty"`
}

// SetupRequest is POST /auth/setup.
type SetupRequest struct {
	Username       string `json:"username"`
	Password       string `json:"password"`
	Email          string `json:"email,omitempty"`
	BootstrapToken string `json:"bootstrap_token,omitempty"`
}

// BootstrapRequest is POST /auth/bootstrap.
type BootstrapRequest struct {
	Token string `json:"token"`
}

// BootstrapResult is shown once: the minted key and, when the account was
// created, its generated password.
type BootstrapResult struct {
	Key               string    `json:"key"`
	KeyID             string    `json:"key_id"`
	ExpiresAt         time.Time `json:"expires_at"`
	User              User      `json:"user"`
	GeneratedPassword string    `json:"generated_password,omitempty"`
}

// Provider is one enabled identity provider: ids and kinds, never a client id.
type Provider struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

// Providers is GET /auth/providers.
type Providers struct {
	Providers []Provider `json:"providers"`
}

// InviteLookup is POST /auth/invites/lookup: the one-time token, in a body and
// never a path.
type InviteLookup struct {
	Token string `json:"token"`
}

// InviteAccept is POST /auth/invites/accept.
type InviteAccept struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// InvitePreview is what the lookup shows a holder of an invite token: what the
// account will be. Nothing about its creator.
type InvitePreview struct {
	Username  string    `json:"username"`
	Email     string    `json:"email,omitempty"`
	Role      string    `json:"role"`
	ExpiresAt time.Time `json:"expires_at"`
}

// TempLoginConsume is POST /auth/temp-login/consume. NewPassword is required
// for a reset link and refused for a plain temp-login link.
type TempLoginConsume struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password,omitempty"`
}

// ProfilePatch is PATCH /me: the fields to change. A field left out is kept.
type ProfilePatch struct {
	DisplayName *string `json:"display_name,omitempty"`
	Email       *string `json:"email,omitempty"`
}

// PasswordChange is PUT /me/password. There is no target-user field: the route
// changes the caller's own password and nobody else's.
type PasswordChange struct {
	Current string `json:"current"`
	New     string `json:"new"`
}

// Session is one open session: its public ID (what revoke takes), never its
// token.
type Session struct {
	PublicID   string    `json:"public_id"`
	UserAgent  string    `json:"user_agent,omitempty"`
	ClientAddr string    `json:"client_addr,omitempty"`
	Origin     string    `json:"origin"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeen   time.Time `json:"last_seen"`
	ExpiresAt  time.Time `json:"expires_at"`
	Current    bool      `json:"current"`
}

func sessionBody(v auth.SessionView) Session {
	s := Session{PublicID: v.PublicID, UserAgent: v.UserAgent, Origin: string(v.Origin), CreatedAt: v.CreatedAt,
		LastSeen: v.LastSeen, ExpiresAt: v.ExpiresAt, Current: v.Current}
	if v.ClientAddr.IsValid() {
		s.ClientAddr = v.ClientAddr.String()
	}
	return s
}

// APIKey is one key as a list or a read shows it: the display prefix of its
// hash, never the key and never the hash. OwnerUsername is filled on the
// routes that list or read another user's key (core:users.manage).
type APIKey struct {
	ID            string     `json:"id"`
	Owner         string     `json:"owner"`
	OwnerUsername string     `json:"owner_username,omitempty"`
	Name          string     `json:"name"`
	Scopes        []string   `json:"scopes"`
	Status        string     `json:"status"`
	CreatedAt     time.Time  `json:"created_at"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	RevokedAt     *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt    *time.Time `json:"last_used_at,omitempty"`
	LastUsedIP    string     `json:"last_used_ip,omitempty"`
	UseCount      int64      `json:"use_count"`
	Imported      bool       `json:"imported"`
}

func keyBody(k *auth.APIKey, ownerName string) APIKey {
	scopes := make([]string, 0, len(k.Scopes))
	for _, s := range k.Scopes {
		scopes = append(scopes, string(s))
	}
	out := APIKey{ID: k.ID, Owner: string(k.Owner), OwnerUsername: ownerName, Name: k.Name, Scopes: scopes,
		Status: string(k.Status), CreatedAt: k.CreatedAt, UseCount: k.UseCount, Imported: k.Imported,
		ExpiresAt: when(k.ExpiresAt), RevokedAt: when(k.RevokedAt), LastUsedAt: when(k.LastUsedAt)}
	if k.LastUsedIP.IsValid() {
		out.LastUsedIP = k.LastUsedIP.String()
	}
	return out
}

func when(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// KeyCreate is POST /me/api-keys and POST /users/{id}/api-keys. ExpiresIn is a
// duration string ("720h"); empty is the default lifetime.
type KeyCreate struct {
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes"`
	ExpiresIn string   `json:"expires_in,omitempty"`
}

// KeyCreated is shown once: the key itself, which no later call returns.
type KeyCreated struct {
	Key    string `json:"key"`
	APIKey APIKey `json:"api_key"`
}

// KeyStatus is PATCH /me/api-keys/{id} and PATCH /api-keys/{id}.
type KeyStatus struct {
	Status string `json:"status"`
}

// Invite is an open invite as the management list shows it: never its token.
type Invite struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	Email     string    `json:"email,omitempty"`
	Role      string    `json:"role"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func inviteBody(v auth.InviteView) Invite {
	return Invite{ID: v.ID, Username: v.Username, Email: v.Email, Role: string(v.Role), CreatedBy: string(v.CreatedBy),
		CreatedAt: v.CreatedAt, ExpiresAt: v.ExpiresAt}
}

// InviteCreate is POST /users/invites. TTL is a duration string; empty is
// core.auth.invite.ttl.
type InviteCreate struct {
	Username string `json:"username"`
	Email    string `json:"email,omitempty"`
	Role     string `json:"role"`
	TTL      string `json:"ttl,omitempty"`
}

// InviteCreated is shown once: the fragment link, which holds the token.
type InviteCreated struct {
	Link   string `json:"link"`
	Invite Invite `json:"invite"`
}

// LinkCreated is a temp-login or reset link, shown once: the fragment link,
// which holds the token. The core API has no "set another user's password"
// route; an admin sends this link.
type LinkCreated struct {
	Link string `json:"link"`
}

// RolesReplace is PUT /users/{id}/roles.
type RolesReplace struct {
	Roles []string `json:"roles"`
}

// pageOf pages an already collected list the way httpapi.Page defines it.
func pageOf[T any](all []T, p httpapi.PageParams) httpapi.Page[T] {
	total := len(all)
	if p.Offset >= total {
		all = nil
	} else {
		all = all[p.Offset:]
	}
	if len(all) > p.Limit && p.Limit > 0 {
		all = all[:p.Limit]
	}
	items := make([]T, 0, len(all))
	items = append(items, all...)
	return httpapi.Page[T]{Items: items, Count: len(items), Limit: p.Limit, Offset: p.Offset, Total: int64(total)}
}
