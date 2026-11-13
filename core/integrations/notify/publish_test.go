// file: core/integrations/notify/publish_test.go
// version: 1.0.0
// guid: 7c3e1a95-d208-4b6f-8e41-a05f92d7c318
// last-edited: 2026-11-13

package notify_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/notify"
	"github.com/falkcorp/mediacore/storage"
)

// A delivery is staged in the caller's own batch: it commits with the
// caller's state change and vanishes with it (I1).
func TestPublish_SameBatchAsCallerState(t *testing.T) {
	r := newRig(t, rigOpts{noRelay: true})
	r.putRule(rule("roof", []string{signalName}, commissioner))

	// A batch that is abandoned stages nothing: the delivery was never
	// written anywhere else.
	lost := r.gothamKS.NewBatch()
	must(t, lost.Set([]byte("case/riddler"), []byte("open")))
	must(t, r.svc.Publish(context.Background(), lost, signalNote("Riddler")))
	lost.Close()

	// A batch that commits carries the state row and the delivery together.
	b := r.gothamKS.NewBatch()
	must(t, b.Set([]byte("case/joker"), []byte("open")))
	n := signalNote("Joker")
	must(t, r.svc.Publish(context.Background(), b, n))
	must(t, b.Commit(context.Background(), storage.Sync))
	b.Close()

	if v, err := r.gothamKS.Get(context.Background(), []byte("case/joker")); err != nil || string(v) != "open" {
		t.Fatalf("the caller's state row = %q, %v", v, err)
	}
	r.startRelay()
	eventually(t, "the committed delivery", func() bool { return r.fake.Sent() == 1 })
	stays(t, "no other delivery", 150*time.Millisecond, func() bool { return r.fake.Sent() == 1 })
	got := r.fake.Calls()[0].Notification
	if got.Fields["villain"] != "Joker" {
		t.Errorf("delivered the %q notification, want the committed Joker one", got.Fields["villain"])
	}
	if got.ID == "" || got.App != "batcave" {
		t.Errorf("ID %q App %q: Publish must stamp both", got.ID, got.App)
	}
}

// One entry per distinct resolved target: an instance two rules name is
// delivered to once, a disabled rule and a rule above the severity add none (I1).
func TestPublish_OneDeliveryPerTargetInCallerBatch(t *testing.T) {
	r := newRig(t, rigOpts{noRelay: true, metrics: true})
	r.putRule(rule("a-roof", []string{signalName}, commissioner, alfred))
	r.putRule(rule("b-all-gotham", []string{"gotham.*"}, alfred, oracle))
	high := rule("c-critical", []string{signalName}, integrations.Ref{Kind: integrations.KindNotify, Instance: "never"})
	high.MinSeverity = notify.SeverityCritical
	r.putRule(high)
	off := rule("d-off", []string{signalName}, integrations.Ref{Kind: integrations.KindNotify, Instance: "also-never"})
	off.Enabled = false
	r.putRule(off)

	b := r.gothamKS.NewBatch()
	defer b.Close()
	must(t, r.svc.Publish(context.Background(), b, signalNote("Penguin")))
	must(t, b.Commit(context.Background(), storage.Sync))
	r.startRelay()

	eventually(t, "three deliveries", func() bool { return r.fake.Sent() == 3 })
	stays(t, "exactly three deliveries", 200*time.Millisecond, func() bool { return r.fake.Sent() == 3 })
	for _, ref := range []integrations.Ref{commissioner, alfred, oracle} {
		if n := len(callsTo(r.fake, ref)); n != 1 {
			t.Errorf("%s got %d deliveries, want 1", ref, n)
		}
	}
	ids := map[string]bool{}
	for _, id := range r.fake.IDs() {
		ids[id] = true
	}
	if len(ids) != 1 {
		t.Errorf("deliveries carry %d notification IDs, want the one Publish assigned", len(ids))
	}
	if got := sample(t, r.mp, "mediacore_notify_targets_total", map[string]string{"topic": signalName, "result": "resolved"}); got != 3 {
		t.Errorf("targets_total{resolved} = %v, want 3", got)
	}
}

