// file: core/kernel/events_flip_test.go
// version: 0.1.0
// guid: 9d1e47b2-6c03-4a58-b7e9-2f5a0c83d614
// last-edited: 2026-11-22

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/internal/logjournal"
	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// Batman names only: Alfred watches the signal and sends a patrol out.

// alertRaised is the payload of the fixture's durable topic.
type alertRaised struct {
	Block string `json:"block"`
}

func alertTopic() outbox.Topic[alertRaised] {
	return outbox.NewTopic[alertRaised]("alfred.alert.raised.v1")
}

const (
	alertConsumer   = "alfred.patrol-on-alert"
	alfredPatrol    = tasks.Kind("alfred.patrol")
	disabledAlertCo = "arkham.escape-on-alert"
)

// alfredProbe is what the fixture module kept of its boot.
type alfredProbe struct {
	deps module.Deps
	ran  chan struct{}
}

// flipHost is an identity-boot host whose "alfred" module declares a durable
// topic, one outbox consumer that starts a patrol run, and the task kind it
// starts. A second module "arkham", disabled, lists a consumer of its own.
func flipHost(t *testing.T, mutate ...func(*Config)) (*swapHost, Config, *alfredProbe) {
	t.Helper()
	h := newSwapHost(t)
	c := h.envConfig(nil)
	c.IdentityBoot = true
	p := &alfredProbe{ran: make(chan struct{}, 4)}
	patrol := tasks.Task(alfredPatrol, tasks.TaskSpec[struct{}, struct{}]{
		Title:   "Patrol the Batcave",
		Effects: tasks.ReadOnly(),
		// A core scope, as the Deluge kinds use: the executor's catalog is
		// core-only, so a system actor holds nothing for a module-scope Start.
		Perms:  tasks.Permissions{Start: rbac.Need("core:settings.manage"), View: rbac.Need("alfred:signal")},
		Resume: tasks.ResumeFromZero,
		Run: func(*tasks.RC, struct{}) (struct{}, error) {
			p.ran <- struct{}{}
			return struct{}{}, nil
		},
	})
	c.Modules = []module.Module{
		storageMod("alfred", func(r *module.Registrar) error {
			p.deps = r.Deps()
			r.Topics(alertTopic().Spec(events.TopicSpec{Permission: rbac.Need("alfred:signal")}, 4))
			r.Tasks(patrol)
			r.OutboxHandler(outbox.ConsumeIntoTask(alertTopic(), alertConsumer, alfredPatrol,
				func(outbox.Message[alertRaised]) (any, error) { return struct{}{}, nil }, outbox.Live()))
			return r.Err()
		}, seedStep(1, "cave")).with(func(m *module.Manifest) {
			m.Permissions = []rbac.PermissionSpec{{Name: "alfred:signal", Title: "Signal"}}
			m.TaskLedger = []tasks.Kind{alfredPatrol}
			m.OutboxConsumers = []string{alertConsumer}
		}),
	}
	for _, f := range mutate {
		f(&c)
	}
	return h, c, p
}

// flipBoot boots flipHost and returns the kernel.
func flipBoot(t *testing.T, mutate ...func(*Config)) (*Kernel, *alfredProbe) {
	t.Helper()
	h, c, p := flipHost(t, mutate...)
	return h.mustBoot(c), p
}

