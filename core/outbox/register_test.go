// file: core/outbox/register_test.go
// version: 1.0.0
// guid: 5e2b1c64-8f0d-4a37-b6c9-3d7a91e4f2b8
// last-edited: 2026-11-12

package outbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

func okHandler(context.Context, outbox.Message[Dispatch]) error { return nil }

func okParams(m outbox.Message[Dispatch]) (any, error) {
	return respondParams{Villain: m.Payload.Villain, Block: m.Payload.Block}, nil
}

func okRaw(context.Context, outbox.RawMessage) error { return nil }

// Every registration rule has its own sentinel, and every sentinel's
// registration is refused (never accepted and never skipped): the table is the
// refusal list of 05-events §5E.6 and §5E.7.
func TestOutbox_RegistrationRefusals(t *testing.T) {
	type tc struct {
		name  string
		owner string
		h     outbox.Handler
		want  error
	}
	cases := []tc{
		{"undeclared core consumer", "core", outbox.Consume(notice(), "core.notify.unlisted", okHandler), outbox.ErrConsumerUndeclared},
		{"undeclared module consumer", "gotham", outbox.ConsumeIntoTask(signal(), "gotham.dispatch.unlisted", runKind, okParams, outbox.Live()), outbox.ErrConsumerUndeclared},
		{"consumer name not owner-prefixed", "core", outbox.Consume(notice(), "gotham.dispatch.respond", okHandler), outbox.ErrConsumerName},
		{"consumer name with an upper-case segment", "core", outbox.Consume(notice(), "core.Notify", okHandler), outbox.ErrConsumerName},
		{"foreign run kind", "gotham", outbox.ConsumeIntoTask(signal(), consumerC, "wayne.vault.guard", okParams, outbox.Live()), outbox.ErrForeignKind},
		{"task consumer without a mode", "gotham", outbox.ConsumeIntoTask(signal(), consumerC, runKind, okParams), outbox.ErrModeOption},
		{"task consumer with both modes", "gotham", outbox.ConsumeIntoTask(signal(), consumerC, runKind, okParams, outbox.Live(), outbox.Preview()), outbox.ErrModeOption},
		{"task consumer with Live twice", "gotham", outbox.ConsumeIntoTask(signal(), consumerC, runKind, okParams, outbox.Live(), outbox.Live()), outbox.ErrModeOption},
		{"raw task consumer without a mode", "wayne", outbox.ConsumeIntoTaskRaw(signalName, consumerW, "wayne.vault.guard", func(outbox.RawMessage) (any, error) { return nil, nil }), outbox.ErrModeOption},
		{"Live on a plain consumer", "core", outbox.Consume(notice(), consumerN, okHandler, outbox.Live()), outbox.ErrModeNotAllowed},
		{"Preview on a raw consumer", "core", outbox.ConsumeRaw(noticeName, consumerN, okRaw, outbox.Preview()), outbox.ErrModeNotAllowed},
		{"Unordered Tx", "core", outbox.ConsumeTx(notice(), consumerN, func(context.Context, *outbox.Tx, outbox.Message[Dispatch]) error { return nil }, outbox.Unordered()), outbox.ErrUnorderedTx},
		{"Unordered task", "gotham", outbox.ConsumeIntoTask(signal(), consumerC, runKind, okParams, outbox.Live(), outbox.Unordered()), outbox.ErrUnorderedTx},
		{"Where of another payload type", "core", outbox.Consume(notice(), consumerN, okHandler, outbox.Where(func(outbox.Message[Alarm]) bool { return true })), outbox.ErrWhereType},
		{"WhereRaw on a typed consumer", "core", outbox.Consume(notice(), consumerN, okHandler, outbox.WhereRaw(func(outbox.RawMessage) bool { return true })), outbox.ErrWhereType},
		{"typed Where on a raw consumer", "core", outbox.ConsumeRaw(noticeName, consumerN, okRaw, outbox.Where(func(outbox.Message[Dispatch]) bool { return true })), outbox.ErrWhereType},
		{"module plain consumer", "gotham", outbox.Consume(signal(), consumerC, okHandler), outbox.ErrModuleHandler},
		{"module Tx consumer", "gotham", outbox.ConsumeTx(signal(), consumerC, func(context.Context, *outbox.Tx, outbox.Message[Dispatch]) error { return nil }), outbox.ErrModuleHandler},
		{"module raw consumer", "gotham", outbox.ConsumeRaw(signalName, consumerC, okRaw), outbox.ErrModuleHandler},
		{"typed consumer of another module's topic", "gotham", outbox.ConsumeIntoTask(notice0Topic(alarmName), consumerC, runKind, okParams, outbox.Live()), outbox.ErrForeignTopic},
		{"unknown topic", "core", outbox.ConsumeRaw("core.nothing.here.v1", consumerN, okRaw), outbox.ErrUnknownTopic},
		{"bus-only topic", "core", outbox.ConsumeRaw(busOnly, consumerN, okRaw), outbox.ErrNotDurable},
		{"payload type differs from the topic's", "core", outbox.Consume(outbox.NewTopic[Alarm](noticeName), consumerN, func(context.Context, outbox.Message[Alarm]) error { return nil }), outbox.ErrPayloadType},
		{"zero Handler", "core", outbox.Handler{}, outbox.ErrInvalidHandler},
		{"MaxAttempts zero", "core", outbox.Consume(notice(), consumerN, okHandler, outbox.MaxAttempts(0)), outbox.ErrInvalidHandler},
		{"Backoff with min above max", "core", outbox.Consume(notice(), consumerN, okHandler, outbox.Backoff(time.Minute, time.Second)), outbox.ErrInvalidHandler},
		{"Backoff with a zero floor", "core", outbox.Consume(notice(), consumerN, okHandler, outbox.Backoff(0, time.Second)), outbox.ErrInvalidHandler},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, rigOpts{noExec: true, noFreeze: true})
			err := r.ob.Register(c.owner, c.h)
			if !errors.Is(err, c.want) {
				t.Fatalf("Register = %v, want %v", err, c.want)
			}
			// A refused consumer is not recorded: its name is still free.
			if err := r.ob.Register("core", outbox.Consume(notice(), consumerN, okHandler)); err != nil {
				t.Fatalf("a refused registration left state behind: %v", err)
			}
		})
	}
}

