// file: core/kernel/updater_test.go
// version: 0.1.0
// guid: 0717c1ca-d3a0-4377-8d98-64f0fe175dd7
// last-edited: 2026-11-22

package kernel

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/updater"
)

// upSource is a release source whose Latest fails with err and which holds no
// assets: the constructors under test never download.
type upSource struct{ err error }

func (s upSource) Latest(context.Context, updater.Channel) (updater.Release, error) {
	return updater.Release{}, s.err
}

func (upSource) Open(context.Context, updater.Asset) (io.ReadCloser, int64, error) {
	return nil, -1, errors.New("no assets")
}

type upRestarter struct{}

func (upRestarter) Restart(context.Context, string) error { return nil }

func upAssetName(r updater.Release, goos, goarch string) string {
	return "batcave-" + goos + "-" + goarch
}

func upInputs() updaterInputs {
	return updaterInputs{AppID: "gotham", BinaryName: "batmobile", Source: upSource{err: errors.New("the Batcave is dark")}, AssetName: upAssetName, DefaultMode: updater.ModeCheck}
}

func upServices(t *testing.T) updaterServices {
	t.Helper()
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ks, ok := bootAuditDB(t, updaterOwnerSpec()).Keyspace(updaterOwnerID)
	if !ok {
		t.Fatal("no c/updater/ keyspace")
	}
	return updaterServices{
		Restarter: upRestarter{}, Events: bus, Store: ks,
		ReaderCheck: func(context.Context, string, string) error { return nil },
	}
}

// updaterOptions carries every input through: a dropped field is a wrong app
// id, a user agent without the binary, a source nobody asked for, the default
// asset namer or a mode other than the app's.
func TestUpdaterOptions_CarriesEveryInput(t *testing.T) {
	in := upInputs()
	o := updaterOptions(in)
	if o.AppID != "gotham" || o.BinaryName != "batmobile" || o.Source != in.Source || o.DefaultMode != updater.ModeCheck {
		t.Fatalf("options = %+v, want the inputs", o)
	}
	if o.AssetName == nil || o.AssetName(updater.Release{}, "linux", "arm64") != "batcave-linux-arm64" {
		t.Fatal("the asset namer was not carried")
	}
	// An app with no namer keeps core's default: the constructor adds none.
	in.AssetName = nil
	if o := updaterOptions(in); o.AssetName != nil {
		t.Fatal("a nil asset namer became a non-nil one")
	}
}

// HTTP and Notify are optional (HEAD parity while F7-09 is open); the four
// services an apply cannot do without are not, and a keyspace of another owner
// is refused.
func TestUpdaterDeps_NilHTTPAndNotifyAccepted_RequiredRefused(t *testing.T) {
	s := upServices(t)
	if s.HTTP != nil || s.Notify != nil {
		t.Fatal("the fixture sets HTTP or Notify")
	}
	d, err := updaterDeps(s)
	if err != nil {
		t.Fatalf("nil HTTP and Notify refused: %v", err)
	}
	if d.HTTP != nil || d.Notify != nil || d.Restarter == nil || d.Events != s.Events || d.Store.Owner() != updaterOwnerID || d.ReaderCheck == nil {
		t.Fatalf("deps = %+v, want the services carried and HTTP/Notify nil", d)
	}

	cases := map[string]func(*updaterServices){
		"no restarter":    func(s *updaterServices) { s.Restarter = nil },
		"no event bus":    func(s *updaterServices) { s.Events = nil },
		"no keyspace":     func(s *updaterServices) { s.Store = storage.Keyspace{} },
		"no reader check": func(s *updaterServices) { s.ReaderCheck = nil },
		"another owner":   func(s *updaterServices) { s.Store = otherOwnerKeyspace(t) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := upServices(t)
			mutate(&s)
			if _, err := updaterDeps(s); err == nil {
				t.Fatal("the services were accepted")
			}
		})
	}
}

func otherOwnerKeyspace(t *testing.T) storage.Keyspace {
	t.Helper()
	ks, ok := bootAuditDB(t, auditOwnerSpec()).Keyspace(auditOwnerID)
	if !ok {
		t.Fatal("no c/audit/ keyspace")
	}
	return ks
}

// The updater builds with nil HTTP and Notify (I10) and reports the running
// version; refusals of core's New (no version, no source) come through.
func TestNewKernelUpdater_NilHTTPAndNotify(t *testing.T) {
	u, err := newKernelUpdater(upInputs(), upServices(t), updater.WithVersion("v1.2.3"))
	if err != nil {
		t.Fatal(err)
	}
	st := u.Status()
	if st.Current != "v1.2.3" || st.Mode != updater.ModeCheck {
		t.Fatalf("status = %+v, want the running version and the app's default mode", st)
	}
	if _, err := newKernelUpdater(upInputs(), upServices(t)); !errors.Is(err, updater.ErrNoVersion) {
		t.Errorf("no version: err = %v, want ErrNoVersion", err)
	}
	bad := upInputs()
	bad.Source = nil
	if _, err := newKernelUpdater(bad, upServices(t), updater.WithVersion("v1.2.3")); err == nil {
		t.Error("a nil Source was accepted")
	}
	s := upServices(t)
	s.Restarter = nil
	if _, err := newKernelUpdater(upInputs(), s, updater.WithVersion("v1.2.3")); err == nil {
		t.Error("missing services were accepted")
	}
}

