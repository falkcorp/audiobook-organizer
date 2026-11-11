// file: core/internal/integrationhttp/routes_test.go
// version: 1.3.0
// guid: c8a14f73-2d6b-4e90-b5a7-91f0e3d2864b
// last-edited: 2026-11-11

package integrationhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/integrationstest"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// ---- helpers --------------------------------------------------------------

func mustJSON(t testing.TB, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	return string(b)
}

// errBlob is everything an answer built from err can show a client or a log:
// its Error() text and its serialized members.
func errBlob(t testing.TB, err error) string {
	t.Helper()
	if err == nil {
		return ""
	}
	blob := err.Error()
	if he, ok := errors.AsType[*httpapi.Error](err); ok {
		blob += " " + he.Detail + " " + mustJSON(t, he.Fields)
	}
	return blob
}

func assertNoLeak(t testing.TB, what, blob string, leaks ...string) {
	t.Helper()
	for _, l := range leaks {
		if strings.Contains(blob, l) {
			t.Errorf("%s leaks %q: %s", what, l, blob)
		}
	}
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

// replaceOf is body as a complete PUT body: every replace-required field is
// present (limits {} when body sets none, as GET answers).
func replaceOf(body IntegrationInstanceBody) IntegrationInstanceReplace {
	r := IntegrationInstanceReplace{
		ID: body.ID, Adapter: body.Adapter, Name: &body.Name, Enabled: body.Enabled, Priority: &body.Priority, Local: &body.Local,
		Settings: body.Settings, Secrets: body.Secrets, Limits: body.Limits, Version: body.Version,
	}
	if r.Limits == nil {
		r.Limits = &IntegrationLimits{}
	}
	return r
}

// update replaces an instance through the handler.
func (e *env) update(ctx context.Context, kind, id string, body IntegrationInstanceBody) (IntegrationInstance, error) {
	req := updateReq{Kind: kind, ID: id, Body: replaceOf(body)}
	if err := req.Validate(); err != nil {
		return IntegrationInstance{}, err
	}
	return e.api.updateInstance(ctx, in(ctx, req))
}

// testScoped runs the instance-scoped test through the handler.
func (e *env) testScoped(ctx context.Context, kind, id string, body IntegrationInstanceBody) (IntegrationTestResult, error) {
	req := testInstanceReq{Kind: kind, ID: id, Body: body}
	if err := req.Validate(); err != nil {
		return IntegrationTestResult{}, err
	}
	return e.api.testInstance(ctx, in(ctx, req))
}

// testUnscoped runs the unscoped test through the handler.
func (e *env) testUnscoped(ctx context.Context, kind string, body IntegrationInstanceBody) (IntegrationTestResult, error) {
	req := testNewReq{Kind: kind, Body: body}
	if err := req.Validate(); err != nil {
		return IntegrationTestResult{}, err
	}
	return e.api.testNew(ctx, in(ctx, req))
}

// ---- the route table ------------------------------------------------------

var wantRoutes = []struct {
	method, path, name, perm string
	expensive                bool
}{
	{http.MethodGet, "/api/core/v1/integrations/adapters", "listIntegrationAdapters", "core:integrations.read", false},
	{http.MethodGet, "/api/core/v1/integrations/{kind}/instances", "listIntegrationInstances", "core:integrations.read", false},
	{http.MethodPost, "/api/core/v1/integrations/{kind}/instances", "createIntegrationInstance", "core:integrations.manage", false},
	{http.MethodGet, "/api/core/v1/integrations/{kind}/instances/{id}", "getIntegrationInstance", "core:integrations.read", false},
	{http.MethodPut, "/api/core/v1/integrations/{kind}/instances/{id}", "updateIntegrationInstance", "core:integrations.manage", false},
	{http.MethodDelete, "/api/core/v1/integrations/{kind}/instances/{id}", "deleteIntegrationInstance", "core:integrations.manage", false},
	{http.MethodPost, "/api/core/v1/integrations/{kind}/test", "testIntegration", "core:integrations.manage", true},
	{http.MethodPost, "/api/core/v1/integrations/{kind}/instances/{id}/test", "testIntegrationInstance", "core:integrations.manage", true},
	{http.MethodGet, "/api/core/v1/integrations/{kind}/instances/{id}/health", "getIntegrationInstanceHealth", "core:integrations.read", true},
	{http.MethodDelete, "/api/core/v1/integrations/{kind}/instances/{id}/hold", "releaseIntegrationInstanceHold", "core:integrations.manage", false},
}

func registered(t *testing.T) *httpapi.Router {
	t.Helper()
	r := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{Log: slog.New(slog.DiscardHandler)})
	Register(r.CoreGroup(), nil, nil, nil)
	return r
}

// TestRegister_RouteTable pins M1.3's HTTP table: each route's method, path,
// unique lowerCamel name and the permission it declares, all on the core
// group with no guard and a bounded body where there is one (STOP condition
// 4: no route registers with a zero Perm or outside CoreGroup).
func TestRegister_RouteTable(t *testing.T) {
	r := registered(t)
	var got []httpapi.RouteInfo
	for ri := range r.Routes() {
		got = append(got, ri)
	}
	if len(got) != len(wantRoutes) {
		t.Fatalf("%d routes registered, want %d: %+v", len(got), len(wantRoutes), got)
	}
	names := map[string]bool{}
	for i, w := range wantRoutes {
		ri := got[i]
		if ri.Owner != "core" || ri.Kind != httpapi.RouteKindNative || ri.Method != w.method || ri.Pattern != w.path || ri.Name != w.name {
			t.Errorf("route %d = %s %s %s (%s, kind %d), want %s %s %s on core", i, ri.Owner, ri.Method, ri.Pattern, ri.Name, ri.Kind, "core", w.method, w.path)
		}
		if ri.Perm.IsZero() || ri.Perm.String() != w.perm {
			t.Errorf("%s requires %s, want %s", w.name, ri.Perm, w.perm)
		}
		if ri.Guards != (httpapi.Guards{}) {
			t.Errorf("%s has guards %+v: ProtectAdmin is the Registry's", w.name, ri.Guards)
		}
		wantRate := httpapi.RateDefault
		if w.expensive {
			wantRate = httpapi.RateExpensive
		}
		if ri.RateClass != wantRate {
			t.Errorf("%s rate class = %d, want %d", w.name, ri.RateClass, wantRate)
		}
		if names[ri.Name] {
			t.Errorf("route name %s is used twice", ri.Name)
		}
		names[ri.Name] = true
	}
}

// TestRegister_BuildFailsOnlyForWantOfAuth: until F4-08 a router refuses
// every non-Public native route with ErrNoAuthService, so no handler can be
// built from one here (the handler tests below call the handlers directly).
// What Build can still check is everything else about the registration: a
// malformed shape, a body or response schema httpapi cannot describe, a
// non-snake_case name, a duplicate or a pattern conflict would each be a
// further error.
func TestRegister_BuildFailsOnlyForWantOfAuth(t *testing.T) {
	_, err := registered(t).Build()
	if err == nil {
		t.Fatal("Build succeeded without an auth service")
	}
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		t.Fatalf("Build error %T does not join its problems", err)
	}
	problems := joined.Unwrap()
	if len(problems) != len(wantRoutes) {
		t.Errorf("Build reported %d problems, want %d (one ErrNoAuthService per route): %v", len(problems), len(wantRoutes), err)
	}
	for _, p := range problems {
		if !errors.Is(p, httpapi.ErrNoAuthService) {
			t.Errorf("Build reported %v, want only ErrNoAuthService", p)
		}
	}
}

// ---- the permission and 401 tables (stage 13: F4-08) ----------------------

// TestIntegrationHTTP_ReadNeedsIntegrationsRead: every read route refuses a
// principal without core:integrations.read (403) and answers 401 with no
// principal. Route permission (stage 13) arrives at F4-08, so each cell skips;
// F7-09, which mounts the package, un-skips them.
func TestIntegrationHTTP_ReadNeedsIntegrationsRead(t *testing.T) {
	for _, c := range []struct{ name, method, path string }{
		{"adapters", http.MethodGet, "/integrations/adapters"},
		{"list", http.MethodGet, "/integrations/notify/instances"},
		{"get", http.MethodGet, "/integrations/notify/instances/gcpd-roof"},
		{"health", http.MethodGet, "/integrations/notify/instances/gcpd-roof/health"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Skip("needs stage 13: F4-08")
		})
	}
}

// TestIntegrationHTTP_WritesNeedIntegrationsManage: every write route,
// including the two test routes and the hold route, refuses a principal
// without core:integrations.manage (403) and answers 401 with no principal; a
// hold-route refusal calls no ReleaseHold. Skipped until stage 13 (F4-08).
func TestIntegrationHTTP_WritesNeedIntegrationsManage(t *testing.T) {
	for _, c := range []struct {
		name, method, path string
		holdUntouched      bool
	}{
		{"create", http.MethodPost, "/integrations/notify/instances", false},
		{"update", http.MethodPut, "/integrations/notify/instances/gcpd-roof", false},
		{"delete", http.MethodDelete, "/integrations/notify/instances/gcpd-roof", false},
		{"test", http.MethodPost, "/integrations/notify/test", false},
		{"instanceTest", http.MethodPost, "/integrations/notify/instances/gcpd-roof/test", false},
		{"hold", http.MethodDelete, "/integrations/notify/instances/gcpd-roof/hold", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Skip("needs stage 13: F4-08")
		})
	}
}

// ---- I1: no stored secret to a new origin ---------------------------------

func TestIntegrationHTTP_InstanceTestChangedOriginSendsNoStoredSecret(t *testing.T) {
	e := newEnv(t)
	ctx := managerCtx()
	e.create(t, ctx, kindNotify, signalBody(roofID, "http://192.0.2.20/", "alfred-1"))
	const stored, moved = "http://192.0.2.20/", "http://192.0.2.99/"

	// A body that moves the origin and omits the secret sends none.
	res, err := e.testScoped(ctx, kindNotify, roofID, signalBody("", moved, ""))
	if err != nil || res.OK {
		t.Errorf("a test at a new origin without a secret = %+v, %v", res, err)
	}
	// An echoed mask is not a secret either.
	if res, err := e.testScoped(ctx, kindNotify, roofID, signalBody("", moved, "********")); err != nil || res.OK {
		t.Errorf("a mask counted as a secret: %+v, %v", res, err)
	}
	for _, s := range e.out.Sent() {
		if s.Credential == "alfred-1" {
			t.Fatalf("the stored secret was sent by a test: %+v", s)
		}
	}
	// The same body with the origin unchanged uses the stored secret.
	if res, err := e.testScoped(ctx, kindNotify, roofID, signalBody("", stored, "")); err != nil || !res.OK {
		t.Errorf("a test at the stored origin = %+v, %v", res, err)
	}
	if got := e.credentialsSentTo(stored); !slices.Equal(got, []string{"alfred-1"}) {
		t.Errorf("credentials sent to the stored origin = %v", got)
	}
	// A secret the body re-supplies is the only one sent to the new origin.
	if res, err := e.testScoped(ctx, kindNotify, roofID, signalBody("", moved, "alfred-2")); err != nil || !res.OK {
		t.Errorf("a test at a new origin with a secret = %+v, %v", res, err)
	}
	if got := e.credentialsSentTo(moved); !slices.Equal(got, []string{"alfred-2"}) {
		t.Errorf("credentials sent to the new origin = %v", got)
	}
	if e.secretValue(t, kindNotify, roofID, "password") != "alfred-1" || e.get(t, ctx, kindNotify, roofID).Version != 1 {
		t.Error("a test changed the stored instance")
	}
}

func TestIntegrationHTTP_UnscopedTestUsesOnlyBody(t *testing.T) {
	e := newEnv(t)
	ctx := managerCtx()
	e.create(t, ctx, kindNotify, signalBody(roofID, "http://192.0.2.20/", "alfred-1"))

	// Naming the stored instance in the body does not scope the test.
	res, err := e.testUnscoped(ctx, kindNotify, signalBody(roofID, "http://192.0.2.20/", ""))
	if err != nil || res.OK {
		t.Errorf("an unscoped test with no secret = %+v, %v", res, err)
	}
	for _, s := range e.out.Sent() {
		if s.Credential != "" {
			t.Fatalf("an unscoped test sent %+v", s)
		}
	}
	if res, err := e.testUnscoped(ctx, kindNotify, signalBody("", "http://192.0.2.98/", "alfred-3")); err != nil || !res.OK {
		t.Errorf("an unscoped test with a secret = %+v, %v", res, err)
	}
	if got := e.credentialsSentTo("http://192.0.2.98/"); !slices.Equal(got, []string{"alfred-3"}) {
		t.Errorf("credentials sent = %v", got)
	}
	if e.get(t, ctx, kindNotify, roofID).Version != 1 {
		t.Error("an unscoped test persisted something")
	}
}

