// file: core/httpapi/basicgate_mount_test.go
// version: 1.0.0
// guid: 5d3a7c1e-92b4-4f08-a6d1-7e0c2b9f4a13
// last-edited: 2026-11-19

package httpapi

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/secrets"
)

// gatedMountRouter is the fixture router of gcpdBasicRouterBuilt with the
// gcpd mount carrying gate as its LegacyMount.BasicGate, built.
func gatedMountRouter(t *testing.T, gate *BasicGateOptions) http.Handler {
	t.Helper()
	r := NewRouter(Options{}, Deps{})
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	for _, p := range []string{"GET /livez", "GET /readyz", "GET /metrics"} {
		r.Infra(p, rbac.Public, ok)
	}
	for _, c := range []string{"/api/", "/ping", "/status", "/login"} {
		if err := r.Claim("gcpd", c); err != nil {
			t.Fatal(err)
		}
	}
	r.AllowAuthMethods("gcpd", "gcpd.signal")
	routes := gcpdBasicRoutes()
	if err := r.MountLegacy(LegacyMount{Owner: "gcpd", Handler: newFakeGin(ginRoutesOf(routes)...), Auth: LegacyAuthGin,
		ServesSPA: true, TopSegments: []string{"ping", "status"}, SPAShared: []string{"login"},
		BasicExemptAssets: headBasicAssets, BasicGate: gate, Routes: manifestOf(routes...)}); err != nil {
		t.Fatal(err)
	}
	return mustBuild(t, r)
}

func gatePW(v string, err error) BasicGatePasswordFunc {
	return func(context.Context) (secrets.Value, error) {
		if err != nil {
			return secrets.Value{}, err
		}
		return secrets.NewValue(v), nil
	}
}

// TestBuild_BasicGateIsStage8OfTheOuterChain: a mount's BasicGate puts
// stage 8 around the whole router, between stages 7 and 9: a refused request
// is answered by the gate with HEAD's challenge, never reaches the router,
// and its 401 carries the headers the stages before it set.
func TestBuild_BasicGateIsStage8OfTheOuterChain(t *testing.T) {
	h := gatedMountRouter(t, &BasicGateOptions{
		Settings: settingsOf(true, "bwayne"), Password: gatePW("Wayne-Manor", nil), Realm: headRealm})

	// No credentials: refused, on a gated legacy route, a core-written 404 and a native infra route.
	for _, target := range []string{"/api/v1/cases/42", "/api/v1/nowhere", "/ping", "/metrics", "/"} {
		rec := basicRequest(h, http.MethodGet, target, "")
		if !refused(t, rec) {
			t.Errorf("GET %s without credentials = %d, want the gate's 401", target, rec.Code)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("GET %s: the gate's 401 lacks stage 5's security headers: %v", target, rec.Header())
		}
	}
	// Wrong user, wrong password, right pair.
	if !refused(t, basicRequest(h, http.MethodGet, "/api/v1/cases/42", "alfred:Wayne-Manor")) {
		t.Error("wrong user passed the gate")
	}
	if !refused(t, basicRequest(h, http.MethodGet, "/api/v1/cases/42", "bwayne:Arkham")) {
		t.Error("wrong password passed the gate")
	}
	if rec := basicRequest(h, http.MethodGet, "/api/v1/cases/42", "bwayne:Wayne-Manor"); rec.Code == http.StatusUnauthorized {
		t.Errorf("right credentials refused: %d", rec.Code)
	}
	// The exempt set still passes unauthenticated.
	for _, target := range []string{"/livez", "/readyz", "/api/health", "/api/v1/items/42", "/assets/cowl.js"} {
		if rec := basicRequest(h, http.MethodGet, target, ""); rec.Code == http.StatusUnauthorized {
			t.Errorf("GET %s is exempt but the gate refused it", target)
		}
	}
}

