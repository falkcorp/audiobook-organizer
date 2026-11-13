// file: core/integrations/notify/harness_test.go
// version: 1.0.0
// guid: 0f6b7a2e-91c4-4d38-b5a7-3e8d12c64f90
// last-edited: 2026-11-13

package notify_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/integrationstest"
	"github.com/falkcorp/mediacore/integrations/notify"
	"github.com/falkcorp/mediacore/integrations/notify/notifytest"
	"github.com/falkcorp/mediacore/internal/executor"
	"github.com/falkcorp/mediacore/internal/instancestore"
	"github.com/falkcorp/mediacore/internal/outboxstore"
	"github.com/falkcorp/mediacore/internal/taskstore"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// The tests open their store with storage.Open/Prepare/Migrate over MemFS,
// never coretest.NewStore: core/integrations/notify is in the kernel's import
// graph (the W27 rule), and coretest imports the kernel.

const storePath = "/batcave/main.pebble"

// The owners the kernel registers for the stores a rig joins.
const (
	secretsOwner storage.Owner = "core/secrets"
	auditOwner   storage.Owner = "core/audit"
)

// Fixture topics: Gotham raises signals, Wayne's vault trips (Batman names
// only; the repo is public).
const (
	signalName  = "gotham.signal.raised.v1"   // notifiable, default severity warning
	escapedName = "gotham.villain.escaped.v1" // an event with no notify metadata
	vaultName   = "wayne.vault.tripped.v1"    // notifiable, read needs wayne:view
	brokenName  = "gotham.channel.broken.v1"  // notifiable: the health-changed stand-in
)

// Signal is the payload of the fixture topics.
type Signal struct {
	Villain string `json:"villain"`
	Block   int    `json:"block"`
}

func signalTopic() events.Topic[Signal]  { return events.NewTopic[Signal](signalName) }
func escapedTopic() events.Topic[Signal] { return events.NewTopic[Signal](escapedName) }

// The channels the rules aim at.
var (
	commissioner = integrations.Ref{Kind: integrations.KindNotify, Instance: "commissioner"}
	alfred       = integrations.Ref{Kind: integrations.KindNotify, Instance: "alfred"}
	oracle       = integrations.Ref{Kind: integrations.KindNotify, Instance: "oracle"}
)

const batphone integrations.AdapterID = "gotham.batphone"

type batphoneSettings struct {
	Channel string `json:"channel,omitempty"`
}

// offsetClock is real time plus a settable offset: it keeps moving, so a
// retry scheduled on it still comes due, and a test can jump a day ahead
// without a day of waiting.
type offsetClock struct{ off atomic.Int64 }

func (c *offsetClock) now() time.Time { return time.Now().Add(time.Duration(c.off.Load())) }

func (c *offsetClock) advance(d time.Duration) { c.off.Add(int64(d)) }

// rigOpts shapes one rig; the zero value is the defaults with the relay running
// and no rules.
type rigOpts struct {
	notifyCfg    func(*notify.Config)
	noRelay      bool
	metrics      bool // build the notify scope (for the golden and the counters)
	disabled     *atomic.Bool
	noInstance   bool // create no channel instances
	store        notify.RuleStore
	outboxConfig func(*outbox.Config)
}