// ---- I2, I3: no secret leaves; echoed masks are not re-supply ------------

func TestIntegrationHTTP_NoSecretInResponses(t *testing.T) {
	e := newEnv(t)
	e.fake.TestFn = func(_ context.Context, i *integrationstest.FakeInstance) error {
		return &integrations.Error{
			Ref: i.Config.Ref, Op: "caps", Class: integrations.ClassAuth, Status: http.StatusUnauthorized,
			Detail: "<html>The Joker's plan</html>", Err: errors.New("remote said: the Joker's plan"),
		}
	}
	e.reg = e.registry(t)
	e.api.reg = e.reg
	ctx := adminCtx()
	leaks := []string{"alfred-secret-1", "alfred-secret-2", "integrations.notify." + roofID + ".password", `"secret"`, "Joker"}

	created := e.create(t, ctx, kindNotify, signalBody(roofID, "http://192.0.2.20/", "alfred-secret-1"))
	assertNoLeak(t, "create", mustJSON(t, created), leaks...)
	if !created.SecretsSet["password"] {
		t.Errorf("the create does not say the password is set: %+v", created.SecretsSet)
	}
	if _, ok := created.Settings["password"]; ok {
		t.Errorf("the password field is in settings: %+v", created.Settings)
	}

	got := e.get(t, ctx, kindNotify, roofID)
	assertNoLeak(t, "get", mustJSON(t, got), leaks...)

	list, err := e.api.listInstances(ctx, in(ctx, listInstancesReq{Kind: kindNotify, Limit: 50}))
	if err != nil {
		t.Fatal(err)
	}
	assertNoLeak(t, "list", mustJSON(t, list), leaks...)

	body := signalBody("", "http://192.0.2.20/", "alfred-secret-2")
	body.Version = got.Version
	updated, err := e.update(ctx, kindNotify, roofID, body)
	if err != nil {
		t.Fatal(err)
	}
	assertNoLeak(t, "update", mustJSON(t, updated), leaks...)

	res, err := e.testScoped(ctx, kindNotify, roofID, signalBody("", "http://192.0.2.20/", ""))
	if err != nil || res.OK || res.Class != "auth" || res.Status != http.StatusUnauthorized {
		t.Fatalf("test = %+v, %v", res, err)
	}
	assertNoLeak(t, "test", mustJSON(t, res), leaks...)

	h, err := e.api.getHealth(ctx, in(ctx, instanceReq{Kind: kindNotify, ID: roofID}))
	if err != nil {
		t.Fatal(err)
	}
	assertNoLeak(t, "health", mustJSON(t, h), leaks...)

	// A credential pasted into a URL field, or a URL with userinfo, is a 422
	// that quotes nothing.
	for _, bad := range []string{"batman:hunter2", "http://batman:hunter2@192.0.2.20/"} {
		_, err := e.api.createInstance(ctx, in(ctx, createReq{Kind: kindNotify, Body: signalBody("riddler", bad, "")}))
		wantStatus(t, err, http.StatusUnprocessableEntity, "validation_failed")
		assertNoLeak(t, "a 422", errBlob(t, err), "hunter2")
	}
}

func TestIntegrationHTTP_EchoedMaskKeepsStoredSecret(t *testing.T) {
	e := newEnv(t)
	ctx := managerCtx()
	e.create(t, ctx, kindNotify, signalBody(roofID, "http://192.0.2.20/", "alfred-1"))
	for _, echo := range []string{"", "********", "[REDACTED]", `{"secret":"integrations.notify.gcpd-roof.password"}`} {
		body := signalBody("", "http://192.0.2.20/", "")
		body.Secrets = map[string]string{"password": echo}
		body.Version = e.get(t, ctx, kindNotify, roofID).Version
		if _, err := e.update(ctx, kindNotify, roofID, body); err != nil {
			t.Fatalf("update with %q: %v", echo, err)
		}
		if got := e.secretValue(t, kindNotify, roofID, "password"); got != "alfred-1" {
			t.Errorf("an echoed %q replaced the stored secret with %q", echo, got)
		}
	}
}

// ---- I4: statuses ---------------------------------------------------------

func TestIntegrationHTTP_StatusesFollowM1_5(t *testing.T) {
	e := newEnv(t)
	ctx := managerCtx()

	t.Run("422 nothing written", func(t *testing.T) {
		bad := signalBody("joker", "http://192.0.2.20/", "")
		bad.Settings["channel"] = 5
		_, err := e.api.createInstance(ctx, in(ctx, createReq{Kind: kindNotify, Body: bad}))
		wantStatus(t, err, http.StatusUnprocessableEntity, "validation_failed")
		if he, _ := errors.AsType[*httpapi.Error](err); he == nil || he.Fields[0].Code != "settings_schema" {
			t.Errorf("a schema failure = %v", err)
		}
		unknown := signalBody("joker", "", "")
		unknown.Settings["gas"] = true
		_, err = e.api.createInstance(ctx, in(ctx, createReq{Kind: kindNotify, Body: unknown}))
		wantStatus(t, err, http.StatusUnprocessableEntity, "validation_failed")
		wrongKind := signalBody("joker", "", "")
		wrongKind.Adapter = string(oracle)
		_, err = e.api.createInstance(ctx, in(ctx, createReq{Kind: kindNotify, Body: wrongKind}))
		wantStatus(t, err, http.StatusUnprocessableEntity, "validation_failed")
		missing := signalBody("joker", "", "")
		missing.Adapter = "hello.lazarus"
		_, err = e.api.createInstance(ctx, in(ctx, createReq{Kind: kindNotify, Body: missing}))
		wantStatus(t, err, http.StatusUnprocessableEntity, "validation_failed")
		if _, err := e.api.getInstance(ctx, in(ctx, instanceReq{Kind: kindNotify, ID: "joker"})); err == nil {
			t.Error("a refused create stored the instance")
		}
		if got := e.actions(t); len(got) != 0 {
			t.Errorf("a refused create audited %v", got)
		}
	})

	created := e.create(t, ctx, kindNotify, signalBody(roofID, "http://192.0.2.20/", "alfred-1"))

	t.Run("409 version conflict", func(t *testing.T) {
		body := signalBody("", "http://192.0.2.20/", "")
		body.Version = created.Version + 5
		_, err := e.update(ctx, kindNotify, roofID, body)
		wantStatus(t, err, http.StatusConflict, "version_conflict")
		_, err = e.api.createInstance(ctx, in(ctx, createReq{Kind: kindNotify, Body: signalBody(roofID, "", "")}))
		wantStatus(t, err, http.StatusConflict, "version_conflict")
	})

	t.Run("409 locked field", func(t *testing.T) {
		if err := e.reg.SetOverlay(context.Background(), overlay("integrations.notify.gcpd-roof.channel", "batcave")); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := e.reg.SetOverlay(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
		}()
		cur := e.get(t, ctx, kindNotify, roofID)
		if cur.Locked["channel"] == "" {
			t.Fatalf("channel is not locked: %+v", cur.Locked)
		}
		body := signalBody("", "http://192.0.2.20/", "")
		body.Settings["channel"] = "arkham"
		body.Version = cur.Version
		_, err := e.update(ctx, kindNotify, roofID, body)
		wantStatus(t, err, http.StatusConflict, "field_locked")
	})

	t.Run("403 Local needs an interactive administrator", func(t *testing.T) {
		for name, c := range map[string]context.Context{"manager": managerCtx(), "api key": apiKeyCtx()} {
			body := signalBody("", "http://192.0.2.20/", "")
			body.Local = true
			body.Version = e.get(t, ctx, kindNotify, roofID).Version
			_, err := e.update(c, kindNotify, roofID, body)
			if he, ok := errors.AsType[*httpapi.Error](err); !ok || he.Status != http.StatusForbidden {
				t.Errorf("%s: update with Local = %v, want 403", name, err)
			}
			_, err = e.testScoped(c, kindNotify, roofID, body)
			if he, ok := errors.AsType[*httpapi.Error](err); !ok || he.Status != http.StatusForbidden {
				t.Errorf("%s: test with Local = %v, want 403", name, err)
			}
		}
		if e.get(t, ctx, kindNotify, roofID).Local {
			t.Error("a non-admin set Local")
		}
		body := signalBody("", "http://192.0.2.20/", "")
		body.Local = true
		body.Version = e.get(t, ctx, kindNotify, roofID).Version
		if got, err := e.update(adminCtx(), kindNotify, roofID, body); err != nil || !got.Local {
			t.Errorf("an interactive admin's Local = %+v, %v", got, err)
		}
	})

	t.Run("503 secrets backend down", func(t *testing.T) {
		e.sec.setDown(true)
		defer e.sec.setDown(false)
		before := e.actions(t)
		_, err := e.api.createInstance(ctx, in(ctx, createReq{Kind: kindNotify, Body: signalBody("penguin", "http://192.0.2.21/", "")}))
		wantStatus(t, err, http.StatusServiceUnavailable, "unavailable")
		if _, err := e.api.getInstance(ctx, in(ctx, instanceReq{Kind: kindNotify, ID: "penguin"})); err == nil {
			t.Error("a create with the secrets backend down stored a row")
		}
		if got := e.actions(t); !slices.Equal(got, before) {
			t.Errorf("a refused create audited %v", got)
		}
	})

	t.Run("404", func(t *testing.T) {
		const none = "no-such-instance"
		get := func() error {
			_, err := e.api.getInstance(ctx, in(ctx, instanceReq{Kind: kindNotify, ID: none}))
			return err
		}
		wantStatus(t, get(), http.StatusNotFound, "not_found")
		_, err := e.update(ctx, kindNotify, none, signalBody("", "", ""))
		if err == nil {
			t.Fatal("update of a missing instance succeeded")
		}
		body := signalBody("", "", "")
		body.Version = 1
		_, err = e.update(ctx, kindNotify, none, body)
		wantStatus(t, err, http.StatusNotFound, "not_found")
		_, err = e.api.deleteInstance(ctx, in(ctx, instanceReq{Kind: kindNotify, ID: none}))
		wantStatus(t, err, http.StatusNotFound, "not_found")
		_, err = e.testScoped(ctx, kindNotify, none, signalBody("", "", ""))
		wantStatus(t, err, http.StatusNotFound, "not_found")
		_, err = e.api.getHealth(ctx, in(ctx, instanceReq{Kind: kindNotify, ID: none}))
		wantStatus(t, err, http.StatusNotFound, "not_found")
		_, err = e.api.releaseHold(ctx, in(ctx, instanceReq{Kind: kindNotify, ID: none}))
		wantStatus(t, err, http.StatusNotFound, "not_found")
		if got := e.holds.released(); len(got) != 0 {
			t.Errorf("a hold of a missing instance was released: %v", got)
		}
	})
}

// TestIntegrationHTTP_PathsAreChecked: a kind that is not one, and an ID
// outside the grammar, are 404 on every route that takes them, and the path
// value is not echoed.
func TestIntegrationHTTP_PathsAreChecked(t *testing.T) {
	e := newEnv(t)
	ctx := adminCtx()
	for _, p := range []struct{ kind, id string }{
		{"gadgets", roofID}, {kindNotify, "Bat_Signal"}, {kindNotify, "0dark"}, {kindNotify, strings.Repeat("a", 40)},
	} {
		ir := instanceReq{Kind: p.kind, ID: p.id}
		body := signalBody("", "", "")
		body.Version = 1
		calls := map[string]func() error{
			"get":    func() error { _, err := e.api.getInstance(ctx, in(ctx, ir)); return err },
			"delete": func() error { _, err := e.api.deleteInstance(ctx, in(ctx, ir)); return err },
			"health": func() error { _, err := e.api.getHealth(ctx, in(ctx, ir)); return err },
			"hold":   func() error { _, err := e.api.releaseHold(ctx, in(ctx, ir)); return err },
			"update": func() error { _, err := e.update(ctx, p.kind, p.id, body); return err },
			"test":   func() error { _, err := e.testScoped(ctx, p.kind, p.id, signalBody("", "", "")); return err },
		}
		for name, call := range calls {
			err := call()
			wantStatus(t, err, http.StatusNotFound, "not_found")
			bad := p.id
			if p.kind == "gadgets" {
				bad = p.kind
			}
			assertNoLeak(t, name+" "+p.kind+"/"+p.id, errBlob(t, err), bad)
		}
	}
	for _, call := range []func() error{
		func() error {
			_, err := e.api.listInstances(ctx, in(ctx, listInstancesReq{Kind: "gadgets"}))
			return err
		},
		func() error {
			_, err := e.api.createInstance(ctx, in(ctx, createReq{Kind: "gadgets", Body: signalBody("x", "", "")}))
			return err
		},
		func() error { _, err := e.testUnscoped(ctx, "gadgets", signalBody("", "", "")); return err },
	} {
		wantStatus(t, call(), http.StatusNotFound, "not_found")
	}
}

