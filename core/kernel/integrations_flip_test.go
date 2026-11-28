// file: core/kernel/integrations_flip_test.go
// version: 0.1.0
// guid: bd40b50e-b099-4020-90f0-b1a037830ff4
// last-edited: 2026-11-22

package kernel

import (
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/integrations/ai"
	"github.com/falkcorp/mediacore/integrations/notify"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
	"github.com/falkcorp/mediacore/internal/instancestore"
	"github.com/falkcorp/mediacore/library"
	"github.com/falkcorp/mediacore/storage"
)

// TestIntegrationsOwners_NoClaimsNoLevel: the flip's five owners are enabled
// keyspaces that declare no claim and no migration, so appending them to
// storageOwners raises no storage level and writes only their ledger entries.
func TestIntegrationsOwners_NoClaimsNoLevel(t *testing.T) {
	got := integrationsOwners()
	want := []storage.Owner{instancestore.Owner, providerhttp.Owner, ai.Owner, notify.Owner, library.Owner}
	var owners []storage.Owner
	for _, o := range got {
		owners = append(owners, o.Owner)
		if !o.Enabled || !o.Spec.Keyspace {
			t.Errorf("%s: Enabled=%v Keyspace=%v, want both", o.Owner, o.Enabled, o.Spec.Keyspace)
		}
		if len(o.Spec.Claims)+len(o.Spec.Migrations) != 0 {
			t.Errorf("%s declares claims or migrations: %+v", o.Owner, o.Spec)
		}
	}
	if !slices.Equal(owners, want) {
		t.Fatalf("owners = %v, want %v", owners, want)
	}
	if library.Owner == library.CoreOwner {
		t.Fatalf("library.Owner must not be the core caller identity %q", library.CoreOwner)
	}
	// The same specs open on a fresh store with no level step.
	db := bootAuditDB(t, got...)
	for _, o := range want {
		if _, ok := db.Keyspace(o); !ok {
			t.Errorf("no keyspace for %s after a boot", o)
		}
	}
}

// TestIntegrationsTopicSpecs: the health topic is bus-only, observed with
// core:integrations.read and notifiable at warning severity; the delivery topic
// is the durable notify one, declared here and nowhere else.
func TestIntegrationsTopicSpecs(t *testing.T) {
	specs := integrationsTopicSpecs()
	if len(specs) != 2 {
		t.Fatalf("%d topic specs, want 2", len(specs))
	}
	h, d := specs[0], specs[1]
	if h.Name != "core.integration.health.changed.v1" {
		t.Fatalf("health topic = %q", h.Name)
	}
	if h.Durable != nil {
		t.Error("the health topic is durable; a missed move is re-observed by the next probe")
	}
	if got := h.Permission.String(); got != "core:integrations.read" {
		t.Errorf("health permission = %s", got)
	}
	if h.Notify == nil || h.Notify.DefaultSeverity != "warning" {
		t.Errorf("health notify meta = %+v", h.Notify)
	}
	if d.Name != notify.DeliveryTopicName || d.Durable == nil {
		t.Errorf("delivery topic = %q durable=%v", d.Name, d.Durable != nil)
	}
	// The payload is three tokens and nothing a remote chose.
	var fields []string
	for f := range reflect.TypeFor[integrationHealthChanged]().Fields() {
		fields = append(fields, f.Tag.Get("json"))
	}
	if !slices.Equal(fields, []string{"kind", "instance", "status"}) {
		t.Errorf("health payload fields = %v", fields)
	}
}

// TestIntegrationsOwners_TopicsDeclareOnABus: the topics declare on a bus for core
// (the catalog the outbox and notify read), which is what P7 does for them.
func TestIntegrationsOwners_TopicsDeclareOnABus(t *testing.T) {
	_, _ = newIntegRig(t, nil) // Declare, Subscribe, Freeze and Open succeed
}

