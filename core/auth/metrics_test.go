// file: core/auth/metrics_test.go
// version: 1.0.0
// guid: 9c4e7a21-b6d3-4f85-a1e0-3d8f5b2c7e96
// last-edited: 2026-11-11

package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"go.opentelemetry.io/otel/attribute"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/metrics/metricstest"
	"github.com/falkcorp/mediacore/rbac"
)

// authScope builds a Provider and the core "auth" scope the kernel gives the
// service (05-authn §9).
func authScope(t *testing.T) (*metrics.Provider, *metrics.Scope) {
	t.Helper()
	p, err := metrics.New(context.Background(), metrics.Options{AppID: "gotham", Prometheus: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	sc, err := p.Core("auth", attribute.Key("method"), "outcome", "surface", "imported", "reason")
	if err != nil {
		t.Fatal(err)
	}
	return p, sc
}

// sample is the value of family's series whose labels include want.
func sample(t *testing.T, g prometheus.Gatherer, family string, want map[string]string) float64 {
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
			for k, v := range want {
				if labels[k] != v {
					continue next
				}
			}
			return sampleOf(m)
		}
	}
	t.Fatalf("no series %s%v", family, want)
	return 0
}

func sampleOf(m *dto.Metric) float64 {
	switch {
	case m.GetCounter() != nil:
		return m.GetCounter().GetValue()
	case m.GetGauge() != nil:
		return m.GetGauge().GetValue()
	}
	return m.GetUntyped().GetValue()
}