// P5 builds the outbox and the hub for an app that declared the identity flip,
// hands every module a Deps.Outbox writer, and a boot that did not declare it
// builds neither and leaves the zero Writer.
func TestEventsFlip_P5BuildsOutboxAndHubOnlyForTheIdentityBoot(t *testing.T) {
	k, p := flipBoot(t)
	f := k.flip()
	if f == nil || f.outbox == nil || f.hub == nil {
		t.Fatalf("P5 built no events flip: %+v", f)
	}
	if p.deps.Outbox == (outbox.Writer{}) {
		t.Error("Deps.Outbox is the zero Writer on an identity boot")
	}
	// The writer is the module's own: another module's topic is refused with
	// the ownership sentinel, not the zero-Writer error.
	ks, ok := k.store.db.Keyspace("alfred")
	if !ok {
		t.Fatal("no alfred keyspace")
	}
	b := ks.NewBatch()
	defer func() { _ = b.Close() }()
	err := outbox.Stage(p.deps.Outbox, b, outbox.NewTopic[alertRaised]("wayne.alert.raised.v1"), "k", alertRaised{Block: "Crime Alley"})
	if errors.Is(err, outbox.ErrNotOwner) || errors.Is(err, outbox.ErrUnknownTopic) {
		// either names the topic rule; what must not happen is a zero Writer.
		return
	}
	t.Fatalf("Stage of a topic alfred does not own = %v, want ErrNotOwner or ErrUnknownTopic", err)
}

func TestEventsFlip_SwitchOffBuildsNothing(t *testing.T) {
	h, c, p := flipHost(t)
	c.IdentityBoot = false
	c.Modules = c.Modules[:0]
	c.Modules = append(c.Modules, mod("alfred", func(r *module.Registrar) error { p.deps = r.Deps(); return nil }))
	k := h.mustBoot(c)
	if k.flip() != nil {
		t.Errorf("a switch-off boot built the events flip: %+v", k.flip())
	}
	if p.deps.Outbox != (outbox.Writer{}) {
		t.Error("a switch-off module got a Deps.Outbox")
	}
	if got := k.eventsOwners(); got != nil {
		t.Errorf("a switch-off boot claims events owners: %v", got)
	}
}

// The relay is a core task kind: registering it is what turns the executor on
// for an app that declared the flip, and the outbox is bound to the executor.
func TestEventsFlip_RelayIsRegisteredAndTheExecutorBound(t *testing.T) {
	k, _ := flipBoot(t)
	var found bool
	for _, d := range k.taskDefinitions() {
		if d.Kind() == outbox.RelayKind {
			found = true
		}
	}
	if !found {
		t.Fatalf("taskDefinitions lacks %s", outbox.RelayKind)
	}
	if k.tasks == nil || k.tasks.exec == nil {
		t.Fatal("the relay's registration did not build the executor")
	}
}

// The module's consumer name is declared for every manifest, enabled or not, so
// a disabled module's messages are retained; only the enabled module's handlers
// register.
func TestDisabledModuleOutboxRetained(t *testing.T) {
	h, c, _ := flipHost(t)
	c.Modules = append(c.Modules, mod("arkham", func(r *module.Registrar) error {
		t.Error("a disabled module's Register ran")
		return nil
	}).with(func(m *module.Manifest) {
		m.DefaultEnabled = false
		m.OutboxConsumers = []string{disabledAlertCo}
	}))
	k := h.mustBoot(c)
	ob := k.flip().outbox.ob
	if !ob.Declared(disabledAlertCo) {
		t.Errorf("%s of a disabled module is not declared: its messages would not be retained", disabledAlertCo)
	}
	if !ob.Declared(alertConsumer) {
		t.Errorf("%s of the enabled module is not declared", alertConsumer)
	}
	if got := ob.ConsumerNames(); !slices.Equal(got, []string{alertConsumer}) {
		t.Errorf("registered consumers = %v, want only the enabled module's %s", got, alertConsumer)
	}
}

// A handler whose consumer name the manifest did not list fails boot with
// ProblemOutboxConsumerUndeclared.
func TestEventsFlip_UnlistedConsumerFailsBoot(t *testing.T) {
	h, c, _ := flipHost(t, func(c *Config) {
		c.Modules = []module.Module{storageMod("alfred", func(r *module.Registrar) error {
			r.Topics(alertTopic().Spec(events.TopicSpec{Permission: rbac.Need("alfred:signal")}, 4))
			r.OutboxHandler(outbox.ConsumeIntoTask(alertTopic(), alertConsumer, alfredPatrol,
				func(outbox.Message[alertRaised]) (any, error) { return struct{}{}, nil }, outbox.Live()))
			return nil
		}, seedStep(1, "cave")).with(func(m *module.Manifest) {
			m.Permissions = []rbac.PermissionSpec{{Name: "alfred:signal", Title: "Signal"}}
		})}
	})
	_, err := h.boot(c)
	if err == nil || !hasKind(err, module.ProblemOutboxConsumerUndeclared) {
		t.Fatalf("boot = %v, want ProblemOutboxConsumerUndeclared", err)
	}
}