// TestMapError pins each sentinel's status and that no answer quotes the
// message of the error it maps.
func TestMapError(t *testing.T) {
	schemaErr := fmt.Errorf("%w: settings: %w", integrations.ErrInvalid,
		errors.Join(&config.FieldError{Err: errors.New(`channel: "hunter2" has type string`)}))
	plain := errors.New("disk on fire: hunter2")
	for _, c := range []struct {
		name   string
		err    error
		status int
		code   string
		field  string
	}{
		{"forbidden", fmt.Errorf("%w: hunter2", integrations.ErrForbidden), 403, "permission_denied", ""},
		{"not configured", fmt.Errorf("%w: hunter2", integrations.ErrNotConfigured), 404, "not_found", ""},
		{"locked", fmt.Errorf("%w: hunter2", integrations.ErrFieldLocked), 409, "field_locked", ""},
		{"version", fmt.Errorf("%w: hunter2", integrations.ErrVersionConflict), 409, "version_conflict", ""},
		{"disabled", fmt.Errorf("%w: hunter2", integrations.ErrDisabled), 409, "integrations_disabled", ""},
		{"unknown adapter", fmt.Errorf("%w: hunter2", integrations.ErrUnknownAdapter), 422, "validation_failed", "unknown_adapter"},
		{"invalid", fmt.Errorf("%w: settings field \"url\": \"hunter2\" is not a URL", integrations.ErrInvalid), 422, "validation_failed", "invalid"},
		{"schema", schemaErr, 422, "validation_failed", "settings_schema"},
		{"unavailable", fmt.Errorf("%w: hunter2", integrations.ErrUnavailable), 503, "unavailable", ""},
		{"closed", fmt.Errorf("%w: hunter2", integrations.ErrClosed), 503, "unavailable", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := mapError(c.err)
			wantStatus(t, err, c.status, c.code)
			he, _ := errors.AsType[*httpapi.Error](err)
			if c.field != "" && (he == nil || len(he.Fields) != 1 || he.Fields[0].Code != c.field) {
				t.Errorf("fields = %+v, want one %s", he, c.field)
			}
			if he != nil && c.status == 422 && he.Err != nil {
				t.Errorf("a 422 keeps its cause: %v", he.Err)
			}
			assertNoLeak(t, "the answer", mustJSON(t, he.Fields)+he.Detail, "hunter2")
			if c.status == 422 {
				assertNoLeak(t, "Error()", err.Error(), "hunter2")
			}
		})
	}
	if got := mapError(plain); got != plain {
		t.Errorf("a plain error was changed: %v", got)
	}
	if mapError(nil) != nil {
		t.Error("nil mapped to an error")
	}
}

// ---- hold -----------------------------------------------------------------

func TestIntegrationHTTP_HoldReleaseCallsReleaseHold(t *testing.T) {
	e := newEnv(t)
	ctx := adminCtx()
	e.create(t, ctx, kindNotify, signalBody(roofID, "http://192.0.2.20/", "alfred-1"))
	ref := integrations.Ref{Kind: integrations.KindNotify, Instance: roofID}
	ir := instanceReq{Kind: kindNotify, ID: roofID}
	var probes atomic.Int64
	e.fake.HealthFn = func(ctx context.Context, i *integrationstest.FakeInstance) integrations.Health {
		probes.Add(1)
		return integrations.Health{Status: integrations.HealthOK}
	}
	sent := len(e.out.Sent())

	// No hold: 204 (the call is idempotent), nothing released, nothing probed
	// or sent.
	if _, err := e.api.releaseHold(ctx, in(ctx, ir)); err != nil {
		t.Fatalf("release with no hold: %v", err)
	}
	if e.holds.heldScopeCalls() != 1 {
		t.Errorf("HoldFor was asked %d times, want 1", e.holds.heldScopeCalls())
	}
	if got := e.holds.releaseCalls(); len(got) != 0 {
		t.Fatalf("ReleaseHoldIf called with no hold: %v", got)
	}
	if probes.Load() != 0 || len(e.out.Sent()) != sent {
		t.Errorf("a release with no hold probed %d times and sent %d requests", probes.Load(), len(e.out.Sent())-sent)
	}

	// An empty scope is no scope: it could mean "every hold".
	e.holds.holdOn(ref, integrations.Hold{})
	if _, err := e.api.releaseHold(ctx, in(ctx, ir)); err != nil {
		t.Fatalf("release of an empty scope: %v", err)
	}
	if got := e.holds.releaseCalls(); len(got) != 0 {
		t.Fatalf("a hold with no scope was released: %v", got)
	}

	// An active hold: released once, and the hold handed to ReleaseHoldIf is
	// exactly the one HoldFor read, without a probe.
	quota := e.hold("gotham-quota", 0)
	e.holds.holdOn(ref, quota)
	if _, err := e.api.releaseHold(ctx, in(ctx, ir)); err != nil {
		t.Fatalf("release: %v", err)
	}
	if got := e.holds.releaseCalls(); len(got) != 1 || got[0] != quota {
		t.Errorf("ReleaseHoldIf calls = %v, want one for %v", got, quota)
	}
	if got := e.holds.released(); !slices.Equal(got, []string{"gotham-quota"}) {
		t.Errorf("released = %v, want gotham-quota", got)
	}
	if probes.Load() != 0 || len(e.out.Sent()) != sent {
		t.Errorf("a release probed %d times and sent %d requests", probes.Load(), len(e.out.Sent())-sent)
	}

	// The health view of a held instance still carries the hold, without its
	// remote text.
	e.out.State.Hold = &integrations.Hold{
		Scope: "gotham-quota", Class: integrations.ClassQuota, Detail: "<html>The Joker's plan</html>",
		Until: e.clock.now().Add(time.Hour), SetAt: e.clock.now(),
	}
	h, err := e.api.getHealth(ctx, in(ctx, ir))
	if err != nil {
		t.Fatal(err)
	}
	if h.Hold == nil || h.Hold.Scope != "gotham-quota" || h.Hold.Class != "quota" || h.Status != "degraded" {
		t.Errorf("health = %+v", h)
	}
	assertNoLeak(t, "health", mustJSON(t, h), "Joker")

	// A failing release is a plain error: httpapi answers 500 without text.
	e.holds.holdOn(ref, quota)
	e.holds.err = errors.New("keel over")
	_, err = e.api.releaseHold(ctx, in(ctx, ir))
	if _, ok := errors.AsType[*httpapi.Error](err); ok || err == nil {
		t.Errorf("a failed release = %v, want a plain error", err)
	}

	// No releaser mounted: 503, and nothing is released.
	e.api.holds = nil
	_, err = e.api.releaseHold(ctx, in(ctx, ir))
	wantStatus(t, err, http.StatusServiceUnavailable, "unavailable")
}

// TestIntegrationHTTP_HoldChangedBetweenReadAndRelease: a hold replaced between
// HoldFor and ReleaseHoldIf is not released unseen: 409 hold_changed, the
// newer hold still in force, and the answer carries no hold text. A second
// call reads the current hold and releases it. A hold that ended in that gap
// is a 204 (the goal is met).
func TestIntegrationHTTP_HoldChangedBetweenReadAndRelease(t *testing.T) {
	e := newEnv(t)
	ctx := adminCtx()
	e.create(t, ctx, kindNotify, signalBody(roofID, "http://192.0.2.20/", "alfred-1"))
	ref := integrations.Ref{Kind: integrations.KindNotify, Instance: roofID}
	ir := instanceReq{Kind: kindNotify, ID: roofID}
	older := e.hold("gotham-quota", 0)
	newer := e.hold("gotham-quota", time.Hour)
	newer.Detail = "<html>The Joker's plan</html>"
	e.holds.holdOn(ref, older)
	e.holds.afterRead = func(f *fakeHolds) { f.holdOn(ref, newer) }

	_, err := e.api.releaseHold(ctx, in(ctx, ir))
	wantStatus(t, err, http.StatusConflict, "hold_changed")
	assertNoLeak(t, "the 409", errBlob(t, err), "Joker", "gotham-quota")
	if got := e.holds.released(); len(got) != 0 {
		t.Fatalf("a stale release ended %v", got)
	}
	if got := e.holds.releaseCalls(); len(got) != 1 || got[0] != older {
		t.Errorf("ReleaseHoldIf was handed %v, want the hold that was read", got)
	}
	if cur, ok := e.holds.HoldFor(ref); !ok || cur != newer {
		t.Errorf("the newer hold = %v (in force %v), want it untouched", cur, ok)
	}

	// Read again and decide again: the current hold is released.
	e.holds.afterRead = nil
	if _, err := e.api.releaseHold(ctx, in(ctx, ir)); err != nil {
		t.Fatalf("the second release: %v", err)
	}
	if got := e.holds.released(); !slices.Equal(got, []string{"gotham-quota"}) {
		t.Errorf("released = %v", got)
	}

	// The hold ended between the read and the release: nothing in force, 204.
	e.holds.holdOn(ref, older)
	e.holds.afterRead = func(f *fakeHolds) {
		f.mu.Lock()
		delete(f.held, ref)
		f.mu.Unlock()
	}
	if _, err := e.api.releaseHold(ctx, in(ctx, ir)); err != nil {
		t.Errorf("a hold that ended in the gap: %v", err)
	}
}

// TestIntegrationHTTP_HoldOfUnprobableInstanceIsReleased: a hold on a
// disabled instance, and on a misconfigured one, is released. Registry.Health
// answers those with a static finding and no hold, so a route that read the
// hold from it could not release either.
func TestIntegrationHTTP_HoldOfUnprobableInstanceIsReleased(t *testing.T) {
	e := newEnv(t)
	ctx := adminCtx()
	var probes atomic.Int64
	e.fake.HealthFn = func(ctx context.Context, i *integrationstest.FakeInstance) integrations.Health {
		probes.Add(1)
		return integrations.Health{Status: integrations.HealthOK}
	}
	disabled := signalBody("gcpd-basement", "http://192.0.2.21/", "alfred-1")
	disabled.Enabled = new(bool)
	e.create(t, ctx, kindNotify, disabled)
	e.create(t, ctx, kindNotify, signalBody(roofID, "http://192.0.2.20/", "alfred-2"))
	e.sec.setDown(true)
	t.Cleanup(func() { e.sec.setDown(false) })

	for id, want := range map[string]string{"gcpd-basement": "disabled", roofID: "misconfigured"} {
		ref := integrations.Ref{Kind: integrations.KindNotify, Instance: integrations.InstanceID(id)}
		h, err := e.reg.Health(ctx, ref)
		if err != nil || h.Status.String() != want || h.Hold != nil {
			t.Fatalf("%s: Registry.Health = %+v, %v; want a static %s finding with no hold", id, h, err, want)
		}
		scope := "gotham-quota-" + id
		e.holds.holdOn(ref, e.hold(scope, 0))
		if _, err := e.api.releaseHold(ctx, in(ctx, instanceReq{Kind: kindNotify, ID: id})); err != nil {
			t.Fatalf("%s: release: %v", id, err)
		}
		if got := e.holds.released(); !slices.Contains(got, scope) {
			t.Errorf("%s: released %v, want %s", id, got, scope)
		}
	}
	if probes.Load() != 0 {
		t.Errorf("the releases probed %d times", probes.Load())
	}
}

// TestIntegrationHTTP_HoldRouteChecksManage: the hold route turns away a
// principal without core:integrations.manage itself, in addition to the
// router, before it looks at the instance or the holds. The permission it
// checks is the one the Registry's writes check.
func TestIntegrationHTTP_HoldRouteChecksManage(t *testing.T) {
	e := newEnv(t)
	ctx := adminCtx()
	e.create(t, ctx, kindNotify, signalBody(roofID, "http://192.0.2.20/", "alfred-1"))
	ref := integrations.Ref{Kind: integrations.KindNotify, Instance: roofID}
	e.holds.holdOn(ref, e.hold("gotham-quota", 0))

	reader := integrationstest.As(context.Background(), auth.MethodSession, "core:integrations.read")
	noPerms := &auth.Principal{Method: auth.MethodSession}
	cases := map[string]*httpapi.In[instanceReq]{
		"read only":              in(reader, instanceReq{Kind: kindNotify, ID: roofID}),
		"no checker":             {Req: instanceReq{Kind: kindNotify, ID: roofID}, Principal: noPerms},
		"no instance, read only": in(reader, instanceReq{Kind: kindNotify, ID: "no-such-instance"}),
	}
	for name, req := range cases {
		_, err := e.api.releaseHold(ctx, req)
		if err == nil {
			t.Errorf("%s: released", name)
			continue
		}
		wantStatus(t, err, http.StatusForbidden, "permission_denied")
	}
	if got := e.holds.released(); len(got) != 0 {
		t.Errorf("a caller without manage released %v", got)
	}
	if e.holds.heldScopeCalls() != 0 {
		t.Error("the holds were read for a caller without manage")
	}

	// The route's permission is the Registry's constant (integrations.ManagePerm,
	// also pinned as text by the route table): a principal holding exactly it
	// can write through the Registry and release a hold.
	only := integrationstest.As(context.Background(), auth.MethodSession, integrations.ManagePerm)
	if _, err := e.update(only, kindNotify, roofID, func() IntegrationInstanceBody {
		b := signalBody("", "http://192.0.2.20/", "")
		b.Version = 1
		return b
	}()); err != nil {
		t.Errorf("a principal holding integrations.ManagePerm cannot write: %v", err)
	}
	if _, err := e.api.releaseHold(only, in(only, instanceReq{Kind: kindNotify, ID: roofID})); err != nil {
		t.Errorf("a principal holding integrations.ManagePerm cannot release: %v", err)
	}
}