// TestLibrarySource: the adapter answers core.library.* keys with the JSON of
// the live section's fields, nested keys by walking the dotted path, and
// nothing for another key.
func TestLibrarySource(t *testing.T) {
	var set librarySettings
	set.Transfer.Workers = 3
	set.ProtectedPaths = []string{"/gotham/vault"}
	set.Mappings = []json.RawMessage{json.RawMessage(`{"source":"download:alfred","remote":"/dl/complete","local":{"root":"batcave","rel":"books"}}`)}
	live := librarySource{get: func() librarySettings { return set }}

	lookup := func(p string) (string, bool) {
		ks, ok := live.Lookup(config.Path(p))
		return string(ks.Effective), ok
	}
	if got, ok := lookup("core.library.transfer.workers"); !ok || got != "3" {
		t.Errorf("transfer.workers = %q, %v", got, ok)
	}
	if got, ok := lookup("core.library.protected_paths"); !ok || got != `["/gotham/vault"]` {
		t.Errorf("protected_paths = %q, %v", got, ok)
	}
	if got, ok := lookup("core.library.mappings"); !ok || got != `[{"source":"download:alfred","remote":"/dl/complete","local":{"root":"batcave","rel":"books"}}]` {
		t.Errorf("mappings = %q, %v", got, ok)
	}
	for _, p := range []string{"core.library.nope", "core.library.transfer.nope", "core.integrations.disabled", "core.library.", "gotham.library.roots"} {
		if got, ok := lookup(p); ok {
			t.Errorf("%s = %q, want no value", p, got)
		}
	}
	// A hot reload: the next Lookup reads the new section.
	set.Transfer.Workers = 5
	if got, _ := lookup("core.library.transfer.workers"); got != "5" {
		t.Errorf("after a change transfer.workers = %q, want 5", got)
	}
	if _, ok := (librarySource{}).Lookup("core.library.roots"); ok {
		t.Error("a source with no reader answered")
	}
}

// TestBuildIntegrations_LibraryReadsTheSection: a mapping in core.library
// reaches the Manager through the adapter at build time, and a later change
// reaches the live mapper after a Reload but not a mapper captured earlier.
func TestBuildIntegrations_LibraryReadsTheSection(t *testing.T) {
	mapping := func(remote string) json.RawMessage {
		return json.RawMessage(`{"source":"download:alfred","remote":"` + remote + `","local":{"root":"batcave","rel":"books"}}`)
	}
	r, in := newIntegRig(t, nil)
	r.setLibrary(func(v *librarySettings) { v.Mappings = []json.RawMessage{mapping("/dl/complete")} })
	s := r.build(in)

	live := liveMapper{m: s.library}
	captured := s.library.Mapper()
	if got := live.Mappings("download:alfred"); len(got) != 1 || got[0].Remote != "/dl/complete" {
		t.Fatalf("the Manager did not read core.library.mappings at build: %+v", got)
	}

	r.setLibrary(func(v *librarySettings) {
		v.Mappings = []json.RawMessage{mapping("/dl/complete"), mapping("/dl/incomplete")}
	})
	if err := s.library.(library.Reloader).Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := live.Mappings("download:alfred"); len(got) != 2 {
		t.Fatalf("the live mapper did not follow the Reload: %+v", got)
	}
	if got := captured.Mappings("download:alfred"); len(got) != 1 {
		t.Fatalf("the mapper captured before the Reload changed (%d mappings): a Reload replaces the mapper, which is why the pool needs the live one", len(got))
	}
}

// TestConfigAdapters: the section-to-options converters keep zero values zero
// (a constructor defaults them) and carry set values across.
func TestConfigAdapters(t *testing.T) {
	if c := aiConfig(aiSettings{}); c.KindLimits != nil {
		t.Errorf("zero ai section gave kind limits %v", c.KindLimits)
	}
	var a aiSettings
	a.KindLimits.Text = 2
	if c := aiConfig(a); c.KindLimits[ai.KindText] != 2 || len(c.KindLimits) != 1 {
		t.Errorf("ai kind limits = %v", c.KindLimits)
	}
	if c := notifyConfig(notifySettings{MaxAttempts: 4}); c.MaxAttempts != 4 {
		t.Errorf("notify max_attempts = %d", c.MaxAttempts)
	}
}

