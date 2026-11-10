// file: core/events/metrics.go
// version: 1.0.0
// guid: 82591622-ac05-43eb-8b1a-d29a86a749ec
// last-edited: 2026-11-10

package events

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/falkcorp/mediacore/metrics"
)

// instruments are the bus families of 05-events §5E.10, under the core
// events scope (<namespace>.events; the default namespace is mediacore):
//
//	<ns>_events_published_total{topic}
//	<ns>_events_publish_rejected_total{topic}
//	<ns>_events_delivered_total{topic,subscriber}
//	<ns>_events_dropped_total{topic,subscriber,reason}
//	<ns>_events_handler_panics_total{subscriber}
//	<ns>_events_queue_depth{subscriber}
//	<ns>_events_handler_seconds{subscriber}
//
// topic and subscriber are closed sets declared at boot, so cardinality is
// bounded; a rejected publish's topic is a name compiled into the binary.
type instruments struct {
	publishedC, rejectedC, deliveredC, droppedC, panicsC metric.Int64Counter
	handlerH                                             metric.Float64Histogram
}

// handlerBuckets are handler_seconds' boundaries: a bus handler is expected
// to take milliseconds; the top buckets show one that blocks.
func handlerBuckets() []float64 {
	return []float64{0.0005, 0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 10}
}

func newInstruments(sc *metrics.Scope, b *Bus) (*instruments, error) {
	var m metric.Meter
	prefix := "events"
	if sc == nil {
		m = noop.NewMeterProvider().Meter("events")
	} else {
		m, prefix = sc.Meter(), sc.Prefix()
		if err := sc.View(prefix+".handler", handlerBuckets()...); err != nil {
			return nil, fmt.Errorf("events: handler histogram view: %w", err)
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
	in.publishedC = counter("published", "Bus events accepted by Publish, by topic.")
	in.rejectedC = counter("publish_rejected", "Publishes refused: an undeclared, foreign or Durable topic, or a payload of another type.")
	in.deliveredC = counter("delivered", "Bus events handed to a subscriber's handler.")
	in.droppedC = counter("dropped", "Bus events a subscriber never received: queue_full, encode or closed.")
	in.panicsC = counter("handler_panics", "Subscriber handler panics, recovered.")
	if err != nil {
		return nil, fmt.Errorf("events: counter: %w", err)
	}
	if in.handlerH, err = m.Float64Histogram(prefix+".handler", metric.WithUnit("s"),
		metric.WithDescription("Time spent in a subscriber's handler per event.")); err != nil {
		return nil, fmt.Errorf("events: handler histogram: %w", err)
	}
	depth, err := m.Int64ObservableGauge(prefix+".queue_depth", metric.WithUnit("{event}"),
		metric.WithDescription("Events queued for a subscriber, not yet handled."))
	if err != nil {
		return nil, fmt.Errorf("events: queue depth gauge: %w", err)
	}
	if _, err := m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		if st := b.frozen.Load(); st != nil {
			for _, s := range st.subs {
				o.ObserveInt64(depth, int64(s.depth()), s.attrs.subscriber)
			}
		}
		return nil
	}, depth); err != nil {
		return nil, fmt.Errorf("events: queue depth callback: %w", err)
	}
	return in, nil
}

// subAttrs are a subscription's precomputed attribute sets, so recording
// allocates nothing per event.
type subAttrs struct {
	subscriber, topicSub             metric.MeasurementOption
	dropFull, dropEncode, dropClosed metric.MeasurementOption
}

func newSubAttrs(topic, name string) subAttrs {
	t, s := attribute.String("topic", topic), attribute.String("subscriber", name)
	drop := func(reason string) metric.MeasurementOption {
		return metric.WithAttributeSet(attribute.NewSet(t, s, attribute.String("reason", reason)))
	}
	return subAttrs{
		subscriber: metric.WithAttributeSet(attribute.NewSet(s)),
		topicSub:   metric.WithAttributeSet(attribute.NewSet(t, s)),
		dropFull:   drop(reasonQueueFull), dropEncode: drop(reasonEncode), dropClosed: drop(reasonClosed),
	}
}

func topicAttr(name string) metric.MeasurementOption {
	return metric.WithAttributeSet(attribute.NewSet(attribute.String("topic", name)))
}

func (in *instruments) published(ts *topicState) {
	in.publishedC.Add(context.Background(), 1, ts.attr)
}

func (in *instruments) rejected(name string) {
	in.rejectedC.Add(context.Background(), 1, topicAttr(name))
}

func (in *instruments) delivered(s *sub, d time.Duration) {
	ctx := context.Background()
	in.deliveredC.Add(ctx, 1, s.attrs.topicSub)
	in.handlerH.Record(ctx, d.Seconds(), s.attrs.subscriber)
}

func (in *instruments) dropped(s *sub, reason string) {
	o := s.attrs.dropClosed
	switch reason {
	case reasonQueueFull:
		o = s.attrs.dropFull
	case reasonEncode:
		o = s.attrs.dropEncode
	}
	in.droppedC.Add(context.Background(), 1, o)
}

func (in *instruments) panicked(s *sub) {
	in.panicsC.Add(context.Background(), 1, s.attrs.subscriber)
}

// crockford is the ULID alphabet (Crockford's base32).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// newULID returns a ULID: 48 bits of Unix milliseconds and 80 random bits,
// 26 characters of Crockford base32. Envelope.ID is unique per publish and
// for correlation only, so a failed random read leaves zero bits rather than
// failing a notify-only publish.
func newULID() string {
	var b [16]byte
	_, _ = rand.Read(b[6:])
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(time.Now().UnixMilli()))
	copy(b[:6], ts[2:])
	hi := binary.BigEndian.Uint64(b[:8])
	lo := binary.BigEndian.Uint64(b[8:])
	var out [26]byte
	for i := 25; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out[:])
}
