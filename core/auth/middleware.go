// file: core/auth/middleware.go
// version: 1.0.0
// guid: 9206232a-c4a5-4cca-be1f-569a0020e2c1
// last-edited: 2026-11-10

package auth

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// Authenticator inspects a request and returns an Assertion, or
// ErrNoCredential to let the next one try, or another error (05-authn §3.3).
// Any result other than ErrNoCredential ends authentication for the request:
// a presented-but-invalid credential never falls through to a weaker one. An
// authenticator never returns a Principal or a Checker (SEC2-16): stage 12
// builds the principal from the Assertion through PrincipalFor.
type Authenticator interface {
	Name() string
	Authenticate(r *http.Request) (*Assertion, error)
}

// Assertion is what an extension authenticator proves about a request.
type Assertion struct {
	User         UserID
	Scopes       []rbac.Permission // nil = the user's role permissions
	CredentialID string            // never a secret
	AuthTime     time.Time
	Owner        bool // honoured only for an extension registered with OwnerVerifier=true
}

// Order is an authenticator's relative position; core's API-key and session
// authenticators are fixed at OrderAPIKey and OrderSession. An extension's
// Order is above OrderAPIKey and not OrderSession (§4.2: the key wins).
type Order int

// Core's fixed authenticator positions (§3.3).
const (
	OrderAPIKey  Order = 100
	OrderSession Order = 200
)

// Extension is an identity extension, registered app-level at compile time
// (app.Options.Authenticators, 06 §6.4.5). It is the only place where
// Interactive, MayChangeCredentials or OwnerVerifier may be true.
type Extension struct {
	Authenticator Authenticator
	Spec          MethodSpec // Method "ext:<name>"
	Order         Order
	// OwnerSet reports whether an owner identity is configured; required iff
	// Spec.OwnerVerifier (§4.8, SEC4-14).
	OwnerSet func() bool
}

// extensionPrefix is the method prefix of every identity extension.
const extensionPrefix = "ext:"

// checkExtension is §3.3's registration rule for an identity extension.
func checkExtension(e Extension) error {
	name := strings.TrimPrefix(string(e.Spec.Method), extensionPrefix)
	switch {
	case e.Authenticator == nil:
		return fmt.Errorf("%w: extension %q has no authenticator", ErrMethodSpec, e.Spec.Method)
	case !strings.HasPrefix(string(e.Spec.Method), extensionPrefix) || name == "" || strings.ContainsAny(name, " :/"):
		return fmt.Errorf("%w: extension method %q is not ext:<name>", ErrMethodSpec, e.Spec.Method)
	case e.Spec.AppHandoff != nil:
		return fmt.Errorf("%w: extension %q may not carry an app hand-off", ErrMethodSpec, e.Spec.Method)
	case e.Spec.OwnerVerifier != (e.OwnerSet != nil):
		return fmt.Errorf("%w: extension %q: OwnerSet is required exactly when OwnerVerifier is set", ErrMethodSpec, e.Spec.Method)
	case e.Order <= OrderAPIKey:
		// An API key presented anywhere wins, replacing anything else (§4.2
		// item 2): no extension runs before the key authenticator.
		return fmt.Errorf("%w: extension %q has order %d; extensions run after the API-key authenticator (%d)",
			ErrMethodSpec, e.Spec.Method, e.Order, OrderAPIKey)
	case e.Order == OrderSession:
		return fmt.Errorf("%w: extension %q takes the session authenticator's order %d", ErrMethodSpec, e.Spec.Method, e.Order)
	}
	return nil
}

// stage is one authenticator of stage 12 in order: core's key and session
// authenticators or an extension.
type stage struct {
	order Order
	name  string
	run   func(r *http.Request, c ClientInfo) (*Principal, *Failure, error)
}

// stages returns stage 12's authenticators sorted by Order (stable).
func (s *Service) stages() []stage {
	out := []stage{
		{order: OrderAPIKey, name: "apikey", run: s.authenticateKey},
		{order: OrderSession, name: "session", run: s.authenticateSession},
	}
	for _, e := range s.extensions {
		out = append(out, stage{order: e.Order, name: e.Authenticator.Name(), run: s.extensionStage(e)})
	}
	slices.SortStableFunc(out, func(a, b stage) int { return cmp.Compare(a.order, b.order) })
	return out
}