// No enabled rule covers it: nothing is staged, with a batch or without (I1).
func TestPublish_NoRuleStagesNothing(t *testing.T) {
	r := newRig(t, rigOpts{metrics: true})
	off := rule("off", []string{signalName}, commissioner)
	off.Enabled = false
	r.putRule(off)

	r.publish(signalNote("Scarecrow"))
	b := r.gothamKS.NewBatch()
	must(t, r.svc.Publish(context.Background(), b, signalNote("Scarecrow")))
	must(t, b.Commit(context.Background(), storage.Sync))
	b.Close()

	stays(t, "an empty outbox", 250*time.Millisecond, func() bool {
		st := r.status()
		return r.fake.Sent() == 0 && st.Pending == 0 && st.Retrying == 0 && st.Dead == 0 && st.IntakeBacklog == 0
	})
	if got := sample(t, r.mp, "mediacore_notify_targets_total", map[string]string{"topic": signalName, "result": "no_rule"}); got != 2 {
		t.Errorf("targets_total{no_rule} = %v, want 2", got)
	}
	if got := sample(t, r.mp, "mediacore_notify_published_total", map[string]string{"topic": signalName, "severity": "warning"}); got != 2 {
		t.Errorf("published_total = %v, want 2", got)
	}
}

// Only a declared topic can be published (I2).
func TestPublish_UndeclaredTopicRejected(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.putRule(rule("roof", []string{signalName}, commissioner))
	err := r.svc.Publish(context.Background(), nil, notify.Notification{Topic: "gotham.nothing.happened.v1", Title: "x"})
	if !errors.Is(err, notify.ErrUndeclaredTopic) {
		t.Fatalf("err = %v, want ErrUndeclaredTopic", err)
	}
	stays(t, "nothing sent", 100*time.Millisecond, func() bool { return r.fake.Sent() == 0 })
}

// A declared topic with no notify metadata is not notifiable (I2).
func TestPublish_TopicWithoutNotifyMetaRejected(t *testing.T) {
	r := newRig(t, rigOpts{})
	err := r.svc.Publish(context.Background(), nil, notify.Notification{Topic: escapedName, Title: "x"})
	if !errors.Is(err, notify.ErrNoNotifyMeta) {
		t.Fatalf("err = %v, want ErrNoNotifyMeta", err)
	}
	if errors.Is(err, notify.ErrUndeclaredTopic) {
		t.Error("a declared topic is reported undeclared")
	}
}

// Publish bounds the structured extras and refuses an unknown severity, and a
// zero severity takes the topic's declared default (05-notify section 3).
func TestPublish_ValidatesTheNotification(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.putRule(rule("roof", []string{signalName, vaultName}, commissioner))

	many := map[string]string{}
	for i := range notify.MaxFields + 1 {
		many[strings.Repeat("k", i+1)] = "v"
	}
	for name, n := range map[string]notify.Notification{
		"too many fields": {Topic: signalName, Fields: many},
		"a long value":    {Topic: signalName, Fields: map[string]string{"k": strings.Repeat("v", notify.MaxFieldBytes+1)}},
		"a long key":      {Topic: signalName, Fields: map[string]string{strings.Repeat("k", notify.MaxFieldBytes+1): "v"}},
	} {
		if err := r.svc.Publish(context.Background(), nil, n); !errors.Is(err, notify.ErrFieldsTooLarge) {
			t.Errorf("%s: err = %v, want ErrFieldsTooLarge", name, err)
		}
	}
	if err := r.svc.Publish(context.Background(), nil, notify.Notification{Topic: signalName, Severity: 99}); !errors.Is(err, integrations.ErrInvalid) {
		t.Errorf("an unknown severity: err = %v, want ErrInvalid", err)
	}

	r.publish(notify.Notification{Topic: signalName, Title: "default"})                                   // zero: the topic says warning
	r.publish(notify.Notification{Topic: signalName, Title: "loud", Severity: notify.SeverityCritical})   // explicit: kept
	r.publish(notify.Notification{Topic: vaultName, Title: "vault", Fields: map[string]string{"a": "b"}}) // the vault topic says error
	eventually(t, "three deliveries", func() bool { return r.fake.Sent() == 3 })
	got := map[string]notify.Severity{}
	for _, c := range r.fake.Calls() {
		got[c.Notification.Title] = c.Notification.Severity
	}
	want := map[string]notify.Severity{"default": notify.SeverityWarning, "loud": notify.SeverityCritical, "vault": notify.SeverityError}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s delivered at %v, want %v", k, got[k], v)
		}
	}
}