// ---- I5: audit ------------------------------------------------------------

func TestIntegrationHTTP_AuditOncePerAction(t *testing.T) {
	e := newEnv(t)
	ctx := adminCtx()
	var seen []string
	step := func(name string, want ...string) {
		t.Helper()
		all := e.actions(t)
		got := all[len(seen):]
		seen = all
		if !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want))) {
			t.Errorf("%s recorded %v, want %v", name, got, want)
		}
	}

	e.create(t, ctx, kindNotify, signalBody(roofID, "http://192.0.2.20/", "alfred-1"))
	step("create", "core.integration.create", "core.integration.write_secret")

	body := signalBody("", "http://192.0.2.20/", "")
	body.Name = "GCPD roof, night shift"
	body.Version = 1
	if _, err := e.update(ctx, kindNotify, roofID, body); err != nil {
		t.Fatal(err)
	}
	step("update", "core.integration.update")

	body = signalBody("", "http://192.0.2.77/", "")
	body.Version = 2
	if _, err := e.update(ctx, kindNotify, roofID, body); err != nil {
		t.Fatal(err)
	}
	step("update that moves the URL", "core.integration.change_url secrets_dropped")

	before := e.log.recordCalls()
	if _, err := e.testScoped(ctx, kindNotify, roofID, signalBody("", "http://192.0.2.77/", "alfred-2")); err != nil {
		t.Fatal(err)
	}
	step("instance test", "core.integration.test")
	if _, err := e.testUnscoped(ctx, kindNotify, signalBody("", "http://192.0.2.78/", "alfred-3")); err != nil {
		t.Fatal(err)
	}
	step("unscoped test", "core.integration.test")
	if n := e.log.recordCalls() - before; n != 2 {
		t.Errorf("the test routes recorded %d times for 2 tests", n)
	}

	// Reads, a health probe, a hold release and refused calls record nothing.
	if _, err := e.api.getHealth(ctx, in(ctx, instanceReq{Kind: kindNotify, ID: roofID})); err != nil {
		t.Fatal(err)
	}
	if _, err := e.api.releaseHold(ctx, in(ctx, instanceReq{Kind: kindNotify, ID: roofID})); err != nil {
		t.Fatal(err)
	}
	e.get(t, ctx, kindNotify, roofID)
	bad := signalBody("", "http://192.0.2.77/", "")
	bad.Local = true
	if _, err := e.testScoped(managerCtx(), kindNotify, roofID, bad); err == nil {
		t.Fatal("a manager's Local test was allowed")
	}
	bad.Settings["channel"] = 5
	if _, err := e.testScoped(ctx, kindNotify, roofID, bad); err == nil {
		t.Fatal("an invalid test was allowed")
	}
	step("reads and refused calls")

	if _, err := e.api.deleteInstance(ctx, in(ctx, instanceReq{Kind: kindNotify, ID: roofID})); err != nil {
		t.Fatal(err)
	}
	step("delete", "core.integration.delete")

	// The test record names the instance and the result, and holds no secret.
	for rec, err := range e.audit.Query(context.Background(), auditFilterTest()) {
		if err != nil {
			t.Fatal(err)
		}
		blob := mustJSON(t, rec.Details) + rec.Route + rec.Target.String()
		assertNoLeak(t, "the test record", blob, "alfred")
		if rec.Details["ok"] != true || rec.Details["class"] == nil || rec.Route == "" {
			t.Errorf("test record = %+v", rec)
		}
	}
}

// TestIntegrationHTTP_TestAuditFailureWithholdsResult: a test that cannot be
// recorded answers 503 and shows its caller no result; with no audit logger
// mounted no test runs at all.
func TestIntegrationHTTP_TestAuditFailureWithholdsResult(t *testing.T) {
	e := newEnv(t)
	ctx := adminCtx()
	e.log.failWith(errors.New("audit store full"))
	res, err := e.testUnscoped(ctx, kindNotify, signalBody("", "http://192.0.2.20/", "alfred-1"))
	wantStatus(t, err, http.StatusServiceUnavailable, "unavailable")
	if res != (IntegrationTestResult{}) {
		t.Errorf("a result leaked: %+v", res)
	}
	e.log.failWith(nil)

	e.api.audit = nil
	sent := len(e.out.Sent())
	_, err = e.testUnscoped(ctx, kindNotify, signalBody("", "http://192.0.2.20/", "alfred-1"))
	wantStatus(t, err, http.StatusServiceUnavailable, "unavailable")
	if len(e.out.Sent()) != sent {
		t.Error("a test with no audit logger sent a request")
	}
}

// TestIntegrationHTTP_GuardFailsClosed: a handler with no principal answers
// 401 and one with no Registry 503, before it reads anything.
func TestIntegrationHTTP_GuardFailsClosed(t *testing.T) {
	e := newEnv(t)
	bare := context.Background()
	ir := instanceReq{Kind: kindNotify, ID: roofID}
	calls := map[string]func(a *api) error{
		"adapters": func(a *api) error { _, err := a.listAdapters(bare, in(bare, listAdaptersReq{})); return err },
		"list": func(a *api) error {
			_, err := a.listInstances(bare, in(bare, listInstancesReq{Kind: kindNotify}))
			return err
		},
		"create": func(a *api) error {
			_, err := a.createInstance(bare, in(bare, createReq{Kind: kindNotify, Body: signalBody("x", "", "")}))
			return err
		},
		"get": func(a *api) error { _, err := a.getInstance(bare, in(bare, ir)); return err },
		"update": func(a *api) error {
			_, err := a.updateInstance(bare, in(bare, updateReq{Kind: kindNotify, ID: roofID}))
			return err
		},
		"delete": func(a *api) error { _, err := a.deleteInstance(bare, in(bare, ir)); return err },
		"test":   func(a *api) error { _, err := a.testNew(bare, in(bare, testNewReq{Kind: kindNotify})); return err },
		"itest": func(a *api) error {
			_, err := a.testInstance(bare, in(bare, testInstanceReq{Kind: kindNotify, ID: roofID}))
			return err
		},
		"health": func(a *api) error { _, err := a.getHealth(bare, in(bare, ir)); return err },
		"hold":   func(a *api) error { _, err := a.releaseHold(bare, in(bare, ir)); return err },
	}
	for name, call := range calls {
		if err := call(e.api); err == nil {
			t.Errorf("%s: no principal was served", name)
		} else {
			wantStatus(t, err, http.StatusUnauthorized, "unauthenticated")
		}
	}
	if got := e.holds.released(); len(got) != 0 {
		t.Errorf("an unauthenticated hold route released %v", got)
	}
	noReg := &api{holds: e.holds, audit: e.log, base: "/api/core/v1"}
	ctx := adminCtx()
	for _, call := range []func() error{
		func() error { _, err := noReg.getInstance(ctx, in(ctx, ir)); return err },
		func() error { _, err := noReg.releaseHold(ctx, in(ctx, ir)); return err },
		func() error { _, err := noReg.testNew(ctx, in(ctx, testNewReq{Kind: kindNotify})); return err },
	} {
		wantStatus(t, call(), http.StatusServiceUnavailable, "unavailable")
	}
}

// ---- views and bodies -----------------------------------------------------

