// file: core/integrations/notify/metrics_test.go
// version: 1.0.1
// guid: 41b7e9c3-2d60-4a85-8f13-e6a0c97d5b28
// last-edited: 2026-11-13

package notify_test

import (
	"context"
	"testing"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/notify"
	"github.com/falkcorp/mediacore/integrations/notify/notifytest"
	"github.com/falkcorp/mediacore/metrics/metricstest"
)

// TestNotifyMetrics_SeriesGolden (05-metrics section 5 item 5, section 12):
// the core notify scope serves exactly the families of testdata/series.golden.
// A family with no sample exports nothing, so the test publishes with a
// rule and without one, delivers to a channel that works and to one that
// dead-letters, which gives every family a sample.
func TestNotifyMetrics_SeriesGolden(t *testing.T) {
	r := newRig(t, rigOpts{metrics: true})
	r.fake.SendFn = func(_ context.Context, _ notify.Notification, tg notify.Target) error {
		if tg.Instance == alfred {
			return notifytest.Fail(alfred, integrations.ClassInvalid, 400, 0)
		}
		return nil
	}
	r.putRule(rule("roof", []string{signalName}, commissioner, alfred))
	r.publish(signalNote("Joker"))
	r.publish(notify.Notification{Topic: vaultName, Title: "unrouted"}) // no rule: a no_rule sample
	eventually(t, "a delivery and a dead letter", func() bool {
		return counted(r, commissioner, "ok") == 1 && len(r.dead()) == 1
	})
	metricstest.AssertSeries(t, r.mp, r.sc, "testdata/series.golden")
	metricstest.AssertNoOverflow(t, r.mp)

	if got := sample(t, r.mp, "mediacore_notify_dead_letters", map[string]string{"instance": alfred.String()}); got != 1 {
		t.Errorf("dead_letters{alfred} = %v, want 1", got)
	}
	if got := sample(t, r.mp, "mediacore_notify_targets_total", map[string]string{"topic": vaultName, "result": "no_rule"}); got != 1 {
		t.Errorf("targets_total{no_rule} = %v, want 1", got)
	}
}

// The dead-letter gauge reads a bounded number of pages. Inside the budget its
// per-instance tally is exact; past it, what it could not read is reported as
// instance="unattributed" so the series still add up to the outbox's own count
// instead of quietly under-reporting.
func TestNotifyMetrics_DeadLetterGaugeSurvivesItsPageBudget(t *testing.T) {
	r := newRig(t, rigOpts{metrics: true, notifyCfg: func(c *notify.Config) { c.MaxAttempts = 1 }})
	r.fake.SendFn = func(_ context.Context, _ notify.Notification, tg notify.Target) error {
		return notifytest.Fail(tg.Instance, integrations.ClassInvalid, 400, 0)
	}
	r.putRule(rule("roof", []string{signalName}, commissioner, alfred, oracle))
	r.publish(signalNote("Riddler"))
	eventually(t, "three dead letters", func() bool { return len(r.dead()) == 3 })

	total := func(m map[string]int) (n int) {
		for _, v := range m {
			n += v
		}
		return n
	}
	exact, err := notify.DeadCounts(context.Background(), r.ob, 10, 500)
	must(t, err)
	if total(exact) != 3 || exact[commissioner.String()] != 1 || exact[alfred.String()] != 1 || exact[oracle.String()] != 1 {
		t.Fatalf("an unbudgeted scan = %v, want one per instance", exact)
	}
	if _, ok := exact["unattributed"]; ok {
		t.Errorf("an exact scan reported an unattributed remainder: %v", exact)
	}

	cut, err := notify.DeadCounts(context.Background(), r.ob, 1, 1) // one dead letter read, two cut off
	must(t, err)
	if total(cut) != 3 {
		t.Errorf("a scan cut short counts %d dead letters (%v), want the real 3", total(cut), cut)
	}
	if cut["unattributed"] != 2 {
		t.Errorf("unattributed = %d (%v), want the 2 the budget cut off", cut["unattributed"], cut)
	}
}