// Middleware returns http-api stage 12 (05-http-api §4.2). It runs the
// authenticators in Order and stops at the first one that returns anything
// other than ErrNoCredential: a principal is bound (WithPrincipal, the rbac
// checker, the audit actor), or a Failure is recorded in the context. It
// writes no response except 503 auth_unavailable when the store cannot be
// read (SEC-12; never anonymous-with-access). With no user yet it binds
// nothing (§4.2 item 1): stage 13 then admits only Public and setup routes.
func (s *Service) Middleware() func(http.Handler) http.Handler {
	stages := s.stages()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			c := s.client(r)
			if p, ok := s.authDisabledPrincipal(c); ok {
				next.ServeHTTP(w, r.WithContext(s.bind(ctx, p, c)))
				return
			}
			if !s.HasUsers() {
				next.ServeHTTP(w, r)
				return
			}
			for _, st := range stages {
				p, fail, err := st.run(r, c)
				if err != nil {
					s.log.Error("auth: credential store unavailable", "authenticator", st.name, "err", err)
					s.writeUnavailable(w, r)
					return
				}
				if p != nil {
					ctx = s.bind(ctx, p, c)
					break
				}
				if fail != nil {
					// Recorded for stage 13, not audited: 05-rbac §7.1's
					// table (audit.CoreActions) names no action for a
					// refused credential, and §9 counts stage 12's outcomes
					// in mediacore_auth_attempts_total (F4-02b).
					ctx = withFailure(ctx, *fail)
					break
				}
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// bind binds p into ctx for every later stage.
func (s *Service) bind(ctx context.Context, p *Principal, c ClientInfo) context.Context {
	p.ClientAddr = c.Addr
	ctx = WithPrincipal(ctx, p)
	ctx = s.d.RBAC.Bind(ctx, p.Perms)
	a := audit.Actor{
		Method: string(p.Method), CredentialID: p.CredentialID,
		ClientAddr: addrString(c.Addr), UserAgent: c.UserAgent,
	}
	if p.User != nil {
		a.UserID, a.Username = string(p.User.ID), p.User.Username
	}
	return audit.WithActor(ctx, a)
}

func (s *Service) writeUnavailable(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Retry-After", "5")
	w.WriteHeader(http.StatusServiceUnavailable)
	if _, err := fmt.Fprintf(w, `{"type":"https://mediacore.dev/problems/auth_unavailable","title":"Authentication unavailable","status":503,"code":"auth_unavailable","instance":%q,"request_id":""}`, r.URL.Path); err != nil {
		// The client went away mid-answer; the status line is already sent.
		s.log.Debug("auth: 503 body not written", "err", err)
	}
}

// client is the request's ClientInfo: Deps.Client when the kernel wired
// httpapi's resolver, else the TCP peer as both address and peer, and HTTPS
// only from the connection: no forwarding header is believed without a
// trusted-proxy list (HEAD handlers/auth.go isHTTPSRequest believed
// X-Forwarded-Proto from any peer).
func (s *Service) client(r *http.Request) ClientInfo {
	if s.d.Client != nil {
		return s.d.Client(r)
	}
	c := ClientInfo{UserAgent: r.UserAgent(), HTTPS: r.TLS != nil}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if a, err := netip.ParseAddr(host); err == nil {
		c.Addr, c.Peer = a.Unmap(), a.Unmap()
	}
	return c
}

func addrString(a netip.Addr) string {
	if !a.IsValid() {
		return ""
	}
	return a.String()
}

// presented is one credential value found on a request.
type presented struct {
	raw      string
	cookie   bool // from the session cookie, not the Authorization header
	hostOnly bool // the cookie is the __Host- form
}

// credentials returns the Bearer value and the session cookie value of r,
// in that order, skipping absent ones. Exactly one cookie name is read, the
// one SessionCookie sets for c: on an HTTPS request only the __Host- name,
// so a plain-named cookie a sibling subdomain or a plain-http origin
// planted is never read (cookie tossing, session fixation).
func (s *Service) credentials(r *http.Request, c ClientInfo) []presented {
	var out []presented
	if h := r.Header.Get("Authorization"); h != "" {
		if scheme, val, ok := strings.Cut(h, " "); ok && strings.EqualFold(scheme, "Bearer") && strings.TrimSpace(val) != "" {
			out = append(out, presented{raw: strings.TrimSpace(val)})
		}
	}
	name, hostOnly := s.sessionCookieName(c)
	if ck, err := r.Cookie(name); err == nil && ck.Value != "" {
		out = append(out, presented{raw: ck.Value, cookie: true, hostOnly: hostOnly})
	}
	return out
}

// hostPrefix is the cookie-name prefix of a Secure session cookie (§4.3).
const hostPrefix = "__Host-"

// sessionCookieName is the session cookie's name for a request from c, and
// whether it is the __Host- form, which only a secure origin can set and
// only with Path=/ and no Domain (§4.3: the name gets the prefix when the
// cookie is Secure, i.e. on an HTTPS request, unless
// core.auth.session.cookie_host_prefix=false). Stage 12 reads exactly this
// name and SessionCookie writes exactly this name.
func (s *Service) sessionCookieName(c ClientInfo) (name string, hostOnly bool) {
	st := s.settings()
	if c.HTTPS && !st.NoHostPrefix {
		return hostPrefix + st.SessionCookie, true
	}
	return st.SessionCookie, false
}

// SessionCookie is the session cookie to set on the response to a request
// from c (§4.3): the one name stage 12 reads for c, HttpOnly,
// SameSite=Strict, Path=/, Secure when c.HTTPS. An empty token clears the
// cookie (the §4.3 cookie failure rule, logout). Coined by F4-02: the login,
// logout and stage 13 code that writes the cookie takes it from here, so the
// name written can never drift from the name read.
func (s *Service) SessionCookie(c ClientInfo, token string, expires time.Time) *http.Cookie {
	name, _ := s.sessionCookieName(c)
	ck := &http.Cookie{
		Name: name, Value: token, Path: "/",
		HttpOnly: true, Secure: c.HTTPS, SameSite: http.SameSiteStrictMode,
	}
	if token == "" {
		ck.MaxAge = -1
		return ck
	}
	ck.MaxAge = max(int(expires.Sub(s.now()).Seconds()), 0)
	if ck.MaxAge == 0 {
		ck.MaxAge = -1 // already expired: a zero MaxAge would mean a browser-session cookie
	}
	return ck
}

// isKey reports whether raw is shaped like an API key: native or imported.
func (s *Service) isKey(raw string) bool {
	return strings.HasPrefix(raw, string(KindAPIKey)) || hasImportedPrefix(s.opts.ImportedKeyPrefixes, raw)
}

// authenticateKey is core's API-key authenticator (Order 100): a key
// presented in the Authorization header or as the session cookie's value
// wins over everything after it (§4.2 item 2; HEAD middleware/auth.go
// APIKeyFromRequest). A key is taken from the cookie only when that cookie
// is the __Host- form: a plain-named cookie can be planted, and a key-shaped
// value in it is left to the session authenticator, which refuses it.
func (s *Service) authenticateKey(r *http.Request, c ClientInfo) (*Principal, *Failure, error) {
	var cred *presented
	for _, p := range s.credentials(r, c) {
		if p.cookie && !p.hostOnly {
			continue
		}
		if s.isKey(p.raw) {
			cred = &p
			break
		}
	}
	if cred == nil {
		return nil, nil, nil
	}
	fail := func(code string) (*Principal, *Failure, error) {
		return nil, &Failure{Code: code, Cookie: cred.cookie}, nil
	}
	if strings.HasPrefix(cred.raw, string(KindAPIKey)) {
		if err := ParseCredential(KindAPIKey, cred.raw); err != nil {
			return fail(CodeInvalidCredential)
		}
	}
	ctx := r.Context()
	k, err := s.d.Store.APIKeyByToken(ctx, cred.raw)
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrUndecodableRecord) {
		return fail(CodeInvalidCredential)
	}
	if err != nil {
		return nil, nil, err
	}
	now := s.now()
	if err := keyUsable(k, now); err != nil {
		return fail(failureCode(err))
	}
	u, err := s.d.Store.GetUser(ctx, k.Owner)
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrUndecodableRecord) {
		return fail(CodeInvalidCredential)
	}
	if err != nil {
		return nil, nil, err
	}
	p, err := s.PrincipalFor(ctx, u, MethodAPIKey, k.Scopes, u.CredentialEpoch)
	if err != nil {
		return fail(failureCode(err))
	}
	p.CredentialID, p.AuthTime = k.ID, k.CreatedAt
	s.touch(ctx, "k:"+k.ID, now, func(b *storage.Batch) error { return s.d.Store.TouchAPIKey(b, k.ID, now, c.Addr) })
	return p, nil, nil
}