func TestIntegrationHTTP_SecretsAreMarkers(t *testing.T) {
	e := newEnv(t)
	ctx := adminCtx()
	oracleBody := IntegrationInstanceBody{
		ID: "clocktower", Adapter: string(oracle), Name: "Clocktower", Enabled: truth(),
		Settings: map[string]any{"host": "192.0.2.60", "port": float64(9000)},
	}
	got := e.create(t, ctx, "metadata", oracleBody)
	if set, ok := got.SecretsSet["password"]; !ok || set {
		t.Errorf("an unset optional secret = %v (present %v), want an explicit false", set, ok)
	}
	if got.Settings["host"] != "192.0.2.60" {
		t.Errorf("settings = %+v", got.Settings)
	}

	// A row kept for an adapter this binary lacks: its Ref-holding fields are
	// the secret fields, and no Ref is answered.
	ref := integrations.Ref{Kind: integrations.KindNotify, Instance: "lazarus-pit"}
	b := e.ks.NewBatch()
	if _, err := integrations.NativeInstanceStore(e.ks).Put(context.Background(), b, integrations.StoredInstance{
		Config: integrations.InstanceConfig{
			Ref: ref, Adapter: "hello.lazarus", Enabled: true, Origin: integrations.OriginUI,
			Settings: map[string]any{
				"url": "http://192.0.2.40/", "password": secrets.RefTo("integrations.notify.lazarus-pit.password"),
				"mirror": "http://robin:grayson@192.0.2.41/", "note": "hello",
			},
		},
		SecretsOrigin: "url=http://192.0.2.40:80",
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(context.Background(), storage.Sync); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	e.reg = e.registry(t)
	e.api.reg = e.reg
	lazarus := e.get(t, ctx, kindNotify, "lazarus-pit")
	if !lazarus.SecretsSet["password"] {
		t.Errorf("secrets_set = %+v", lazarus.SecretsSet)
	}
	assertNoLeak(t, "an unknown adapter's row", mustJSON(t, lazarus), "integrations.notify.lazarus-pit.password", `"secret"`, "robin", "grayson")
	if _, ok := lazarus.Settings["mirror"]; ok || lazarus.Settings["url"] != "http://192.0.2.40/" || lazarus.Settings["note"] != "hello" {
		t.Errorf("an unknown adapter's settings = %+v: want the userinfo URL withheld and the rest kept", lazarus.Settings)
	}
	if _, ok := lazarus.Settings["password"]; ok {
		t.Errorf("settings = %+v", lazarus.Settings)
	}
	// An instance of an adapter this binary lacks cannot be replaced, and the
	// refusal comes from the Registry, not from a panic on the missing schema.
	name := "renamed"
	_, err := e.api.updateInstance(ctx, in(ctx, updateReq{Kind: kindNotify, ID: "lazarus-pit", Body: IntegrationInstanceReplace{
		Adapter: "hello.lazarus", Name: &name, Enabled: new(true), Version: 1,
		Priority: new(int), Local: new(bool), Limits: &IntegrationLimits{},
	}}))
	if err == nil {
		t.Error("a replace of an unknown adapter's instance succeeded")
	}
}

// TestWithheldURLFields: which stored fields a PUT may not omit. Only an
// unlocked, URL-typed, non-secret field whose value the view withholds under
// the Registry's rule; the text check and the locked fields are not in it.
func TestWithheldURLFields(t *testing.T) {
	fi := &fieldInfo{
		secret: map[string]bool{"token": true},
		uri:    map[string]bool{"a": true, "b": true, "ok": true, "falsepos": true, "locked": true, "token": true, "ref": true, "unparsed": true},
	}
	cfg := integrations.InstanceConfig{
		Settings: map[string]any{
			"b":        "http://u:p@192.0.2.20/",
			"a":        []any{"https://192.0.2.20/", "https://x:y@192.0.2.20/"},
			"ok":       "http://192.0.2.20/",
			"falsepos": "https://192.0.2.20/cb?next=https://u@x.example",
			"locked":   "http://u:p@192.0.2.21/",
			"token":    "http://u:p@192.0.2.22/",
			"ref":      secrets.RefTo("a:b@c"),
			"unparsed": "http://192.0.2.20:badport/",
			"free":     "http://u:p@192.0.2.23/", // not URL-typed: never withheld
		},
		Locked: map[string]string{"locked": "env"},
	}
	if got, want := withheldURLFields(cfg, fi), []string{"a", "b", "unparsed"}; !slices.Equal(got, want) {
		t.Errorf("withheld = %v, want %v", got, want)
	}
	if got := withheldURLFields(cfg, nil); len(got) != 0 {
		t.Errorf("an unknown adapter's fields: %v", got)
	}
	if err := withheldOmitted(cfg, nil, map[string]any{}); err != nil {
		t.Errorf("an unknown adapter: %v", err)
	}
	// Naming a field, however, satisfies it.
	if err := withheldOmitted(cfg, fi, map[string]any{"a": nil, "b": "x", "unparsed": []any{}}); err != nil {
		t.Errorf("all named: %v", err)
	}
	err := withheldOmitted(cfg, fi, map[string]any{"b": "x"})
	he, _ := errors.AsType[*httpapi.Error](err)
	if he == nil || len(he.Fields) != 2 || he.Fields[0].Field != "/body/settings/a" || he.Fields[1].Field != "/body/settings/unparsed" {
		t.Errorf("fields = %+v", he)
	}
}

// TestFieldsFor: a known adapter with a readable schema has fields; an
// adapter the binary lacks, or one whose schema cannot be read, has none.
func TestFieldsFor(t *testing.T) {
	metas := map[integrations.AdapterID]integrations.Meta{}
	if fieldsFor(metas, "hello.lazarus") != nil {
		t.Error("an unknown adapter has fields")
	}
	if fieldsOfSchema(json.RawMessage(`{`)) != nil || fieldsOfSchema(json.RawMessage(`false`)) != nil {
		t.Error("an unreadable schema has fields")
	}
	if fi := fieldsOfSchema(json.RawMessage(`{"properties":{"home":{"type":"string","format":"uri"}}}`)); fi == nil || !fi.uri["home"] || len(fi.secret) != 0 {
		t.Errorf("a readable schema: %+v", fi)
	}
	e := newEnv(t)
	for m := range e.reg.Adapters(integrations.KindNotify) {
		fi := fieldsFor(map[integrations.AdapterID]integrations.Meta{m.ID: m}, m.ID)
		if fi == nil || !fi.secret["password"] || !fi.uri["url"] {
			t.Errorf("batsignal fields = %+v", fi)
		}
	}
}

// TestInstanceView_NeverEmitsSecretShapedSettings: whatever the schema
// says, a Ref, its decoded form or a Value in settings is never answered.
func TestInstanceView_NeverEmitsSecretShapedSettings(t *testing.T) {
	cfg := integrations.InstanceConfig{
		Ref: integrations.Ref{Kind: integrations.KindNotify, Instance: "x"},
		Settings: map[string]any{
			"plain":   "ok",
			"ref":     secrets.RefTo("integrations.notify.x.token"),
			"refptr":  &secrets.Ref{},
			"decoded": map[string]any{"secret": "integrations.notify.x.pw"},
			"value":   secrets.NewValue("hunter2"),
			"nested":  map[string]any{"a": 1},
			"schema":  "declared secret",
		},
	}
	v := instanceView(cfg, &fieldInfo{secret: map[string]bool{"schema": true}})
	if len(v.Settings) != 2 || v.Settings["plain"] != "ok" || v.Settings["nested"] == nil {
		t.Errorf("settings = %+v", v.Settings)
	}
	for _, f := range []string{"ref", "refptr", "decoded", "value"} {
		if !v.SecretsSet[f] {
			t.Errorf("secrets_set[%s] = false", f)
		}
	}
	if set, ok := v.SecretsSet["schema"]; !ok || set {
		t.Errorf("a declared secret field holding a plain string: set=%v present=%v, want an explicit false", set, ok)
	}
	if v.SecretsSet["plain"] {
		t.Error("a plain field is marked secret")
	}
	if blob, err := json.Marshal(v); err != nil {
		t.Fatal(err)
	} else {
		assertNoLeak(t, "the view", string(blob), "hunter2", "declared secret", "integrations.notify")
	}
}

func TestIntegrationHTTP_ReplaceRules(t *testing.T) {
	e := newEnv(t)
	ctx := managerCtx()
	created := e.create(t, ctx, kindNotify, signalBody(roofID, "http://192.0.2.20/", "alfred-1"))

	t.Run("validation", func(t *testing.T) {
		noEnabled := signalBody("", "http://192.0.2.20/", "")
		noEnabled.Enabled, noEnabled.Version = nil, 1
		noVersion := signalBody("", "http://192.0.2.20/", "")
		otherID := signalBody("riddler", "http://192.0.2.20/", "")
		otherID.Version = 1
		for name, b := range map[string]IntegrationInstanceBody{"enabled": noEnabled, "version": noVersion, "id": otherID} {
			err := (&updateReq{Kind: kindNotify, ID: roofID, Body: replaceOf(b)}).Validate()
			wantStatus(t, err, http.StatusUnprocessableEntity, "validation_failed")
			if he, _ := errors.AsType[*httpapi.Error](err); he == nil || !strings.HasSuffix(he.Fields[0].Field, "/"+name) {
				t.Errorf("%s: fields = %+v", name, he)
			}
		}
		same := signalBody(roofID, "http://192.0.2.20/", "")
		same.Version = 1
		if err := (&updateReq{Kind: kindNotify, ID: roofID, Body: replaceOf(same)}).Validate(); err != nil {
			t.Errorf("a body naming its own ID = %v", err)
		}
	})

	t.Run("omitted secret is kept, null clears, a new URL drops", func(t *testing.T) {
		body := signalBody("", "http://192.0.2.20/", "")
		body.Name = "renamed"
		body.Version = created.Version
		got, err := e.update(ctx, kindNotify, roofID, body)
		if err != nil {
			t.Fatal(err)
		}
		if got.Name != "renamed" || got.Version != created.Version+1 || !got.SecretsSet["password"] {
			t.Errorf("after a rename: %+v", got)
		}
		if e.secretValue(t, kindNotify, roofID, "password") != "alfred-1" {
			t.Error("a rename lost the secret")
		}

		body.Version = got.Version
		body.Settings["password"] = nil
		got, err = e.update(ctx, kindNotify, roofID, body)
		if err != nil {
			t.Fatal(err)
		}
		if got.SecretsSet["password"] || e.secretValue(t, kindNotify, roofID, "password") != "" {
			t.Errorf("null did not clear the secret: %+v", got.SecretsSet)
		}

		body = signalBody("", "http://192.0.2.20/", "alfred-5")
		body.Version = got.Version
		got, err = e.update(ctx, kindNotify, roofID, body)
		if err != nil || !got.SecretsSet["password"] {
			t.Fatalf("re-supply = %+v, %v", got, err)
		}
		body = signalBody("", "http://192.0.2.31/", "")
		body.Version = got.Version
		got, err = e.update(ctx, kindNotify, roofID, body)
		if err != nil {
			t.Fatal(err)
		}
		if got.SecretsSet["password"] || e.secretValue(t, kindNotify, roofID, "password") != "" {
			t.Error("a moved URL kept the stored secret")
		}
	})
}

func TestIntegrationHTTP_CreateLocationAndConflict(t *testing.T) {
	e := newEnv(t)
	ctx := managerCtx()
	req := createReq{Kind: kindNotify, Body: signalBody(roofID, "http://192.0.2.20/", "alfred-1")}
	got, err := e.api.createInstance(ctx, in(ctx, req))
	if err != nil {
		t.Fatal(err)
	}
	if want := "/api/core/v1/integrations/notify/instances/gcpd-roof"; got.Location != want {
		t.Errorf("Location = %q, want %q", got.Location, want)
	}
	if got.Body.Version != 1 || got.Body.Origin != "ui" || got.Body.ID != roofID || got.Body.Kind != kindNotify {
		t.Errorf("created = %+v", got.Body)
	}
	_, err = e.api.createInstance(ctx, in(ctx, req))
	wantStatus(t, err, http.StatusConflict, "version_conflict")
}

func TestIntegrationHTTP_BodyValidation(t *testing.T) {
	long := "SECRETMARK" + strings.Repeat("x", maxSecretBytes)
	ok := func() IntegrationInstanceBody { return signalBody("riddler", "http://192.0.2.20/", "") }
	for _, c := range []struct {
		name  string
		mode  bodyMode
		path  string
		edit  func(*IntegrationInstanceBody)
		field string
	}{
		{"no id", modeCreate, "", func(b *IntegrationInstanceBody) { b.ID = "" }, "/body/id"},
		{"bad id", modeCreate, "", func(b *IntegrationInstanceBody) { b.ID = "Riddler" }, "/body/id"},
		{"no adapter", modeCreate, "", func(b *IntegrationInstanceBody) { b.Adapter = "" }, "/body/adapter"},
		{"bad adapter", modeCreate, "", func(b *IntegrationInstanceBody) { b.Adapter = "nope" }, "/body/adapter"},
		{"version on create", modeCreate, "", func(b *IntegrationInstanceBody) { b.Version = 3 }, "/body/version"},
		{"no enabled", modeCreate, "", func(b *IntegrationInstanceBody) { b.Enabled = nil }, "/body/enabled"},
		{"long name", modeCreate, "", func(b *IntegrationInstanceBody) { b.Name = strings.Repeat("é", maxNameRunes+1) }, "/body/name"},
		{"control name", modeCreate, "", func(b *IntegrationInstanceBody) { b.Name = "bat\nman" }, "/body/name"},
		{"priority", modeCreate, "", func(b *IntegrationInstanceBody) { b.Priority = maxPriority + 1 }, "/body/priority"},
		{"many settings", modeCreate, "", func(b *IntegrationInstanceBody) {
			b.Settings = map[string]any{}
			for i := range maxSettingsKeys + 1 {
				b.Settings[fmt.Sprint("f", i)] = i
			}
		}, "/body/settings"},
		{"empty settings key", modeCreate, "", func(b *IntegrationInstanceBody) { b.Settings = map[string]any{"": 1} }, "/body/settings"},
		{"many secrets", modeCreate, "", func(b *IntegrationInstanceBody) {
			b.Secrets = map[string]string{}
			for i := range maxSecretFields + 1 {
				b.Secrets[fmt.Sprint("s", i)] = "v"
			}
		}, "/body/secrets"},
		{"long secret", modeCreate, "", func(b *IntegrationInstanceBody) { b.Secrets = map[string]string{"password": long} }, "/body/secrets/password"},
		{"bad limits", modeCreate, "", func(b *IntegrationInstanceBody) { b.Limits = &IntegrationLimits{Timeout: "soon"} }, "/body/limits/timeout"},
		{"test id mismatch", modeTest, "gcpd-roof", func(b *IntegrationInstanceBody) { b.ID = "riddler" }, "/body/id"},
		{"replace no version", modeReplace, "riddler", func(b *IntegrationInstanceBody) { b.Version = 0 }, "/body/version"},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := ok()
			if c.mode == modeReplace {
				b.Version = 1
			}
			c.edit(&b)
			errs := b.validate(c.mode, c.path)
			if !slices.ContainsFunc(errs, func(f httpapi.FieldError) bool { return f.Field == c.field }) {
				t.Errorf("fields = %+v, want one at %s", errs, c.field)
			}
			assertNoLeak(t, "the field errors", mustJSON(t, errs), "SECRETMARK")
		})
	}
	good := ok()
	if errs := good.validate(modeCreate, ""); len(errs) != 0 {
		t.Errorf("a valid create = %+v", errs)
	}
	test := ok()
	test.Enabled, test.ID = nil, ""
	if errs := test.validate(modeTest, ""); len(errs) != 0 {
		t.Errorf("a valid test = %+v", errs)
	}
}