// TestNotifyConsumer_IsTheUnorderedDeliverConsumer: core registers the
// service's own consumer for core.notify.deliver, and it is unordered: a
// channel that is down must not hold another channel's entries behind it. The
// outbox exposes neither property, so they are read from the handler's
// registration by reflection.
func TestNotifyConsumer_IsTheUnorderedDeliverConsumer(t *testing.T) {
	r, in := newIntegRig(t, nil)
	s := r.build(in)
	h := notifyConsumer(s)
	reg := reflect.ValueOf(h).FieldByName("reg")
	if !reg.IsValid() || reg.IsNil() {
		t.Fatal("the handler carries no registration (the outbox's Handler changed shape)")
	}
	reg = reg.Elem()
	if got := reg.FieldByName("name").String(); got != notify.DeliverConsumer || got != coreOutboxConsumers()[0] {
		t.Errorf("consumer name = %q, want %q", got, notify.DeliverConsumer)
	}
	if got := reg.FieldByName("topic").String(); got != notify.DeliveryTopicName {
		t.Errorf("consumer topic = %q, want %q", got, notify.DeliveryTopicName)
	}
	if !reg.FieldByName("cfg").FieldByName("unordered").Bool() {
		t.Error("the delivery consumer is ordered: one dead channel would stall every other behind it")
	}
}

// ---- routes -------------------------------------------------------------

type routeWant struct {
	method, pattern, name, perm string
	rate                        httpapi.RateClass
	kind                        httpapi.RouteKind
}

// The access table of the F7-09 B3 brief, section 3, as literals: every route
// the flip mounts, its permission, and that none sets Guards.
var integRoutesWant = []routeWant{
	{"GET", "/api/core/v1/integrations/adapters", "listIntegrationAdapters", "core:integrations.read", httpapi.RateDefault, httpapi.RouteKindNative},
	{"GET", "/api/core/v1/integrations/{kind}/instances", "listIntegrationInstances", "core:integrations.read", httpapi.RateDefault, httpapi.RouteKindNative},
	{"POST", "/api/core/v1/integrations/{kind}/instances", "createIntegrationInstance", "core:integrations.manage", httpapi.RateDefault, httpapi.RouteKindNative},
	{"GET", "/api/core/v1/integrations/{kind}/instances/{id}", "getIntegrationInstance", "core:integrations.read", httpapi.RateDefault, httpapi.RouteKindNative},
	{"PUT", "/api/core/v1/integrations/{kind}/instances/{id}", "updateIntegrationInstance", "core:integrations.manage", httpapi.RateDefault, httpapi.RouteKindNative},
	{"DELETE", "/api/core/v1/integrations/{kind}/instances/{id}", "deleteIntegrationInstance", "core:integrations.manage", httpapi.RateDefault, httpapi.RouteKindNative},
	{"POST", "/api/core/v1/integrations/{kind}/test", "testIntegration", "core:integrations.manage", httpapi.RateExpensive, httpapi.RouteKindNative},
	{"POST", "/api/core/v1/integrations/{kind}/instances/{id}/test", "testIntegrationInstance", "core:integrations.manage", httpapi.RateExpensive, httpapi.RouteKindNative},
	{"GET", "/api/core/v1/integrations/{kind}/instances/{id}/health", "getIntegrationInstanceHealth", "core:integrations.read", httpapi.RateExpensive, httpapi.RouteKindNative},
	{"DELETE", "/api/core/v1/integrations/{kind}/instances/{id}/hold", "releaseIntegrationInstanceHold", "core:integrations.manage", httpapi.RateDefault, httpapi.RouteKindNative},
	{"GET", "/api/core/v1/library/browse", "browseLibrary", "core:library.browse", httpapi.RateDefault, httpapi.RouteKindRaw},
}

func newIntegRouter() *httpapi.Router {
	return httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{Log: slog.New(slog.DiscardHandler)})
}