// The topic spec is core.update.found.v1, observable by core:system.manage,
// and coreTopics (which carries it since the flip) declares on a bus once, with
// no clash against core's own topics.
func TestUpdaterTopicSpecs(t *testing.T) {
	specs := updaterTopicSpecs()
	if len(specs) != 1 || specs[0].Name != "core.update.found.v1" || specs[0].Notify == nil {
		t.Fatalf("specs = %+v, want core.update.found.v1 with notify metadata", specs)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := bus.Declare("core", coreTopics()...); err != nil {
		t.Fatalf("declaring core's topics with the updater's: %v", err)
	}
	if _, ok := bus.Catalog().Lookup("core.update.found.v1"); !ok {
		t.Fatal("the catalog lacks core.update.found.v1 after declaring it")
	}
}

// The owner is the c/updater/ keyspace and nothing else, and its one key is
// writable.
func TestUpdaterOwnerSpec(t *testing.T) {
	spec := updaterOwnerSpec()
	if spec.Owner != "core/updater" || !spec.Spec.Keyspace || !spec.Enabled {
		t.Fatalf("updaterOwnerSpec = %+v, want an enabled core/updater keyspace", spec)
	}
	if len(spec.Spec.Claims)+len(spec.Spec.Migrations)+len(spec.Spec.Serialized) != 0 {
		t.Errorf("the owner carries claims, migrations or serialized prefixes: %+v", spec.Spec)
	}
	db := bootAuditDB(t, spec)
	ks, ok := db.Keyspace(updaterOwnerID)
	if !ok || ks.Prefix() != "c/updater/" {
		t.Fatalf("keyspace = %q (present %v), want c/updater/", ks.Prefix(), ok)
	}
	if err := ks.Set(t.Context(), []byte("seen"), []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if got := keysOf(t, db, updaterOwnerID); !slices.Equal(got, []string{"seen"}) {
		t.Fatalf("keys = %v, want [seen]", got)
	}
}

// A nil updater is no source (the component is left out); a real one is the
// updater's probe, which registerCoreComponents registers as core.updater.
func TestUpdaterHealthSource(t *testing.T) {
	if updaterHealthSource(nil) != nil {
		t.Fatal("a nil updater yielded a probe")
	}
	reg := health.NewRegistry()
	if _, err := registerCoreComponents(reg, coreComponentSources{Updater: updaterHealthSource(nil)}); err != nil {
		t.Fatal(err)
	}
	if _, ok := states(reg)[componentUpdater]; ok {
		t.Fatal("core.updater registered without an updater")
	}

	u, err := newKernelUpdater(upInputs(), upServices(t), updater.WithVersion("v1.2.3"))
	if err != nil {
		t.Fatal(err)
	}
	reg = health.NewRegistry()
	if _, err := registerCoreComponents(reg, coreComponentSources{Updater: updaterHealthSource(u)}); err != nil {
		t.Fatal(err)
	}
	c, ok := states(reg)[componentUpdater]
	if !ok {
		t.Fatal("core.updater is not registered")
	}
	// ModeCheck with no check yet and no error is Up; a check that failed
	// is Degraded check_failed, read through the same source.
	if c.Result.Status != health.StatusUp {
		t.Fatalf("core.updater = %+v, want Up", c.Result)
	}
	if _, err := u.Check(t.Context()); err == nil {
		t.Fatal("a check against a source with no release succeeded")
	}
	if res := updaterHealthSource(u)(t.Context()); res.Status != health.StatusDegraded || res.Reason != "check_failed" {
		t.Fatalf("after a failed check: %+v, want Degraded check_failed", res)
	}
}

// upExecutor records the one start.
type upExecutor struct {
	calls int
	kind  tasks.Kind
	req   tasks.StartRequest
	who   *auth.Principal
	err   error
}

func (e *upExecutor) Start(ctx context.Context, kind tasks.Kind, req tasks.StartRequest) (tasks.RunRef, error) {
	e.calls++
	e.kind, e.req = kind, req
	e.who, _ = auth.PrincipalFrom(ctx)
	if e.err != nil {
		return tasks.RunRef{}, e.err
	}
	return tasks.RunRef{ID: "run-lucius-fox"}, nil
}

func upPrincipalCtx(id string) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{Method: auth.MethodSession, User: &auth.User{ID: auth.UserID(id)}})
}