func TestIntegrationHTTP_LimitsRoundTrip(t *testing.T) {
	e := newEnv(t)
	ctx := managerCtx()
	limits := &IntegrationLimits{
		RPS: 2.5, Burst: 4, MaxRetries: 3, Timeout: "3s", RetryAfterMax: "5m0s",
		Daily:   &IntegrationDaily{Total: 100, Background: 40, Zone: "America/New_York", Counters: []string{"query", "grab"}},
		Breaker: &IntegrationBreaker{Threshold: 5, Cooldown: "30s", ProbeTimeout: "10s"},
		Holds:   &IntegrationHoldPolicy{Durations: map[string]string{"auth": "6h0m0s", "quota": "4h0m0s"}, Max: "24h0m0s", Persist: true},
	}
	body := signalBody(roofID, "http://192.0.2.20/", "alfred-1")
	body.Limits = limits
	created := e.create(t, ctx, kindNotify, body)
	if got := mustJSON(t, created.Limits); got != mustJSON(t, limits) {
		t.Errorf("limits round trip:\n got %s\nwant %s", got, mustJSON(t, limits))
	}
	if got := mustJSON(t, e.get(t, ctx, kindNotify, roofID).Limits); got != mustJSON(t, limits) {
		t.Errorf("stored limits:\n got %s\nwant %s", got, mustJSON(t, limits))
	}
	if limitsView(integrations.Limits{}) != nil {
		t.Error("zero limits render as a value")
	}

	for name, l := range map[string]*IntegrationLimits{
		"/timeout":                   {Timeout: "30 minutes"},
		"/retry_after_max":           {RetryAfterMax: "-1s"},
		"/rps":                       {RPS: -1},
		"/burst":                     {Burst: -1},
		"/max_retries":               {MaxRetries: maxRetries + 1},
		"/daily":                     {Daily: &IntegrationDaily{Total: 1, Background: 2}},
		"/daily/zone":                {Daily: &IntegrationDaily{Zone: "Mars/Olympus"}},
		"/daily/counters":            {Daily: &IntegrationDaily{Counters: []string{""}}},
		"/breaker/cooldown":          {Breaker: &IntegrationBreaker{Cooldown: "x"}},
		"/breaker/threshold":         {Breaker: &IntegrationBreaker{Threshold: -1}},
		"/holds/durations":           {Holds: &IntegrationHoldPolicy{Durations: map[string]string{"bogus": "1s"}}},
		"/holds/durations/quota":     {Holds: &IntegrationHoldPolicy{Durations: map[string]string{"quota": "1 hour"}}},
		"/holds/max":                 {Holds: &IntegrationHoldPolicy{Max: "forever"}},
		"/daily/counters (too many)": {Daily: &IntegrationDaily{Counters: make([]string, maxCounters+1)}},
	} {
		_, errs := parseLimits(l, "")
		if len(errs) == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestIntegrationHTTP_ListsPage(t *testing.T) {
	e := newEnv(t)
	ctx := managerCtx()
	for _, id := range []string{"cc", "aa", "bb"} {
		e.create(t, ctx, kindNotify, signalBody(id, "", ""))
	}
	list := func(limit, offset int) httpapi.Page[IntegrationInstance] {
		t.Helper()
		got, err := e.api.listInstances(ctx, in(ctx, listInstancesReq{Kind: kindNotify, Limit: limit, Offset: offset}))
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	ids := func(p httpapi.Page[IntegrationInstance]) []string {
		var out []string
		for _, i := range p.Items {
			out = append(out, i.ID)
		}
		return out
	}
	if p := list(50, 0); !slices.Equal(ids(p), []string{"aa", "bb", "cc"}) || p.Total != 3 || p.Count != 3 {
		t.Errorf("page = %+v", p)
	}
	if p := list(2, 1); !slices.Equal(ids(p), []string{"bb", "cc"}) || p.Total != 3 || p.Count != 2 || p.Offset != 1 || p.Limit != 2 {
		t.Errorf("page 2 = %+v", p)
	}
	if p := list(2, 9); p.Items == nil || len(p.Items) != 0 || p.Total != 3 {
		t.Errorf("a page past the end = %+v", p)
	}
	if p := list(50, 0); len(ids(listOf(t, e, ctx, "metadata"))) != 0 || p.Total != 3 {
		t.Error("another kind's list is not empty")
	}

	adapters := func(kind string, limit, offset int) httpapi.Page[IntegrationAdapter] {
		t.Helper()
		got, err := e.api.listAdapters(ctx, in(ctx, listAdaptersReq{Kind: kind, Limit: limit, Offset: offset}))
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	all := adapters("", 50, 0)
	if all.Total != 2 || all.Items[0].ID != string(oracle) || all.Items[1].ID != string(batsignal) {
		t.Errorf("adapters = %+v", all)
	}
	if one := adapters("notify", 50, 0); one.Total != 1 || one.Items[0].ID != string(batsignal) {
		t.Errorf("adapters?kind=notify = %+v", one)
	}
	if second := adapters("", 1, 1); len(second.Items) != 1 || second.Items[0].ID != string(batsignal) {
		t.Errorf("adapters page 2 = %+v", second)
	}
	a := all.Items[1]
	if a.Kind != "notify" || a.DisplayName != "Bat-Signal" || a.Origin != "hello" || a.Egress != "configured" || a.Limits == nil || a.Limits.Timeout != "5s" {
		t.Errorf("adapter = %+v", a)
	}
	if !strings.Contains(string(a.SettingsSchema), "x-mediacore-secret") {
		t.Errorf("the schema does not mark the secret field: %s", a.SettingsSchema)
	}
	if err := (&listAdaptersReq{Kind: "gadgets"}).Validate(); err == nil {
		t.Error("?kind=gadgets accepted")
	}
	if err := (&listAdaptersReq{Kind: "notify"}).Validate(); err != nil {
		t.Errorf("?kind=notify = %v", err)
	}
}

func listOf(t *testing.T, e *env, ctx context.Context, kind string) httpapi.Page[IntegrationInstance] {
	t.Helper()
	got, err := e.api.listInstances(ctx, in(ctx, listInstancesReq{Kind: kind, Limit: 50}))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestHealthView_BoundsAdapterText(t *testing.T) {
	long := strings.Repeat("é", 200) // 400 bytes
	v := healthView(integrations.Health{
		Status: integrations.HealthDown, Message: long + "\nline two\x00", RemoteVersion: strings.Repeat("v", 100),
		Latency: 1500 * time.Millisecond, Breaker: integrations.BreakerOpen,
	})
	if len(v.Message) > maxHealthMessage || len(v.RemoteVersion) > maxRemoteVersions {
		t.Errorf("unbounded: %d, %d bytes", len(v.Message), len(v.RemoteVersion))
	}
	if !strings.HasPrefix(v.Message, "éé") || strings.ContainsAny(v.Message, "\n\x00") || v.Message != strings.ToValidUTF8(v.Message, "") {
		t.Errorf("message = %q", v.Message)
	}
	if v.Status != "down" || v.LatencyMS != 1500 || v.Breaker != "open" {
		t.Errorf("view = %+v", v)
	}
	if got := bound("a\nb", 10); got != "a b" {
		t.Errorf("bound = %q", got)
	}
	if got := bound("bad\xffbytes", 20); got != "bad?bytes" {
		t.Errorf("bound = %q", got)
	}
	for in, want := range map[integrations.EgressPolicy]string{
		integrations.EgressConfigured: "configured", integrations.EgressPublicOnly: "public_only",
		integrations.EgressSameHostOrPublic: "same_host_or_public", 99: "unknown",
	} {
		if got := egressName(in); got != want {
			t.Errorf("egressName(%d) = %s", in, got)
		}
	}
	for in, want := range map[integrations.BreakerState]string{
		integrations.BreakerClosed: "closed", integrations.BreakerHalfOpen: "half_open", integrations.BreakerOpen: "open", 9: "unknown",
	} {
		if got := breakerName(in); got != want {
			t.Errorf("breakerName(%d) = %s", in, got)
		}
	}
}

// ---- PUT: required fields and the GET round trip --------------------------

// TestUpdateRoute_IsStrict: the route decodes strictly, so an unknown key is
// a 400 and not a silently dropped edit. What lets a GET answer through is the
// read-only members of IntegrationInstanceReplace, tested below.
func TestUpdateRoute_IsStrict(t *testing.T) {
	rt := updateRoute(rbac.Need(integrations.ManagePerm), nil)
	if rt.Lenient || rt.MaxBody != maxBody || rt.Method != http.MethodPut {
		t.Errorf("update route = lenient %v, max body %d, %s", rt.Lenient, rt.MaxBody, rt.Method)
	}
}

// strictDecode decodes raw into a PUT body the way httpapi does for a route
// that is not Lenient: unknown fields are an error.
func strictDecode(raw string) (IntegrationInstanceReplace, error) {
	var b IntegrationInstanceReplace
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	return b, dec.Decode(&b)
}

// TestIntegrationHTTP_ReplaceDecodesStrictly: a misspelt key is refused, also
// inside limits; the keys a GET answer carries are not.
func TestIntegrationHTTP_ReplaceDecodesStrictly(t *testing.T) {
	const ok = `"name":"GCPD roof","enabled":true,"priority":0,"local":false,"limits":{"rps":2},"version":1`
	for name, raw := range map[string]string{
		"misspelt secrets":      `{` + ok + `,"secret":{"password":"x"}}`,
		"misspelt in limits":    `{"name":"x","enabled":true,"priority":0,"local":false,"limits":{"rsp":2},"version":1}`,
		"misspelt in nested":    `{"name":"x","enabled":true,"priority":0,"local":false,"limits":{"daily":{"totl":2}},"version":1}`,
		"an editable-looking x": `{` + ok + `,"enable":true}`,
	} {
		if _, err := strictDecode(raw); err == nil {
			t.Errorf("%s: a strict decode accepted %s", name, raw)
		}
	}
	readOnly := `{` + ok + `,"kind":"notify","origin":"ui","secrets_set":{"password":true},"locked":{"channel":"env"},"stored":true,"updated":"2026-11-11T12:00:00Z"}`
	b, err := strictDecode(readOnly)
	if err != nil {
		t.Fatalf("the read-only keys of a GET answer were refused: %v", err)
	}
	// Accepted and never read: they reach no config.
	cfg, secretsIn, err := func() (integrations.InstanceConfig, map[string]secrets.Value, error) {
		body := b.body()
		return body.config(integrations.Ref{Kind: integrations.KindNotify, Instance: roofID}, body.Version)
	}()
	if err != nil || secretsIn != nil || cfg.Origin != "" || cfg.Locked != nil || cfg.Stored {
		t.Errorf("a read-only key reached the config: %+v %v %v", cfg, secretsIn, err)
	}
}

// TestIntegrationHTTP_ReplaceRequiresEveryField: a replace that leaves out
// name, enabled, priority, local or limits, or sends one as null, is a 422
// naming it, never a silent reset to a zero value.
func TestIntegrationHTTP_ReplaceRequiresEveryField(t *testing.T) {
	full := func() IntegrationInstanceReplace {
		b := signalBody("", "http://192.0.2.20/", "")
		b.Version = 1
		return replaceOf(b)
	}
	if err := (&updateReq{Kind: kindNotify, ID: roofID, Body: full()}).Validate(); err != nil {
		t.Fatalf("a complete body = %v", err)
	}
	drops := map[string]func(*IntegrationInstanceReplace){
		"name":     func(r *IntegrationInstanceReplace) { r.Name = nil },
		"enabled":  func(r *IntegrationInstanceReplace) { r.Enabled = nil },
		"priority": func(r *IntegrationInstanceReplace) { r.Priority = nil },
		"local":    func(r *IntegrationInstanceReplace) { r.Local = nil },
		"limits":   func(r *IntegrationInstanceReplace) { r.Limits = nil },
	}
	for field, drop := range drops {
		r := full()
		drop(&r)
		err := (&updateReq{Kind: kindNotify, ID: roofID, Body: r}).Validate()
		wantStatus(t, err, http.StatusUnprocessableEntity, "validation_failed")
		he, _ := errors.AsType[*httpapi.Error](err)
		if he == nil || len(he.Fields) != 1 || he.Fields[0].Field != "/body/"+field || he.Fields[0].Code != "required" {
			t.Errorf("without %s: %+v", field, he)
		}
	}
	// Absent and null are the same to a pointer: both are refused.
	var r IntegrationInstanceReplace
	raw := `{"name":"GCPD roof","enabled":true,"priority":0,"local":false,"limits":null,"version":1}`
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, (&updateReq{Kind: kindNotify, ID: roofID, Body: r}).Validate(), http.StatusUnprocessableEntity, "validation_failed")
}

// TestIntegrationHTTP_GetEditPutRoundTrip: the JSON GET answers, edited and
// sent back, is a valid PUT body, with and without limits overrides and with
// an operator-locked field whose value the view leaves out.
func TestIntegrationHTTP_GetEditPutRoundTrip(t *testing.T) {
	e := newEnv(t)
	ctx := adminCtx()
	plain := signalBody(roofID, "http://192.0.2.20/", "alfred-1")
	withLimits := signalBody("gcpd-basement", "http://192.0.2.21/", "alfred-2")
	withLimits.Limits = &IntegrationLimits{RPS: 3, Timeout: "9s"}
	e.create(t, ctx, kindNotify, plain)
	e.create(t, ctx, kindNotify, withLimits)

	for _, id := range []string{roofID, "gcpd-basement"} {
		blob := mustJSON(t, e.get(t, ctx, kindNotify, id))
		for _, key := range []string{`"kind"`, `"origin"`, `"secrets_set"`, `"stored"`, `"updated"`, `"limits"`} {
			if !strings.Contains(blob, key) {
				t.Errorf("%s: the answer lacks %s: %s", id, key, blob)
			}
		}
		// The answer is a valid body for the strict route.
		body, err := strictDecode(blob)
		if err != nil {
			t.Fatalf("%s: a strict decode refused the answer: %v", id, err)
		}
		name := "edited " + id
		body.Name = &name
		req := updateReq{Kind: kindNotify, ID: id, Body: body}
		if err := req.Validate(); err != nil {
			t.Fatalf("%s: the answer sent back is invalid: %v", id, err)
		}
		got, err := e.api.updateInstance(ctx, in(ctx, req))
		if err != nil {
			t.Fatalf("%s: PUT of the answer: %v", id, err)
		}
		if got.Name != name || !got.SecretsSet["password"] {
			t.Errorf("%s: after the round trip: %+v", id, got)
		}
	}
	// {} limits stored nothing: the instance still inherits.
	if cfg, _ := e.reg.Config(integrations.Ref{Kind: integrations.KindNotify, Instance: roofID}); cfg.Limits != nil {
		t.Errorf("limits {} stored %+v, want none", cfg.Limits)
	}
	if cfg, _ := e.reg.Config(integrations.Ref{Kind: integrations.KindNotify, Instance: "gcpd-basement"}); cfg.Limits == nil || cfg.Limits.RPS != 3 {
		t.Errorf("limits were not kept: %+v", cfg.Limits)
	}

	// A locked field holding a URL with userinfo is left out of the view and
	// the answer still round-trips.
	const leaked = "http://robin:grayson@192.0.2.30/"
	if err := e.reg.SetOverlay(context.Background(), overlay("integrations.notify.gcpd-roof.channel", leaked)); err != nil {
		t.Fatal(err)
	}
	cur := e.get(t, ctx, kindNotify, roofID)
	blob := mustJSON(t, cur)
	assertNoLeak(t, "a locked field's URL", blob, "robin", "grayson")
	if _, ok := cur.Settings["channel"]; ok || cur.Locked["channel"] == "" {
		t.Errorf("channel = %v, locked %v: want it left out of settings and still marked locked", cur.Settings["channel"], cur.Locked)
	}
	body, err := strictDecode(blob)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&updateReq{Kind: kindNotify, ID: roofID, Body: body}).Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.api.updateInstance(ctx, in(ctx, updateReq{Kind: kindNotify, ID: roofID, Body: body})); err != nil {
		t.Errorf("PUT of an answer with a withheld locked value: %v", err)
	}
	list, err := e.api.listInstances(ctx, in(ctx, listInstancesReq{Kind: kindNotify, Limit: 50}))
	if err != nil {
		t.Fatal(err)
	}
	assertNoLeak(t, "the list", mustJSON(t, list), "robin", "grayson")
}

// TestInstanceView_WithholdsUnsafeLockedAndURLFields: a field an operator
// value locks, and a URL-typed field, is left out WHOLE when any part of its
// value could carry a URL's credentials, whatever the container. A field that
// is neither is never screened.
func TestInstanceView_WithholdsUnsafeLockedAndURLFields(t *testing.T) {
	type named string
	const ok = "https://192.0.2.40/"
	cycleMap := map[string]any{}
	cycleMap["self"] = cycleMap
	var cyclePtr any
	cyclePtr = &cyclePtr
	deep := func(n int) any {
		var v any = "x"
		for range n {
			v = []any{v}
		}
		return v
	}
	var deepPtr any = "x" // a chain of pointers, which only the reflect walk follows
	for range 100 {
		p := new(any)
		*p = deepPtr
		deepPtr = p
	}
	var wide any = []any{"x"}
	for range 40 { // 2^40 nodes if walked as a tree: the node budget ends it
		wide = []any{wide, wide}
	}
	bad := map[string]any{
		"url":       "https://alfred:hunter2@192.0.2.40:8443/path?x=1",
		"relative":  "//alfred:hunter2@192.0.2.40/",
		"unparsed":  "http://alfred:hunter2@192.0.2.40:badport/",
		"second":    "note //x https://dick:grayson1@192.0.2.43/",
		"third":     "a // b // c // https://tim:drake1@192.0.2.44",
		"spaced":    "https://harvey:dent1 two@192.0.2.45/",
		"backslash": `https:\\selina:kyle1@192.0.2.46/`,
		"query":     "https://192.0.2.40/path?next=//jim:gordon1@192.0.2.47",
		"mapstring": map[string]string{"ok": ok, "bad": "https://a:mapstring1@192.0.2.48/"},
		"keystring": map[string]string{"https://k:keyed1@192.0.2.49/": "v"},
		"maplist":   []map[string]any{{"ok": ok}, {"bad": "https://a:maplist1@192.0.2.50/"}},
		"namedstr":  named("https://a:named1@192.0.2.51/"),
		"ptr":       func() *string { s := "https://a:ptr1@192.0.2.52/"; return &s }(),
		"bytes":     []byte("https://a:bytes1@192.0.2.53/"),
		"intkey":    map[int]string{1: "https://a:intkey1@192.0.2.54/"},
		"anykey":    map[string]any{"ok": ok, "https://k:anykey2@192.0.2.60/": 1}, // only the key is bad
		"intclean":  map[int]bool{1: true},                                        // a clean map that is not keyed by strings
		"refkey":    map[string]int{"https://k:refkey1@192.0.2.59/": 1},           // a credential in a key the type switch does not name
		"struct":    struct{ U string }{U: "https://a:struct1@192.0.2.55/"},
		"fn":        func() {},
		"nested":    map[string]any{"ok": ok, "hook": "https://oracle:secret1@192.0.2.40/x", "https://k:anykey1@192.0.2.56/": 1},
		"list":      []any{"https://192.0.2.41/", "https://a:listed1@192.0.2.42/"},
		"strings":   []string{"https://192.0.2.41/", "https://a:strings1@192.0.2.42/"},
		"typed":     []named{"https://192.0.2.41/", "https://a:typed1@192.0.2.42/"},
		"deep":      deep(100),
		"deepptr":   deepPtr,
		"cyclemap":  cycleMap,
		"cycleptr":  cyclePtr,
		"wide":      wide,
	}
	good := map[string]any{
		"clean":     "https://192.0.2.40/path",
		"text":      "ask the commissioner about it @ noon",
		"email":     "alfred@wayne.example",
		"plainport": "http://192.0.2.40:8080/",
		"number":    float64(3),
		"flag":      true,
		"nothing":   nil,
		"cleanmap":  map[string]string{"a": ok},
		"cleanlist": []any{ok, map[string]any{"b": []string{ok}}},
		"shallow":   deep(10),
	}
	settings := map[string]any{}
	locked := map[string]string{}
	for k, v := range bad {
		settings[k], locked[k] = v, "env"
	}
	for k, v := range good {
		settings[k], locked[k] = v, "env"
	}
	// Unlocked fields are not screened, however they read.
	settings["template"] = "🎧 {{title}} // @{{narrator}}"
	settings["unlocked-map"] = map[string]any{"bad": "https://a:unlocked1@192.0.2.57/"}
	// A URL-typed field is screened though no operator value locks it.
	settings["uri-bad"] = "https://a:uribad1@192.0.2.58/"
	settings["uri-ok"] = ok
	// A URL-typed field is judged by the Registry's rule (url.Parse, userinfo),
	// not by the text check: a "@" after the authority is not userinfo.
	settings["uri-next"] = "https://192.0.2.40/cb?next=https://u@x.example"
	settings["uri-slashes"] = "https://192.0.2.40/a//b@c"
	settings["uri-unparsed"] = "http://192.0.2.40:badport/" // does not parse: withheld
	settings["uri-list"] = []any{ok, "https://192.0.2.40/b"}
	// A locked field of unknown type gets the aggressive text check instead.
	settings["locked-next"] = "https://192.0.2.40/cb?next=https://u@x.example"
	locked["locked-next"] = "env"
	cfg := integrations.InstanceConfig{
		Ref:      integrations.Ref{Kind: integrations.KindNotify, Instance: "x"},
		Settings: settings, Locked: locked,
	}
	v := instanceView(cfg, &fieldInfo{secret: map[string]bool{}, uri: map[string]bool{"uri-bad": true, "uri-ok": true, "uri-next": true, "uri-slashes": true, "uri-unparsed": true, "uri-list": true}})
	blob := mustJSON(t, v)
	assertNoLeak(t, "the view", blob, "hunter2", "alfred:", "secret1", "grayson1", "drake1", "dent1", "kyle1", "gordon1", "mapstring1",
		"keyed1", "refkey1", "anykey2", "maplist1", "named1", "ptr1", "bytes1", "intkey1", "struct1", "typed1", "anykey1", "listed1", "strings1", "uribad1")
	for k := range bad {
		if _, ok := v.Settings[k]; ok {
			t.Errorf("%s was answered: %v", k, v.Settings[k])
		}
	}
	for k, want := range good {
		got, ok := v.Settings[k]
		if !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %v (present %v), want %v kept as stored", k, got, ok, want)
		}
	}
	if v.Settings["template"] != "🎧 {{title}} // @{{narrator}}" || v.Settings["unlocked-map"] == nil || v.Settings["uri-ok"] != ok {
		t.Errorf("an unscreened or clean field was dropped: %v", v.Settings)
	}
	if _, ok := v.Settings["uri-bad"]; ok {
		t.Error("a URL-typed field holding userinfo was answered")
	}
	for _, k := range []string{"uri-next", "uri-slashes", "uri-list"} {
		if _, ok := v.Settings[k]; !ok {
			t.Errorf("%s (a URL with no userinfo) was withheld", k)
		}
	}
	for _, k := range []string{"uri-unparsed", "locked-next"} {
		if _, ok := v.Settings[k]; ok {
			t.Errorf("%s was answered: %v", k, v.Settings[k])
		}
	}
	if len(v.Locked) != len(locked) {
		t.Errorf("locked = %v: the lock reasons are still answered", v.Locked)
	}
}

// TestSchemaFields reads the secret and URL-typed properties of a schema.
func TestSchemaFields(t *testing.T) {
	e := newEnv(t)
	for m := range e.reg.Adapters(integrations.KindNotify) {
		secret, uri := schemaFields(m)
		if !secret["password"] || secret["url"] || !uri["url"] || uri["password"] || uri["channel"] {
			t.Errorf("batsignal: secret %v, uri %v", secret, uri)
		}
	}
	// A URL list is URL-typed through its items; a plain list and a string
	// field of another format are not; an unreadable schema yields none.
	secret, uri := schemaFieldsOf(json.RawMessage(`{"properties":{
		"hooks":{"type":"array","items":{"type":"string","format":"uri"}},
		"names":{"type":"array","items":{"type":"string"}},
		"mail":{"type":"string","format":"email"},
		"home":{"type":"string","format":"uri"},
		"key":{"type":"string","x-mediacore-secret":true}}}`))
	if !uri["hooks"] || !uri["home"] || uri["names"] || uri["mail"] || uri["key"] || !secret["key"] || len(secret) != 1 || len(uri) != 2 {
		t.Errorf("secret %v, uri %v", secret, uri)
	}
	if s, u := schemaFieldsOf(json.RawMessage(`{`)); s != nil || u != nil {
		t.Errorf("an unreadable schema: %v %v", s, u)
	}
	var none integrations.Meta
	if s, u := schemaFields(none); len(s) != 0 || len(u) != 0 {
		t.Errorf("an empty schema: %v %v", s, u)
	}
}

// TestIntegrationHTTP_UnlockedTemplateRoundTrips: free text in an unlocked
// field is answered as stored, "// @" included, and a GET-edit-PUT keeps it.
func TestIntegrationHTTP_UnlockedTemplateRoundTrips(t *testing.T) {
	const template = "🎧 {{title}} // @{{narrator}}"
	e := newEnv(t)
	ctx := adminCtx()
	b := signalBody(roofID, "http://192.0.2.20/", "alfred-1")
	b.Settings["channel"] = template
	if got := e.create(t, ctx, kindNotify, b); got.Settings["channel"] != template {
		t.Errorf("create answered channel = %v", got.Settings["channel"])
	}
	cur := e.get(t, ctx, kindNotify, roofID)
	if cur.Settings["channel"] != template {
		t.Fatalf("GET answered channel = %v", cur.Settings["channel"])
	}
	body, err := strictDecode(mustJSON(t, cur))
	if err != nil {
		t.Fatal(err)
	}
	name := "renamed"
	body.Name = &name
	got, err := e.api.updateInstance(ctx, in(ctx, updateReq{Kind: kindNotify, ID: roofID, Body: body}))
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := e.reg.Config(integrations.Ref{Kind: integrations.KindNotify, Instance: roofID})
	if got.Settings["channel"] != template || cfg.Settings["channel"] != template {
		t.Errorf("after the round trip channel = %v (answer), %v (stored)", got.Settings["channel"], cfg.Settings["channel"])
	}
}

// TestIntegrationHTTP_LockedContainerWithCredentialRoundTrips: an operator
// value that locks an object holding one credential URL is left out of GET
// whole, and the answer still round-trips (a field that kept only some of its
// elements would be a 409 field_locked).
func TestIntegrationHTTP_LockedContainerWithCredentialRoundTrips(t *testing.T) {
	e := newEnv(t)
	ctx := adminCtx()
	e.create(t, ctx, kindNotify, signalBody(roofID, "http://192.0.2.20/", "alfred-1"))
	raw := func(path, js string) iter.Seq2[config.Path, config.OperatorValue] {
		return func(yield func(config.Path, config.OperatorValue) bool) {
			yield(config.Path(path), config.OperatorValue{Value: json.RawMessage(js), Src: config.SourceEnv, By: "env " + path})
		}
	}
	if err := e.reg.SetOverlay(context.Background(), raw("integrations.notify.gcpd-roof.extra",
		`{"ok":"https://192.0.2.40/","bad":"https://robin:grayson@192.0.2.30/"}`)); err != nil {
		t.Fatal(err)
	}
	cur := e.get(t, ctx, kindNotify, roofID)
	if cur.Locked["extra"] == "" {
		t.Fatalf("extra is not locked: %+v", cur.Locked)
	}
	assertNoLeak(t, "the view", mustJSON(t, cur), "robin", "grayson", "192.0.2.40")
	if _, ok := cur.Settings["extra"]; ok {
		t.Errorf("extra was answered in part: %v", cur.Settings["extra"])
	}
	body, err := strictDecode(mustJSON(t, cur))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.api.updateInstance(ctx, in(ctx, updateReq{Kind: kindNotify, ID: roofID, Body: body})); err != nil {
		t.Errorf("PUT of the answer: %v", err)
	}
}

// TestCarriesUserinfo pins the check on its own: every "//" is read, and
// neither whitespace nor a backslash hides an authority.
func TestCarriesUserinfo(t *testing.T) {
	for s, want := range map[string]bool{
		"https://u:p@h/":            true,
		"//u@h":                     true,
		"note //x https://u:p@h":    true, // the second "//"
		"//x //y //u:p@h":           true,
		"https:///u:p@h":            true, // overlapping "//": the second one opens "u:p@h"
		"////u:p@h":                 true, // overlapping "//" are all read
		"https://u:p with space@h/": true,
		`https:\\u:p@h`:             true,
		"https://h/path?x=//u:p@h":  true,
		"https://h/path":            false,
		"mail me@h":                 false,
		"https://h/a@b":             false, // the "@" is in the path
		"a:b@c":                     false,
		"":                          false,
		"//":                        false,
	} {
		if got := carriesUserinfo(s); got != want {
			t.Errorf("carriesUserinfo(%q) = %v, want %v", s, got, want)
		}
	}
}

// TestIntegrationHTTP_PermissionBeforeExistence: PUT and the instance test
// turn away a caller without core:integrations.manage with 403 whether or not
// the instance exists, so a 404 says nothing to such a caller.
func TestIntegrationHTTP_PermissionBeforeExistence(t *testing.T) {
	e := newEnv(t)
	e.create(t, adminCtx(), kindNotify, signalBody(roofID, "http://192.0.2.20/", "alfred-1"))
	reader := integrationstest.As(context.Background(), auth.MethodSession, "core:integrations.read")
	noPerms := &auth.Principal{Method: auth.MethodSession}
	body := signalBody("", "http://192.0.2.20/", "")
	body.Version = 1
	for _, id := range []string{roofID, "no-such-instance"} {
		for who, p := range map[string]*auth.Principal{"reader": mustPrincipal(reader), "no checker": noPerms} {
			_, err := e.api.updateInstance(reader, &httpapi.In[updateReq]{Req: updateReq{Kind: kindNotify, ID: id, Body: replaceOf(body)}, Principal: p})
			if err == nil {
				t.Fatalf("%s updated %s", who, id)
			}
			wantStatus(t, err, http.StatusForbidden, "permission_denied")
			_, err = e.api.testInstance(reader, &httpapi.In[testInstanceReq]{Req: testInstanceReq{Kind: kindNotify, ID: id, Body: body}, Principal: p})
			if err == nil {
				t.Fatalf("%s tested %s", who, id)
			}
			wantStatus(t, err, http.StatusForbidden, "permission_denied")
		}
	}
	if len(e.out.Sent()) != 0 {
		t.Errorf("a refused caller sent %d requests", len(e.out.Sent()))
	}
}

func mustPrincipal(ctx context.Context) *auth.Principal {
	p, _ := auth.PrincipalFrom(ctx)
	return p
}

// ---- small things ---------------------------------------------------------

// TestListAdapters_KindMessageNamesEveryKind: the message of a bad ?kind= is
// built from the kinds there are.
func TestListAdapters_KindMessageNamesEveryKind(t *testing.T) {
	err := (&listAdaptersReq{Kind: "gadgets"}).Validate()
	wantStatus(t, err, http.StatusUnprocessableEntity, "validation_failed")
	he, _ := errors.AsType[*httpapi.Error](err)
	if he == nil || len(he.Fields) != 1 {
		t.Fatalf("err = %+v", he)
	}
	msg := he.Fields[0].Message
	for _, k := range integrations.Kinds() {
		if !strings.Contains(msg, string(k)) {
			t.Errorf("the message %q omits kind %s", msg, k)
		}
	}
	if want := "must be one of " + kindList(); msg != want {
		t.Errorf("message = %q, want %q", msg, want)
	}
}

// TestIntegrationHTTP_ScopedTestAuditNamesStoredAdapter: the record of a test
// of a stored instance names that instance's adapter even when the body leaves
// the adapter out; an unscoped test names the body's.
func TestIntegrationHTTP_ScopedTestAuditNamesStoredAdapter(t *testing.T) {
	e := newEnv(t)
	ctx := adminCtx()
	e.create(t, ctx, kindNotify, signalBody(roofID, "http://192.0.2.20/", "alfred-1"))
	scoped := signalBody("", "http://192.0.2.20/", "")
	scoped.Adapter = ""
	if _, err := e.testScoped(ctx, kindNotify, roofID, scoped); err != nil {
		t.Fatal(err)
	}
	if _, err := e.testUnscoped(ctx, kindNotify, signalBody("", "http://192.0.2.78/", "alfred-3")); err != nil {
		t.Fatal(err)
	}
	var got []string
	for rec, err := range e.audit.Query(ctx, auditFilterTest()) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprint(rec.Details["scoped"], " ", rec.Details["adapter"]))
	}
	slices.Sort(got)
	if want := []string{"false " + string(batsignal), "true " + string(batsignal)}; !slices.Equal(got, want) {
		t.Errorf("recorded (scoped adapter) = %q, want %q", got, want)
	}
}

