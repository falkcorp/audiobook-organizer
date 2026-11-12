// file: core/kernel/outbox_test.go
// version: 1.0.0
// guid: d0b27e65-8a13-4c4f-b6e9-5f1c3a7d2e80
// last-edited: 2026-11-12

package kernel

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

type kernelDispatch struct {
	Villain string `json:"villain"`
}

var (
	kernelSignal = outbox.NewTopic[kernelDispatch]("gotham.signal.raised.v1")
	kernelNotice = outbox.NewTopic[kernelDispatch]("core.notify.delivery.requested.v1")
)

// newTestKernelOutbox builds a kernelOutbox over a booted store, with
// gotham's signal topic and core's notice topic durable in the catalog.
func newTestKernelOutbox(t *testing.T, settings func() outboxSettings) (*kernelOutbox, *storage.DB) {
	t.Helper()
	db := bootAuditDB(t, outboxOwnerSpec())
	p, err := metrics.New(context.Background(), metrics.Options{AppID: "gotham", Prometheus: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := bus.Declare("gotham", kernelSignal.Spec(events.TopicSpec{Permission: rbac.Need("gotham:view")}, 4)); err != nil {
		t.Fatal(err)
	}
	if err := bus.Declare("core", kernelNotice.Spec(events.TopicSpec{Permission: rbac.Need("core:system.view")}, 2)); err != nil {
		t.Fatal(err)
	}
	bus.Freeze()
	reg := health.NewRegistry()
	rep, err := reg.Register(outboxHealthCheck())
	if err != nil {
		t.Fatal(err)
	}
	ko, err := newKernelOutbox(db, p, bus.Catalog(), settings, rep, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err != nil {
		t.Fatal(err)
	}
	return ko, db
}

func TestOutboxOwnerSpec(t *testing.T) {
	spec := outboxOwnerSpec()
	if spec.Owner != "core/outbox" || !spec.Spec.Keyspace || !spec.Enabled {
		t.Errorf("outboxOwnerSpec = %+v, want an enabled core/outbox keyspace", spec)
	}
	if len(spec.Spec.Serialized) != 3 {
		t.Errorf("Serialized = %v, want exactly seq/, cur/ and dead/", spec.Spec.Serialized)
	}
	db := bootAuditDB(t, spec)
	if _, ok := db.Keyspace("core/outbox"); !ok {
		t.Error("the spec gave no c/outbox/ keyspace")
	}
}

// The build writes nothing, and a missing keyspace is reported by name, not as
// a nil dereference at the first Stage.
func TestKernelOutbox_BuildWritesNothingAndNamesAMissingOwner(t *testing.T) {
	ko, db := newTestKernelOutbox(t, nil)
	if keys := keysOf(t, db, "core/outbox"); len(keys) != 0 {
		t.Fatalf("newKernelOutbox wrote %d keys", len(keys))
	}
	if ko.ob == nil {
		t.Fatal("no outbox")
	}
	if _, err := newKernelOutbox(nil, nil, nil, nil, nil, nil, nil); err == nil {
		t.Error("a build with no store succeeded")
	}
	other := bootAuditDB(t, auditOwnerSpec())
	p, _ := metrics.New(context.Background(), metrics.Options{AppID: "gotham", Prometheus: true})
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	_, err := newKernelOutbox(other, p, nil, nil, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "core/outbox") || !strings.Contains(err.Error(), "outboxOwnerSpec") {
		t.Errorf("a store without the owner: %v, want an error naming core/outbox and outboxOwnerSpec", err)
	}
}

// The core consumer list is static and sorted into the Outbox's declared set;
// settings map one-to-one onto the outbox's Config.
func TestKernelOutbox_ConfigAndCoreConsumers(t *testing.T) {
	if got := coreOutboxConsumers(); !slices.Equal(got, []string{"core.notify.deliver"}) {
		t.Fatalf("core consumers = %v", got)
	}
	s := outboxSettings{
		MaxAttempts: 7, BackoffMin: dur(2 * time.Second), BackoffMax: dur(3 * time.Minute), RelayWorkers: 5,
		RelayRotate: dur(2 * time.Hour), RelayEnabled: true, HandlerTimeout: dur(11 * time.Second), IntakePoll: dur(900 * time.Millisecond),
		UnorderedParallelism: 6, OrphanRetention: dur(48 * time.Hour),
	}
	want := outbox.Config{
		MaxAttempts: 7, BackoffMin: 2 * time.Second, BackoffMax: 3 * time.Minute, RelayWorkers: 5, RelayRotate: 2 * time.Hour,
		RelayEnabled: true, HandlerTimeout: 11 * time.Second, IntakePoll: 900 * time.Millisecond, UnorderedParallelism: 6,
		OrphanRetention: 48 * time.Hour,
	}
	if got := outboxConfig(s); got != want {
		t.Fatalf("outboxConfig = %+v, want %+v", got, want)
	}
	// The section's defaults are the outbox's own defaults (05-events §5E.9).
	var sec any
	for _, c := range coreSections() {
		if reflect.ValueOf(c).MethodByName("Path").Call(nil)[0].Interface().(config.Path) == "core.outbox" {
			sec = c
		}
	}
	if sec == nil {
		t.Fatal("no core.outbox section")
	}
	def := func(key string) any { return defaultValue(t, sec, "core.outbox."+key) }
	got := outbox.Config{
		MaxAttempts: int(def("max_attempts").(int64)), BackoffMin: def("backoff_min").(time.Duration),
		BackoffMax: def("backoff_max").(time.Duration), RelayWorkers: int(def("relay_workers").(int64)),
		RelayRotate: def("relay_rotate").(time.Duration), RelayEnabled: def("relay_enabled").(bool),
		HandlerTimeout: def("handler_timeout").(time.Duration), IntakePoll: def("intake_poll").(time.Duration),
		UnorderedParallelism: int(def("unordered_parallelism").(int64)), OrphanRetention: def("orphan_retention").(time.Duration),
	}
	if got != outbox.DefaultConfig() {
		t.Fatalf("core.outbox defaults %+v differ from outbox.DefaultConfig %+v", got, outbox.DefaultConfig())
	}
	ko, _ := newTestKernelOutbox(t, func() outboxSettings { return s })
	for _, n := range []string{"core.notify.deliver"} {
		if !ko.ob.Declared(n) {
			t.Errorf("%s is not declared", n)
		}
	}
}

// Registration refusals become module problems naming the same rule, keeping
// the outbox sentinel for errors.Is; a clean registration returns none.
func TestKernelOutbox_RegistrationProblems(t *testing.T) {
	ko, _ := newTestKernelOutbox(t, nil)
	if probs := ko.declareConsumers(map[module.ID][]string{"gotham": {"gotham.dispatch.respond"}}); len(probs) != 0 {
		t.Fatalf("declareConsumers: %v", probs)
	}
	probs := ko.declareConsumers(map[module.ID][]string{"gotham": {"Gotham.Bad"}})
	if len(probs) != 1 || !errors.Is(probs[0], outbox.ErrConsumerName) {
		t.Fatalf("a malformed consumer name: %v", probs)
	}
	ok := outbox.Consume(kernelNotice, "core.notify.deliver", func(context.Context, outbox.Message[kernelDispatch]) error { return nil })
	if probs := ko.register("", []outbox.Handler{ok}); len(probs) != 0 {
		t.Fatalf("core register: %v", probs)
	}
	undeclared := outbox.Consume(kernelNotice, "core.notify.unlisted", func(context.Context, outbox.Message[kernelDispatch]) error { return nil })
	probs = ko.register("", []outbox.Handler{undeclared})
	if len(probs) != 1 || !errors.Is(probs[0], outbox.ErrConsumerUndeclared) {
		t.Fatalf("an undeclared consumer: %v", probs)
	}
	if k := probs[0].Kind; k != module.ProblemOutboxConsumerUndeclared {
		t.Fatalf("problem kind %v, want OutboxConsumerUndeclared", k)
	}
	module1 := outbox.Consume(kernelSignal, "gotham.dispatch.respond", func(context.Context, outbox.Message[kernelDispatch]) error { return nil })
	probs = ko.register("gotham", []outbox.Handler{module1})
	if len(probs) != 1 || !errors.Is(probs[0], outbox.ErrModuleHandler) || probs[0].Kind != module.ProblemInvalidRegistration {
		t.Fatalf("a module's plain consumer: %v", probs)
	}
	if probs := ko.freeze(); len(probs) != 0 {
		t.Fatalf("freeze: %v", probs)
	}
	if probs := ko.register("", []outbox.Handler{ok}); len(probs) != 1 || !errors.Is(probs[0], outbox.ErrFrozen) {
		t.Fatalf("a registration after freeze: %v", probs)
	}
}

// writer(id) is the module's staging handle: it stamps the module and refuses
// a topic of another namespace.
func TestKernelOutbox_WriterRefusesForeignTopic(t *testing.T) {
	ko, db := newTestKernelOutbox(t, nil)
	if probs := ko.freeze(); len(probs) != 0 {
		t.Fatal(probs)
	}
	ks, _ := db.Keyspace("core/outbox")
	b := ks.NewBatch()
	defer b.Close()
	if err := outbox.Stage(ko.writer("wayne"), b, kernelSignal, "k", kernelDispatch{Villain: "Joker"}); !errors.Is(err, outbox.ErrNotOwner) {
		t.Fatalf("wayne staging gotham's topic = %v, want ErrNotOwner", err)
	}
	if err := outbox.Stage(ko.writer("gotham"), b, kernelSignal, "k", kernelDispatch{Villain: "Joker"}); err != nil {
		t.Fatalf("gotham staging its own topic: %v", err)
	}
}

// The outbox's relay definition is the core kind, and the nudger starts once, stops on
// demand, and stops on its context.
func TestKernelOutbox_RelayDefinitionAndNudger(t *testing.T) {
	ko, _ := newTestKernelOutbox(t, nil)
	var def tasks.Definition = ko.ob.RelayDefinition()
	if def.Kind() != outbox.RelayKind {
		t.Fatalf("relay kind %q", def.Kind())
	}
	if err := ko.stopNudger(context.Background()); err != nil {
		t.Fatalf("stopNudger before start: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ko.startNudger(ctx)
	ko.startNudger(ctx) // a second call is a no-op
	if err := ko.stopNudger(context.Background()); err != nil {
		t.Fatalf("stopNudger: %v", err)
	}
	cancel()
	ko.startNudger(nudgeCtx(t))
	if err := ko.stopNudger(context.Background()); err != nil {
		t.Fatalf("restart: %v", err)
	}
}

func nudgeCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx
}

// The component is push-only and neither Critical nor Readiness.
func TestOutboxHealthCheck(t *testing.T) {
	c := outboxHealthCheck()
	if c.ID != "core.outbox" || c.Critical || c.Readiness || c.Probe != nil {
		t.Fatalf("outboxHealthCheck = %+v", c)
	}
}
