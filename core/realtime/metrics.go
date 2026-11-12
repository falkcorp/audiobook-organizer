// file: core/realtime/metrics.go
// version: 1.0.0
// guid: c360e829-39c4-440b-a689-5de1ca6ce8f9
// last-edited: 2026-11-11

package realtime

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/falkcorp/mediacore/metrics"
)

// instruments are the hub's families of 05-realtime §9, under the core
// realtime scope (<namespace>.realtime; the default namespace is mediacore):
//
//	<ns>_realtime_subscribers
//	<ns>_realtime_events_published_total{topic}
//	<ns>_realtime_events_delivered_total{topic}
//	<ns>_realtime_events_filtered_total{topic,reason}   reason: perm, visible
//	<ns>_realtime_events_dropped_total{reason}          reason: overflow, decode, filter_panic, oversize, redact
//	<ns>_realtime_resets_total{reason}                  reason: restart, gap, overflow
//	<ns>_realtime_stream_refusals_total{reason}         reason: global_cap, bucket_cap, denied
//	<ns>_realtime_streams_closed_total{reason}          reason: client, shutdown, credential_revoked, permission_revoked, write_timeout
//	<ns>_realtime_ring_oldest_age_seconds
//
// topic is a catalog name and every reason a closed set, so cardinality is
// bounded. oversize and redact extend the spec's dropped set: an event over
// MaxPayload, and a Redact that returned something that is not JSON.
type instruments struct {
	publishedC, deliveredC, filteredC, droppedC, resetsC, refusalsC, closedC metric.Int64Counter
}

func newInstruments(sc *metrics.Scope, h *Hub) (*instruments, error) {
	var m metric.Meter
	prefix := "realtime"
	if sc == nil {
		m = noop.NewMeterProvider().Meter("realtime")
	} else {
		m, prefix = sc.Meter(), sc.Prefix()
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
	in.publishedC = counter("events_published", "Events appended to the resume ring, by topic.")
	in.deliveredC = counter("events_delivered", "Events written to a stream, by topic.")
	in.filteredC = counter("events_filtered", "Events withheld from a subscriber at send time: perm or visible.")
	in.droppedC = counter("events_dropped", "Events never delivered: overflow, decode, filter_panic, oversize or redact.")
	in.resetsC = counter("resets", "core.stream.reset.v1 frames written: restart, gap or overflow.")
	in.refusalsC = counter("stream_refusals", "Streams refused before they started: global_cap, bucket_cap or denied.")
	in.closedC = counter("streams_closed", "Streams that ended, by reason.")
	if err != nil {
		return nil, fmt.Errorf("realtime: counter: %w", err)
	}
	subs, err := m.Int64ObservableGauge(prefix+".subscribers", metric.WithUnit("{stream}"),
		metric.WithDescription("Open event streams."))
	if err != nil {
		return nil, fmt.Errorf("realtime: subscribers gauge: %w", err)
	}
	age, err := m.Float64ObservableGauge(prefix+".ring_oldest_age", metric.WithUnit("s"),
		metric.WithDescription("Age of the oldest event in the resume ring."))
	if err != nil {
		return nil, fmt.Errorf("realtime: ring age gauge: %w", err)
	}
	if _, err := m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		if h.br != nil {
			o.ObserveInt64(subs, int64(h.br.Len()))
			o.ObserveFloat64(age, h.br.OldestAge().Seconds())
		}
		return nil
	}, subs, age); err != nil {
		return nil, fmt.Errorf("realtime: gauge callback: %w", err)
	}
	return in, nil
}

func attrs(kv ...string) metric.MeasurementOption {
	a := make([]attribute.KeyValue, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		a = append(a, attribute.String(kv[i], kv[i+1]))
	}
	return metric.WithAttributeSet(attribute.NewSet(a...))
}

func (in *instruments) published(topic string) {
	in.publishedC.Add(context.Background(), 1, attrs("topic", topic))
}

func (in *instruments) delivered(topic string) {
	in.deliveredC.Add(context.Background(), 1, attrs("topic", topic))
}

func (in *instruments) filtered(topic, reason string) {
	in.filteredC.Add(context.Background(), 1, attrs("topic", topic, "reason", reason))
}

func (in *instruments) dropped(reason string) {
	in.droppedC.Add(context.Background(), 1, attrs("reason", reason))
}

// overflowed counts the queued entries one overflow dropped.
func (in *instruments) overflowed(n int) {
	in.droppedC.Add(context.Background(), int64(n), attrs("reason", "overflow"))
}

func (in *instruments) reset(reason string) {
	in.resetsC.Add(context.Background(), 1, attrs("reason", reason))
}

func (in *instruments) refused(reason string) {
	in.refusalsC.Add(context.Background(), 1, attrs("reason", reason))
}

func (in *instruments) closed(reason string) {
	in.closedC.Add(context.Background(), 1, attrs("reason", reason))
}
