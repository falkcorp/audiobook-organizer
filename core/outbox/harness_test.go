// file: core/outbox/harness_test.go
// version: 1.0.1
// guid: 1e7fbb59-5fbb-4c44-9df0-2d6b9f8a1c70
// last-edited: 2026-11-12

package outbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/internal/executor"
	"github.com/falkcorp/mediacore/internal/outboxstore"
	"github.com/falkcorp/mediacore/internal/taskstore"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
	"github.com/falkcorp/mediacore/tasks"
)

// The tests open their store with storage.Open/Prepare/Migrate over MemFS,
// never coretest.NewStore: core/outbox is in the kernel's import graph (the
// W27 rule), and coretest imports the kernel.

const storePath = "/batcave/main.pebble"

// Fixture payloads and topics: Gotham raises signals, Wayne's vault trips
// (Batman names only; the repo is public).

type Dispatch struct {
	Villain string `json:"villain"`
	Block   int    `json:"block"`
}

type Alarm struct {
	Vault string `json:"vault"`
}

const (
	signalName = "gotham.signal.raised.v1"
	alarmName  = "wayne.vault.tripped.v1"
	noticeName = "core.notify.delivery.requested.v1"
	busOnly    = "gotham.villain.escaped.v1"
)

func signal() outbox.Topic[Dispatch] { return outbox.NewTopic[Dispatch](signalName) }
func alarm() outbox.Topic[Alarm]     { return outbox.NewTopic[Alarm](alarmName) }
func notice() outbox.Topic[Dispatch] { return outbox.NewTopic[Dispatch](noticeName) }

const (
	// Core's consumers: plain and Tx handlers are core's alone.
	consumerN  = "core.notify.deliver"
	consumerN2 = "core.notify.audit"
	consumerN3 = "core.notify.mirror"
	// Gotham's and Wayne's consumers are task runs.
	consumerC = "gotham.dispatch.respond"
	consumerD = "gotham.dispatch.relay"
	consumerW = "wayne.vault.guard"
	runKind   = "gotham.signal.respond"
)

// rigOpts shapes one rig; the zero value is Gotham's signal topic with four
// partitions and the relay running.
type rigOpts struct {
	fs         *vfs.MemFS
	cfg        func(*outbox.Config)
	partitions int
	core       []string
	declared   map[string][]string // module -> consumer names listed by its manifest
	register   func(t testing.TB, r *rig) []registration
	defs       []tasks.Definition
	now        func() time.Time
	rand       func() float64
	hooks      *outbox.TestHooks
	noRelay    bool
	noExec     bool // no executor: staging and operator operations only
	noFreeze   bool // leave registration open (registration tests)
	inst       *outbox.Instruments
	sweep      time.Duration
	log        *slog.Logger // the Outbox's and the executor's (the relay logs through its run) logger; nil is silent
}

// registration is one owner's handlers.
type registration struct {
	owner string
	hs    []outbox.Handler
}

type rig struct {
	t        testing.TB
	fs       *vfs.MemFS
	db       *storage.DB
	gothamKS storage.Keyspace
	outKS    storage.Keyspace
	taskKS   storage.Keyspace
	bus      *events.Bus
	ob       *outbox.Outbox
	x        tasks.Executor
	stores   tasks.Stores
	reg      *health.Registry
	inst     *outbox.Instruments

	cfgMu sync.Mutex
	cfg   outbox.Config
}

