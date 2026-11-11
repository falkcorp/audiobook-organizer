// file: core/auth/service.go
// version: 1.0.0
// guid: ca90483b-e984-4a46-b126-bc0bb9aba4a5
// last-edited: 2026-11-10

package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// Defaults of the core.auth.* settings the service reads (05-authn §8).
const (
	DefaultSessionTTL    = 24 * time.Hour
	DefaultRememberTTL   = 168 * time.Hour
	DefaultKeyTTL        = 720 * time.Hour
	DefaultKeyMaxTTL     = 8760 * time.Hour
	// DefaultSessionCookie is core.auth.session.cookie_name's fallback; the
	// kernel passes <AppID>_session (§4.3, SEC-11).
	DefaultSessionCookie = "mediacore_session"
	conflictRetries      = 4
	auditModule          = "core"
	refKindUser          = "user"
	refKindAPIKey        = "apikey"
	refKindSession       = "session"
	permUsersManage      = rbac.Permission("core:users.manage")
	actionAPIKeyCreate   = "core.apikey.create"
	actionAPIKeyStatus   = "core.apikey.status"
	actionSessionRevoke  = "core.session.revoke"
	actionAuthenticate   = "core.auth.authenticate"
	reasonNotInteractive = "not_interactive"
)

// Options is the app's compile-time auth configuration, passed as
// app.Options.Auth (06 §6.4.5). The zero value is what new apps and hello
// get.
type Options struct {
	// ImportedKeyPrefixes are the prefixes of API keys an app imported at its
	// identity cutover; the key authenticator recognises them (§4.1, §4.4;
	// ABO ["abk_"]).
	ImportedKeyPrefixes []string
	// DefaultDisabledAllowRemote is core.auth.disabled_allow_remote's default
	// (§4.11; ABO true, SEC4-8); operator layers and overrides still win.
	DefaultDisabledAllowRemote bool
	// OIDCCallbackBase is the path prefix of the OIDC/OAuth callback (§3.2,
	// §4.13); empty for hello and new apps.
	OIDCCallbackBase string
}

// Settings are the live core.auth.* values the service reads on each call
// (05-authn §8). A zero field takes its default. Coined by F4-02
// (seams.d/F4-02.md): the kernel builds it from the config snapshot, so auth
// never imports config.
type Settings struct {
	SessionTTL    time.Duration // core.auth.session.ttl
	RememberTTL   time.Duration // core.auth.session.remember_ttl
	KeyDefaultTTL time.Duration // core.auth.apikey.default_ttl
	KeyMaxTTL     time.Duration // core.auth.apikey.max_ttl
	SessionCookie string        // core.auth.session.cookie_name
}

func (s Settings) withDefaults() Settings {
	if s.SessionTTL <= 0 {
		s.SessionTTL = DefaultSessionTTL
	}
	if s.RememberTTL <= 0 {
		s.RememberTTL = DefaultRememberTTL
	}
	if s.KeyDefaultTTL <= 0 {
		s.KeyDefaultTTL = DefaultKeyTTL
	}
	if s.KeyMaxTTL <= 0 {
		s.KeyMaxTTL = DefaultKeyMaxTTL
	}
	if s.SessionCookie == "" {
		s.SessionCookie = DefaultSessionCookie
	}
	return s
}

// Deps are the service's collaborators (05-authn §3.2). NewBatch, Settings,
// Extensions and Client are coined by F4-02 (seams.d/F4-02.md): the service
// stages every write on a batch of the identity store's own DB, reads its
// settings through a function rather than importing config, receives the
// app's identity extensions (app.Options.Authenticators) from the kernel,
// and reads the client address httpapi resolved through Client, because
// auth (L4) may not import httpapi (L6). Secrets and Events join with the
// rows that use them (OIDC, invalidation events).
type Deps struct {
	Store       Store
	NewBatch    func() *storage.Batch // an indexed batch over Store's keyspace (authstore.Store.NewBatch)
	RBAC        *rbac.Service
	Audit       audit.Logger
	Revocations journal.RevocationJournal
	Settings    func() Settings // nil: every default
	Clock       func() time.Time
	Log         *slog.Logger
	Metrics     *metrics.Scope // "mediacore.auth"; nil records nothing
	// Extensions are the app's identity extensions, in any order (stage 12
	// sorts them by Order).
	Extensions []Extension
	// Client returns a request's ClientInfo as httpapi resolved it
	// (trusted-proxy aware); nil uses the TCP peer.
	Client func(*http.Request) ClientInfo
}

// Service is core's authentication service (05-authn §3.2).
type Service struct {
	opts     Options
	d        Deps
	now      func() time.Time
	log      *slog.Logger
	hasUsers atomic.Bool

	extensions []Extension
	methods    map[Method]MethodSpec // registered extension and compat methods
	touches    touchTable
}