// Each route's start enqueues its own kind, live and manual, with its
// parameters, as the request's principal; a requester who is not that
// principal, a missing executor and an executor's refusal all fail.
func TestUpdaterStarter(t *testing.T) {
	ctx := upPrincipalCtx("alfred")
	ex := &upExecutor{}
	st := newUpdaterStarter(ex)

	cases := []struct {
		name   string
		start  func() (string, error)
		kind   tasks.Kind
		params string
	}{
		{"check", func() (string, error) { return st.StartCheck(ctx, "alfred") }, updater.KindCheck, "{}"},
		{"apply", func() (string, error) { return st.StartApply(ctx, updater.ApplyParams{Version: "v9.9.9"}, "alfred") }, updater.KindApply, `{"version":"v9.9.9"}`},
		{"rollback", func() (string, error) { return st.StartRollback(ctx, "alfred") }, updater.KindRollback, "{}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			*ex = upExecutor{}
			id, err := c.start()
			if err != nil || id != "run-lucius-fox" {
				t.Fatalf("start = %q, %v", id, err)
			}
			if ex.kind != c.kind || ex.req.Mode != tasks.ModeRequestLive || ex.req.Trigger != tasks.TriggerManual || string(ex.req.Params) != c.params {
				t.Fatalf("started %s %+v, want %s live manual %s", ex.kind, ex.req, c.kind, c.params)
			}
			if ex.who == nil || string(ex.who.User.ID) != "alfred" {
				t.Fatalf("started as %+v, want the request's principal", ex.who)
			}
		})
	}

	refusals := map[string]func() error{
		"another requester": func() error { _, err := st.StartCheck(ctx, "harvey"); return err },
		"empty requester":   func() error { _, err := st.StartApply(upPrincipalCtx(""), updater.ApplyParams{}, ""); return err },
		"no principal":      func() error { _, err := st.StartRollback(context.Background(), "alfred"); return err },
	}
	for name, f := range refusals {
		t.Run(name, func(t *testing.T) {
			*ex = upExecutor{}
			if err := f(); !errors.Is(err, errUpdaterActor) || ex.calls != 0 {
				t.Fatalf("err = %v with %d starts, want errUpdaterActor and none", err, ex.calls)
			}
		})
	}
	if _, err := newUpdaterStarter(nil).StartCheck(ctx, "alfred"); err == nil {
		t.Error("a start without an executor succeeded")
	}
	boom := errors.New("executor stopped")
	if _, err := newUpdaterStarter(&upExecutor{err: boom}).StartCheck(ctx, "alfred"); !errors.Is(err, boom) {
		t.Errorf("executor refusal: err = %v, want it passed through", err)
	}
}

// updaterRoutes is every route of r under /api/core/v1/updater, keyed
// "METHOD pattern".
func updaterRoutes(r *httpapi.Router) map[string]httpapi.RouteInfo {
	out := map[string]httpapi.RouteInfo{}
	for ri := range r.Routes() {
		if strings.Contains(ri.Pattern, "/updater") {
			out[ri.Method+" "+ri.Pattern] = ri
		}
	}
	return out
}

// mountUpdater mounts the four updater routes on a core group, with the
// permissions the spec gives them; nil group and nil updater refuse. Calling
// it here, on a test router, proves the function works; boot never calls it.
func TestMountUpdater_MountsTheFourRoutes(t *testing.T) {
	u, err := newKernelUpdater(upInputs(), upServices(t), updater.WithVersion("v1.2.3"))
	if err != nil {
		t.Fatal(err)
	}
	r := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{})
	if err := mountUpdater(r.CoreGroup(), u, newUpdaterStarter(&upExecutor{}), nil); err != nil {
		t.Fatal(err)
	}
	got := updaterRoutes(r)
	want := map[string]string{
		"GET /api/core/v1/updater":           "core:system.manage",
		"POST /api/core/v1/updater/check":    "core:system.manage",
		"POST /api/core/v1/updater/apply":    "core:admin",
		"POST /api/core/v1/updater/rollback": "core:admin",
	}
	if len(got) != len(want) {
		t.Fatalf("mounted %d updater routes, want %d: %v", len(got), len(want), got)
	}
	for key, perm := range want {
		ri, ok := got[key]
		if !ok {
			t.Errorf("route %s is not mounted (have %v)", key, got)
			continue
		}
		if ps := ri.Perm.Permissions(); len(ps) != 1 || string(ps[0]) != perm {
			t.Errorf("%s requires %v, want %s", key, ps, perm)
		}
		if ri.Owner != "core" {
			t.Errorf("%s owner = %q, want core", key, ri.Owner)
		}
	}
	if !got["POST /api/core/v1/updater/apply"].Guards.Interactive || got["POST /api/core/v1/updater/check"].Guards.Interactive {
		t.Error("apply must need an interactive session and check must not")
	}

	if err := mountUpdater(nil, u, nil, nil); err == nil {
		t.Error("a nil group was accepted")
	}
	if err := mountUpdater(httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{}).CoreGroup(), nil, nil, nil); err == nil {
		t.Error("a nil updater was accepted")
	}
}
