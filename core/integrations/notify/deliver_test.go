// file: core/integrations/notify/deliver_test.go
// version: 1.0.2
// guid: b8d24f6a-3e17-4c90-a5b2-9f0e7d1c4a63
// last-edited: 2026-11-13

package notify_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/notify"
	"github.com/falkcorp/mediacore/integrations/notify/notifytest"
)

// counted is the deliveries_total sample for an instance and a class.
func counted(r *rig, ref integrations.Ref, class string) float64 {
	return sample(r.t, r.mp, "mediacore_notify_deliveries_total", map[string]string{"instance": ref.String(), "class": class})
}

// TestDeliver_TransientRetriedPermanentDeadLettered: a transient failure is
// retried until it succeeds, with the same notification ID each time; a
// permanent one is dead-lettered after one attempt and never retried. The two
// channels share a rule, so each one's fate must not touch the other's.
func TestDeliver_TransientRetriedPermanentDeadLettered(t *testing.T) {
	r := newRig(t, rigOpts{metrics: true})
	r.fake.SendFn = func(_ context.Context, n notify.Notification, tg notify.Target) error {
		switch tg.Instance {
		case alfred:
			return notifytest.Fail(alfred, integrations.ClassInvalid, 400, 0)
		case commissioner:
			if len(callsTo(r.fake, commissioner)) <= 2 {
				return notifytest.Fail(commissioner, integrations.ClassTransient, 503, 0)
			}
		}
		return nil
	}
	r.putRule(rule("roof", []string{signalName}, commissioner, alfred))
	r.publish(signalNote("Penguin"))

	eventually(t, "the commissioner's third attempt and alfred's dead letter", func() bool {
		return len(callsTo(r.fake, commissioner)) == 3 && len(r.dead()) == 1
	})
	stays(t, "no further attempts", 250*time.Millisecond, func() bool {
		return len(callsTo(r.fake, commissioner)) == 3 && len(callsTo(r.fake, alfred)) == 1
	})
	ids := map[string]bool{}
	for _, c := range callsTo(r.fake, commissioner) {
		ids[c.Notification.ID] = true
	}
	if len(ids) != 1 {
		t.Errorf("retries carried %d different notification IDs, want the one", len(ids))
	}
	dead := r.dead()
	if dead[0].Attempts != 1 {
		t.Errorf("the permanent failure took %d attempts, want 1", dead[0].Attempts)
	}
	for ref, want := range map[integrations.Ref]map[string]float64{
		commissioner: {"transient": 2, "ok": 1},
		alfred:       {"invalid": 1},
	} {
		for class, n := range want {
			if got := counted(r, ref, class); got != n {
				t.Errorf("deliveries_total{%s,%s} = %v, want %v", ref, class, got, n)
			}
		}
	}
	if st := r.status(); st.Dead != 1 || st.Pending != 0 {
		t.Errorf("status = pending %d dead %d, want 0 and 1", st.Pending, st.Dead)
	}
}

// core.notify.max_attempts is the handler's own bound, read live: the last
// attempt of a channel that never recovers dead-letters, and a larger live
// value gives a later entry more.
func TestDeliver_MaxAttemptsDeadLetters(t *testing.T) {
	r := newRig(t, rigOpts{notifyCfg: func(c *notify.Config) { c.MaxAttempts = 3 }})
	r.fake.SendFn = func(context.Context, notify.Notification, notify.Target) error {
		return notifytest.Fail(commissioner, integrations.ClassTransient, 503, 0)
	}
	r.putRule(rule("roof", []string{signalName}, commissioner))
	r.publish(signalNote("Riddler"))
	eventually(t, "the dead letter", func() bool { return len(r.dead()) == 1 })
	if n := r.fake.Sent(); n != 3 {
		t.Errorf("attempts = %d, want max_attempts 3", n)
	}
	if got := r.dead()[0].Attempts; got != 3 {
		t.Errorf("the dead letter records %d attempts, want 3", got)
	}

	r.setNotifyConfig(func(c *notify.Config) { c.MaxAttempts = 5 })
	before := r.fake.Sent()
	r.publish(signalNote("Scarecrow"))
	eventually(t, "the second dead letter", func() bool { return len(r.dead()) == 2 })
	if n := r.fake.Sent() - before; n != 5 {
		t.Errorf("attempts after raising max_attempts = %d, want 5", n)
	}
}

