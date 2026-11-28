// file: core/internal/integrationhttp/reach_perkind_f709_test.go
// version: 0.1.0
// guid: 5c1d8e37-92a4-4b60-a3f8-0e6b7d2c41a9
// last-edited: 2026-11-28

package integrationhttp

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/rbac"
)

// F7-09 round 2: the per-kind cells. One route serves every kind, so the rule
// that differs by kind is the handler's lookup (probeReach, kindreach.go); these
// cells run each of the five kinds through the real routes as the principals of
// the reach table (docs/plans/platform-core/f7-09-integrations-per-kind-reach.md)
// and pin every row of it.

// kindFixture is one kind's adapter and a body for it.
type kindFixture struct {
	kind    integrations.Kind
	adapter integrations.AdapterID
	// health and test are what core answers for each probe of the kind.
	health, test probeWant
}

// probeWant is the table's row for one kind and probe: served, or the 404 and the
// legacy route it names ("" for a kind with none) and what core waits for.
type probeWant struct {
	served        bool
	legacy, until string
}

func (k kindFixture) want(p probe) probeWant {
	if p == probeTest {
		return k.test
	}
	return k.health
}

var (
	servedWant  = probeWant{served: true}
	delugeUntil = "the per-kind co-requirement lands"
)

func kindFixtures() []kindFixture {
	return []kindFixture{
		{kind: integrations.KindAI, adapter: sage, health: servedWant, test: servedWant},
		{kind: integrations.KindDownload, adapter: seeder,
			health: probeWant{legacy: "GET /api/v1/deluge/status", until: delugeUntil},
			test:   probeWant{legacy: "POST /api/v1/deluge/test-connection", until: delugeUntil}},
		{kind: integrations.KindIndexer, adapter: finder},
		{kind: integrations.KindMetadata, adapter: oracle,
			health: probeWant{legacy: "GET /api/v1/metadata/providers/throttles", until: "a core route can express library.view (books:view)"},
			test:   servedWant},
		{kind: integrations.KindNotify, adapter: batsignal, health: servedWant, test: servedWant},
	}
}

// body is a non-secret body for the kind, id empty for a replace or an unsaved test.
func (k kindFixture) body(id string) IntegrationInstanceBody {
	if k.kind == integrations.KindNotify {
		return signalBody(id, "http://192.0.2.30/", "")
	}
	return IntegrationInstanceBody{
		ID: id, Adapter: string(k.adapter), Name: "Gotham " + string(k.kind), Enabled: truth(),
		Settings: map[string]any{"host": "192.0.2.30", "port": float64(8080)},
	}
}

func (k kindFixture) base() string { return "/integrations/" + string(k.kind) }

// allKindsEnv is an env with an adapter of every kind and a stored instance
// "gotham-<kind>" of each.
func allKindsEnv(t *testing.T) *env {
	t.Helper()
	e := newEnvKinds(t, true)
	for _, k := range kindFixtures() {
		e.create(t, adminCtx(), string(k.kind), k.body("gotham-"+string(k.kind)))
	}
	return e
}

func instanceOf(k kindFixture) string { return "gotham-" + string(k.kind) }

// TestKindReach_EveryKindIsDecided is the exhaustiveness cell: every kind in
// integrations.Kinds() has a case in probeReach (known) for both probes, and the
// decision is the table's. A kind added to integrations.Kinds() without a row in
// the per-kind table and a case here fails by name.
func TestKindReach_EveryKindIsDecided(t *testing.T) {
	fixtures := map[integrations.Kind]kindFixture{}
	for _, k := range kindFixtures() {
		fixtures[k.kind] = k
	}
	for _, kind := range integrations.Kinds() {
		fx, ok := fixtures[kind]
		if !ok {
			t.Errorf("kind %s has no fixture: add it to the per-kind table and to kindFixtures", kind)
			continue
		}
		for _, p := range []probe{probeHealth, probeTest} {
			r := probeReach(kind, p)
			if !r.known {
				t.Errorf("probeReach(%s, %d) has no case: decide it in the per-kind table", kind, p)
			}
			w := fx.want(p)
			if r.served != w.served {
				t.Errorf("probeReach(%s, %d).served = %v, want %v", kind, p, r.served, w.served)
			}
			if r.served && r.req.String() != instancePerm().String() {
				t.Errorf("probeReach(%s, %d) asks %s, want the instance requirement %s", kind, p, r.req, instancePerm())
			}
			if r.legacy != w.legacy || r.until != w.until {
				t.Errorf("probeReach(%s, %d) names legacy %q until %q, want %q until %q", kind, p, r.legacy, r.until, w.legacy, w.until)
			}
		}
	}
	if len(fixtures) != len(integrations.Kinds()) {
		t.Errorf("%d fixtures for %d kinds", len(fixtures), len(integrations.Kinds()))
	}
	if r := probeReach(integrations.Kind("gadgets"), probeHealth); r.known || r.served {
		t.Errorf("a kind that is none is known or served: %+v", r)
	}
}