// TestBuild_BasicGateDisabledPassesEverything and the fail-closed rows.
func TestBuild_BasicGateDisabledAndFailClosed(t *testing.T) {
	off := gatedMountRouter(t, &BasicGateOptions{Settings: settingsOf(false, "bwayne"),
		Password: gatePW("Wayne-Manor", nil), Realm: headRealm})
	if rec := basicRequest(off, http.MethodGet, "/api/v1/cases/42", ""); rec.Code == http.StatusUnauthorized {
		t.Fatalf("disabled gate refused: %d", rec.Code)
	}
	for name, pw := range map[string]BasicGatePasswordFunc{
		"resolver error":  gatePW("", errors.New("keyring offline")),
		"not set":         gatePW("", secrets.ErrNotSet),
		"key unavailable": gatePW("", secrets.ErrKeyUnavailable),
		"empty value":     gatePW("", nil),
		"error with an value": func(context.Context) (secrets.Value, error) {
			return secrets.NewValue("Wayne-Manor"), errors.New("torn read")
		},
	} {
		h := gatedMountRouter(t, &BasicGateOptions{Settings: settingsOf(true, "bwayne"), Password: pw, Realm: headRealm})
		for _, creds := range []string{"", "bwayne:Wayne-Manor", "bwayne:"} {
			if !refused(t, basicRequest(h, http.MethodGet, "/api/v1/cases/42", creds)) {
				t.Errorf("%s: creds %q passed an enabled gate with no usable password (must fail closed)", name, creds)
			}
		}
		if rec := basicRequest(h, http.MethodGet, "/api/health", ""); rec.Code == http.StatusUnauthorized {
			t.Errorf("%s: the exempt health route was refused", name)
		}
	}
}

// TestBuild_BasicGateNeverLogsTheCredentials: whatever the gate or the
// router logs on a refusal or an unusable password, no credential appears.
func TestBuild_BasicGateNeverLogsTheCredentials(t *testing.T) {
	var buf bytes.Buffer
	lg := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r := NewRouter(Options{}, Deps{Log: lg})
	routes := gcpdBasicRoutes()
	for _, c := range []string{"/api/", "/ping", "/status", "/login"} {
		if err := r.Claim("gcpd", c); err != nil {
			t.Fatal(err)
		}
	}
	r.AllowAuthMethods("gcpd", "gcpd.signal")
	if err := r.MountLegacy(LegacyMount{Owner: "gcpd", Handler: newFakeGin(ginRoutesOf(routes)...), Auth: LegacyAuthGin,
		ServesSPA: true, TopSegments: []string{"ping", "status"}, SPAShared: []string{"login"},
		BasicGate: &BasicGateOptions{Settings: settingsOf(true, "bwayne"), Password: gatePW("Wayne-Manor", nil),
			Realm: headRealm, Log: lg}, Routes: manifestOf(routes...)}); err != nil {
		t.Fatal(err)
	}
	h := mustBuild(t, r)
	for _, creds := range []string{"bwayne:Wayne-Manor", "bwayne:Penguin-Pass", "robin:Wayne-Manor", ""} {
		basicRequest(h, http.MethodGet, "/api/v1/cases/42", creds)
	}
	for _, secret := range []string{"Wayne-Manor", "Penguin-Pass", "bwayne:", "Ym53YXluZTo"} {
		if strings.Contains(buf.String(), secret) {
			t.Errorf("log carries %q:\n%s", secret, buf.String())
		}
	}
}

// TestBuild_BasicGateInputsAreChecked: a mount's BasicGate that cannot be
// built (no Settings, no Password, a realm that breaks the quoted-string) or
// two mounts carrying one fail Build, and leave no gate facts behind.
func TestBuild_BasicGateInputsAreChecked(t *testing.T) {
	build := func(gates ...*BasicGateOptions) error {
		r := NewRouter(Options{}, Deps{})
		for i, g := range gates {
			owner := []string{"gcpd", "arkham"}[i]
			if err := r.Claim(owner, "/"+owner+"/"); err != nil {
				t.Fatal(err)
			}
			if err := r.MountLegacy(LegacyMount{Owner: owner, Handler: newFakeGin(), Auth: LegacyAuthGin,
				TopSegments: []string{owner}, BasicGate: g, Routes: manifestOf()}); err != nil {
				t.Fatal(err)
			}
		}
		_, err := r.Build()
		return err
	}
	good := func() *BasicGateOptions {
		return &BasicGateOptions{Settings: settingsOf(true, ""), Password: gatePW("x", nil)}
	}
	if err := build(good()); err != nil {
		t.Fatalf("a valid gate failed Build: %v", err)
	}
	if err := build(&BasicGateOptions{Password: gatePW("x", nil)}); !errors.Is(err, errBasicGateInput) {
		t.Errorf("no Settings: err = %v", err)
	}
	if err := build(&BasicGateOptions{Settings: settingsOf(true, ""), Password: gatePW("x", nil), Realm: `Bat"cave`}); !errors.Is(err, errBasicGateInput) {
		t.Errorf("bad realm: err = %v", err)
	}
	if err := build(good(), good()); !errors.Is(err, errBasicGateInput) {
		t.Errorf("two mounts with a BasicGate: err = %v", err)
	}
}