// P7 hands the hub's subscriptions to the bus before it freezes: the hub
// refuses a second Subscriptions call, which shows the first one was made.
func TestEventsFlip_HubSubscriptionsAreTakenAtP7(t *testing.T) {
	k, _ := flipBoot(t)
	_, probs := k.flip().hub.subscriptions()
	if len(probs) != 1 {
		t.Fatalf("a second subscriptions() call gave %v, want one refusal: the boot never took the hub's subscriptions", probs)
	}
}

// The journal's live tail is declared with the modules' other-user predicate,
// not nil: a module that flags a record keeps it from a viewer without
// core:users.view.
func TestEventsFlip_TailIsDeclaredWithTheModulePredicate(t *testing.T) {
	h, c, _ := flipHost(t, func(c *Config) {
		c.Modules = append(c.Modules, mod("selina", func(r *module.Registrar) error {
			r.LogOtherUserData(func(rec log.Record, _ string) bool { return rec.Message == "named another user" })
			return nil
		}))
	})
	k := h.mustBoot(c)
	spec, ok := k.events.catalog().Lookup(logjournal.TailTopicName)
	if !ok {
		t.Fatalf("%s is not declared", logjournal.TailTopicName)
	}
	view := logjournal.RecordView{Time: time.Unix(1, 0).UTC(), Level: "info", Module: "selina", Message: "named another user"}
	flagged := events.RawEnvelope{Payload: mustJSON(t, view)}
	view.Message = "an ordinary line"
	plain := events.RawEnvelope{Payload: mustJSON(t, view)}
	if spec.Visible == nil {
		t.Fatal("the tail has no Visible")
	}
	if spec.Visible(context.Background(), nil, flagged) {
		t.Error("a record the module flagged is visible to a viewer without core:users.view: the tail was declared with a nil predicate")
	}
	if !spec.Visible(context.Background(), nil, plain) {
		t.Error("an ordinary record is withheld")
	}
}

