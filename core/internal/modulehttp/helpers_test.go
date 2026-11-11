// file: core/internal/modulehttp/helpers_test.go
// version: 1.0.0
// guid: 9ba39148-db7c-44fc-9856-a6a70311b0ac
// last-edited: 2026-11-10

package modulehttp

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"go.opentelemetry.io/otel/attribute"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/rbac"
)

// fakeView is a ModulesView over a fixed snapshot.
type fakeView struct {
	mu    sync.Mutex
	snap  Snapshot
	err   error
	calls int
}

func (f *fakeView) Snapshot(context.Context) (Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.snap, f.err
}

// perms is a rbac.Checker over a fixed set. authed says whether the holder
// satisfies rbac.AnyAuthenticated (a confined key does not).
type perms struct {
	has    map[rbac.Permission]bool
	authed bool
}

func (p perms) Has(s rbac.Permission) bool { return p.has[s] }
func (p perms) Satisfies(r rbac.Requirement) bool {
	if r.Kind() == rbac.KindAuthenticated {
		return p.authed
	}
	return false
}
func (p perms) Confined() bool              { return !p.authed }
func (p perms) Concrete() []rbac.Permission { return nil }

// batman is a signed-in session principal holding the given permissions.
func session(held ...rbac.Permission) *auth.Principal {
	h := map[rbac.Permission]bool{}
	for _, s := range held {
		h[s] = true
	}
	return &auth.Principal{Method: auth.MethodSession, CredentialID: "sess-bruce", Perms: perms{has: h, authed: true}}
}

// admin holds core:admin; user holds nothing but is signed in.
func admin() *auth.Principal { return session(rbac.CoreAdmin) }
func user() *auth.Principal  { return session() }

// confinedKey is an API key principal that does not satisfy AnyAuthenticated.
func confinedKey() *auth.Principal {
	return &auth.Principal{Method: auth.MethodAPIKey, CredentialID: "key-robin", Perms: perms{authed: false}}
}

// in is the decoded request a handler receives for p.
func in[R any](p *auth.Principal, req R) *httpapi.In[R] {
	return &httpapi.In[R]{Req: req, Principal: p}
}

// gotham is the snapshot every manifest test starts from: two enabled modules
// and one disabled one, deliberately out of ID order.
func gotham() Snapshot {
	return Snapshot{
		CoreVersion:     "0.4.0",
		App:             App{ID: "gcpd", DisplayName: "Gotham City Police", Version: "2.0.0", UIBuildID: "b3f1c2"},
		RestartRequired: true,
		Modules: []Module{
			{ID: "robin", Version: "1.0.0", DisplayName: "Robin", Enabled: true,
				UI: UI{Pages: []string{"patrol"}, Nav: []string{"patrol"}}},
			{ID: "joker", Version: "0.1.0", DisplayName: "Joker", Enabled: false},
			{ID: "batman", Version: "1.12.0", DisplayName: "Batman", Enabled: true,
				UI: UI{Pages: []string{"cave", "gadget"}, SettingsPanels: []string{"cave"}, SetupSteps: []string{"roof-signal"}}},
		},
	}
}

// uiScope is the core "ui" scope of a fresh Provider, with every label key the
// ui families use.
func uiScope(t testing.TB) (*metrics.Provider, *metrics.Scope) {
	t.Helper()
	p, err := metrics.New(context.Background(), metrics.Options{AppID: "gotham", Prometheus: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	sc, err := p.Core("ui", attribute.Key("outcome"), "kind", "module", "directive")
	if err != nil {
		t.Fatal(err)
	}
	return p, sc
}

// newRouter is a router with no auth service, as every router at this base is.
func newRouter() *httpapi.Router {
	return httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{Log: slog.New(slog.DiscardHandler)})
}

// asPrincipal serves h with p bound into every request's context, which is
// what stage 12 does once core authentication exists (F4-08); a nil p serves
// the request anonymously.
func asPrincipal(h http.Handler, p *auth.Principal) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
	})
}

// get serves GET path through h and returns the recorded response.
func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// sample is the value of family's series whose labels equal want, and false
// when there is none.
func sample(t testing.TB, g prometheus.Gatherer, family string, want map[string]string) (float64, bool) {
	t.Helper()
	mfs, err := g.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != family {
			continue
		}
	next:
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			if len(labels) != len(want) {
				continue
			}
			for k, v := range want {
				if labels[k] != v {
					continue next
				}
			}
			return counterValue(m), true
		}
	}
	return 0, false
}

func counterValue(m *dto.Metric) float64 {
	if m.GetCounter() != nil {
		return m.GetCounter().GetValue()
	}
	return 0
}

// wantStatus fails t unless err is an *httpapi.Error with status and code.
func wantStatus(t testing.TB, err error, status int, code string) {
	t.Helper()
	he, ok := errors.AsType[*httpapi.Error](err)
	if !ok {
		t.Errorf("err = %v (%T), want an *httpapi.Error %d %s", err, err, status, code)
		return
	}
	if he.Status != status || he.Code != code {
		t.Errorf("err = %d %s (%v), want %d %s", he.Status, he.Code, err, status, code)
	}
}

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

func anyAuthenticated() rbac.Requirement { return rbac.AnyAuthenticated }
