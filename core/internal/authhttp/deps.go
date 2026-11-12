// file: core/internal/authhttp/deps.go
// version: 1.0.0
// guid: 0c8e5b7d-91a3-4d2f-b6e0-47f1a2c93d68
// last-edited: 2026-11-10

package authhttp

import (
	"context"
	"iter"
	"net/http"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/auth/oidc"
	"github.com/falkcorp/mediacore/rbac"
)

// Entry is the sign-in surface of auth.Service: every call a caller makes
// before it holds a session, plus the two reads the status route shows.
type Entry interface {
	Login(ctx context.Context, in auth.LoginInput) (*auth.IssuedSession, error)
	ConsumeTempLogin(ctx context.Context, token, newPassword string, client auth.ClientInfo) (*auth.IssuedSession, error)
	LookupInvite(ctx context.Context, token string, client auth.ClientInfo) (auth.InviteView, error)
	AcceptInvite(ctx context.Context, in auth.AcceptInviteInput) (*auth.User, *auth.IssuedSession, error)
	Setup(ctx context.Context, in auth.SetupInput) (*auth.User, error)
	RedeemBootstrap(ctx context.Context, token string, client auth.ClientInfo) (*auth.BootstrapResult, error)
	Posture() auth.Posture
	HasUsers() bool
}

// Cookies writes the session cookie under the one name stage 12 reads
// (auth.Service.SessionCookie): an empty token clears it.
type Cookies interface {
	SessionCookie(c auth.ClientInfo, token string, expires time.Time) *http.Cookie
}

// Self is what the caller does to its own credentials and what any actor does
// to a key or a session by ID. Each method re-checks the actor.
type Self interface {
	ChangeOwnPassword(ctx context.Context, actor *auth.Principal, current, next string) error
	Sessions(ctx context.Context, user auth.UserID) iter.Seq2[auth.SessionView, error]
	RevokeSession(ctx context.Context, actor *auth.Principal, publicID string) error
	APIKeys(ctx context.Context, owner auth.UserID) iter.Seq2[*auth.APIKey, error]
	CreateAPIKey(ctx context.Context, actor *auth.Principal, spec auth.KeySpec) (string, *auth.APIKey, error)
	SetAPIKeyStatus(ctx context.Context, actor *auth.Principal, id string, st auth.KeyStatus) error
	RevokeAPIKey(ctx context.Context, actor *auth.Principal, id string) error
}

// Accounts is the user list and the account actions. Dominance and
// core:users.manage are the Service's to enforce on every write.
type Accounts interface {
	Users(ctx context.Context, f auth.UserFilter) iter.Seq2[*auth.User, error]
	SetStatus(ctx context.Context, actor *auth.Principal, id auth.UserID, st auth.UserStatus) error
	DeleteUser(ctx context.Context, actor *auth.Principal, id auth.UserID) error
	SetRoles(ctx context.Context, actor *auth.Principal, id auth.UserID, roles []rbac.RoleName) error
	MintResetLink(ctx context.Context, actor *auth.Principal, target auth.UserID) (string, error)
	MintTempLogin(ctx context.Context, actor *auth.Principal, target auth.UserID) (string, error)
}

// Invites is the invite management surface (the public lookup and accept are
// Entry's).
type Invites interface {
	CreateInvite(ctx context.Context, actor *auth.Principal, in auth.InviteSpec) (string, auth.InviteView, error)
	DeleteInvite(ctx context.Context, actor *auth.Principal, id string) error
	Invites(ctx context.Context) iter.Seq2[auth.InviteView, error]
}

// UserLookup reads one user.
type UserLookup interface {
	User(ctx context.Context, id auth.UserID) (*auth.User, error)
}

// Dominance is rbac.Service.Dominates: nil when actor holds every permission
// of every role in target (05-rbac §4.7 rule 5). A read of another user's key
// uses it to decide whether the actor may see the key at all.
type Dominance interface {
	Dominates(actor rbac.Checker, target []rbac.RoleName) error
}

// Flow is auth/oidc's *Flow.
type Flow interface {
	Start(ctx context.Context, req oidc.StartRequest) (*oidc.Started, error)
	Callback(ctx context.Context, req oidc.CallbackRequest) (*oidc.Completed, error)
	Providers() []oidc.ProviderInfo
}

// ProfileUpdate is a PATCH /me: the fields the caller set. A nil field is left
// as it is.
type ProfileUpdate struct {
	DisplayName *string
	Email       *string
}

// Profiles writes the caller's own profile. An e-mail change is an identity
// change (it is what an SSO sign-in links on): an implementation must clear
// EmailVerified, hold the account e-mail lock around the uniqueness check and
// revoke the access an old address granted, as HEAD's UpdateMe does.
type Profiles interface {
	UpdateProfile(ctx context.Context, actor *auth.Principal, in ProfileUpdate) (*auth.User, error)
}

// FromService fills the identity dependencies from one auth.Service and the
// rbac.Service that decides dominance; the kernel adds Flow, Profiles, Audit,
// Client and the callback base.
func FromService(svc *auth.Service, rb *rbac.Service) Deps {
	return Deps{Entry: svc, Cookies: svc, Self: svc, Accounts: svc, Invites: svc, Users: svc, Dominance: rb}
}