// notice0Topic is a typed Topic[Dispatch] on any topic name.
func notice0Topic(name string) outbox.Topic[Dispatch] { return outbox.NewTopic[Dispatch](name) }

// A well-formed registration of every form is accepted, and a name registered
// twice is refused the second time.
func TestOutbox_RegistrationAcceptsEveryForm(t *testing.T) {
	r := newRig(t, rigOpts{noExec: true, noFreeze: true})
	must(t, r.ob.Register("core",
		outbox.Consume(notice(), consumerN, okHandler, outbox.Unordered(), outbox.MaxAttempts(5), outbox.Backoff(time.Millisecond, time.Second), outbox.FromBeginning()),
		outbox.ConsumeTx(notice(), consumerN2, func(context.Context, *outbox.Tx, outbox.Message[Dispatch]) error { return nil }),
		outbox.ConsumeRaw(noticeName, consumerN3, okRaw, outbox.WhereRaw(func(outbox.RawMessage) bool { return true })),
	))
	must(t, r.ob.Register("gotham",
		outbox.ConsumeIntoTask(signal(), consumerC, runKind, okParams, outbox.Live(), outbox.Where(func(outbox.Message[Dispatch]) bool { return true })),
		outbox.ConsumeIntoTaskRaw(signalName, consumerD, runKind, func(outbox.RawMessage) (any, error) { return respondParams{}, nil }, outbox.Preview()),
	))
	must(t, r.ob.Register("wayne", outbox.ConsumeIntoTaskRaw(signalName, consumerW, "wayne.vault.guard", func(outbox.RawMessage) (any, error) { return nil, nil }, outbox.Live())))
	err := r.ob.Register("core", outbox.Consume(notice(), consumerN, okHandler))
	if !errors.Is(err, outbox.ErrDuplicateConsumer) {
		t.Fatalf("second registration of %s = %v, want ErrDuplicateConsumer", consumerN, err)
	}
	must(t, r.ob.Freeze())
	err = r.ob.Register("core", outbox.Consume(notice(), "core.notify.late", okHandler))
	if !errors.Is(err, outbox.ErrFrozen) {
		t.Fatalf("Register after Freeze = %v, want ErrFrozen", err)
	}
	if err := r.ob.DeclareConsumers("gotham", "gotham.late.one"); !errors.Is(err, outbox.ErrFrozen) {
		t.Fatalf("DeclareConsumers after Freeze = %v, want ErrFrozen", err)
	}
	must(t, r.ob.Freeze()) // idempotent
}