// loggerOr is l, or the silent logger when l is nil.
func loggerOr(l *slog.Logger) *slog.Logger {
	if l != nil {
		return l
	}
	return quietLogger()
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// testConfig keeps every clock short.
func testConfig() outbox.Config {
	return outbox.Config{
		MaxAttempts: 3, BackoffMin: time.Millisecond, BackoffMax: 4 * time.Millisecond, RelayWorkers: 4,
		RelayRotate: time.Hour, RelayEnabled: true, HandlerTimeout: 5 * time.Second, IntakePoll: 5 * time.Millisecond,
		UnorderedParallelism: 4, OrphanRetention: 7 * 24 * time.Hour,
	}
}

func openDB(t testing.TB, fs *vfs.MemFS) *storage.DB {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path: storePath,
		Owners: []storage.OwnerSpec{
			{Owner: taskstore.Owner, Spec: taskstore.StorageSpec(), Enabled: true},
			{Owner: outboxstore.Owner, Spec: outboxstore.StorageSpec(), Enabled: true},
			{Owner: "gotham", Spec: storage.Spec{Keyspace: true}, Enabled: true},
		},
		Build: "coretest+fixture", FreeSpaceMargin: 1, FS: storage.FSOf(fs),
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

func keyspace(t testing.TB, db *storage.DB, o storage.Owner) storage.Keyspace {
	t.Helper()
	ks, ok := db.Keyspace(o)
	if !ok {
		t.Fatalf("owner %s has no keyspace", o)
	}
	return ks
}

func catalogWith(t testing.TB, perms ...rbac.Permission) *rbac.Catalog {
	t.Helper()
	specs := rbac.CoreScopes()
	for _, p := range perms {
		specs = append(specs, rbac.PermissionSpec{Name: p, Title: string(p), Description: "Fixture scope " + string(p)})
	}
	cat, err := rbac.NewCatalog(specs...)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// newRig boots a store, the catalog, an Outbox with the registrations opts
// asks for, and (unless noExec) an executor that runs the relay's definition
// and the fixture kind gotham.signal.respond.
func newRig(t testing.TB, opts rigOpts) *rig {
	t.Helper()
	fs := opts.fs
	if fs == nil {
		fs = vfs.NewCrashableMem()
	}
	db := openDB(t, fs)
	r := &rig{t: t, fs: fs, db: db, gothamKS: keyspace(t, db, "gotham"), outKS: keyspace(t, db, outboxstore.Owner),
		taskKS: keyspace(t, db, taskstore.Owner), cfg: testConfig()}
	if opts.cfg != nil {
		opts.cfg(&r.cfg)
	}
	parts := opts.partitions
	if parts == 0 {
		parts = 4
	}

	bus, err := events.NewBus(events.BusOptions{})
	must(t, err)
	must(t, bus.Declare("gotham", signal0(parts)))
	must(t, bus.Declare("wayne", alarm0(2)))
	must(t, bus.Declare("core", notice0(2)))
	must(t, bus.Declare("gotham", events.NewTopic[Dispatch](busOnly).Spec(events.TopicSpec{Permission: rbac.Need("gotham:view")})))
	bus.Freeze()
	r.bus = bus

	var rep health.Reporter
	r.reg = health.NewRegistry()
	rep, err = r.reg.Register(health.Check{ID: outbox.ComponentID, Title: "Outbox"})
	must(t, err)
	inst := opts.inst
	if inst == nil {
		inst, err = outbox.NewInstruments(nil)
		must(t, err)
	}
	r.inst = inst

	core := opts.core
	if core == nil {
		core = []string{consumerN, consumerN2, consumerN3}
	}
	declared := opts.declared
	if declared == nil {
		declared = map[string][]string{"gotham": {consumerC, consumerD}, "wayne": {consumerW}}
	}
	ob, err := outbox.New(outbox.Options{
		Keyspace: r.outKS, Catalog: bus.Catalog(), CoreConsumers: core, Log: loggerOr(opts.log),
		Config: func() outbox.Config { r.cfgMu.Lock(); defer r.cfgMu.Unlock(); return r.cfg },
		Now:    opts.now, Rand: opts.rand, Health: rep, Instruments: inst,
	})
	must(t, err)
	r.ob = ob
	for mod, names := range declared {
		must(t, ob.DeclareConsumers(mod, names...))
	}
	if opts.sweep > 0 {
		ob.SetSweepEvery(opts.sweep)
	}
	if opts.hooks != nil {
		ob.SetTestHooks(*opts.hooks)
	}
	if opts.register != nil {
		for _, reg := range opts.register(t, r) {
			must(t, ob.Register(reg.owner, reg.hs...))
		}
	}
	if !opts.noFreeze {
		must(t, ob.Freeze())
	}
	if opts.noExec {
		return r
	}

	r.stores = tasks.NativeStores(r.taskKS)
	defs := append([]tasks.Definition{ob.RelayDefinition()}, opts.defs...)
	x, err := executor.New(executor.Options{
		Stores: r.stores, Keyspace: &r.taskKS, Definitions: defs, InstrumentsOff: true,
		Catalog: catalogWith(t, "gotham:run", "wayne:run"), Logger: loggerOr(opts.log),
		Config: executor.Config{DispatchTick: 20 * time.Millisecond, WatchdogInterval: time.Hour,
			ProgressInterval: time.Nanosecond, MinCheckpointInterval: time.Nanosecond},
	})
	must(t, err)
	r.x = x
	ob.BindRuns(x)
	must(t, x.Run(context.Background()))
	t.Cleanup(func() { _ = x.Shutdown(context.Background()) })
	if !opts.noRelay {
		r.startRelay()
	}
	return r
}

func signal0(parts int) events.TopicSpec {
	return signal().Spec(events.TopicSpec{Permission: rbac.Need("gotham:view")}, parts)
}
func alarm0(parts int) events.TopicSpec {
	return alarm().Spec(events.TopicSpec{Permission: rbac.Need("wayne:view")}, parts)
}
func notice0(parts int) events.TopicSpec {
	return notice().Spec(events.TopicSpec{Permission: rbac.Need("core:system.view")}, parts)
}

func (r *rig) setConfig(edit func(*outbox.Config)) {
	r.cfgMu.Lock()
	defer r.cfgMu.Unlock()
	edit(&r.cfg)
}

// startRelay starts a Live relay run and waits until it is in its loop.
func (r *rig) startRelay() tasks.RunID {
	r.t.Helper()
	ref, err := r.x.Start(context.Background(), outbox.RelayKind, tasks.StartRequest{Mode: tasks.ModeRequestLive})
	if err != nil {
		r.t.Fatalf("start relay: %v", err)
	}
	eventually(r.t, "the relay to run", func() bool { return r.ob.Running() })
	return ref.ID
}

// healthOf is the core.outbox component's current Result.
func (r *rig) healthOf() health.Result {
	r.t.Helper()
	for _, c := range r.reg.Snapshot().Components {
		if c.ID == outbox.ComponentID {
			return c.Result
		}
	}
	r.t.Fatal("core.outbox is not registered")
	return health.Result{}
}

// waitRun polls until run id is in one of states.
func (r *rig) waitRun(id tasks.RunID, states ...tasks.RunState) tasks.RunRecord {
	r.t.Helper()
	var rec tasks.RunRecord
	eventually(r.t, "the run to reach "+fmt.Sprint(states), func() bool {
		var err error
		rec, err = r.stores.Runs.Get(context.Background(), id)
		must(r.t, err)
		return slices.Contains(states, rec.State)
	})
	return rec
}

// close shuts the rig down cleanly so a second rig can open the same file system.
func (r *rig) close() {
	r.t.Helper()
	if r.x != nil {
		must(r.t, r.x.Shutdown(context.Background()))
	}
	must(r.t, r.db.Close())
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

// stage commits one Dispatch of core's notice topic for key through a
// producer batch that also writes a case row, so a test can see the message
// and the producer's write land together.
func (r *rig) stage(key string, d Dispatch) {
	r.t.Helper()
	b := r.gothamKS.NewBatch()
	defer b.Close()
	must(r.t, outbox.Stage(r.ob.Writer("core"), b, notice(), key, d))
	must(r.t, b.Set([]byte("case/"+key+"/"+d.Villain), []byte("open")))
	must(r.t, b.Commit(context.Background(), storage.Sync))
}

// stageGotham is stage for Gotham's own topic.
func (r *rig) stageGotham(key string, d Dispatch) {
	r.t.Helper()
	b := r.gothamKS.NewBatch()
	defer b.Close()
	must(r.t, outbox.Stage(r.ob.Writer("gotham"), b, signal(), key, d))
	must(r.t, b.Set([]byte("case/"+key+"/"+d.Villain), []byte("open")))
	must(r.t, b.Commit(context.Background(), storage.Sync))
}

func (r *rig) stageMany(key string, villains ...string) {
	r.t.Helper()
	for i, v := range villains {
		r.stage(key, Dispatch{Villain: v, Block: i})
	}
}

// delivery is what a recording handler saw.
type delivery struct {
	Consumer string
	Seq      uint64
	ID       string
	Key      string
	Attempt  int
	Villain  string
	Block    int
}

type recorder struct {
	mu  sync.Mutex
	got []delivery
}

func (rc *recorder) add(d delivery) {
	rc.mu.Lock()
	rc.got = append(rc.got, d)
	rc.mu.Unlock()
}

func (rc *recorder) all() []delivery {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return append([]delivery(nil), rc.got...)
}

func (rc *recorder) count() int { return len(rc.all()) }

func (rc *recorder) villains() []string {
	var out []string
	for _, d := range rc.all() {
		out = append(out, d.Villain)
	}
	return out
}

func (rc *recorder) handler(consumer string) outbox.HandlerFunc[Dispatch] {
	return func(_ context.Context, m outbox.Message[Dispatch]) error {
		rc.add(delivery{Consumer: consumer, Seq: m.Seq, ID: m.IdempotencyKey, Key: m.Key, Attempt: m.Attempt,
			Villain: m.Payload.Villain, Block: m.Payload.Block})
		return nil
	}
}

// consumeInto registers rc as a core consumer of the notice topic.
func consumeInto(rc *recorder, consumer string, opts ...outbox.ConsumerOption) func(testing.TB, *rig) []registration {
	return func(testing.TB, *rig) []registration {
		return []registration{{owner: "core", hs: []outbox.Handler{outbox.Consume(notice(), consumer, rc.handler(consumer), opts...)}}}
	}
}

// respondDef is the fixture kind gotham.signal.respond: a task whose run
// records its params.
type respondParams struct {
	Villain string `json:"villain"`
	Block   int    `json:"block"`
}

type responder struct {
	mu   sync.Mutex
	runs []respondParams
}

func (rs *responder) add(p respondParams) {
	rs.mu.Lock()
	rs.runs = append(rs.runs, p)
	rs.mu.Unlock()
}

func (rs *responder) all() []respondParams {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return append([]respondParams(nil), rs.runs...)
}

func (rs *responder) def() tasks.Definition {
	return tasks.Task(runKind, tasks.TaskSpec[respondParams, string]{
		Title:   "Respond to a signal",
		Effects: tasks.Writes(tasks.Res("gotham", "cases")),
		Perms:   tasks.Permissions{Start: rbac.Need("gotham:run")},
		Resume:  tasks.ResumeFromZero,
		Run: func(_ *tasks.RC, p respondParams) (string, error) {
			rs.add(p)
			return "answered", nil
		},
	})
}

// blockingDef is def with a run that waits for release, so a start made while
// it waits meets a run that exists.
func (rs *responder) blockingDef(release <-chan struct{}) tasks.Definition {
	return tasks.Task(runKind, tasks.TaskSpec[respondParams, string]{
		Title:   "Respond to a signal, slowly",
		Effects: tasks.Writes(tasks.Res("gotham", "cases")),
		Perms:   tasks.Permissions{Start: rbac.Need("gotham:run")},
		Resume:  tasks.ResumeFromZero,
		Run: func(rc *tasks.RC, p respondParams) (string, error) {
			rs.add(p)
			select {
			case <-release:
			case <-rc.Context().Done():
			}
			return "answered", nil
		},
	})
}

// respondInto registers consumerC as Gotham's task-starting consumer of the signal topic.
func respondInto(testing.TB, *rig) []registration {
	h := outbox.ConsumeIntoTask(signal(), consumerC, runKind, func(m outbox.Message[Dispatch]) (any, error) {
		return respondParams{Villain: m.Payload.Villain, Block: m.Payload.Block}, nil
	}, outbox.Live())
	return []registration{{owner: "gotham", hs: []outbox.Handler{h}}}
}

// rawJSON encodes v.
func rawJSON(t testing.TB, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	must(t, err)
	return b
}

func errIs(err, target error) bool { return errors.Is(err, target) }

// count counts the keys under a prefix of the outbox keyspace.
func (r *rig) count(prefix []byte) int { return r.countIn(r.outKS, prefix) }

// countIn counts the keys under a prefix of ks.
func (r *rig) countIn(ks storage.Keyspace, prefix []byte) int {
	r.t.Helper()
	n := 0
	for _, err := range ks.Scan(context.Background(), storage.ScanOptions{Start: prefix, End: prefixEnd(prefix), KeysOnly: true}) {
		must(r.t, err)
		n++
	}
	return n
}

// runsOf lists the runs of kind, newest state included.
func (r *rig) runsOf(kind tasks.Kind) []tasks.RunRecord {
	r.t.Helper()
	var out []tasks.RunRecord
	for rec, err := range r.stores.Runs.List(context.Background(), tasks.RunQuery{Kinds: []tasks.Kind{kind}}) {
		must(r.t, err)
		out = append(out, rec)
	}
	return out
}

// tryRoot is the try/ family's prefix (keys are tuple-encoded, not slash-joined).
func tryRoot() []byte { return keys.Prefix(keys.Str("try")) }

// msgTopicPrefix is m/<topic>/, curPrefix cur/<consumer>/ and seqRoot seq/: the
// tests build them here rather than the production package carrying helpers
// only they call.
func msgTopicPrefix(topic string) []byte { return keys.Prefix(keys.Str("m"), keys.Str(topic)) }
func curPrefix(consumer string) []byte   { return keys.Prefix(keys.Str("cur"), keys.Str(consumer)) }
func seqRoot() []byte                    { return keys.Prefix(keys.Str("seq")) }

func prefixEnd(p []byte) []byte {
	out := append([]byte(nil), p...)
	for i := len(out) - 1; i >= 0; i-- {
		out[i]++
		if out[i] != 0 {
			return out[:i+1]
		}
	}
	return nil
}