// An instance that does not exist retries (an operator may create it) and is
// dead-lettered at max_attempts like any other failure.
func TestDeliver_UnknownInstanceIsRetriedThenDeadLettered(t *testing.T) {
	r := newRig(t, rigOpts{metrics: true, notifyCfg: func(c *notify.Config) { c.MaxAttempts = 2 }})
	ghost := integrations.Ref{Kind: integrations.KindNotify, Instance: "bat-signal-gone"}
	r.putRule(rule("roof", []string{signalName}, ghost))
	r.publish(signalNote("Joker"))
	eventually(t, "the dead letter", func() bool { return len(r.dead()) == 1 })
	if got := r.dead()[0].Attempts; got != 2 {
		t.Errorf("attempts = %d, want 2", got)
	}
	if r.fake.Sent() != 0 {
		t.Error("something was sent to an instance that does not exist")
	}
}

// A Retry-After the channel returned holds the next attempt back at least that
// long, although the outbox's own backoff here is a few milliseconds.
func TestDeliver_RetryAfterHonoured(t *testing.T) {
	const wait = 400 * time.Millisecond
	r := newRig(t, rigOpts{})
	var first atomic.Bool
	r.fake.SendFn = func(context.Context, notify.Notification, notify.Target) error {
		if first.CompareAndSwap(false, true) {
			return notifytest.Fail(commissioner, integrations.ClassRateLimited, 429, wait)
		}
		return nil
	}
	r.putRule(rule("roof", []string{signalName}, commissioner))
	r.publish(signalNote("Bane"))
	eventually(t, "the retry", func() bool { return r.fake.Sent() == 2 })
	calls := r.fake.Calls()
	if gap := calls[1].At.Sub(calls[0].At); gap < wait-20*time.Millisecond {
		t.Errorf("the retry came %v after the 429, want at least the Retry-After of %v", gap, wait)
	}
}

// One broken channel does not loop and does not hold the others up: it
// dead-letters after one attempt, the channels beside it deliver, and the
// news that it broke (a notifiable event the kernel publishes on a health
// change) reaches the other channels through their rules.
func TestDeliver_BrokenChannelAlertsOtherChannels(t *testing.T) {
	r := newRig(t, rigOpts{metrics: true})
	r.fake.SendFn = func(_ context.Context, _ notify.Notification, tg notify.Target) error {
		if tg.Instance == commissioner {
			return notifytest.Fail(commissioner, integrations.ClassAuth, 401, 0)
		}
		return nil
	}
	r.putRule(rule("roof", []string{signalName}, commissioner, alfred, oracle))
	r.putRule(rule("health", []string{brokenName}, alfred, oracle))

	r.publish(signalNote("Joker"))
	eventually(t, "the healthy channels and the dead letter", func() bool {
		return len(callsTo(r.fake, alfred)) == 1 && len(callsTo(r.fake, oracle)) == 1 && len(r.dead()) == 1
	})
	// The kernel's health hook then reports the broken channel.
	r.publish(notify.Notification{Topic: brokenName, Title: "The commissioner's channel is broken", Fields: map[string]string{"instance": commissioner.String()}})
	eventually(t, "the alert on the other channels", func() bool {
		return len(callsTo(r.fake, alfred)) == 2 && len(callsTo(r.fake, oracle)) == 2
	})
	stays(t, "the broken channel is neither retried nor sent the alert", 250*time.Millisecond, func() bool {
		return len(callsTo(r.fake, commissioner)) == 1 && len(callsTo(r.fake, alfred)) == 2
	})
	if got := counted(r, commissioner, "auth"); got != 1 {
		t.Errorf("deliveries_total{commissioner,auth} = %v, want 1", got)
	}
	if got := r.dead()[0].Attempts; got != 1 {
		t.Errorf("an auth failure took %d attempts, want 1", got)
	}
}

