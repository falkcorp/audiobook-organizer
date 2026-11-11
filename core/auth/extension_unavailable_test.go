// file: core/auth/extension_unavailable_test.go
// version: 1.0.0
// guid: 7e2b9c41-3a58-4d06-9f1b-c8a4e0d63b75
// last-edited: 2026-11-11

package auth_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/rbac"
)

// sequenceExt is an identity extension named name that answers with a fixed
// result and counts its calls.
type sequenceExt struct {
	name  string
	a     *auth.Assertion
	err   error
	calls *int
}

func (e sequenceExt) Name() string { return e.name }
func (e sequenceExt) Authenticate(*http.Request) (*auth.Assertion, error) {
	*e.calls++
	return e.a, e.err
}

// extScope is a metrics scope that keeps the extension label.
func extScope(t *testing.T) (*metrics.Provider, *metrics.Scope) {
	t.Helper()
	p, err := metrics.New(context.Background(), metrics.Options{AppID: "gotham", Prometheus: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	sc, err := p.Core("auth", attribute.Key("method"), "outcome", "surface", "imported", "reason", "extension")
	if err != nil {
		t.Fatal(err)
	}
	return p, sc
}

// 05-authn §6, "Extension authenticator internal error": an extension that
// reports ErrExtensionUnavailable (its IdP's keys could not be fetched)
// opens to the next method, warns and counts mediacore_auth_extension_errors_total;
// a presented assertion that verifies false (any other error) still ends
// authentication with a refusal, and a store failure is still 503.
func TestStage12_ExtensionUnavailableOpensToNextMethod(t *testing.T) {
	f := newFx(t)
	barbara := f.user("barbara", rbac.RoleViewer)
	is := f.issue(t, barbara)
	p, sc := extScope(t)
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))

	build := func(first, second sequenceExt) *auth.Service {
		t.Helper()
		mk := func(e sequenceExt, order auth.Order) auth.Extension {
			return auth.Extension{Authenticator: e, Order: order, Spec: auth.MethodSpec{Method: auth.Method("ext:" + e.name), Interactive: true}}
		}
		svc, err := auth.New(context.Background(), auth.Options{ImportedKeyPrefixes: []string{"abk_"}}, auth.Deps{
			Store: f.st, NewBatch: f.st.NewBatch, RBAC: f.rb, Audit: f.audit, Revocations: f.jrnl, Clock: f.clock,
			Extensions: []auth.Extension{mk(first, 150), mk(second, 160)}, Metrics: sc, Log: log,
		})
		if err != nil {
			t.Fatal(err)
		}
		return svc
	}
	var c1, c2 int

	t.Run("an outage opens to the next extension and then the session", func(t *testing.T) {
		svc := build(
			sequenceExt{name: "wayne", err: fmt.Errorf("fetch keys from https://keys.example.invalid: %w", auth.ErrExtensionUnavailable), calls: &c1},
			sequenceExt{name: "kane", err: auth.ErrNoCredential, calls: &c2})
		_, got := serve(t, svc, request("", is.Token))
		if got.p == nil || got.p.Method != auth.MethodSession || got.failed {
			t.Fatalf("the session after an outage: principal %+v failure %+v", got.p, got.fail)
		}
		if c1 != 1 || c2 != 1 {
			t.Fatalf("authenticators run: first %d second %d, want 1 and 1", c1, c2)
		}
		if n := sample(t, p.Gatherer(), "mediacore_auth_extension_errors_total", map[string]string{"extension": "wayne"}); n != 1 {
			t.Fatalf("extension_errors_total{extension=wayne} = %v, want 1", n)
		}
		if !strings.Contains(logs.String(), "identity extension could not decide") || !strings.Contains(logs.String(), "extension=wayne") {
			t.Fatalf("no warning logged: %s", logs.String())
		}
		if strings.Contains(logs.String(), "keys.example.invalid") {
			t.Fatalf("the error text, which quotes a URL, reached the log: %s", logs.String())
		}
	})

	t.Run("an outage grants nothing: no session, anonymous", func(t *testing.T) {
		svc := build(
			sequenceExt{name: "wayne", err: auth.ErrExtensionUnavailable, calls: &c1},
			sequenceExt{name: "kane", err: auth.ErrNoCredential, calls: &c2})
		_, got := serve(t, svc, request("", ""))
		if got.p != nil || got.failed {
			t.Fatalf("an outage with no other credential bound %+v / failure %+v, want anonymous", got.p, got.fail)
		}
	})

	t.Run("a later extension's assertion is still honoured after an outage", func(t *testing.T) {
		svc := build(
			sequenceExt{name: "wayne", err: auth.ErrExtensionUnavailable, calls: &c1},
			sequenceExt{name: "kane", a: &auth.Assertion{User: barbara.ID, CredentialID: "kane-1"}, calls: &c2})
		_, got := serve(t, svc, request("", ""))
		if got.p == nil || got.p.Method != "ext:kane" {
			t.Fatalf("principal %+v, want ext:kane", got.p)
		}
	})

	t.Run("a presented assertion that verifies false is a refusal, not an outage", func(t *testing.T) {
		svc := build(
			sequenceExt{name: "wayne", err: auth.ErrInvalidCredentials, calls: &c1},
			sequenceExt{name: "kane", a: &auth.Assertion{User: barbara.ID}, calls: &c2})
		c2 = 0
		_, got := serve(t, svc, request("", is.Token))
		if got.p != nil || !got.failed || got.fail.Code != auth.CodeInvalidCredential {
			t.Fatalf("a bad assertion: principal %+v failure %+v, want invalid_credential and no principal", got.p, got.fail)
		}
		if c2 != 0 {
			t.Fatalf("the next authenticator ran after a refusal (%d calls)", c2)
		}
	})

	t.Run("an unavailable store still answers 503", func(t *testing.T) {
		svc := build(
			sequenceExt{name: "wayne", err: errors.Join(auth.ErrStoreUnavailable, auth.ErrExtensionUnavailable), calls: &c1},
			sequenceExt{name: "kane", err: auth.ErrNoCredential, calls: &c2})
		w, got := serve(t, svc, request("", ""))
		if w.Code != http.StatusServiceUnavailable || got.called {
			t.Fatalf("status %d, handler called %v, want 503 and not called", w.Code, got.called)
		}
	})
}