// probeCall is one probe request to a kind.
type probeCall struct {
	name, method, path string
	body               func() any
	probe              probe
}

func probeCalls(k kindFixture) []probeCall {
	id := instanceOf(k)
	return []probeCall{
		{name: "health", method: http.MethodGet, path: k.base() + "/instances/" + id + "/health", probe: probeHealth},
		{name: "test of an instance", method: http.MethodPost, path: k.base() + "/instances/" + id + "/test", probe: probeTest,
			body: func() any { return k.body("") }},
		{name: "test of an unsaved body", method: http.MethodPost, path: k.base() + "/test", probe: probeTest,
			body: func() any { return k.body("") }},
	}
}

// TestReach_HealthAndTestPerKind runs the health and the two test routes of every
// kind as the principals that reach the handler (the floor holders). For ai and
// notify, and for metadata's test, the settings atom (as a session and as an API key) and
// core:admin reach it and the integrations atom is refused by the backstop; for
// download and indexer every one of them gets the 404 (download's names the
// legacy route that serves it), core:admin included. A caller below the floor is
// the router's 403 for every kind, whatever the kind.
func TestReach_HealthAndTestPerKind(t *testing.T) {
	atFloor := []reachCaller{reachSettingsAtom, reachSettingsKey, reachAdminOnly, reachIntegrationsAtom, reachIntegrationsKey, reachNativeTwoScopes}
	belowFloor := []reachCaller{reachReadOnly, reachSettingsScopeOnly, reachNoScope}
	passes := func(c reachCaller) bool { return c.principal(reachCatalog(t)).Perms.Satisfies(instancePerm()) }
	for _, k := range kindFixtures() {
		for _, call := range probeCalls(k) {
			t.Run(string(k.kind)+"/"+call.name, func(t *testing.T) {
				e := allKindsEnv(t)
				for _, c := range atFloor {
					rec := reachDo(t, reachRouter(t, e, c, nil), call.method, call.path, call.body)
					switch {
					case !k.want(call.probe).served:
						if rec.Code != http.StatusNotFound {
							t.Errorf("as %s = %d %s, want 404: core does not serve %s %s", c.name, rec.Code, rec.Body.String(), k.kind, call.name)
							continue
						}
						p := problemOf(t, rec)
						if p.Code != "not_found" {
							t.Errorf("as %s: code %q, want not_found", c.name, p.Code)
						}
						if w := k.want(call.probe); w.legacy != "" {
							if !strings.Contains(p.Detail, w.legacy) || !strings.Contains(p.Detail, "until "+w.until) {
								t.Errorf("as %s: detail %q does not name %q until %q", c.name, p.Detail, w.legacy, w.until)
							}
						} else if strings.Contains(p.Detail, "legacy route") {
							t.Errorf("as %s: detail %q names a legacy route the kind does not have", c.name, p.Detail)
						}
					case passes(c):
						if rec.Code != http.StatusOK {
							t.Errorf("as %s = %d %s, want 200", c.name, rec.Code, rec.Body.String())
						}
					default:
						if rec.Code != http.StatusForbidden {
							t.Errorf("as %s = %d %s, want the backstop's 403", c.name, rec.Code, rec.Body.String())
							continue
						}
						wantBackstop(t, problemOf(t, rec), instancePerm())
					}
				}
				for _, c := range belowFloor {
					rec := reachDo(t, reachRouter(t, e, c, nil), call.method, call.path, call.body)
					if rec.Code != http.StatusForbidden {
						t.Errorf("below the floor, as %s = %d %s, want the router's 403", c.name, rec.Code, rec.Body.String())
						continue
					}
					if p := problemOf(t, rec); p.Required != string(integrations.ManagePerm) {
						t.Errorf("below the floor, as %s: required %q, want %s", c.name, p.Required, integrations.ManagePerm)
					}
				}
			})
		}
	}
}