// One call reports every bad consumer, and the good ones in it are still
// registered.
func TestOutbox_RegisterJoinsErrors(t *testing.T) {
	r := newRig(t, rigOpts{noExec: true, noFreeze: true})
	err := r.ob.Register("core",
		outbox.Consume(notice(), "core.notify.unlisted", okHandler),
		outbox.Consume(notice(), consumerN, okHandler),
		outbox.ConsumeRaw("core.nothing.here.v1", consumerN2, okRaw),
	)
	if !errors.Is(err, outbox.ErrConsumerUndeclared) || !errors.Is(err, outbox.ErrUnknownTopic) {
		t.Fatalf("Register = %v, want both ErrConsumerUndeclared and ErrUnknownTopic", err)
	}
	if err := r.ob.Register("core", outbox.Consume(notice(), consumerN, okHandler)); !errors.Is(err, outbox.ErrDuplicateConsumer) {
		t.Fatalf("the good consumer of a mixed call is not registered: %v", err)
	}
}

// DeclareConsumers refuses a malformed name and a name declared twice, and
// Declared sees both the core list and a manifest's names.
func TestOutbox_DeclareConsumers(t *testing.T) {
	r := newRig(t, rigOpts{noExec: true, noFreeze: true})
	if err := r.ob.DeclareConsumers("gotham", "gotham.Bad"); !errors.Is(err, outbox.ErrConsumerName) {
		t.Fatalf("bad name: %v", err)
	}
	if err := r.ob.DeclareConsumers("gotham", consumerC); !errors.Is(err, outbox.ErrDuplicateConsumer) {
		t.Fatalf("declared twice: %v", err)
	}
	for _, n := range []string{consumerN, consumerC, consumerW} {
		if !r.ob.Declared(n) {
			t.Fatalf("%s is not Declared", n)
		}
	}
	if r.ob.Declared("gotham.nothing") {
		t.Fatal("an undeclared name is Declared")
	}
	if got := r.ob.ConsumerNames(); len(got) != 0 {
		t.Fatalf("ConsumerNames lists registered consumers only, got %v before any registration", got)
	}
	must(t, r.ob.Register("core", outbox.Consume(notice(), consumerN2, okHandler), outbox.Consume(notice(), consumerN, okHandler)))
	if got := r.ob.ConsumerNames(); len(got) != 2 || got[0] != consumerN2 || got[1] != consumerN {
		t.Fatalf("ConsumerNames = %v, want the two registered, sorted", got)
	}
}

// A durable topic whose partition count the key layout cannot hold is refused
// at Freeze, so the failure is at boot and not at the first message.
func TestOutbox_FreezeRefusesBadPartitions(t *testing.T) {
	r := newRig(t, rigOpts{partitions: 100, noExec: true, noFreeze: true})
	err := r.ob.Freeze()
	if !errors.Is(err, outbox.ErrBadPartitions) {
		t.Fatalf("Freeze with a 100-partition topic = %v, want ErrBadPartitions", err)
	}
}