// The context Publish was given ended long before the relay runs the entry;
// the delivery runs under the relay's context and is never cancelled by it (I5).
func TestDeliver_PublisherCtxCancelDoesNotCancelDelivery(t *testing.T) {
	r := newRig(t, rigOpts{noRelay: true})
	r.putRule(rule("roof", []string{signalName}, commissioner))
	var sawErr atomic.Pointer[error]
	r.fake.SendFn = func(ctx context.Context, _ notify.Notification, _ notify.Target) error {
		err := ctx.Err()
		sawErr.Store(&err)
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	must(t, r.svc.Publish(ctx, nil, signalNote("Penguin")))
	cancel()
	r.startRelay()
	eventually(t, "the delivery", func() bool { return r.fake.Sent() == 1 })
	if p := sawErr.Load(); p == nil || *p != nil {
		t.Errorf("the channel's context was already done: %v", p)
	}
	stays(t, "no dead letter", 150*time.Millisecond, func() bool { return len(r.dead()) == 0 })
}

// An entry older than core.notify.max_age is dead-lettered unsent, and one
// within it is delivered. The clock jumps a day between the two stagings
// (the relay is not running, as after a long outage).
func TestDeliver_EntryOlderThanMaxAgeDeadLettered(t *testing.T) {
	r := newRig(t, rigOpts{noRelay: true, metrics: true}) // default max_age is 24h
	r.putRule(rule("roof", []string{signalName}, commissioner))
	r.publish(signalNote("Riddler"))
	r.clock.advance(25 * time.Hour)
	r.publish(signalNote("Scarecrow"))
	r.startRelay()

	eventually(t, "the stale entry's dead letter and the fresh entry's delivery", func() bool {
		return len(r.dead()) == 1 && r.fake.Sent() == 1
	})
	if v := r.fake.Calls()[0].Notification.Fields["villain"]; v != "Scarecrow" {
		t.Errorf("delivered %q, want only the fresh Scarecrow entry", v)
	}
	if got := counted(r, commissioner, "expired"); got != 1 {
		t.Errorf("deliveries_total{expired} = %v, want 1", got)
	}
	stays(t, "the stale entry is not sent later", 150*time.Millisecond, func() bool { return r.fake.Sent() == 1 })
}

// With core.integrations.disabled set nothing is sent, nothing dead-letters
// and no attempt is used up: when the switch is back, the entry delivers even
// though max_attempts is 1 (I5).
func TestDeliver_IntegrationsDisabledKeepsEntryPending(t *testing.T) {
	var off atomic.Bool
	off.Store(true)
	// max_attempts is 2 and the first real send fails: if the refusals above
	// had used attempts, that failure would be the last and dead-letter.
	r := newRig(t, rigOpts{metrics: true, disabled: &off, notifyCfg: func(c *notify.Config) { c.MaxAttempts = 2 }})
	r.fake.SendFn = failOnce(r.fake)
	r.putRule(rule("roof", []string{signalName}, commissioner))
	r.publish(signalNote("Joker"))

	eventually(t, "a refused attempt", func() bool { return counted(r, commissioner, "disabled") >= 1 })
	// Let the wait it was given pass several times over while still off.
	for range 3 {
		r.clock.advance(2 * time.Minute)
		time.Sleep(60 * time.Millisecond)
	}
	if r.fake.Sent() != 0 || len(r.dead()) != 0 {
		t.Fatalf("sent %d, dead %d while the switch is off", r.fake.Sent(), len(r.dead()))
	}
	if st := r.status(); st.Pending+st.Retrying < 1 {
		t.Errorf("the entry left the queue: %+v", st)
	}

	off.Store(false)
	r.clock.advance(2 * time.Minute)
	eventually(t, "the failed send and its retry once the switch is back", func() bool { return r.fake.Sent() == 2 })
	stays(t, "never dead-lettered", 150*time.Millisecond, func() bool { return len(r.dead()) == 0 })
}

// The same holds for one instance switched off.
func TestDeliver_DisabledInstanceKeepsEntryPending(t *testing.T) {
	r := newRig(t, rigOpts{metrics: true, notifyCfg: func(c *notify.Config) { c.MaxAttempts = 2 }})
	r.fake.SendFn = failOnce(r.fake)
	cfg, ok := r.reg.Config(commissioner)
	if !ok {
		t.Fatal("no commissioner instance")
	}
	cfg.Enabled = false
	cfg, err := r.reg.Put(admin(), cfg, nil)
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	r.putRule(rule("roof", []string{signalName}, commissioner))
	r.publish(signalNote("Two-Face"))
	eventually(t, "a refused attempt", func() bool { return counted(r, commissioner, "disabled") >= 1 })
	r.clock.advance(2 * time.Minute)
	time.Sleep(80 * time.Millisecond)
	if r.fake.Sent() != 0 || len(r.dead()) != 0 {
		t.Fatalf("sent %d, dead %d while the instance is off", r.fake.Sent(), len(r.dead()))
	}
	cfg.Enabled = true
	if _, err := r.reg.Put(admin(), cfg, nil); err != nil {
		t.Fatalf("enable: %v", err)
	}
	r.clock.advance(2 * time.Minute)
	eventually(t, "the failed send and its retry once it is back", func() bool { return r.fake.Sent() == 2 })
	stays(t, "never dead-lettered", 150*time.Millisecond, func() bool { return len(r.dead()) == 0 })
}

// failOnce is a SendFn whose first send fails transiently and whose later
// sends succeed.
func failOnce(f *notifytest.FakeNotifier) func(context.Context, notify.Notification, notify.Target) error {
	return func(_ context.Context, _ notify.Notification, tg notify.Target) error {
		if f.Sent() <= 1 {
			return notifytest.Fail(tg.Instance, integrations.ClassTransient, 503, 0)
		}
		return nil
	}
}

// A switch that goes off between the lease and the send is the same case: the
// refusal the adapter raises at send time keeps the entry's attempts too.
func TestDeliver_DisabledAtSendKeepsAttempts(t *testing.T) {
	r := newRig(t, rigOpts{metrics: true, notifyCfg: func(c *notify.Config) { c.MaxAttempts = 2 }})
	r.fake.SendFn = func(_ context.Context, _ notify.Notification, tg notify.Target) error {
		switch n := r.fake.Sent(); {
		case n <= 3:
			return fmt.Errorf("the switch went off: %w", integrations.ErrDisabled)
		case n == 4:
			return notifytest.Fail(tg.Instance, integrations.ClassTransient, 503, 0)
		}
		return nil
	}
	r.putRule(rule("roof", []string{signalName}, commissioner))
	r.publish(signalNote("Bane"))
	// Three refusals, then a transient failure, then the send: max_attempts 2
	// is only enough for the last two if the refusals cost nothing.
	eventually(t, "the fifth send", func() bool {
		r.clock.advance(2 * time.Minute)
		return r.fake.Sent() == 5
	})
	stays(t, "never dead-lettered", 150*time.Millisecond, func() bool { return len(r.dead()) == 0 })
	if got := counted(r, commissioner, "disabled"); got != 3 {
		t.Errorf("deliveries_total{disabled} = %v, want 3", got)
	}
}

// core.notify.max_age still applies to an entry held back by the switch.
// (max_age is 2h here so the clock's jump stays inside the relay's rotation.)
func TestDeliver_DisabledEntryStillExpires(t *testing.T) {
	var off atomic.Bool
	off.Store(true)
	r := newRig(t, rigOpts{metrics: true, disabled: &off, notifyCfg: func(c *notify.Config) { c.MaxAge = 2 * time.Hour }})
	r.putRule(rule("roof", []string{signalName}, commissioner))
	r.publish(signalNote("Bane"))
	eventually(t, "a refused attempt", func() bool { return counted(r, commissioner, "disabled") >= 1 })
	time.Sleep(50 * time.Millisecond) // the refusal's retry is on the books
	r.clock.advance(3 * time.Hour)
	eventually(t, "the expired dead letter", func() bool {
		r.clock.advance(2 * time.Minute) // past the wait the refused attempt was given
		return len(r.dead()) == 1
	})
	if r.fake.Sent() != 0 {
		t.Error("an expired entry was sent")
	}
	if got := counted(r, commissioner, "expired"); got != 1 {
		t.Errorf("deliveries_total{expired} = %v, want 1", got)
	}
}

// Deliveries to different channels run side by side: a channel stuck in a
// send does not hold up another's.
func TestDeliver_SlowChannelDoesNotBlockAnother(t *testing.T) {
	r := newRig(t, rigOpts{})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	r.fake.SendFn = func(ctx context.Context, _ notify.Notification, tg notify.Target) error {
		if tg.Instance == commissioner {
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return nil
	}
	r.putRule(rule("roof", []string{signalName}, commissioner, alfred))
	r.publish(signalNote("Scarecrow"))
	eventually(t, "alfred's delivery while the commissioner's is stuck", func() bool { return len(callsTo(r.fake, alfred)) == 1 })
	once.Do(func() { close(release) })
}
