// file: core/internal/outboxhttp/env_test.go
// version: 1.0.1
// guid: 8a716b84-f689-41d2-9003-16a075efcb08
// last-edited: 2026-11-19

package outboxhttp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"
	dto "github.com/prometheus/client_model/go"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/internal/executor"
	"github.com/falkcorp/mediacore/internal/outboxstore"
	"github.com/falkcorp/mediacore/internal/taskstore"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// The tests open their stores with storage.Open/Prepare/Migrate over MemFS,
// never coretest.NewStore (the kernel's import graph must not reach it), and
// run the real outbox and the real audit store, so a dead letter, a cursor and
// an audit record are the ones the production code writes. The audit store has
// a database of its own so a test can make its writes fail after the outbox's
// operation committed, as a full disk or a closed store would.

const auditOwner storage.Owner = "core/audit"

// Fixture topics and consumers (Batman names only; the repo is public).
const (
	noticeTopic = "core.notify.delivery.requested.v1" // durable, 2 partitions
	signalTopic = "gotham.signal.raised.v1"           // durable, 4 partitions
	busTopic    = "gotham.villain.escaped.v1"         // bus only, notify + realtime metadata

	consumerDeliver = "core.notify.deliver"     // registered: enabled
	consumerMirror  = "core.notify.mirror"      // on the core list, not registered
	consumerRespond = "gotham.dispatch.respond" // a disabled module's consumer

	noticeParts = 2
	signalParts = 4
)

// Dispatch is the fixture payload of every topic.
type Dispatch struct {
	Villain string `json:"villain"`
	Block   int    `json:"block"`
}

func notice() outbox.Topic[Dispatch] { return outbox.NewTopic[Dispatch](noticeTopic) }
func signal() outbox.Topic[Dispatch] { return outbox.NewTopic[Dispatch](signalTopic) }

// env is a booted outbox, its audit store and the handlers over both.
type env struct {
	t      testing.TB
	ob     *outbox.Outbox
	st     *outboxstore.Store
	ks     storage.Keyspace
	reg    *health.Registry
	aud    *audit.Store
	audDB  *storage.DB
	prov   *metrics.Provider
	cat    events.Catalog
	api    *api
	logBuf *lockedBuffer
	got    *received
	taskKS storage.Keyspace // the task keyspace of a relay env
}

// received is what the registered consumer was handed, in order.
type received struct {
	mu   sync.Mutex
	list []outbox.Message[Dispatch]
}

func (r *received) add(m outbox.Message[Dispatch]) {
	r.mu.Lock()
	r.list = append(r.list, m)
	r.mu.Unlock()
}

func (r *received) all() []outbox.Message[Dispatch] {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]outbox.Message[Dispatch](nil), r.list...)
}

// lockedBuffer is a log sink the handlers may write from any goroutine.
type lockedBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

func bootDB(t testing.TB, name string, owners ...storage.OwnerSpec) *storage.DB {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path:            filepath.Join("/batcave", name, "main.pebble"),
		Owners:          owners,
		Build:           "coretest+fixture",
		FreeSpaceMargin: 1,
		FS:              storage.FSOf(vfs.NewMem()),
	})
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		_ = o.Close()
		t.Fatalf("boot %s: %v", name, err)
	}
	t.Cleanup(func() { _ = db.Close() }) // a test may have closed it already
	return db
}

func keyspaceOf(t testing.TB, db *storage.DB, o storage.Owner) storage.Keyspace {
	t.Helper()
	ks, ok := db.Keyspace(o)
	if !ok {
		t.Fatalf("owner %s has no keyspace", o)
	}
	return ks
}