// ConsumeIntoTaskRaw lets Wayne consume Gotham's topic by name without
// importing its payload type; the run starts in the mode the consumer asked
// for, and the message's payload reaches the params function intact.
func TestOutbox_ConsumeIntoTaskRawWithoutImport(t *testing.T) {
	rs := &responder{}
	var mu sync.Mutex
	var raws []outbox.RawMessage
	r := newRig(t, rigOpts{
		defs: []tasks.Definition{rs.def(), wayneKind(rs)},
		register: func(testing.TB, *rig) []registration {
			return []registration{{owner: "wayne", hs: []outbox.Handler{outbox.ConsumeIntoTaskRaw(signalName, consumerW, "wayne.vault.guard",
				func(m outbox.RawMessage) (any, error) {
					mu.Lock()
					raws = append(raws, m)
					mu.Unlock()
					var d Dispatch
					if err := json.Unmarshal(m.Payload, &d); err != nil {
						return nil, err
					}
					return respondParams(d), nil
				}, outbox.Live())}}}
		},
	})
	r.stageGotham("batcave", Dispatch{Villain: "Scarecrow", Block: 7})
	eventually(t, "the run", func() bool { return len(rs.all()) == 1 })
	if got := rs.all()[0]; got.Villain != "Scarecrow" || got.Block != 7 {
		t.Fatalf("run params %+v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(raws) != 1 || raws[0].Topic != signalName || raws[0].Key != "batcave" || raws[0].Attempt != 1 {
		t.Fatalf("raw message %+v", raws)
	}
	runs := r.runsOf("wayne.vault.guard")
	if len(runs) != 1 || runs[0].Mode != tasks.ModeLive {
		t.Fatalf("runs %+v, want one Live run", runs)
	}
}

// wayneKind is a second fixture kind, in Wayne's namespace, that records
// through rs.
func wayneKind(rs *responder) tasks.Definition {
	return tasks.Task("wayne.vault.guard", tasks.TaskSpec[respondParams, string]{
		Title:   "Guard the vault",
		Effects: tasks.Writes(tasks.Res("wayne", "vault")),
		Perms:   tasks.Permissions{Start: rbac.Need("wayne:run")},
		Resume:  tasks.ResumeFromZero,
		Run:     func(_ *tasks.RC, p respondParams) (string, error) { rs.add(p); return "guarded", nil },
	})
}

// A ConsumeIntoTask consumer starts its run in the mode it names: Live runs
// are Live, Preview runs are Preview (never the default), once per message.
func TestOutbox_ConsumeIntoTaskModes(t *testing.T) {
	for _, tc := range []struct {
		name string
		opt  outbox.ConsumerOption
		want tasks.Mode
	}{
		{"live", outbox.Live(), tasks.ModeLive},
		{"preview", outbox.Preview(), tasks.ModePreview},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rs := &responder{}
			r := newRig(t, rigOpts{
				defs: []tasks.Definition{rs.def()},
				register: func(testing.TB, *rig) []registration {
					return []registration{{owner: "gotham", hs: []outbox.Handler{
						outbox.ConsumeIntoTask(signal(), consumerC, runKind, okParams, tc.opt)}}}
				},
			})
			r.stageGotham("batcave", Dispatch{Villain: "Mr. Freeze", Block: 1})
			eventually(t, "the run to be recorded", func() bool { return len(r.runsOf(runKind)) == 1 })
			runs := r.runsOf(runKind)
			if runs[0].Mode != tc.want {
				t.Fatalf("run mode %v, want %v", runs[0].Mode, tc.want)
			}
			if runs[0].Trigger != tasks.TriggerOutbox {
				t.Fatalf("run trigger %q, want outbox", runs[0].Trigger)
			}
			stays(t, "one run per message", 100*time.Millisecond, func() bool { return len(r.runsOf(runKind)) == 1 })
		})
	}
}