// TestAuthSeries_Golden (05-authn §9, 05-metrics §5 item 5): the auth scope
// serves exactly the families of testdata/series.golden, and each moves with
// what it counts: stage 12 outcomes by method, throttle refusals by surface,
// bootstrap redemptions, Revalidate refusals by reason, and the live session
// and key counts.
func TestAuthSeries_Golden(t *testing.T) {
	ctx := context.Background()
	f := newFx(t)
	p, sc := authScope(t)
	admin := f.user("bruce-wayne", rbac.RoleAdmin)
	dick := f.user("dick-grayson", "gotham.detective")
	svc, err := auth.New(ctx, auth.Options{ImportedKeyPrefixes: []string{"abk_"}}, auth.Deps{
		Store: f.st, NewBatch: f.st.NewBatch, RBAC: f.rb, Audit: f.audit, Revocations: f.jrnl, Clock: f.clock,
		Hasher: f.hasher, Settings: f.liveSettings, Metrics: sc,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.svc = svc
	svc.SetLoginSleep(func(time.Duration) {})

	metricstest.AssertSeries(t, p, sc, "testdata/series.golden")
	g := p.Gatherer()
	attempts := func(method, outcome string) float64 {
		return sample(t, g, "mediacore_auth_attempts_total", map[string]string{"method": method, "outcome": outcome})
	}
	if attempts("api_key", "success") != 0 || attempts("session", "invalid") != 0 {
		t.Fatal("a counter does not start at zero")
	}

	// Stage 12: a live native key, a live imported key, a revoked key, a bad
	// key, a live session cookie, an expired session cookie.
	good, k := f.mint(f.session(admin), dick.ID, "gotham:patrol")
	imported := importKey(t, f, "key-redbird", dick.ID, auth.KeyActive, "gotham:patrol")
	deadRaw, dead := f.mint(f.session(admin), dick.ID, "gotham:patrol")
	if err := svc.SetAPIKeyStatus(ctx, f.session(admin), dead.ID, auth.KeyRevoked); err != nil {
		t.Fatal(err)
	}
	sess, err := svc.IssueSession(ctx, dick, auth.OriginPassword, false, from("192.0.2.70"))
	if err != nil {
		t.Fatal(err)
	}
	// A second native key, so the imported split is asymmetric; and a key
	// whose owner is sign-in restricted, refused as an account that may not
	// sign in.
	f.mint(f.session(admin), dick.ID, "gotham:patrol")
	oswald := f.user("oswald-cobblepot", "gotham.detective")
	restrictedRaw, _ := f.mint(f.session(admin), oswald.ID, "gotham:patrol")
	f.edit(f.reload(oswald.ID), func(u *auth.User) { u.SignInRestricted = true })
	serve(t, svc, request(good, ""))
	serve(t, svc, request(restrictedRaw, ""))
	serve(t, svc, request(imported, ""))
	serve(t, svc, request(deadRaw, ""))
	serve(t, svc, request("mck_not-a-key", ""))
	serve(t, svc, request("", sess.Token))
	_ = k
	for _, c := range []struct {
		method, outcome string
		want            float64
	}{
		{"api_key", "success", 2}, {"api_key", "inactive", 1}, {"api_key", "revoked", 1}, {"api_key", "invalid", 1}, {"session", "success", 1},
		{"session", "invalid", 0}, {"api_key", "expired", 0},
	} {
		if got := attempts(c.method, c.outcome); got != c.want {
			t.Errorf("attempts{%s,%s} = %v, want %v", c.method, c.outcome, got, c.want)
		}
	}

	// Gauges: one live session, two live keys (one imported), the revoked key
	// not counted. The first assertion above scanned an empty store and the
	// scan is cached for 30 s, so move the clock past it.
	f.advance(time.Minute)
	if got := sample(t, g, "mediacore_auth_sessions_active", nil); got != 1 {
		t.Errorf("sessions_active = %v", got)
	}
	if got := sample(t, g, "mediacore_auth_api_keys_active", map[string]string{"imported": "false"}); got != 3 {
		t.Errorf("api_keys_active{false} = %v", got)
	}
	if got := sample(t, g, "mediacore_auth_api_keys_active", map[string]string{"imported": "true"}); got != 1 {
		t.Errorf("api_keys_active{true} = %v", got)
	}

	// Throttling: failed sign-ins past the per-source budget are refused and
	// counted on the login surface.
	src := from("192.0.2.71")
	var refused bool
	for i := 0; i < auth.DefaultMaxFailuresPerSource+2 && !refused; i++ {
		_, err := svc.Login(ctx, auth.LoginInput{Username: "no-such-vigilante", Password: "wrong-password-1", Client: src})
		refused = err != nil && errorsIsThrottled(err)
	}
	if !refused {
		t.Fatal("the login throttle never refused")
	}
	if got := sample(t, g, "mediacore_auth_throttled_total", map[string]string{"surface": "login"}); got < 1 {
		t.Errorf("throttled{login} = %v", got)
	}
	if got := sample(t, g, "mediacore_auth_throttled_total", map[string]string{"surface": "invite"}); got != 0 {
		t.Errorf("throttled{invite} = %v", got)
	}

	// Bootstrap redemptions: a good token, a malformed one.
	if _, err := svc.RedeemBootstrap(ctx, f.bootToken(auth.DefaultBootstrapTokenTTL), from("192.0.2.72")); err != nil {
		t.Fatal(err)
	}
	_, _ = svc.RedeemBootstrap(ctx, "not-a-token", from("192.0.2.73"))
	if got := sample(t, g, "mediacore_auth_bootstrap_redemptions_total", map[string]string{"outcome": "success"}); got != 1 {
		t.Errorf("redemptions{success} = %v", got)
	}
	if got := sample(t, g, "mediacore_auth_bootstrap_redemptions_total", map[string]string{"outcome": "invalid"}); got != 1 {
		t.Errorf("redemptions{invalid} = %v", got)
	}

	// Revalidate: a stale epoch is counted, a store-healthy pass is not.
	pr := f.session(dick)
	pr.CredentialID = sess.PublicID
	if err := svc.Revalidate(ctx, pr); err != nil {
		t.Fatal(err)
	}
	f.bumpEpoch(dick.ID, dick.CredentialEpoch+1)
	if err := svc.Revalidate(ctx, pr); err == nil {
		t.Fatal("a stale principal revalidated")
	}
	if got := sample(t, g, "mediacore_auth_principal_invalidations_total", map[string]string{"reason": "epoch"}); got != 1 {
		t.Errorf("invalidations{epoch} = %v", got)
	}

	metricstest.AssertSeries(t, p, sc, "testdata/series.golden")
	metricstest.AssertNoOverflow(t, p)
}

// TestAuthMetrics_NilScopeRecordsNothing: a service built without a scope
// serves requests and counts nothing, without a panic.
func TestAuthMetrics_NilScopeRecordsNothing(t *testing.T) {
	f := newFx(t)
	admin := f.user("bruce-wayne", rbac.RoleAdmin)
	raw, _ := f.mint(f.session(admin), admin.ID, "core:admin")
	_, got := serve(t, f.svc, request(raw, ""))
	if got.p == nil {
		t.Fatal("the key did not authenticate")
	}
	serve(t, f.svc, request("mck_nope", ""))
	_ = f.svc.Revalidate(context.Background(), got.p)
}

func errorsIsThrottled(err error) bool { return errors.Is(err, auth.ErrThrottled) }