// authenticateSession is core's session authenticator (Order 200): an mcs_
// token as Bearer or the session cookie (§4.3). An expired record is refused
// and deleted in the same request (prune on read until the sweep task, COR4-13).
func (s *Service) authenticateSession(r *http.Request, c ClientInfo) (*Principal, *Failure, error) {
	var cred *presented
	for _, p := range s.credentials(r, c) {
		if p.cookie || strings.HasPrefix(p.raw, string(KindSession)) {
			cred = &p
			break
		}
	}
	if cred == nil {
		return nil, nil, nil
	}
	fail := func(code string) (*Principal, *Failure, error) {
		return nil, &Failure{Code: code, Cookie: cred.cookie}, nil
	}
	if err := ParseCredential(KindSession, cred.raw); err != nil {
		return fail(CodeInvalidCredential)
	}
	ctx := r.Context()
	rec, err := s.d.Store.SessionByToken(ctx, cred.raw)
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrUndecodableRecord) {
		return fail(CodeInvalidCredential)
	}
	if err != nil {
		return nil, nil, err
	}
	now := s.now()
	if !now.Before(rec.ExpiresAt) {
		s.pruneSession(ctx, rec)
		return fail(CodeExpired)
	}
	u, err := s.d.Store.GetUser(ctx, rec.UserID)
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrUndecodableRecord) {
		return fail(CodeInvalidCredential)
	}
	if err != nil {
		return nil, nil, err
	}
	p, err := s.PrincipalFor(ctx, u, rec.Origin.Method(), nil, rec.CredentialEpoch)
	if err != nil {
		return fail(failureCode(err))
	}
	p.CredentialID, p.AuthTime = rec.PublicID, rec.CreatedAt
	if s.coalesce("s:"+rec.PublicID, now) {
		s.stageTouch(ctx, func(b *storage.Batch) error { return s.d.Store.TouchSession(b, rec.PublicID, now) })
	}
	return p, nil, nil
}