// TestInstanceView_UnknownAdapterScreensEveryString: every string-valued
// setting of an instance whose adapter this binary lacks is judged as a
// URL-typed field is (parse-based), wherever it sits.
func TestInstanceView_UnknownAdapterScreensEveryString(t *testing.T) {
	cfg := integrations.InstanceConfig{
		Ref: integrations.Ref{Kind: integrations.KindNotify, Instance: "x"},
		Settings: map[string]any{
			"url":    "http://u:p@192.0.2.40/",
			"other":  "https://a:b@192.0.2.40",
			"list":   []any{"https://192.0.2.40/", "https://c:d@192.0.2.40/"},
			"nested": map[string]any{"hook": "https://e:f@192.0.2.40/"},
			"note":   "hello // @world", // not a URL with userinfo: kept
			"clean":  "https://192.0.2.40/cb?next=https://u@x.example",
			"num":    float64(5),
		},
	}
	v := instanceView(cfg, nil)
	for _, k := range []string{"url", "other", "list", "nested"} {
		if _, ok := v.Settings[k]; ok {
			t.Errorf("%s was answered: %v", k, v.Settings[k])
		}
	}
	for _, k := range []string{"note", "clean", "num"} {
		if _, ok := v.Settings[k]; !ok {
			t.Errorf("%s was withheld", k)
		}
	}
}