func checkIntegRoutes(t *testing.T, r *httpapi.Router) {
	t.Helper()
	var got []routeWant
	for ri := range r.Routes() {
		if ri.Guards != (httpapi.Guards{}) {
			t.Errorf("%s %s sets guards %+v; the access table has none", ri.Method, ri.Pattern, ri.Guards)
		}
		if ri.Owner != "core" {
			t.Errorf("%s %s owner = %q", ri.Method, ri.Pattern, ri.Owner)
		}
		got = append(got, routeWant{ri.Method, ri.Pattern, ri.Name, ri.Perm.String(), ri.RateClass, ri.Kind})
	}
	if !slices.Equal(got, integRoutesWant) {
		t.Fatalf("mounted routes differ from the access table:\n got %v\nwant %v", got, integRoutesWant)
	}
}

// TestDescribeIntegrationRoutes: Describe's mount carries the eleven routes of
// the access table with their permissions and no guards.
func TestDescribeIntegrationRoutes(t *testing.T) {
	r := newIntegRouter()
	if err := describeIntegrationRoutes(r); err != nil {
		t.Fatal(err)
	}
	checkIntegRoutes(t, r)
}

// TestMountRoutes_LiveServicesCarryTheSameTable: the live mounts register the
// same table as Describe's, over the real services.
func TestMountRoutes_LiveServicesCarryTheSameTable(t *testing.T) {
	rig, in := newIntegRig(t, nil)
	s := rig.build(in)
	r := newIntegRouter()
	mountIntegrationRoutes(r.CoreGroup(), s)
	if err := mountLibraryRoutes(r.CoreGroup(), s.library, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	checkIntegRoutes(t, r)
}

// TestMountLibraryRoutes_Refusals: a mount without a Manager, or on a group
// that does not put the route under libraryhttp's base path, is refused.
func TestMountLibraryRoutes_Refusals(t *testing.T) {
	r := newIntegRouter()
	if err := mountLibraryRoutes(r.CoreGroup(), nil, nil); err == nil {
		t.Error("a nil Manager mounted")
	}
	var typed library.Manager = (*nilManager)(nil)
	if err := mountLibraryRoutes(r.CoreGroup(), typed, nil); err == nil {
		t.Error("a typed-nil Manager mounted")
	}
	sub := r.CoreGroup().Sub("/elsewhere")
	if err := mountLibraryRoutes(sub, library.Manager(nil), nil); err == nil {
		t.Error("a nil Manager mounted on a sub-group")
	}
	if err := mountLibraryBrowse(sub, emptyLibrary{}, nil); err == nil {
		t.Error("the library route mounted on a group that is not the core group")
	}
}

type nilManager struct{ library.Manager }

// TestRouteCollaborators_UntypedNilWhenAbsent: the route package fails closed
// on a nil hold releaser or audit logger, and only sees nil if it is an
// untyped one. Absent services are untyped nils; present ones are passed.
func TestRouteCollaborators_UntypedNilWhenAbsent(t *testing.T) {
	// typed nils inside the services
	s := &integrationsServices{in: integrationsInputs{Audit: (*audit.Store)(nil)}, providerhttp: (*providerhttp.Registry)(nil)}
	holds, rec := s.routeCollaborators()
	if holds != nil {
		t.Errorf("holds = %#v, want an untyped nil", holds)
	}
	if rec != nil {
		t.Errorf("audit = %#v, want an untyped nil", rec)
	}

	rig, in := newIntegRig(t, nil)
	live := rig.build(in)
	holds, rec = live.routeCollaborators()
	if holds == nil || rec == nil {
		t.Fatalf("live services gave holds=%v audit=%v", holds, rec)
	}
}

// TestBuildIntegrations_LibraryEntryErrorIsNotABootFailure: a root or mapping
// entry that does not decode is reported and skipped; the build still
// succeeds (05-library 5L.4 step 3).
func TestBuildIntegrations_LibraryEntryErrorIsNotABootFailure(t *testing.T) {
	r, in := newIntegRig(t, nil)
	r.setLibrary(func(v *librarySettings) { v.Mappings = []json.RawMessage{json.RawMessage(`"not a mapping"`)} })
	s := r.build(in)
	if s.library == nil {
		t.Fatal("no library Manager")
	}
	if !strings.Contains(r.logs.String(), "library roots loaded with errors") {
		t.Fatalf("the decode error was not reported:\n%s", r.logs.String())
	}
}
