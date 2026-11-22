// file: internal/books/guest_policy_boot_helpers_test.go
// version: 0.1.0
// guid: c8b48dee-d74b-42c6-96aa-b398f4cdd851
// last-edited: 2026-11-22

package books

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
)

// Helpers of the guest-policy full-boot tests (F4-08 B10r). Every name starts with
// gp so a sibling step's helpers in this package cannot collide with them.

// The Cloudflare identifiers the real client validates (cfaccesspolicy.New):
// 32 hex characters, a policy id, a token with no spaces. Nothing here is real.
const (
	gpCFAccount = "0123456789abcdef0123456789abcdef"
	gpCFPolicy  = "11111111-2222-3333-4444-555555555555"
	gpCFToken   = "gp-test-token-not-a-secret"
)

// gpCF is a fake of the one Cloudflare endpoint the guest-policy client uses,
// GET and PUT of the guest Access policy. It keeps the policy as the client sends
// it back, so a removal is visible as an address that is gone, and it counts what
// it refused.
type gpCF struct {
	mu         sync.Mutex
	policy     map[string]any
	failPut    bool
	puts       int
	failedPuts int
	added      []string
	removed    []string
}

func newGPCF(include ...string) *gpCF {
	rules := make([]any, 0, len(include))
	for _, e := range include {
		rules = append(rules, map[string]any{"email": map[string]any{"email": e}})
	}
	return &gpCF{policy: map[string]any{
		"id": gpCFPolicy, "name": "audiobook-guests", "decision": "allow", "include": rules,
	}}
}

func gpEmails(policy map[string]any) []string {
	var out []string
	rules, _ := policy["include"].([]any)
	for _, r := range rules {
		m, _ := r.(map[string]any)
		inner, _ := m["email"].(map[string]any)
		if s, ok := inner["email"].(string); ok {
			out = append(out, strings.ToLower(s))
		}
	}
	slices.Sort(out)
	return out
}

// addresses is the sorted e-mail include list as it stands.
func (f *gpCF) addresses() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return gpEmails(f.policy)
}

func (f *gpCF) has(email string) bool { return slices.Contains(f.addresses(), email) }

func (f *gpCF) setFail(on bool) {
	f.mu.Lock()
	f.failPut = on
	f.mu.Unlock()
}

func (f *gpCF) failed() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.failedPuts
}

func (f *gpCF) removals() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.removed...)
}

func (f *gpCF) additions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.added...)
}

func (f *gpCF) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "result": f.policy})
	case http.MethodPut:
		if f.failPut {
			f.failedPuts++
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false,
				"errors": []any{map[string]any{"code": 10000, "message": "Authentication error"}}})
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		before, after := gpEmails(f.policy), gpEmails(body)
		for _, e := range after {
			if !slices.Contains(before, e) {
				f.added = append(f.added, e)
			}
		}
		for _, e := range before {
			if !slices.Contains(after, e) {
				f.removed = append(f.removed, e)
			}
		}
		f.puts++
		f.policy = body
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "result": body})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// gpInstallCF serves cf over TLS on a loopback port and makes http.DefaultTransport
// dial that port for Cloudflare's API host (and only that host). The guest-policy
// client is built from the environment-only settings with no transport of its own,
// so DefaultTransport is what it goes through; the replacement stays an
// *http.Transport because other packages clone DefaultTransport by that type. The
// restore is registered before any boot, so it runs after the app's own cleanups.
func gpInstallCF(t *testing.T, cf *gpCF) {
	t.Helper()
	srv := httptest.NewTLSServer(cf)
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	orig := http.DefaultTransport
	tr := orig.(*http.Transport).Clone()
	tr.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if host, _, _ := net.SplitHostPort(addr); host == "api.cloudflare.com" {
			// The test server's certificate names example.com.
			d := tls.Dialer{Config: &tls.Config{RootCAs: pool, ServerName: "example.com"}}
			return d.DialContext(ctx, "tcp", srv.Listener.Addr().String())
		}
		d := tls.Dialer{}
		return d.DialContext(ctx, network, addr)
	}
	http.DefaultTransport = tr
	t.Cleanup(func() {
		tr.CloseIdleConnections()
		http.DefaultTransport = orig
	})
}

// gpAllowAll is the permission set of a test-built administrator principal. Core's
// own checks run on it exactly as on a session's: it is the principal that is
// built here, never a core internal that is bypassed.
type gpAllowAll struct{}

func (gpAllowAll) Has(rbac.Permission) bool        { return true }
func (gpAllowAll) Satisfies(rbac.Requirement) bool { return true }
func (gpAllowAll) Confined() bool                  { return false }
func (gpAllowAll) Concrete() []rbac.Permission     { return nil }

// gpAdminCtx binds an interactive administrator principal for userID on ctx, the
// shape core's AccountAdmin writes check (ctxAdmin).
func gpAdminCtx(ctx context.Context, userID string) context.Context {
	return coreauth.WithPrincipal(ctx, &coreauth.Principal{
		User:         &coreauth.User{ID: coreauth.UserID(userID)},
		Method:       coreauth.MethodSession,
		CredentialID: "gp-test-session",
		Perms:        gpAllowAll{},
	})
}

// gpTap is the handles a wrapped books module saw in Register.
type gpTap struct {
	mu        sync.Mutex
	admin     coreauth.Admin
	lifecycle func(coreauth.Lifecycle) error
	// again is what a second RegisterLifecycle call returned, right after books'
	// own: ErrLifecycleRegistered when books registered one.
	again error
}

func (g *gpTap) adminHandle() coreauth.Admin {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.admin
}

// gpTapModule wraps the books module to hand a test core's AccountAdmin and to
// try a second Lifecycle registration after books' own.
type gpTapModule struct {
	module.Module
	tap *gpTap
}

func (g gpTapModule) Register(r *module.Registrar) error {
	if err := g.Module.Register(r); err != nil {
		return err
	}
	d := r.Deps()
	g.tap.mu.Lock()
	defer g.tap.mu.Unlock()
	g.tap.admin = d.AuthAdmin
	g.tap.lifecycle = d.AuthLifecycle
	if d.AuthLifecycle != nil {
		g.tap.again = d.AuthLifecycle(coreauth.Lifecycle{
			InviteAccepted: func(context.Context, coreauth.InviteAccepted) {},
			StatusChanged:  func(context.Context, coreauth.StatusChanged) {},
			EmailChanged:   func(context.Context, coreauth.EmailChanged) {},
			UserDeleted:    func(context.Context, coreauth.UserDeleted) {},
			UserMergedAway: func(context.Context, coreauth.UserMergedAway) {},
		})
	}
	return nil
}

// gpNoLifecycle wraps the books module so core hands it no lifecycle handle: the
// manifest no longer declares AuthLifecycle.
type gpNoLifecycle struct{ module.Module }

func (g gpNoLifecycle) Manifest() module.Manifest {
	m := g.Module.Manifest()
	m.AuthLifecycle = false
	return m
}

// gpDoJSON is idDo that also returns the decoded body, for the routes that answer a
// created object.
func gpDoJSON(t *testing.T, h http.Handler, method, path string, body any, ck *http.Cookie, out any) *httptest.ResponseRecorder {
	t.Helper()
	rec := idDo(t, h, method, path, body, ck)
	if out != nil && rec.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), out), rec.Body.String())
	}
	return rec
}
