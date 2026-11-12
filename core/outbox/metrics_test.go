// file: core/outbox/metrics_test.go
// version: 1.0.1
// guid: 8e3c6a17-9d42-4b05-a1f8-6c70d2e5b943
// last-edited: 2026-11-12

package outbox_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/metrics/metricstest"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// TestOutboxMetrics_SeriesGolden (05-metrics §5 item 5, §12): the core outbox
// scope serves exactly the families of testdata/series.golden. A family with
// no sample exports nothing, so the test stages, delivers, retries, filters
// and drops an orphan once each, then refreshes the status the gauges read.
func TestOutboxMetrics_SeriesGolden(t *testing.T) {
	p, err := metrics.New(context.Background(), metrics.Options{AppID: "gotham", Prometheus: true})
	must(t, err)
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	sc, err := p.Core("outbox", "topic", "consumer", "state")
	must(t, err)
	inst, err := outbox.NewInstruments(sc)
	must(t, err)

	clk := &skew{}
	rc := &recorder{}
	var failed bool
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	rs := &responder{}
	r := newRig(t, rigOpts{
		now: clk.now, inst: inst, sweep: sweepFast, defs: []tasks.Definition{rs.blockingDef(release)},
		cfg: func(c *outbox.Config) { c.RelayRotate = 24 * time.Hour; c.OrphanRetention = time.Hour },
		register: func(t testing.TB, r *rig) []registration {
			return append(respondInto(t, r), registration{owner: "core", hs: []outbox.Handler{
				outbox.Consume(notice(), consumerN, func(ctx context.Context, m outbox.Message[Dispatch]) error {
					if m.Payload.Villain == "Joker" && !failed {
						failed = true
						return errors.New("the Joker slipped the net")
					}
					return rc.handler(consumerN)(ctx, m)
				}),
				outbox.Consume(notice(), consumerN2, rc.handler(consumerN2), outbox.Where(func(m outbox.Message[Dispatch]) bool { return m.Payload.Villain == "Nobody" })),
			}})
		},
	})
	r.stage("wayne-manor", Dispatch{Villain: "Joker"})
	eventually(t, "the retried delivery", func() bool { return rc.count() == 1 })

	// Two identical starts while the first run is held: the second folds.
	r.stageGotham("batmobile", Dispatch{Villain: "Penguin", Block: 1})
	eventually(t, "the first run to start", func() bool { return len(r.runsOf(runKind)) == 1 })
	r.stageGotham("batmobile", Dispatch{Villain: "Penguin", Block: 1})
	eventually(t, "both signals to be taken over", func() bool { return r.messages(signalName) == 0 })

	// A message on a topic nobody consumes, aged past orphan_retention.
	b := r.gothamKS.NewBatch()
	must(t, outbox.Stage(r.ob.Writer("wayne"), b, alarm(), "vault", Alarm{Vault: "Wayne Tower"}))
	must(t, b.Commit(context.Background(), storage.Sync))
	b.Close()
	eventually(t, "the orphan to be sequenced", func() bool { return r.messages(alarmName) == 1 })
	clk.advance(2 * time.Hour)
	eventually(t, "the orphan to be dropped", func() bool { return r.messages(alarmName) == 0 })

	r.ob.RefreshStatus(context.Background())
	metricstest.AssertSeries(t, p, sc, "testdata/series.golden")
	metricstest.AssertNoOverflow(t, p)
}