// TestReach_DownloadHealthIs404ForEveryPrincipal is the S4a cell: core serves no
// health for kind=download, for any principal that passes stage 13, core:admin
// included, whether the instance exists or not (the 404 comes before the lookup,
// so it says nothing about which instances exist). The settings atom must not
// gain /deluge/status's reach, and the integrations atom keeps it on the legacy
// route.
func TestReach_DownloadHealthIs404ForEveryPrincipal(t *testing.T) {
	e := allKindsEnv(t)
	for _, c := range []reachCaller{reachAdminOnly, reachSettingsAtom, reachSettingsKey, reachIntegrationsAtom, reachIntegrationsKey, reachNativeTwoScopes, reachAuthOff} {
		for _, id := range []string{"gotham-download", "no-such-seeder"} {
			rec := reachDo(t, reachRouter(t, e, c, nil), http.MethodGet, "/integrations/download/instances/"+id+"/health", nil)
			if rec.Code != http.StatusNotFound {
				t.Errorf("as %s, %s: %d %s, want 404", c.name, id, rec.Code, rec.Body.String())
				continue
			}
			if d := problemOf(t, rec).Detail; !strings.Contains(d, "GET /api/v1/deluge/status") {
				t.Errorf("as %s, %s: detail %q does not name the legacy route", c.name, id, d)
			}
		}
	}
}

// TestReach_SettingsKeyWritesNonSecretConfigPerKind is the S4d cell: at HEAD a
// settings.manage API key writes every kind's non-secret config through
// PUT /config (internal/books boot probe, recorded in the per-kind table), so
// core must still let a non-interactive key create and replace an instance of
// every kind, and must not put Guards.Interactive on either route. The
// integrations atom as a key is refused for every kind. Secret fields are
// #5073's gate (admin and interactive), not asserted here: the bodies carry none.
func TestReach_SettingsKeyWritesNonSecretConfigPerKind(t *testing.T) {
	for ri := range registered(t).Routes() {
		if (ri.Name == "createIntegrationInstance" || ri.Name == "updateIntegrationInstance") && ri.Guards != (httpapi.Guards{}) {
			t.Errorf("%s has guards %+v: a settings-atom API key wrote this config at HEAD", ri.Name, ri.Guards)
		}
	}
	for _, k := range kindFixtures() {
		t.Run(string(k.kind), func(t *testing.T) {
			e := newEnvKinds(t, true)
			h := reachRouter(t, e, reachSettingsKey, nil)
			rec := reachDo(t, h, http.MethodPost, k.base()+"/instances", func() any { return k.body("batcave-" + string(k.kind)) })
			if rec.Code != http.StatusCreated {
				t.Fatalf("settings-atom key create = %d %s, want 201", rec.Code, rec.Body.String())
			}
			var got IntegrationInstance
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			put := func() any {
				b := k.body("")
				b.Name = "Gotham " + string(k.kind) + " moved"
				b.Version = got.Version
				return replaceOf(b)
			}
			if rec := reachDo(t, h, http.MethodPut, k.base()+"/instances/batcave-"+string(k.kind), put); rec.Code != http.StatusOK {
				t.Errorf("settings-atom key replace = %d %s, want 200", rec.Code, rec.Body.String())
			}
			rec = reachDo(t, reachRouter(t, e, reachIntegrationsKey, nil), http.MethodPost, k.base()+"/instances",
				func() any { return k.body("batcave-other-" + string(k.kind)) })
			if rec.Code != http.StatusForbidden {
				t.Errorf("integrations-atom key create = %d %s, want 403", rec.Code, rec.Body.String())
			}
			if _, ok := e.reg.Config(integrations.Ref{Kind: k.kind, Instance: integrations.InstanceID("batcave-other-" + string(k.kind))}); ok {
				t.Error("the refused create was applied")
			}
		})
	}
}