// ListenOptions.OnShutdown carries the hub's close; the real httpapi listener
// registers it. (The listener half is httpapi's TestListen_OnShutdownRuns.)
func TestEventsFlip_RunHandsTheListenerTheHubClose(t *testing.T) {
	h := newSwapHost(t)
	c := h.envConfig(nil)
	c.IdentityBoot = true
	// Listen runs on Run's goroutine and the poll below on the test's.
	var mu sync.Mutex
	var got httpapi.ListenOptions
	c.Listen = func(o httpapi.ListenOptions, _ http.Handler, _ *slog.Logger) (Server, error) {
		mu.Lock()
		got = o
		mu.Unlock()
		return &fakeServer{rec: &recorder{}, stopped: make(chan struct{})}, nil
	}
	c.Notifier = notifier{&recorder{}}
	c.Exit = func(int) {}
	withCore(&c, "core.http.listen", "127.0.0.1:0")
	k := h.mustBoot(c)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- k.Run(ctx) }()
	pollFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got.OnShutdown) > 0
	}, "Run never gave the listener a shutdown hook")
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// The new owner declares no level: --print-storage-levels is the same with and
// without it, so the older binary's rollback guard is unaffected.
func TestEventsOwners_RaiseNoStorageLevel(t *testing.T) {
	k, _ := flipBoot(t)
	owners := k.storageOwners()
	var without []storage.OwnerSpec
	var seen bool
	for _, o := range owners {
		if o.Owner == outboxOwnerID {
			seen = true
			continue
		}
		without = append(without, o)
	}
	if !seen {
		t.Fatalf("storageOwners lacks %s", outboxOwnerID)
	}
	with, base := storage.Supported(owners), storage.Supported(without)
	if len(with) != len(base) {
		t.Fatalf("levels with = %v, without = %v", with, base)
	}
	for o, l := range base {
		if with[o] != l {
			t.Errorf("owner %s level %d with the outbox owner, %d without", o, with[o], l)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// An outbox message staged through Deps.Outbox reaches the consumer's task:
// the relay runs on the executor the kernel started, and ConsumeIntoTask starts
// the module's run through the executor BindRuns handed the outbox. This is
// delivery end to end on the real boot.
func TestEventsFlip_StagedMessageStartsTheConsumersTask(t *testing.T) {
	k, p := flipBoot(t)
	if err := k.openEvents(); err != nil {
		t.Fatal(err)
	}
	if err := k.startTasks(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k.stopTasks(context.Background()) })
	ks, _ := k.store.db.Keyspace("alfred")
	b := ks.NewBatch()
	if err := outbox.Stage(p.deps.Outbox, b, alertTopic(), "gotham", alertRaised{Block: "Crime Alley"}); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if err := b.Commit(t.Context(), storage.Sync); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.ran:
	case <-time.After(30 * time.Second):
		t.Fatal("a staged message never started the consumer's task: the relay, the nudger or BindRuns is not live")
	}
}

// The executor publishes core.task.run-changed.v1 on the bus: Options.Events is
// set, so a subscriber sees the relay's own run.
func TestEventsFlip_ExecutorPublishesTaskTopics(t *testing.T) {
	got := make(chan struct{}, 8)
	k, _ := flipBoot(t, func(c *Config) {
		c.Modules = append(c.Modules, mod("selina", func(r *module.Registrar) error {
			r.Subscribe(events.OnRaw(tasks.RunChangedTopicSpec().Name, "selina.watch-runs",
				func(context.Context, events.RawEnvelope) {
					select {
					case got <- struct{}{}:
					default:
					}
				}))
			return nil
		}))
	})
	if err := k.openEvents(); err != nil {
		t.Fatal(err)
	}
	if err := k.startTasks(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k.stopTasks(context.Background()) })
	select {
	case <-got:
	case <-time.After(30 * time.Second):
		t.Fatal("no core.task.run-changed.v1 reached a subscriber: the executor has no bus")
	}
}

// depsScopingEvents is TestDepsScoping's F6 cell (06 §6.4.4): on the identity
// boot every module's Deps.Outbox and Deps.Events are bound to that module. A
// second module "bane" stages and publishes through its own handles: the topic
// alfred declared is refused to it with ErrNotOwner (outbox) and never reaches
// the bus (events), while alfred's own staging into the same topic works.
func depsScopingEvents(t *testing.T) {
	var bane module.Deps
	k, alfred := flipBoot(t, func(c *Config) {
		c.Modules = append(c.Modules, storageMod("bane", func(r *module.Registrar) error {
			bane = r.Deps()
			return r.Err()
		}))
	})
	if bane.Outbox == (outbox.Writer{}) {
		t.Fatal("bane got the zero Deps.Outbox on an identity boot")
	}
	if alfred.deps.Outbox == bane.Outbox {
		t.Fatal("two modules share one Deps.Outbox writer: it is not module-scoped")
	}
	ks, _ := k.store.db.Keyspace("alfred")
	stage := func(w outbox.Writer) error {
		b := ks.NewBatch()
		defer func() { _ = b.Close() }()
		return outbox.Stage(w, b, alertTopic(), "gotham", alertRaised{Block: "Crime Alley"})
	}
	if err := stage(bane.Outbox); !errors.Is(err, outbox.ErrNotOwner) {
		t.Fatalf("bane staging into alfred's topic = %v, want ErrNotOwner", err)
	}
	if err := stage(alfred.deps.Outbox); err != nil {
		t.Fatalf("alfred staging into its own topic: %v", err)
	}
	if alfred.deps.Events == bane.Events {
		t.Fatal("two modules share one Deps.Events scope")
	}
}

