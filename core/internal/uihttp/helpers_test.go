// file: core/internal/uihttp/helpers_test.go
// version: 1.0.1
// guid: e7a2b9c4-1d58-4f36-a0c3-6b9d4e8f2a15
// last-edited: 2026-11-11

package uihttp

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"go.opentelemetry.io/otel/attribute"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/rbac"
)

// perms is a rbac.Checker that holds nothing and is signed in.
type perms struct{}

func (perms) Has(rbac.Permission) bool { return false }
func (perms) Satisfies(r rbac.Requirement) bool {
	return r.Kind() == rbac.KindAuthenticated
}
func (perms) Confined() bool              { return false }
func (perms) Concrete() []rbac.Permission { return nil }

// confined is a checker that is not AnyAuthenticated: a Confined key
// (a worker or facade key, 05-rbac 4.10).
type confined struct{ perms }

func (confined) Satisfies(rbac.Requirement) bool { return false }
func (confined) Confined() bool                  { return true }

// confinedKey is an API key principal holding only Confining scopes.
func confinedKey() *auth.Principal {
	return &auth.Principal{Method: auth.MethodAPIKey, CredentialID: "key-robin", Perms: confined{}}
}

// session is a signed-in browser session.
func session() *auth.Principal {
	return &auth.Principal{Method: auth.MethodSession, CredentialID: "sess-barbara", Perms: perms{}}
}

func in[R any](p *auth.Principal, req R) *httpapi.In[R] {
	return &httpapi.In[R]{Req: req, Principal: p}
}

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

// newRouter is a router with no auth service, as every router at this base is.
func newRouter() *httpapi.Router {
	return httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{Log: discard()})
}

func routes(r *httpapi.Router) []httpapi.RouteInfo {
	var out []httpapi.RouteInfo
	for ri := range r.Routes() {
		out = append(out, ri)
	}
	return out
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

// lineLog is a logger writing to a buffer, for asserting what a handler logs.
type lineLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lineLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lineLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func capture() (*slog.Logger, *lineLog) {
	l := &lineLog{}
	return slog.New(slog.NewTextHandler(l, &slog.HandlerOptions{Level: slog.LevelDebug})), l
}

// seriesOf returns each series of family as its label set, with its value.
func seriesOf(t testing.TB, g prometheus.Gatherer, family string) []map[string]string {
	t.Helper()
	mfs, err := g.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]string
	for _, mf := range mfs {
		if mf.GetName() != family {
			continue
		}
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			labels["_value"] = trimFloat(value(m))
			out = append(out, labels)
		}
	}
	return out
}

func value(m *dto.Metric) float64 {
	if m.GetCounter() != nil {
		return m.GetCounter().GetValue()
	}
	return 0
}

func trimFloat(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// sample is the value of the series of family whose labels (ignoring the value
// pseudo-label) equal want, and false when there is none.
func sample(t testing.TB, g prometheus.Gatherer, family string, want map[string]string) (string, bool) {
	t.Helper()
next:
	for _, s := range seriesOf(t, g, family) {
		if len(s) != len(want)+1 {
			continue
		}
		for k, v := range want {
			if s[k] != v {
				continue next
			}
		}
		return s["_value"], true
	}
	return "", false
}

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

// post serves a POST of body with content type ct through h.
func post(h http.Handler, path, ct, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	h.ServeHTTP(rec, req)
	return rec
}