// A params function that fails permanently dead-letters the message; one that
// returns an unencodable value does too (no redelivery can change it).
func TestOutbox_ConsumeIntoTaskParamsFailure(t *testing.T) {
	rs := &responder{}
	r := newRig(t, rigOpts{
		defs: []tasks.Definition{rs.def()},
		register: func(testing.TB, *rig) []registration {
			return []registration{{owner: "gotham", hs: []outbox.Handler{
				outbox.ConsumeIntoTask(signal(), consumerC, runKind, func(m outbox.Message[Dispatch]) (any, error) {
					if m.Payload.Villain == "Joker" {
						return make(chan int), nil // does not encode
					}
					return respondParams{Villain: m.Payload.Villain}, nil
				}, outbox.Live())}}}
		},
	})
	r.stageGotham("batcave", Dispatch{Villain: "Joker"})
	r.stageGotham("batcave", Dispatch{Villain: "Penguin"})
	eventually(t, "the dead letter and the later run", func() bool {
		l, _, _ := r.ob.ListDead(context.Background(), "", 5)
		return len(l) == 1 && len(rs.all()) == 1
	})
	l, _, _ := r.ob.ListDead(context.Background(), "", 5)
	if l[0].Token != "permanent" || l[0].Attempts != 1 {
		t.Fatalf("dead letter %+v, want permanent after one attempt", l[0])
	}
	if rs.all()[0].Villain != "Penguin" {
		t.Fatalf("run saw %+v", rs.all())
	}
}

// Dead letters make the component Degraded with the dead_letters reason, and a
// relay switched off by its dial reports relay_disabled without losing or
// delivering anything; turning it back on delivers the backlog in order.
func TestOutbox_RelayDisabledDegradedNoLoss(t *testing.T) {
	rc := &recorder{}
	r := newRig(t, rigOpts{register: consumeInto(rc, consumerN)})
	r.setConfig(func(c *outbox.Config) { c.RelayEnabled = false })
	// The relay in flight notices the dial on its next config read; start a
	// fresh run so it reads the switch at once.
	r.stageMany("wayne-manor", "Joker", "Penguin")
	eventually(t, "the relay to report it is off", func() bool {
		h := r.healthOf()
		return h.Status == health.StatusDegraded && h.Reason == "relay_disabled"
	})
	// Whatever the running relay delivered before the switch is fine; after it
	// reports off, nothing more is delivered and nothing is lost.
	before := rc.count()
	r.stageMany("wayne-manor", "Riddler")
	stays(t, "no delivery while off", 150*time.Millisecond, func() bool { return rc.count() == before })

	r.setConfig(func(c *outbox.Config) { c.RelayEnabled = true })
	r.startRelay()
	eventually(t, "the backlog", func() bool { return rc.count() == 3 })
	if got := rc.villains(); got[0] != "Joker" || got[1] != "Penguin" || got[2] != "Riddler" {
		t.Fatalf("backlog delivered as %v", got)
	}
	eventually(t, "core.outbox to recover", func() bool { return r.healthOf().Status == health.StatusUp })
}

// Status lists every declared consumer, enabled or not.
func TestOutbox_StatusListsDeclaredConsumers(t *testing.T) {
	rc := &recorder{}
	r := newRig(t, rigOpts{register: consumeInto(rc, consumerN)})
	r.stage("wayne-manor", Dispatch{Villain: "Joker"})
	eventually(t, "the delivery", func() bool { return rc.count() == 1 })
	st, err := r.ob.Status(context.Background())
	must(t, err)
	byName := map[string]outbox.ConsumerStatus{}
	for _, c := range st.Consumers {
		byName[c.Name] = c
	}
	for _, n := range []string{consumerN, consumerN2, consumerN3, consumerC, consumerD, consumerW} {
		if _, ok := byName[n]; !ok {
			t.Fatalf("Status misses declared consumer %s: %+v", n, st.Consumers)
		}
	}
	if c := byName[consumerN]; !c.Enabled || !c.Declared || c.Module != "core" || c.Topic != noticeName {
		t.Fatalf("registered consumer %+v", c)
	}
	if c := byName[consumerC]; c.Enabled || !c.Declared || c.Module != "gotham" {
		t.Fatalf("declared-but-unregistered consumer %+v", c)
	}
	var topic *outbox.TopicStatus
	for i := range st.Topics {
		if st.Topics[i].Topic == noticeName {
			topic = &st.Topics[i]
		}
	}
	if topic == nil || topic.High != 1 || topic.Partitions != 2 {
		t.Fatalf("topic status %+v", st.Topics)
	}
}