// viewHolder is a Checker that holds perms and satisfies the requirements in
// sat, compared by value.
type viewHolder struct {
	perms []rbac.Permission
	sat   []rbac.Requirement
}

func (v viewHolder) Has(p rbac.Permission) bool { return slices.Contains(v.perms, p) }
func (v viewHolder) Satisfies(r rbac.Requirement) bool {
	return slices.ContainsFunc(v.sat, func(s rbac.Requirement) bool { return reflect.DeepEqual(s, r) })
}
func (viewHolder) Confined() bool                { return false }
func (v viewHolder) Concrete() []rbac.Permission { return v.perms }

// TestEventsFlip_TaskTopicsFollowTheDefinitionsViewRule: the run-carrying task
// topics core declares at P7 carry the definition rule as their Visible
// (tasks.WithViewRule over the registrations), not the fail-closed admin
// default. alfred.patrol asks alfred:signal to view, so a holder of that scope
// and no core:tasks.view sees a patrol run on the stream, a holder of
// core:tasks.view alone does not, and a run of a kind nobody registered takes
// core:tasks.view. The log-appended topic keeps the admin default: a line
// names no kind.
func TestEventsFlip_TaskTopicsFollowTheDefinitionsViewRule(t *testing.T) {
	k, _ := flipBoot(t)
	signal := viewHolder{perms: []rbac.Permission{"alfred:signal"}, sat: []rbac.Requirement{rbac.Need("alfred:signal")}}
	auditor := viewHolder{perms: []rbac.Permission{"core:tasks.view"}}
	run := func(kind string) events.RawEnvelope {
		return events.RawEnvelope{Payload: mustJSON(t, map[string]string{"kind": kind})}
	}
	for _, topic := range []string{tasks.RunChangedTopicName, tasks.FinishedTopicName} {
		spec, ok := k.events.bus.Catalog().Lookup(topic)
		if !ok {
			t.Fatalf("%s is not in the catalog", topic)
		}
		for _, c := range []struct {
			name string
			who  rbac.Checker
			env  events.RawEnvelope
			want bool
		}{
			{"alfred:signal holder, alfred.patrol", signal, run(string(alfredPatrol)), true},
			{"core:tasks.view holder, alfred.patrol", auditor, run(string(alfredPatrol)), false},
			{"core:tasks.view holder, unregistered kind", auditor, run("alfred.gone"), true},
			{"alfred:signal holder, unregistered kind", signal, run("alfred.gone"), false},
		} {
			if got := spec.Visible(t.Context(), c.who, c.env); got != c.want {
				t.Errorf("%s, %s: visible = %v, want %v", topic, c.name, got, c.want)
			}
		}
	}
	// core.task.log-appended.v1: a line names no kind, so no per-kind rule can
	// apply and it stays with administrators. A core:tasks.view holder, who sees
	// the runs themselves, does not see their log lines on the stream (they read
	// them over REST, where the run is known).
	logs, _ := k.events.bus.Catalog().Lookup(tasks.LogAppendedTopicName)
	admin := viewHolder{perms: []rbac.Permission{rbac.CoreAdmin}}
	for _, c := range []struct {
		name string
		who  rbac.Checker
		want bool
	}{
		{"alfred:signal holder", signal, false},
		{"core:tasks.view holder", auditor, false},
		{"core:admin holder", admin, true},
		{"no checker", nil, false},
	} {
		if got := logs.Visible(t.Context(), c.who, run(string(alfredPatrol))); got != c.want {
			t.Errorf("core.task.log-appended.v1, %s: visible = %v, want %v", c.name, got, c.want)
		}
	}
}
