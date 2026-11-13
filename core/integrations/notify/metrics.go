// file: core/integrations/notify/metrics.go
// version: 1.0.0
// guid: 91d4c7a2-5e08-4b3f-a6d1-0f83e2b7c5a4
// last-edited: 2026-11-13

package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/outbox"
)

// instruments are the notify families of 05-notify §8, under the core notify
// scope (<namespace>.notify; the default namespace is mediacore):
//
//	<ns>_notify_published_total{topic,severity}
//	<ns>_notify_targets_total{topic,result}
//	<ns>_notify_deliveries_total{instance,class}
//	<ns>_notify_delivery_seconds{adapter}
//	<ns>_notify_dead_letters{instance}
//
// topic is bounded by the events catalog, instance and adapter by the
// registry's instances; severity and class are closed sets.
type instruments struct {
	publishedC, targetsC, deliveriesC metric.Int64Counter
	deliveryH                         metric.Float64Histogram
}

// deliveryBuckets are delivery_seconds' boundaries: a send is bounded by
// core.notify.send_timeout (15s by default).
func deliveryBuckets() []float64 {
	return []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 15, 30}
}

// The result values of targets_total.
const (
	resultResolved = "resolved"
	resultNoRule   = "no_rule"
)

// deadScanPages bounds the dead-letter gauge's scan: at most this many pages of
// 500 are read per collection, so a scrape never walks an unbounded backlog.
const deadScanPages = 10

func newInstruments(sc *metrics.Scope, ob *outbox.Outbox) (*instruments, error) {
	var m metric.Meter
	prefix := "notify"
	if sc == nil {
		m = noop.NewMeterProvider().Meter("notify")
	} else {
		m, prefix = sc.Meter(), sc.Prefix()
		if err := sc.View(prefix+".delivery", deliveryBuckets()...); err != nil {
			return nil, fmt.Errorf("notify: delivery histogram view: %w", err)
		}
	}
	in := &instruments{}
	var err error
	counter := func(name, desc string) metric.Int64Counter {
		if err != nil {
			return nil
		}
		var c metric.Int64Counter
		c, err = m.Int64Counter(prefix+"."+name, metric.WithDescription(desc))
		return c
	}
	in.publishedC = counter("published", "Notifications accepted by Publish, by topic and severity.")
	in.targetsC = counter("targets", "Targets Publish resolved: resolved deliveries staged, or no_rule when no rule matched.")
	in.deliveriesC = counter("deliveries", "Delivery attempts by instance and class (ok when sent).")
	if err != nil {
		return nil, fmt.Errorf("notify: counter: %w", err)
	}
	if in.deliveryH, err = m.Float64Histogram(prefix+".delivery", metric.WithUnit("s"),
		metric.WithDescription("Time spent in a channel's Send, by adapter.")); err != nil {
		return nil, fmt.Errorf("notify: delivery histogram: %w", err)
	}
	if ob == nil {
		return in, nil
	}
	dead, err := m.Int64ObservableGauge(prefix+".dead_letters", metric.WithUnit("{delivery}"),
		metric.WithDescription("Dead-lettered deliveries of core.notify.deliver, by instance."))
	if err != nil {
		return nil, fmt.Errorf("notify: dead-letter gauge: %w", err)
	}
	if _, err := m.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		counts, err := deadByInstance(ctx, ob)
		if err != nil {
			return err
		}
		for ref, n := range counts {
			o.ObserveInt64(dead, int64(n), metric.WithAttributeSet(attribute.NewSet(attribute.String("instance", ref.String()))))
		}
		return nil
	}, dead); err != nil {
		return nil, fmt.Errorf("notify: dead-letter callback: %w", err)
	}
	return in, nil
}

// deadByInstance tallies the dead letters of core.notify.deliver by the
// instance their payload names, reading at most deadScanPages pages.
func deadByInstance(ctx context.Context, ob *outbox.Outbox) (map[integrations.Ref]int, error) {
	out := map[integrations.Ref]int{}
	after := ""
	for range deadScanPages {
		letters, next, err := ob.ListDead(ctx, after, 500)
		if err != nil {
			return nil, err
		}
		for _, l := range letters {
			if l.Consumer != DeliverConsumer {
				continue
			}
			var d Delivery
			if json.Unmarshal(l.Payload, &d) == nil {
				out[d.Target.Instance]++
			}
		}
		if next == "" {
			break
		}
		after = next
	}
	return out, nil
}

func (in *instruments) published(topic string, sev Severity) {
	in.publishedC.Add(context.Background(), 1, metric.WithAttributeSet(attribute.NewSet(
		attribute.String("topic", topic), attribute.String("severity", sev.String()))))
}

func (in *instruments) targets(topic, result string, n int) {
	in.targetsC.Add(context.Background(), int64(n), metric.WithAttributeSet(attribute.NewSet(
		attribute.String("topic", topic), attribute.String("result", result))))
}

func (in *instruments) delivery(ref integrations.Ref, class string, adapter string, took time.Duration) {
	in.deliveriesC.Add(context.Background(), 1, metric.WithAttributeSet(attribute.NewSet(
		attribute.String("instance", ref.String()), attribute.String("class", class))))
	if adapter != "" && took >= 0 {
		in.deliveryH.Record(context.Background(), took.Seconds(), metric.WithAttributeSet(attribute.NewSet(attribute.String("adapter", adapter))))
	}
}