// TestReach_NotifyTestIsNotTheIntegrationsAtoms: HEAD has no webhook test route,
// so notify's test is a new "send a request to a target" path. The integrations
// atom (session and key) must not reach it, and the settings atom must (its
// reach equals its notify config writes). Kill cell for a probeReach that asks
// the floor alone for notify.
func TestReach_NotifyTestIsNotTheIntegrationsAtoms(t *testing.T) {
	e := allKindsEnv(t)
	k := kindFixtures()[4]
	if k.kind != integrations.KindNotify {
		t.Fatalf("fixture 4 is %s, want notify", k.kind)
	}
	for _, call := range probeCalls(k)[1:] {
		for _, c := range []reachCaller{reachIntegrationsAtom, reachIntegrationsKey} {
			rec := reachDo(t, reachRouter(t, e, c, nil), call.method, call.path, call.body)
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s as %s = %d %s, want 403", call.name, c.name, rec.Code, rec.Body.String())
			}
		}
		if rec := reachDo(t, reachRouter(t, e, reachSettingsAtom, nil), call.method, call.path, call.body); rec.Code != http.StatusOK {
			t.Errorf("%s as the settings atom = %d %s, want 200", call.name, rec.Code, rec.Body.String())
		}
	}
}

// TestReach_RouteDocsNameThePerKindRule: the routes' documentation is built from
// the lookup the backstop reads, so what the OpenAPI says is what is enforced. The
// expected texts are literal here, so a lookup that changed is a failure by name
// and not a doc that silently follows.
func TestReach_RouteDocsNameThePerKindRule(t *testing.T) {
	operator := rbac.OperatorRequirement().String()
	wantHealth := "ai asks " + operator +
		"; download is not served here (404), use GET /api/v1/deluge/status" +
		"; indexer is not served (404)" +
		"; metadata is not served here (404), use GET /api/v1/metadata/providers/throttles" +
		"; notify asks " + operator + "."
	wantTest := "ai asks " + operator +
		"; download is not served here (404), use POST /api/v1/deluge/test-connection" +
		"; indexer is not served (404)" +
		"; metadata asks " + operator +
		"; notify asks " + operator + "."
	if got := docFor(probeHealth); got != wantHealth {
		t.Errorf("docFor(health) = %q, want %q", got, wantHealth)
	}
	if got := docFor(probeTest); got != wantTest {
		t.Errorf("docFor(test) = %q, want %q", got, wantTest)
	}
	// And the routes carry them.
	r := registered(t)
	doc, err := r.OpenAPI("core")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{wantHealth, wantTest} {
		if !strings.Contains(string(raw), jsonEscaped(want)) {
			t.Errorf("the OpenAPI document does not carry %q", want)
		}
	}
}

// jsonEscaped is s as it appears inside a JSON string (json.Marshal escapes <, >
// and &).
func jsonEscaped(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}

// TestReach_UnknownKindIsA404BeforeAnyPermission: a kind that is none is the
// route's 404 for a caller at the floor, never a 403 that says what is asked.
func TestReach_UnknownKindIsA404BeforeAnyPermission(t *testing.T) {
	e := allKindsEnv(t)
	for _, c := range []reachCaller{reachIntegrationsAtom, reachSettingsAtom} {
		for _, path := range []string{"/integrations/gadgets/instances/x/health", "/integrations/gadgets/test"} {
			method := http.MethodGet
			var body func() any
			if strings.HasSuffix(path, "/test") {
				method, body = http.MethodPost, func() any { return signalBody("", "http://192.0.2.30/", "") }
			}
			if rec := reachDo(t, reachRouter(t, e, c, nil), method, path, body); rec.Code != http.StatusNotFound {
				t.Errorf("%s as %s = %d %s, want 404", path, c.name, rec.Code, rec.Body.String())
			}
		}
	}
}