type rig struct {
	t        testing.TB
	db       *storage.DB
	notifyKS storage.Keyspace
	gothamKS storage.Keyspace
	outKS    storage.Keyspace
	taskKS   storage.Keyspace
	bus      *events.Bus
	ob       *outbox.Outbox
	x        tasks.Executor
	stores   tasks.Stores
	reg      *integrations.Registry
	svc      *notify.Service
	fake     *notifytest.FakeNotifier
	clock    *offsetClock
	mp       *metrics.Provider
	sc       *metrics.Scope

	cfgMu     sync.Mutex
	notifyCfg notify.Config
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func keyspace(t testing.TB, db *storage.DB, o storage.Owner) storage.Keyspace {
	t.Helper()
	ks, ok := db.Keyspace(o)
	if !ok {
		t.Fatalf("owner %s has no keyspace", o)
	}
	return ks
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func openDB(t testing.TB) *storage.DB {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path: storePath,
		Owners: []storage.OwnerSpec{
			{Owner: taskstore.Owner, Spec: taskstore.StorageSpec(), Enabled: true},
			{Owner: outboxstore.Owner, Spec: outboxstore.StorageSpec(), Enabled: true},
			{Owner: notify.Owner, Spec: notify.StorageSpec(), Enabled: true},
			{Owner: instancestore.Owner, Spec: instancestore.StorageSpec(), Enabled: true},
			{Owner: secretsOwner, Spec: storage.Spec{Keyspace: true, Serialized: []string{""}}, Enabled: true},
			{Owner: auditOwner, Spec: storage.Spec{Keyspace: true}, Enabled: true},
			{Owner: "gotham", Spec: storage.Spec{Keyspace: true}, Enabled: true},
		},
		Build: "coretest+fixture", FreeSpaceMargin: 1, FS: storage.FSOf(vfs.NewMem()),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		_ = o.Close()
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func schemaOf[T any](t testing.TB) config.Schema {
	t.Helper()
	s, err := config.SchemaFor[T]()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The principals the cases write as.
// admin holds the manage and admin scopes and may see every fixture topic
// (integrationstest's Checker holds exactly what it is given: core:admin does
// not imply the module scopes the way the real checker does).
func admin() context.Context {
	return integrationstest.As(context.Background(), auth.MethodSession,
		integrationstest.Manage, integrationstest.Admin, "gotham:view", "wayne:view")
}

// managerWith holds core:integrations.manage and the extra permissions.
func managerWith(perms ...rbac.Permission) context.Context {
	return integrationstest.As(context.Background(), auth.MethodSession, append([]rbac.Permission{integrationstest.Manage}, perms...)...)
}

func nobody() context.Context {
	return integrationstest.As(context.Background(), auth.MethodSession)
}

// testOutboxConfig keeps every outbox clock short. The relay's rotation is set
// as long as the task kind allows (a little under a day: a run that lives
// past its deadline ends and must be started again), because the tests jump
// the injected clock while it runs; none jumps it by more than a few hours
// mid-run.
func testOutboxConfig() outbox.Config {
	return outbox.Config{
		MaxAttempts: 3, BackoffMin: time.Millisecond, BackoffMax: 4 * time.Millisecond, RelayWorkers: 4,
		RelayRotate: 100000 * time.Hour, RelayEnabled: true, HandlerTimeout: 5 * time.Second, IntakePoll: 5 * time.Millisecond,
		UnorderedParallelism: 4, OrphanRetention: 7 * 24 * time.Hour,
	}
}

func catalogWith(t testing.TB) *rbac.Catalog {
	t.Helper()
	cat, err := rbac.NewCatalog(rbac.CoreScopes()...)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

// newRig boots a store, an events bus with the fixture topics, an Outbox with
// the notify consumer registered, a Registry with the fake channel adapter
// and three channel instances, a notify Service and (unless noRelay) a
// running relay.
func newRig(t testing.TB, opts rigOpts) *rig {
	t.Helper()
	ctx := context.Background()
	db := openDB(t)
	r := &rig{
		t: t, db: db, notifyKS: keyspace(t, db, notify.Owner), gothamKS: keyspace(t, db, "gotham"),
		outKS: keyspace(t, db, outboxstore.Owner), taskKS: keyspace(t, db, taskstore.Owner),
		fake: &notifytest.FakeNotifier{}, clock: &offsetClock{},
	}
	if opts.notifyCfg != nil {
		opts.notifyCfg(&r.notifyCfg)
	}

	bus, err := events.NewBus(events.BusOptions{})
	must(t, err)
	must(t, bus.Declare("gotham",
		signalTopic().Spec(events.TopicSpec{Permission: rbac.Need("gotham:view"),
			Notify: &events.NotifyMeta{Title: "The signal is lit", DefaultSeverity: "warning"}}),
		escapedTopic().Spec(events.TopicSpec{Permission: rbac.Need("gotham:view")}),
		events.NewTopic[Signal](brokenName).Spec(events.TopicSpec{Permission: rbac.Need("gotham:view"),
			Notify: &events.NotifyMeta{Title: "A channel is broken", DefaultSeverity: "error"}}),
	))
	must(t, bus.Declare("wayne", events.NewTopic[Signal](vaultName).Spec(events.TopicSpec{Permission: rbac.Need("wayne:view"),
		Notify: &events.NotifyMeta{Title: "The vault tripped", DefaultSeverity: "error"}})))
	must(t, bus.Declare("core", notify.DeliveryTopicSpec()))
	bus.Freeze()
	r.bus = bus

	hreg := health.NewRegistry()
	rep, err := hreg.Register(health.Check{ID: outbox.ComponentID, Title: "Outbox"})
	must(t, err)
	inst, err := outbox.NewInstruments(nil)
	must(t, err)
	obCfg := testOutboxConfig()
	if opts.outboxConfig != nil {
		opts.outboxConfig(&obCfg)
	}
	ob, err := outbox.New(outbox.Options{
		Keyspace: r.outKS, Catalog: bus.Catalog(), CoreConsumers: []string{notify.DeliverConsumer}, Log: quietLogger(),
		Config: func() outbox.Config { return obCfg }, Now: r.clock.now, Health: rep, Instruments: inst,
	})
	must(t, err)
	r.ob = ob

	// The Registry: the real secrets and audit stores, the fake channel.
	root, err := os.OpenRoot(t.TempDir())
	must(t, err)
	t.Cleanup(func() { _ = root.Close() })
	sec, err := secrets.Open(ctx, secrets.Options{StateDir: root, Keyspace: keyspace(t, db, secretsOwner), AppID: "gotham", Now: r.clock.now})
	must(t, err)
	aks := keyspace(t, db, auditOwner)
	au, err := audit.New(ctx, audit.Options{}, audit.Deps{Keyspace: aks, Seq: aks.Sequence("seq"), Now: r.clock.now})
	must(t, err)
	iks := keyspace(t, db, instancestore.Owner)
	out := &integrationstest.Outbound{}
	ro := integrations.Options{
		Store: integrations.NativeInstanceStore(iks), NewBatch: iks.NewBatch, Secrets: sec, Audit: au,
		Outbound: out.Func(), Now: r.clock.now,
	}
	if opts.disabled != nil {
		ro.Settings = func() integrations.Settings { return integrations.Settings{Disabled: opts.disabled.Load()} }
	}
	reg, err := integrations.New(ro)
	must(t, err)
	must(t, reg.Register("gotham", r.fake.Descriptor(integrations.Meta{
		ID: batphone, Kind: integrations.KindNotify, DisplayName: "Bat-Phone", Settings: schemaOf[batphoneSettings](t),
	})))
	must(t, reg.Load(ctx, nil))
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = reg.Close(cctx)
	})
	r.reg = reg
	if !opts.noInstance {
		for _, ref := range []integrations.Ref{commissioner, alfred, oracle} {
			if _, err := reg.Put(admin(), integrations.InstanceConfig{
				Ref: ref, Adapter: batphone, Name: string(ref.Instance), Enabled: true, Settings: map[string]any{"channel": "gcpd"},
			}, nil); err != nil {
				t.Fatalf("Put %s: %v", ref, err)
			}
		}
	}

	var sc *metrics.Scope
	if opts.metrics {
		r.mp, err = metrics.New(ctx, metrics.Options{AppID: "gotham", Prometheus: true})
		must(t, err)
		t.Cleanup(func() { _ = r.mp.Shutdown(context.Background()) })
		sc, err = r.mp.Core("notify", "instance", "class", "adapter", "topic", "severity", "result")
		must(t, err)
		r.sc = sc
	}
	svc, err := notify.New(ctx, notify.Options{
		AppID: "batcave", Keyspace: r.notifyKS, Store: opts.store, Catalog: bus.Catalog(), Outbox: ob, Registry: reg,
		Config:  func() notify.Config { r.cfgMu.Lock(); defer r.cfgMu.Unlock(); return r.notifyCfg },
		Metrics: sc, Log: quietLogger(), Now: r.clock.now,
	})
	must(t, err)
	r.svc = svc
	must(t, ob.Register("core", svc.Consumer()))
	must(t, ob.Freeze())

	stores := tasks.NativeStores(r.taskKS)
	r.stores = stores
	x, err := executor.New(executor.Options{
		Stores: stores, Keyspace: &r.taskKS, Definitions: []tasks.Definition{ob.RelayDefinition()}, InstrumentsOff: true,
		Catalog: catalogWith(t), Logger: quietLogger(),
		Config: executor.Config{DispatchTick: 20 * time.Millisecond, WatchdogInterval: time.Hour,
			ProgressInterval: time.Nanosecond, MinCheckpointInterval: time.Nanosecond},
	})
	must(t, err)
	r.x = x
	ob.BindRuns(x)
	must(t, x.Run(ctx))
	t.Cleanup(func() { _ = x.Shutdown(context.Background()) })
	if !opts.noRelay {
		r.startRelay()
	}
	return r
}

// startRelay starts a Live relay run and waits until it is in its loop.
func (r *rig) startRelay() {
	r.t.Helper()
	ref, err := r.x.Start(context.Background(), outbox.RelayKind, tasks.StartRequest{Mode: tasks.ModeRequestLive})
	if err != nil {
		r.t.Fatalf("start relay: %v", err)
	}
	eventually(r.t, "the relay to run", func() bool {
		rec, err := r.stores.Runs.Get(context.Background(), ref.ID)
		must(r.t, err)
		return rec.State == tasks.StateRunning
	})
}

func (r *rig) setNotifyConfig(edit func(*notify.Config)) {
	r.cfgMu.Lock()
	defer r.cfgMu.Unlock()
	edit(&r.notifyCfg)
}

// putRule stores a rule as an admin.
func (r *rig) putRule(rule notify.Rule) {
	r.t.Helper()
	must(r.t, r.svc.PutRule(admin(), rule))
}

// rule is an enabled rule over topics aiming at refs.
func rule(id string, topics []string, refs ...integrations.Ref) notify.Rule {
	return notify.Rule{ID: id, Topics: topics, Instances: refs, Enabled: true}
}

// publish publishes n with a batch of its own and fails t on an error.
func (r *rig) publish(n notify.Notification) {
	r.t.Helper()
	must(r.t, r.svc.Publish(context.Background(), nil, n))
}

// signalNote is a notification on the signal topic.
func signalNote(villain string) notify.Notification {
	return notify.Notification{
		Topic: signalName, Title: "The signal is lit", Body: villain + " is loose in Gotham.",
		Fields: map[string]string{"villain": villain},
	}
}

// Dead lists the dead letters of the notify consumer.
func (r *rig) dead() []outbox.DeadLetter {
	r.t.Helper()
	var out []outbox.DeadLetter
	after := ""
	for {
		letters, next, err := r.ob.ListDead(context.Background(), after, 100)
		must(r.t, err)
		for _, l := range letters {
			if l.Consumer == notify.DeliverConsumer {
				out = append(out, l)
			}
		}
		if next == "" {
			return out
		}
		after = next
	}
}

// status is the notify consumer's status row.
func (r *rig) status() outbox.ConsumerStatus {
	r.t.Helper()
	st, err := r.ob.Status(context.Background())
	must(r.t, err)
	for _, c := range st.Consumers {
		if c.Name == notify.DeliverConsumer {
			return c
		}
	}
	r.t.Fatal("the notify consumer has no status row")
	return outbox.ConsumerStatus{}
}

// callsTo lists the fake's calls aimed at ref.
func callsTo(f *notifytest.FakeNotifier, ref integrations.Ref) []notifytest.Call {
	var out []notifytest.Call
	for _, c := range f.Calls() {
		if c.Target.Instance == ref {
			out = append(out, c)
		}
	}
	return out
}

// eventually polls cond for up to 10 s.
func eventually(t testing.TB, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// stays asserts cond holds for d.
func stays(t testing.TB, what string, d time.Duration, cond func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if !cond() {
			t.Fatalf("%s did not hold", what)
		}
		time.Sleep(3 * time.Millisecond)
	}
}

// sample reads the value of a counter or gauge series whose labels include
// labels; 0 when there is none.
func sample(t testing.TB, p *metrics.Provider, family string, labels map[string]string) float64 {
	t.Helper()
	mfs, err := p.Gatherer().Gather()
	must(t, err)
	for _, mf := range mfs {
		if mf.GetName() != family {
			continue
		}
	next:
		for _, m := range mf.GetMetric() {
			got := map[string]string{}
			for _, lp := range m.GetLabel() {
				got[lp.GetName()] = lp.GetValue()
			}
			for k, v := range labels {
				if got[k] != v {
					continue next
				}
			}
			switch {
			case m.GetCounter() != nil:
				return m.GetCounter().GetValue()
			case m.GetGauge() != nil:
				return m.GetGauge().GetValue()
			}
		}
	}
	return 0
}
