// file: core/integrations/ai/metrics.go
// version: 1.0.0
// guid: 8f3a6d21-4c97-4e58-b0d2-7a1e9c5f3b84
// last-edited: 2026-11-12

package ai

import (
	"context"
	"fmt"
	"slices"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/metrics"
)

// The attribute keys of the "ai" scope (05-ai section 8). A kernel creating the
// scope declares exactly these; instance appears on counters and gauges only,
// never on a histogram (rule C5).
const (
	attrCapability attribute.Key = "capability"
	attrInstance   attribute.Key = "instance"
	attrModel      attribute.Key = "model"
	attrClass      attribute.Key = "class"
	attrKind       attribute.Key = "kind"
	attrDirection  attribute.Key = "direction"
	attrMode       attribute.Key = "mode"
	attrState      attribute.Key = "state"
	attrResult     attribute.Key = "result"
)

// ScopeKeys are the attribute keys the "ai" scope must declare
// (metrics.Provider.Core("ai", ai.ScopeKeys()...)).
func ScopeKeys() []attribute.Key {
	return []attribute.Key{attrCapability, attrInstance, attrModel, attrClass, attrKind,
		attrDirection, attrMode, attrState, attrResult}
}

// Token directions and usage modes, the values of the direction and mode labels.
const (
	dirInput       = "input"
	dirCachedInput = "cached_input"
	dirOutput      = "output"
	modeSync       = "sync"
	modeBatch      = "batch"
	classOK        = "ok"
	modelOther     = "other"
)

// instruments are the router's section 8 series. A nil *instruments records
// nothing, so a router built without a metrics scope works unchanged.
type instruments struct {
	requests     metric.Int64Counter
	tokens       metric.Int64Counter
	audioSeconds metric.Float64Counter
	request      metric.Float64Histogram
	slotWait     metric.Float64Histogram
	failovers    metric.Int64Counter
	noCapable    metric.Int64Counter
	repairs      metric.Int64Counter
	gauges       metric.Registration
}

// newInstruments creates the series on sc and registers the gauge callback,
// which reads r. A nil sc is no instruments.
func newInstruments(sc *metrics.Scope, r *Router) (*instruments, error) {
	if sc == nil {
		return nil, nil
	}
	m, p := sc.Meter(), sc.Prefix()
	in := &instruments{}
	var err error
	wrap := func(what string, e error) error {
		if e != nil {
			return fmt.Errorf("ai: %s instrument: %w", what, e)
		}
		return nil
	}
	if in.requests, err = m.Int64Counter(p+".requests",
		metric.WithDescription("AI attempts by capability, instance, model and outcome class (ok on success).")); wrap("requests", err) != nil {
		return nil, wrap("requests", err)
	}
	if in.tokens, err = m.Int64Counter(p+".tokens",
		metric.WithDescription("Tokens consumed, by capability, instance, model, direction and mode (sync or batch).")); wrap("tokens", err) != nil {
		return nil, wrap("tokens", err)
	}
	// No unit: the exporter would turn unit "s" into a doubled _seconds_seconds_total.
	if in.audioSeconds, err = m.Float64Counter(p+".audio_seconds",
		metric.WithDescription("Seconds of audio transcribed, by capability and instance.")); wrap("audio seconds", err) != nil {
		return nil, wrap("audio seconds", err)
	}
	if err = sc.View(p+".request", requestBuckets()...); wrap("request view", err) != nil {
		return nil, wrap("request view", err)
	}
	if in.request, err = m.Float64Histogram(p+".request", metric.WithUnit("s"),
		metric.WithDescription("Attempt duration by capability and kind.")); wrap("request", err) != nil {
		return nil, wrap("request", err)
	}
	if err = sc.View(p+".slot_wait", slotWaitBuckets()...); wrap("slot wait view", err) != nil {
		return nil, wrap("slot wait view", err)
	}
	if in.slotWait, err = m.Float64Histogram(p+".slot_wait", metric.WithUnit("s"),
		metric.WithDescription("Time a call waited for an endpoint slot, by kind.")); wrap("slot wait", err) != nil {
		return nil, wrap("slot wait", err)
	}
	if in.failovers, err = m.Int64Counter(p+".failovers",
		metric.WithDescription("Attempts that failed over to the next endpoint, by capability, instance and class.")); wrap("failovers", err) != nil {
		return nil, wrap("failovers", err)
	}
	if in.noCapable, err = m.Int64Counter(p+".no_capable",
		metric.WithDescription("Calls refused because no endpoint was capable, by capability.")); wrap("no capable", err) != nil {
		return nil, wrap("no capable", err)
	}
	if in.repairs, err = m.Int64Counter(p+".structured_repairs",
		metric.WithDescription("Structured-output repair turns, by capability and result.")); wrap("repairs", err) != nil {
		return nil, wrap("repairs", err)
	}
	inflight, err := m.Int64ObservableGauge(p+".inflight",
		metric.WithDescription("Requests in flight, by instance."))
	if wrap("inflight", err) != nil {
		return nil, wrap("inflight", err)
	}
	benched, err := m.Int64ObservableGauge(p+".endpoint_benched",
		metric.WithDescription("1 while an endpoint is in failure cooldown, by instance."))
	if wrap("benched", err) != nil {
		return nil, wrap("benched", err)
	}
	jobs, err := m.Int64ObservableGauge(p+".batch_jobs",
		metric.WithDescription("Open batch jobs by state, from the last poll."))
	if wrap("batch jobs", err) != nil {
		return nil, wrap("batch jobs", err)
	}
	in.gauges, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		r.observeGauges(o, inflight, benched, jobs)
		return nil
	}, inflight, benched, jobs)
	if wrap("register gauges", err) != nil {
		return nil, wrap("register gauges", err)
	}
	return in, nil
}