func mustDo(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// newEnv is the outbox with no relay: staging, the operator operations and the
// status read only, so nothing moves a row but the test and the handlers.
func newEnv(t testing.TB) *env { return newEnvWith(t, false) }

// newRelayEnv is the outbox with a task keyspace, so that startRelay can run the
// relay on an executor and a message staged or retried is sequenced and delivered
// to the registered consumer. The relay does not run until startRelay: a test
// that plants rows by hand beside a running relay races it.
func newRelayEnv(t testing.TB) *env { return newEnvWith(t, true) }

func newEnvWith(t testing.TB, relay bool) *env {
	t.Helper()
	ctx := context.Background()

	owners := []storage.OwnerSpec{{Owner: outboxstore.Owner, Spec: outboxstore.StorageSpec(), Enabled: true}}
	if relay {
		owners = append(owners, storage.OwnerSpec{Owner: taskstore.Owner, Spec: taskstore.StorageSpec(), Enabled: true})
	}
	outDB := bootDB(t, "outbox", owners...)
	ks := keyspaceOf(t, outDB, outboxstore.Owner)

	bus, err := events.NewBus(events.BusOptions{})
	mustDo(t, err)
	mustDo(t, bus.Declare("core", notice().Spec(events.TopicSpec{Permission: rbac.Need("core:system.view")}, noticeParts)))
	mustDo(t, bus.Declare("gotham",
		signal().Spec(events.TopicSpec{Permission: rbac.Need("gotham:view")}, signalParts),
		events.NewTopic[Dispatch](busTopic).Spec(events.TopicSpec{
			Permission: rbac.Need("gotham:view"),
			Notify:     &events.NotifyMeta{Title: "A villain escaped", DefaultSeverity: "warning"},
			Realtime:   &events.RealtimeMeta{Coalesce: 250 * time.Millisecond},
		})))
	bus.Freeze()

	reg := health.NewRegistry()
	rep, err := reg.Register(health.Check{ID: outbox.ComponentID, Title: "Outbox"})
	mustDo(t, err)
	ob, err := outbox.New(outbox.Options{
		Keyspace: ks, Catalog: bus.Catalog(), Health: rep,
		CoreConsumers: []string{consumerDeliver, consumerMirror},
		Config: func() outbox.Config {
			return outbox.Config{
				MaxAttempts: 3, BackoffMin: time.Millisecond, BackoffMax: 4 * time.Millisecond, RelayWorkers: 4,
				RelayRotate: time.Hour, RelayEnabled: true, HandlerTimeout: 5 * time.Second, IntakePoll: 5 * time.Millisecond,
				UnorderedParallelism: 4, OrphanRetention: 7 * 24 * time.Hour,
			}
		},
	})
	mustDo(t, err)
	mustDo(t, ob.DeclareConsumers("gotham", consumerRespond))
	got := &received{}
	mustDo(t, ob.Register("core", outbox.Consume(notice(), consumerDeliver,
		func(_ context.Context, m outbox.Message[Dispatch]) error { got.add(m); return nil })))
	mustDo(t, ob.Freeze())
	var taskKS storage.Keyspace
	if relay {
		taskKS = keyspaceOf(t, outDB, taskstore.Owner)
	}

	audDB := bootDB(t, "audit", storage.OwnerSpec{Owner: auditOwner, Spec: storage.Spec{Keyspace: true}, Enabled: true})
	aks := keyspaceOf(t, audDB, auditOwner)
	prov, err := metrics.New(ctx, metrics.Options{AppID: "gotham", Prometheus: true})
	mustDo(t, err)
	t.Cleanup(func() { _ = prov.Shutdown(context.Background()) })
	sc, err := prov.Core("audit", "namespace", "outcome", "path")
	mustDo(t, err)
	aud, err := audit.New(ctx, audit.Options{}, audit.Deps{Keyspace: aks, Seq: aks.Sequence("seq"), Metrics: sc})
	mustDo(t, err)

	logBuf := &lockedBuffer{}
	e := &env{t: t, ob: ob, st: outboxstore.New(ks), ks: ks, reg: reg, aud: aud, audDB: audDB, prov: prov, cat: bus.Catalog(), logBuf: logBuf, got: got, taskKS: taskKS}
	e.api = newAPI("/api/core/v1", ob, bus.Catalog(), aud, WithLogger(slog.New(slog.NewTextHandler(logBuf, nil))))
	return e
}

// startRelay runs the core.outbox.relay task kind on an executor over the task
// keyspace, as the kernel does (the shape of outbox's own test harness), and
// returns once the run is started (a test waits for what the relay does).
func (e *env) startRelay() {
	t, ob, taskKS := e.t, e.ob, e.taskKS
	t.Helper()
	ctx := context.Background()
	cat, err := rbac.NewCatalog(rbac.CoreScopes()...)
	mustDo(t, err)
	x, err := executor.New(executor.Options{
		Stores: tasks.NativeStores(taskKS), Keyspace: &taskKS, Definitions: []tasks.Definition{ob.RelayDefinition()},
		InstrumentsOff: true, Catalog: cat, Logger: discardLogger(),
		Config: executor.Config{DispatchTick: 20 * time.Millisecond, WatchdogInterval: time.Hour,
			ProgressInterval: time.Nanosecond, MinCheckpointInterval: time.Nanosecond},
	})
	mustDo(t, err)
	ob.BindRuns(x)
	mustDo(t, x.Run(ctx))
	t.Cleanup(func() { _ = x.Shutdown(context.Background()) })
	_, err = x.Start(ctx, outbox.RelayKind, tasks.StartRequest{Mode: tasks.ModeRequestLive})
	mustDo(t, err)
}

// eventually polls cond for up to ten seconds.
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

// ---- seeding ----------------------------------------------------------------

// deadLetter writes one dead letter of consumer on topic, whose payload and key
// are what no response may carry, and (when withCursors) the consumer's
// cursors at the beginning of the topic.
func (e *env) deadLetter(consumer, topic string, parts int, seq uint64, withCursors bool) {
	e.t.Helper()
	ctx := context.Background()
	now := time.Now()
	b := e.st.OpenBatch()
	defer b.Close()
	mustDo(e.t, e.st.StageDead(ctx, b, consumer, topic, seq, outboxstore.Dead{
		Msg: outboxstore.Msg{
			ID: fmt.Sprintf("01J0000000000000000BATMN%02d", seq), Key: "wayne-manor",
			Payload: json.RawMessage(`{"villain":"Joker","block":7}`), StagedAt: now.Add(-time.Hour), Producer: "core",
		},
		Part: outboxstore.Partition("wayne-manor", parts), Token: "permanent", Attempts: 3, At: now,
	}))
	mustDo(e.t, b.Commit(ctx, storage.Sync))
	if withCursors {
		e.cursors(consumer, topic, parts)
	}
}

// cursors gives consumer a cursor on every partition of topic, at 0.
func (e *env) cursors(consumer, topic string, parts int) {
	e.t.Helper()
	b := e.st.OpenBatch()
	defer b.Close()
	_, err := e.st.StageCursorInit(context.Background(), b, consumer, topic, parts, true, time.Now())
	mustDo(e.t, err)
	mustDo(e.t, b.Commit(context.Background(), storage.Sync))
}

// sequence numbers n messages of topic, as the relay would, spread over keys.
func (e *env) sequence(topic string, parts, n int) {
	e.t.Helper()
	rows := make([]outboxstore.IntakeRow, 0, n)
	for i := range n {
		key := fmt.Sprintf("gotham-%d", i)
		rows = append(rows, outboxstore.IntakeRow{ID: fmt.Sprintf("01J0000000000000000SEQ%03d", i), Intake: outboxstore.Intake{
			Msg: outboxstore.Msg{
				ID: fmt.Sprintf("01J0000000000000000SEQ%03d", i), Key: key, Payload: json.RawMessage(`{"villain":"Penguin","block":1}`),
				StagedAt: time.Now(), Producer: "gotham",
			},
		}})
	}
	b := e.st.OpenBatch()
	defer b.Close()
	_, err := e.st.StageSequence(context.Background(), b, topic, parts, rows)
	mustDo(e.t, err)
	mustDo(e.t, b.Commit(context.Background(), storage.Sync))
}

// ---- reading ----------------------------------------------------------------

// deadRows is every dead letter in the store.
func (e *env) deadRows() []outbox.DeadLetter {
	e.t.Helper()
	var all []outbox.DeadLetter
	after := ""
	for {
		page, next, err := e.ob.ListDead(context.Background(), after, 500)
		mustDo(e.t, err)
		all = append(all, page...)
		if next == "" || len(page) == 0 {
			return all
		}
		after = next
	}
}

// intake is the intake rows of topic.
func (e *env) intake(topic string) []outboxstore.IntakeRow {
	e.t.Helper()
	rows, _, err := e.st.ScanIntake(context.Background(), topic, time.Now().Add(time.Hour), 100, 1<<20)
	mustDo(e.t, err)
	return rows
}

// records is every audit record, newest first.
func (e *env) records() []audit.Record {
	e.t.Helper()
	var out []audit.Record
	for r, err := range e.aud.Query(context.Background(), audit.Filter{}) {
		mustDo(e.t, err)
		out = append(out, r)
	}
	return out
}

// auditWriteErrors is mediacore_audit_write_errors_total{path="sync"}.
func (e *env) auditWriteErrors() float64 {
	e.t.Helper()
	mfs, err := e.prov.Gatherer().Gather()
	mustDo(e.t, err)
	for _, mf := range mfs {
		if mf.GetName() != "mediacore_audit_write_errors_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			if labelOf(m, "path") == "sync" {
				return m.GetCounter().GetValue()
			}
		}
	}
	e.t.Fatal("no mediacore_audit_write_errors_total{path=sync} series")
	return 0
}

func labelOf(m *dto.Metric, name string) string {
	for _, lp := range m.GetLabel() {
		if lp.GetName() == name {
			return lp.GetValue()
		}
	}
	return ""
}

// breakAudit closes the audit store's database: every later Record fails.
func (e *env) breakAudit() {
	e.t.Helper()
	mustDo(e.t, e.audDB.Close())
}

// consumerStatus is consumer's status as the outbox reports it.
func (e *env) consumerStatus(name string) outbox.ConsumerStatus {
	e.t.Helper()
	st, err := e.ob.Status(context.Background())
	mustDo(e.t, err)
	for _, c := range st.Consumers {
		if c.Name == name {
			return c
		}
	}
	e.t.Fatalf("status lists no consumer %s", name)
	return outbox.ConsumerStatus{}
}

// ---- principals -------------------------------------------------------------

// bruce is an interactive session. The handlers read no permission: the router's
// stage 13 does, and the principal only names the actor.
func bruce() *auth.Principal {
	return &auth.Principal{
		User: &auth.User{ID: "u-bruce", Username: "bruce"}, Method: auth.MethodSession,
		ClientAddr: netip.MustParseAddr("192.0.2.10"), CredentialID: "sess-batcave",
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