// extensionStage runs one identity extension: ErrNoCredential passes on, any
// other error is a failure, and an Assertion becomes a principal through
// PrincipalFor (the extension never decides what the caller may do).
func (s *Service) extensionStage(e Extension) func(*http.Request, ClientInfo) (*Principal, *Failure, error) {
	return func(r *http.Request, _ ClientInfo) (*Principal, *Failure, error) {
		a, err := e.Authenticator.Authenticate(r)
		if errors.Is(err, ErrNoCredential) {
			return nil, nil, nil
		}
		if errors.Is(err, ErrStoreUnavailable) {
			return nil, nil, err
		}
		if err != nil || a == nil || a.User == "" {
			return nil, &Failure{Code: failureCode(err)}, nil
		}
		ctx := r.Context()
		u, err := s.d.Store.GetUser(ctx, a.User)
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrUndecodableRecord) {
			return nil, &Failure{Code: CodeInvalidCredential}, nil
		}
		if err != nil {
			return nil, nil, err
		}
		p, err := s.PrincipalFor(ctx, u, e.Spec.Method, a.Scopes, u.CredentialEpoch)
		if err != nil {
			return nil, &Failure{Code: failureCode(err)}, nil
		}
		p.CredentialID, p.AuthTime = a.CredentialID, a.AuthTime
		if a.Owner && e.Spec.OwnerVerifier {
			p.Assurance |= AssuranceOwner
		}
		return p, nil, nil
	}
}

// failureCode maps a refusal to its stage 13 code (§6).
func failureCode(err error) string {
	switch {
	case errors.Is(err, ErrExpired):
		return CodeExpired
	case errors.Is(err, ErrRevoked), errors.Is(err, ErrStaleEpoch):
		return CodeRevoked
	case errors.Is(err, ErrInactiveUser):
		return CodeInactiveUser
	case errors.Is(err, ErrResetRequired):
		return CodeResetRequired
	}
	return CodeInvalidCredential
}

// pruneSession deletes an expired session record; best effort.
func (s *Service) pruneSession(ctx context.Context, rec *SessionRecord) {
	s.stageTouch(ctx, func(b *storage.Batch) error { return s.d.Store.DeleteSession(b, rec) })
}

// touch records a credential's last use at most once a minute per
// credential (§4.3, §4.4).
func (s *Service) touch(ctx context.Context, id string, now time.Time, stage func(*storage.Batch) error) {
	if s.coalesce(id, now) {
		s.stageTouch(ctx, stage)
	}
}

// stageTouch commits one advisory write NoSync. A conflict means a revoke,
// logout or other writer got there first: the touch is dropped, never
// retried over it (§3.4).
func (s *Service) stageTouch(ctx context.Context, stage func(*storage.Batch) error) {
	b := s.d.NewBatch()
	defer func() {
		if err := b.Close(); err != nil {
			s.log.Warn("auth: last-use batch not closed", "err", err)
		}
	}()
	if err := stage(b); err != nil {
		s.log.Warn("auth: last-use write not staged", "err", err)
		return
	}
	if err := b.Commit(ctx, storage.NoSync); err != nil && !errors.Is(err, storage.ErrConflict) {
		s.log.Warn("auth: last-use write failed", "err", err)
	}
}

// touchCoalesceWindow is how often one credential's last use is written.
const touchCoalesceWindow = time.Minute

// touchTableMax bounds the coalescer's memory; at the bound it starts over,
// which costs at most one extra write per credential.
const touchTableMax = 8192

// touchTable is the per-credential last-write time of the coalescer
// (instance state, never a package variable).
type touchTable struct {
	mu   sync.Mutex
	last map[string]time.Time
}

// coalesce reports whether id's last use should be written at now, and
// records it when so.
func (s *Service) coalesce(id string, now time.Time) bool {
	t := &s.touches
	t.mu.Lock()
	defer t.mu.Unlock()
	if prev, ok := t.last[id]; ok && now.Sub(prev) < touchCoalesceWindow {
		return false
	}
	if t.last == nil || len(t.last) >= touchTableMax {
		t.last = make(map[string]time.Time)
	}
	t.last[id] = now
	return true
}