// New builds the service. It reads the store once (whether any user that can
// sign in exists, for HasUsers) and writes nothing (DATA4-10).
func New(ctx context.Context, opts Options, d Deps) (*Service, error) {
	var missing []string
	if d.Store == nil {
		missing = append(missing, "Store")
	}
	if d.NewBatch == nil {
		missing = append(missing, "NewBatch")
	}
	if d.RBAC == nil {
		missing = append(missing, "RBAC")
	}
	if d.Audit == nil {
		missing = append(missing, "Audit")
	}
	if d.Revocations == nil {
		missing = append(missing, "Revocations")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("auth: New: missing deps %v", missing)
	}
	s := &Service{opts: opts, d: d, now: d.Clock, log: d.Log}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	s.opts.ImportedKeyPrefixes = slices.Clone(opts.ImportedKeyPrefixes)
	s.methods = map[Method]MethodSpec{}
	for _, e := range d.Extensions {
		if err := checkExtension(e); err != nil {
			return nil, err
		}
		if _, dup := s.methods[e.Spec.Method]; dup {
			return nil, fmt.Errorf("%w: extension method %q registered twice", ErrMethodSpec, e.Spec.Method)
		}
		s.methods[e.Spec.Method] = e.Spec
		s.extensions = append(s.extensions, e)
	}
	for u, err := range d.Store.ScanUsers(ctx) {
		if errors.Is(err, ErrUndecodableRecord) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
		}
		if u.Status != StatusSystem {
			s.hasUsers.Store(true)
			break
		}
	}
	return s, nil
}

// HasUsers reports whether any user other than _system exists (§4.2 item 1).
// It is an atomic flag set by New and by every user creation; it never reads
// the store.
func (s *Service) HasUsers() bool { return s.hasUsers.Load() }

func (s *Service) settings() Settings {
	if s.d.Settings == nil {
		return Settings{}.withDefaults()
	}
	return s.d.Settings().withDefaults()
}

// interactive reports whether m is an interactive method: core's own
// classification (Method.Interactive), or what its registration declared.
func (s *Service) interactive(m Method) bool {
	if m.Interactive() {
		return true
	}
	spec, ok := s.methods[m]
	return ok && spec.Interactive
}

// knownMethod reports whether m is a method the service builds principals
// for: core's credential methods and the registered extension and compat
// methods.
func (s *Service) knownMethod(m Method) bool {
	switch m {
	case MethodSession, MethodSessionDelegated, MethodAPIKey:
		return true
	}
	_, ok := s.methods[m]
	return ok
}

// authDisabledPrincipal is §4.11's auth-off binding; until auth-off mode
// lands it binds nothing.
func (s *Service) authDisabledPrincipal(ClientInfo) (*Principal, bool) { return nil, false }

// journalLine appends r into b, the revoking batch, before it commits
// (§4.12). An Append failure never blocks the revocation: the batch records
// a gap instead (05-backup §5B.3.6).
func (s *Service) journalLine(ctx context.Context, b *storage.Batch, r journal.Revocation) {
	r.At = s.now().UTC()
	if err := s.d.Revocations.Append(ctx, b, r); err != nil {
		s.log.Warn("auth: revocation journal append failed; recording a gap", "kind", string(r.Kind), "err", err)
		if gerr := s.d.Revocations.Gap(b, r.At); gerr != nil {
			s.log.Error("auth: revocation journal gap failed", "kind", string(r.Kind), "err", gerr)
		}
	}
}

// actorUser returns actor's user ID, or ErrDenied when actor has none.
func actorUser(actor *Principal) (UserID, error) {
	if actor == nil || actor.User == nil || actor.User.ID == "" || actor.Perms == nil {
		return "", fmt.Errorf("%w: no authenticated actor", ErrDenied)
	}
	return actor.User.ID, nil
}

// mayManage checks that actor may act on owner's records: its own, or, for
// another user, core:users.manage through an interactive method and
// dominance over that user's roles (05-rbac §4.7 rule 5; SEC-4).
func (s *Service) mayManage(ctx context.Context, actor *Principal, owner UserID) error {
	self, err := actorUser(actor)
	if err != nil {
		return err
	}
	if owner == self {
		return nil
	}
	if !actor.Perms.Has(permUsersManage) {
		return fmt.Errorf("%w: %s is needed to act on another user", ErrDenied, permUsersManage)
	}
	if !s.interactive(actor.Method) {
		return fmt.Errorf("%w: acting on another user needs an interactive sign-in", ErrDenied)
	}
	target, err := s.d.Store.GetUser(ctx, owner)
	if err != nil {
		return err
	}
	if err := s.d.RBAC.Dominates(actor.Perms, target.Roles); err != nil {
		return fmt.Errorf("%w: %w", ErrDominance, err)
	}
	return nil
}

func ref(kind, id string) entity.Ref { return entity.Ref{Module: auditModule, Kind: kind, ID: id} }

// commitRetrying runs stage over a fresh batch and commits it Sync, again on
// storage.ErrConflict (a concurrent writer of a record stage read), up to
// conflictRetries times. stage re-reads everything it decides on.
func (s *Service) commitRetrying(ctx context.Context, stage func(b *storage.Batch) error) error {
	return storage.RetryOnConflict(ctx, conflictRetries, func(ctx context.Context) error {
		b := s.d.NewBatch()
		defer func() { _ = b.Close() }()
		if err := stage(b); err != nil {
			return err
		}
		return b.Commit(ctx, storage.Sync)
	})
}