// Each Publish gets its own ULID and the clock's time (the idempotency key
// receivers dedupe on), and an explicit time is kept.
func TestPublish_StampsAFreshIDAndTime(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.putRule(rule("roof", []string{signalName}, commissioner))
	at := time.Date(2039, 4, 1, 22, 0, 0, 0, time.UTC)
	r.publish(signalNote("Bane"))
	n := signalNote("Bane")
	n.At = at
	n.ID = "01JBATCALLERCHOSEN0000000X" // a caller cannot choose it
	r.publish(n)
	eventually(t, "two deliveries", func() bool { return r.fake.Sent() == 2 })
	calls := r.fake.Calls()
	a, b := calls[0].Notification, calls[1].Notification
	if a.ID == b.ID || len(a.ID) != 26 || len(b.ID) != 26 || b.ID == n.ID {
		t.Errorf("IDs %q and %q: want two fresh 26-character ULIDs", a.ID, b.ID)
	}
	for _, c := range calls {
		x := c.Notification
		if x.At.Equal(at) && x.Fields["villain"] != "Bane" {
			t.Error("mixed up the notifications")
		}
	}
	var zeroSeen, explicit bool
	for _, c := range calls {
		if c.Notification.At.Equal(at) {
			explicit = true
		} else if !c.Notification.At.IsZero() && time.Since(c.Notification.At) < time.Minute {
			zeroSeen = true
		}
	}
	if !explicit || !zeroSeen {
		t.Errorf("At: explicit time kept = %v, zero filled from the clock = %v", explicit, zeroSeen)
	}
}

// A module's Publisher publishes only its own topics (I2).
func TestPublisher_ForeignTopicRefused(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.putRule(rule("all", []string{"gotham.*", "wayne.*"}, commissioner))

	gotham, wayne := r.svc.For("gotham"), r.svc.For("wayne")
	if err := gotham.Publish(context.Background(), nil, signalNote("Two-Face")); err != nil {
		t.Fatalf("a module's own topic: %v", err)
	}
	for name, err := range map[string]error{
		"another module's topic": gotham.Publish(context.Background(), nil, notify.Notification{Topic: vaultName, Title: "x"}),
		"the other way round":    wayne.Publish(context.Background(), nil, signalNote("Two-Face")),
		"no module":              r.svc.For("").Publish(context.Background(), nil, signalNote("Two-Face")),
		"a prefix of the module": r.svc.For("goth").Publish(context.Background(), nil, signalNote("Two-Face")),
		"a core topic":           gotham.Publish(context.Background(), nil, notify.Notification{Topic: notify.DeliveryTopicName}),
	} {
		if !errors.Is(err, notify.ErrForeignTopic) {
			t.Errorf("%s: err = %v, want ErrForeignTopic", name, err)
		}
	}
	eventually(t, "the one own-topic delivery", func() bool { return r.fake.Sent() == 1 })
	stays(t, "no other delivery", 150*time.Millisecond, func() bool { return r.fake.Sent() == 1 })
}

// A Notify topic published on the bus alone sends nothing: the owner emits a
// notification through Publish, and no bus subscriber routes one (I2).
func TestNotifyMeta_BusEventSendsNothing(t *testing.T) {
	r := newRig(t, rigOpts{metrics: true})
	r.putRule(rule("roof", []string{signalName}, commissioner, alfred))
	must(t, r.bus.Open())
	t.Cleanup(func() { _ = r.bus.Close(context.Background()) })

	for range 3 {
		events.Publish(r.bus.Scope("gotham"), context.Background(), signalTopic(), "block-7", Signal{Villain: "Joker", Block: 7})
	}
	stays(t, "an idle notify pipeline", 300*time.Millisecond, func() bool {
		st := r.status()
		return r.fake.Sent() == 0 && st.Pending == 0 && st.Retrying == 0 && st.IntakeBacklog == 0
	})
	if got := sample(t, r.mp, "mediacore_notify_published_total", map[string]string{"topic": signalName}); got != 0 {
		t.Errorf("published_total = %v after bus events alone, want 0", got)
	}
}

// The events-side twin: Notify metadata alone stages nothing for
// core.notify.deliver, whether or not any rule matches (05-events 5E.12).
func TestNotifyMetaAloneProducesNoNotification(t *testing.T) {
	for _, withRule := range []bool{false, true} {
		t.Run(map[bool]string{false: "no rule", true: "a matching rule"}[withRule], func(t *testing.T) {
			r := newRig(t, rigOpts{})
			if withRule {
				r.putRule(rule("all", []string{"gotham.*", "wayne.*"}, commissioner))
			}
			must(t, r.bus.Open())
			t.Cleanup(func() { _ = r.bus.Close(context.Background()) })
			events.Publish(r.bus.Scope("gotham"), context.Background(), events.NewTopic[Signal](brokenName), "", Signal{Villain: "Joker"})
			events.Publish(r.bus.Scope("wayne"), context.Background(), events.NewTopic[Signal](vaultName), "", Signal{Villain: "Bane"})
			stays(t, "nothing staged for core.notify.deliver", 250*time.Millisecond, func() bool {
				st := r.status()
				return r.fake.Sent() == 0 && st.Pending == 0 && st.IntakeBacklog == 0 && st.Dead == 0
			})
		})
	}
}