func requestBuckets() []float64 {
	return []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60, 120, 300, 600, 1800}
}

func slotWaitBuckets() []float64 {
	return []float64{0.001, 0.01, 0.1, 0.5, 1, 5, 15, 60, 300}
}

// observeGauges observes the three gauges: every ai instance's in-flight depth
// and bench state, and the open-job counts of the last poll.
func (r *Router) observeGauges(o metric.Observer, inflight, benched, jobs metric.Int64Observable) {
	for c := range r.reg.Instances(integrations.KindAI) {
		inst := metric.WithAttributes(attrInstance.String(string(c.Ref.Instance)))
		o.ObserveInt64(inflight, int64(r.slots.depth(c.Ref)), inst)
		var b int64
		if !r.bench.benchedUntil(c.Ref).IsZero() {
			b = 1
		}
		o.ObserveInt64(benched, b, inst)
	}
	r.pollMu.Lock()
	counts := r.openJobs
	r.pollMu.Unlock()
	for _, st := range []JobState{JobPending, JobSubmitted, JobApplying} {
		o.ObserveInt64(jobs, int64(counts[st]), metric.WithAttributes(attrState.String(st.String())))
	}
}

// close unregisters the gauge callback.
func (in *instruments) close() error {
	if in == nil || in.gauges == nil {
		return nil
	}
	if err := in.gauges.Unregister(); err != nil {
		return fmt.Errorf("ai: unregister the job gauge: %w", err)
	}
	return nil
}

// modelLabel bounds the model label to the configured models plus "other"
// (ABO's modelLabelSet): a model no ai instance names is "other".
func (r *Router) modelLabel(model string) string {
	if r.m == nil {
		return model // nothing records it
	}
	if model == "" {
		return modelOther
	}
	for c := range r.reg.Instances(integrations.KindAI) {
		set, err := decodeSettings(c.Settings)
		if err != nil {
			continue
		}
		if slices.Contains(configuredModels(set), model) {
			return model
		}
	}
	return modelOther
}

// configuredModels lists every model an endpoint's settings name.
func configuredModels(set EndpointSettings) []string {
	out := []string{set.TextModel, set.EmbedModel, set.TranscribeModel}
	for _, m := range set.CapabilityModels {
		out = append(out, m)
	}
	return out
}

// request records one attempt: the requests counter by class and the duration
// histogram. class is "ok" for a success.
func (in *instruments) attempt(ctx context.Context, capability string, kind Kind, ref integrations.Ref, model, class string, d time.Duration) {
	if in == nil {
		return
	}
	in.requests.Add(ctx, 1, metric.WithAttributes(attrCapability.String(capability),
		attrInstance.String(string(ref.Instance)), attrModel.String(model), attrClass.String(class)))
	in.request.Record(ctx, d.Seconds(), metric.WithAttributes(attrCapability.String(capability), attrKind.String(string(kind))))
}

func (in *instruments) waited(ctx context.Context, kind Kind, d time.Duration) {
	if in == nil {
		return
	}
	in.slotWait.Record(ctx, d.Seconds(), metric.WithAttributes(attrKind.String(string(kind))))
}

func (in *instruments) failedOver(ctx context.Context, capability string, ref integrations.Ref, class integrations.Class) {
	if in == nil {
		return
	}
	in.failovers.Add(ctx, 1, metric.WithAttributes(attrCapability.String(capability),
		attrInstance.String(string(ref.Instance)), attrClass.String(class.String())))
}

func (in *instruments) refused(ctx context.Context, capability string) {
	if in == nil {
		return
	}
	in.noCapable.Add(ctx, 1, metric.WithAttributes(attrCapability.String(capability)))
}

func (in *instruments) repaired(ctx context.Context, capability, result string) {
	if in == nil {
		return
	}
	in.repairs.Add(ctx, 1, metric.WithAttributes(attrCapability.String(capability), attrResult.String(result)))
}

// usage records what a call consumed: tokens by direction, audio seconds.
func (in *instruments) usage(ctx context.Context, capability string, ref integrations.Ref, model, mode string, u Usage) {
	if in == nil {
		return
	}
	base := []attribute.KeyValue{attrCapability.String(capability), attrInstance.String(string(ref.Instance)),
		attrModel.String(model), attrMode.String(mode)}
	for dir, n := range map[string]int64{dirInput: u.InputTokens, dirCachedInput: u.CachedInputTokens, dirOutput: u.OutputTokens} {
		if n > 0 {
			in.tokens.Add(ctx, n, metric.WithAttributes(append(slices.Clone(base), attrDirection.String(dir))...))
		}
	}
	if u.AudioSeconds > 0 {
		in.audioSeconds.Add(ctx, u.AudioSeconds, metric.WithAttributes(attrCapability.String(capability),
			attrInstance.String(string(ref.Instance))))
	}
}
