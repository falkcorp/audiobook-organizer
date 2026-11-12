// file: core/outbox/metrics.go
// version: 1.0.1
// guid: 1d4cb2b8-5f9f-4b3e-8a0c-0f4a8ee3f8a1
// last-edited: 2026-11-12

package outbox

import (
	"context"
	"fmt"
	"sync/atomic"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/falkcorp/mediacore/metrics"
)

// Instruments are the outbox families of 05-events §5E.10, under the core
// outbox scope (<namespace>.outbox; the default namespace is mediacore):
//
//	<ns>_outbox_staged_total{topic}            Stage calls (a discarded batch still counts)
//	<ns>_outbox_intake_pending{topic}          intake rows not yet sequenced
//	<ns>_outbox_intake_age_seconds{topic}      age of the oldest intake row
//	<ns>_outbox_delivered_total{topic,consumer}
//	<ns>_outbox_retries_total{topic,consumer}
//	<ns>_outbox_dead_letters{consumer}
//	<ns>_outbox_pending{consumer,state}        state: enabled | disabled
//	<ns>_outbox_lag_seconds{consumer}          age of the oldest unacked message
//	<ns>_outbox_filtered_total{topic,consumer} acked by a Where filter
//	<ns>_outbox_folded_total{topic,consumer}   acked after a run start folded into an existing run
//	<ns>_outbox_orphans_dropped_total{topic}
//
// topic and consumer are closed sets (declared at boot, disabled modules'
// consumer names included), so cardinality is bounded. The gauges read the
// last Status the relay computed.
type Instruments struct {
	stagedC, deliveredC, retriesC, filteredC, foldedC, orphansC metric.Int64Counter
	snap                                                        atomic.Pointer[Status]
}

// NewInstruments registers the outbox families on sc (nil: nothing records).
func NewInstruments(sc *metrics.Scope) (*Instruments, error) {
	var m metric.Meter
	prefix := "outbox"
	if sc == nil {
		m = noop.NewMeterProvider().Meter("outbox")
	} else {
		m, prefix = sc.Meter(), sc.Prefix()
	}
	in := &Instruments{}
	var err error
	counter := func(name, desc string) metric.Int64Counter {
		if err != nil {
			return nil
		}
		var c metric.Int64Counter
		c, err = m.Int64Counter(prefix+"."+name, metric.WithDescription(desc))
		return c
	}
	in.stagedC = counter("staged", "Messages staged into a producer's batch (a discarded batch still counts).")
	in.deliveredC = counter("delivered", "Messages a consumer took over: handler returned nil, or the run was started.")
	in.retriesC = counter("retries", "Deliveries that failed and were scheduled for another try.")
	in.filteredC = counter("filtered", "Messages acked without running because the consumer's Where rejected them.")
	in.foldedC = counter("folded", "Deliveries acked after a run start folded into an existing run instead of queueing one.")
	in.orphansC = counter("orphans_dropped", "Messages dropped because only a removed consumer still held them.")
	if err != nil {
		return nil, fmt.Errorf("outbox: counter: %w", err)
	}
	gauge := func(name, unit, desc string) metric.Int64ObservableGauge {
		if err != nil {
			return nil
		}
		var g metric.Int64ObservableGauge
		g, err = m.Int64ObservableGauge(prefix+"."+name, metric.WithUnit(unit), metric.WithDescription(desc))
		return g
	}
	intakePending := gauge("intake_pending", "{message}", "Intake rows not yet sequenced, by topic.")
	intakeAge := gauge("intake_age", "s", "Age of the oldest intake row, by topic.")
	dead := gauge("dead_letters", "{message}", "Dead letters waiting for an operator, by consumer.")
	pending := gauge("pending", "{message}", "Messages a consumer has not taken over, by consumer and state.")
	lag := gauge("lag", "s", "Age of the oldest message a consumer has not taken over.")
	if err != nil {
		return nil, fmt.Errorf("outbox: gauge: %w", err)
	}
	if _, err := m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		st := in.snap.Load()
		if st == nil {
			return nil
		}
		for _, t := range st.Topics {
			a := metric.WithAttributeSet(attribute.NewSet(attribute.String("topic", t.Topic)))
			o.ObserveInt64(intakePending, int64(t.IntakePending), a)
			o.ObserveInt64(intakeAge, int64(t.IntakeAge.Seconds()), a)
		}
		for _, c := range st.Consumers {
			ca := metric.WithAttributeSet(attribute.NewSet(attribute.String("consumer", c.Name)))
			state := "disabled"
			if c.Enabled {
				state = "enabled"
			}
			o.ObserveInt64(dead, int64(c.Dead), ca)
			o.ObserveInt64(pending, int64(c.Pending), metric.WithAttributeSet(attribute.NewSet(
				attribute.String("consumer", c.Name), attribute.String("state", state))))
			o.ObserveInt64(lag, int64(c.Lag.Seconds()), ca)
		}
		return nil
	}, intakePending, intakeAge, dead, pending, lag); err != nil {
		return nil, fmt.Errorf("outbox: gauge callback: %w", err)
	}
	return in, nil
}

func topicAttr(topic string) metric.MeasurementOption {
	return metric.WithAttributeSet(attribute.NewSet(attribute.String("topic", topic)))
}

func pairAttr(topic, consumer string) metric.MeasurementOption {
	return metric.WithAttributeSet(attribute.NewSet(attribute.String("topic", topic), attribute.String("consumer", consumer)))
}

// publish makes st the snapshot the gauges report.
func (in *Instruments) publish(st *Status) {
	if in != nil {
		in.snap.Store(st)
	}
}

func (in *Instruments) staged(topic string) {
	if in != nil {
		in.stagedC.Add(context.Background(), 1, topicAttr(topic))
	}
}

func (in *Instruments) delivered(topic, consumer string) {
	if in != nil {
		in.deliveredC.Add(context.Background(), 1, pairAttr(topic, consumer))
	}
}

func (in *Instruments) retried(topic, consumer string) {
	if in != nil {
		in.retriesC.Add(context.Background(), 1, pairAttr(topic, consumer))
	}
}

func (in *Instruments) filtered(topic, consumer string) {
	if in != nil {
		in.filteredC.Add(context.Background(), 1, pairAttr(topic, consumer))
	}
}

func (in *Instruments) folded(topic, consumer string) {
	if in != nil {
		in.foldedC.Add(context.Background(), 1, pairAttr(topic, consumer))
	}
}

func (in *Instruments) orphansDropped(topic string, n int) {
	if in != nil && n > 0 {
		in.orphansC.Add(context.Background(), int64(n), topicAttr(topic))
	}
}