// TestIntegrationHTTP_UrlTypedFalsePositivesRoundTrip: a URL with no userinfo
// is answered and round-trips unchanged even when a later "@" or "//" would
// have tripped a text check.
func TestIntegrationHTTP_UrlTypedFalsePositivesRoundTrip(t *testing.T) {
	e := newEnv(t)
	ctx := adminCtx()
	for id, url := range map[string]string{
		"gcpd-roof":     "http://192.0.2.20/cb?next=https://u@x.example",
		"gcpd-basement": "http://192.0.2.21/a//b@c",
	} {
		e.create(t, ctx, kindNotify, signalBody(id, url, "alfred-1"))
		cur := e.get(t, ctx, kindNotify, id)
		if cur.Settings["url"] != url {
			t.Fatalf("%s: GET answered url = %v, want %q", id, cur.Settings["url"], url)
		}
		body, err := strictDecode(mustJSON(t, cur))
		if err != nil {
			t.Fatal(err)
		}
		name := "renamed " + id
		body.Name = &name
		got, err := e.api.updateInstance(ctx, in(ctx, updateReq{Kind: kindNotify, ID: id, Body: body}))
		if err != nil {
			t.Fatalf("%s: round trip: %v", id, err)
		}
		cfg, _ := e.reg.Config(integrations.Ref{Kind: integrations.KindNotify, Instance: integrations.InstanceID(id)})
		if got.Settings["url"] != url || cfg.Settings["url"] != url || e.secretValue(t, kindNotify, id, "password") != "alfred-1" {
			t.Errorf("%s: after the round trip url = %v (answer), %v (stored)", id, got.Settings["url"], cfg.Settings["url"])
		}
	}
}

// TestIntegrationHTTP_LegacyUserinfoURLIsWithheldAndNeverClearedByOmission: a
// stored URL with userinfo (a row from before the Registry refused it) is left
// out of GET, and a PUT that omits it is a 422 withheld_field: the stored
// URL and the stored secrets are untouched. A PUT that enters a clean URL
// succeeds and keeps the secrets.
func TestIntegrationHTTP_LegacyUserinfoURLIsWithheldAndNeverClearedByOmission(t *testing.T) {
	const legacy = "http://waynecorp:hunter2@192.0.2.20/"
	e := newEnv(t)
	ctx := adminCtx()
	e.create(t, ctx, kindNotify, signalBody(roofID, "http://192.0.2.20/", "alfred-1"))
	ref := integrations.Ref{Kind: integrations.KindNotify, Instance: roofID}

	// Rewrite the stored row as an older build would have left it.
	store := integrations.NativeInstanceStore(e.ks)
	rows, err := store.Load(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %v, %v", rows, err)
	}
	row := rows[0]
	row.Config.Settings = map[string]any{"url": legacy, "channel": "gcpd", "password": row.Config.Settings["password"]}
	b := e.ks.NewBatch()
	if _, err := store.Put(context.Background(), b, row); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(context.Background(), storage.Sync); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	e.reg = e.registry(t)
	e.api.reg = e.reg

	cur := e.get(t, ctx, kindNotify, roofID)
	assertNoLeak(t, "the view", mustJSON(t, cur), "waynecorp", "hunter2")
	if _, ok := cur.Settings["url"]; ok || cur.Settings["channel"] != "gcpd" || !cur.SecretsSet["password"] {
		t.Fatalf("view = %+v", cur)
	}
	before, _ := e.reg.Config(ref)

	// A rename that sends the answer back omits the URL: refused, nothing changes.
	body, err := strictDecode(mustJSON(t, cur))
	if err != nil {
		t.Fatal(err)
	}
	name := "renamed"
	body.Name = &name
	_, err = e.api.updateInstance(ctx, in(ctx, updateReq{Kind: kindNotify, ID: roofID, Body: body}))
	wantStatus(t, err, http.StatusUnprocessableEntity, "withheld_field")
	he, _ := errors.AsType[*httpapi.Error](err)
	if he == nil || len(he.Fields) != 1 || he.Fields[0].Field != "/body/settings/url" || he.Fields[0].Code != "withheld_field" {
		t.Errorf("fields = %+v", he)
	}
	assertNoLeak(t, "the 422", errBlob(t, err), "waynecorp", "hunter2")
	after, _ := e.reg.Config(ref)
	if after.Settings["url"] != legacy || after.Version != before.Version || after.Name != before.Name {
		t.Errorf("the refused PUT changed the instance: %+v -> %+v", before, after)
	}
	if e.secretValue(t, kindNotify, roofID, "password") != "alfred-1" {
		t.Error("the refused PUT touched the stored secret")
	}

	// An explicit null names the field: that is a clear the caller asked for.
	// (Not sent here: it would clear the URL, which is what null means.)

	// Entering a clean URL succeeds, and the secret stays: the origin is unchanged.
	body.Settings = map[string]any{"url": "http://192.0.2.20/", "channel": "gcpd"}
	got, err := e.api.updateInstance(ctx, in(ctx, updateReq{Kind: kindNotify, ID: roofID, Body: body}))
	if err != nil {
		t.Fatalf("PUT with a clean URL: %v", err)
	}
	if got.Settings["url"] != "http://192.0.2.20/" || got.Name != name || !got.SecretsSet["password"] {
		t.Errorf("after the clean PUT: %+v", got)
	}
	if e.secretValue(t, kindNotify, roofID, "password") != "alfred-1" {
		t.Error("the clean PUT lost the stored secret")
	}
}
