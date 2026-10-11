// file: internal/ai/ai_metrics.go
// version: 1.3.1
// guid: 877e9607-e859-4bdc-99e7-21514913381b
// last-edited: 2026-10-11

package ai

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/telemetry"
)

// aiMetricsLog reports instrument-construction failures (internal/logger, printf-style).
var aiMetricsLog = logger.New("ai.metrics")

// The five ai_* families (spec 11 §3.9, D55/D66/D67) are OTel instruments from
// telemetry.Meter("ai"); the Prometheus exporter turns the dotted names into
//
//	ai_requests_total{provider,model,task,outcome}
//	ai_request_duration_seconds{provider,model,task}   (buckets: views.go)
//	ai_tokens_total{provider,model,task,direction}
//	ai_failures_total{provider,model,task,reason}
//	ai_parse_results_total{task,accepted}
//
// on the existing /metrics scrape. "endpoint" and "node" are deliberately not
// labels (rule C5: the pool is growable, and the histogram multiplies every
// label by its bucket count); the endpoint id is a span attribute only. The
// model label is bounded for the same reason: it names the configured models
// and folds the rest to "other", see modelLabelSet.
//
// This file must not import openai-go: aidispatch's
// TestGuard_NoAIBackendCallsOutsideTheDispatcher treats any file that does as
// an SDK caller. The part that needs the SDK's error type lives in
// openai_parser.go (aiHTTPFailure).

// The closed set of call sites; the values are metric label values.
const (
	TaskFilenameParse     = "filename_parse"
	TaskAudiobookParse    = "audiobook_parse"
	TaskBatchParse        = "batch_parse"
	TaskCoverArt          = "cover_art"
	TaskCoverText         = "cover_text"
	TaskAuthorDedupReview = "author_dedup_review"
	TaskAuthorDiscovery   = "author_discovery"
	TaskMetadataReview    = "metadata_review"
	TaskEmbed             = "embed"
)

// Provider label values.
const (
	providerOpenAI = "openai"
	providerLocal  = "local"
)

// Outcome label values of ai_requests_total.
const (
	outcomeOK       = "ok"
	outcomeError    = "error"
	outcomeTimeout  = "timeout"
	outcomeCanceled = "canceled"
)

// Reason label values of ai_failures_total.
const (
	reasonRateLimit     = "rate_limit"
	reasonQuota         = "quota"
	reasonUnreachable   = "unreachable"
	reasonTimeout       = "timeout"
	reasonPermanent     = "permanent"
	reasonReplyParse    = "reply_parse"
	reasonCountMismatch = "count_mismatch"
	reasonOther         = "other"
)

var (
	attrProvider  = telemetry.Provider
	attrModel     = telemetry.Model
	attrTask      = telemetry.Task
	attrOutcome   = telemetry.Outcome
	attrReason    = telemetry.Reason
	attrDirection = telemetry.Direction
	attrAccepted  = telemetry.Accepted
)

// maxModelLabelLen bounds a model label value; see callMeta.normalized.
const maxModelLabelLen = 64

// maxModelLabels is how many distinct (provider, model) pairs the process names
// in the model label over its whole life; every other model is "other". It is
// the cardinality guard of rule C2 for a growable pool: every routed parser
// sends its own endpoint's model, so the number of models is not a constant of
// the product.
//
// The arithmetic: the duration histogram exports 15 series (12 buckets, +Inf,
// _sum, _count) per (provider, model, task), the widest family. "other" is one
// more value per provider and nothing else is reserved (an empty model is
// folded to "other" too, see normalized), so at most maxModelLabels + 2 pairs
// exist: (5 + 2) x 9 tasks x 15 = 945, under the 1,000-series cap. Raising this
// by one costs 135 series and breaks the cap, so TestSeriesCardinalityBound
// fails first. Keeping "unknown" as a label of its own would be two more pairs
// (one per provider) and cap this at 3.
//
// "Over its whole life" matters: the OTel instruments are cumulative, so a
// series stays on /metrics until restart even after nothing records to it, and
// a pair dropped from the named set would still count against C2. A name is
// therefore never revoked.
const maxModelLabels = 5

// modelPair is the admission key: the same model name served by a local backend
// and by OpenAI are two label values of two providers.
type modelPair struct{ provider, model string }

// modelLabelSet decides which models the model label names (owner decision
// 2026-10-11, AI-MODEL-LABEL-CAP-POLICY): the models configured in settings,
// always, and nothing else; every other model is "other".
//
// The named set is derived from the config, never from the order calls arrive
// in. It is filled from config.Snapshot the first time a label is needed and
// again whenever config.Generation moves, which covers the startup load,
// PUT /config and the update service's rollback alike. So a restart with the
// same settings names the same models whatever runs first, a model an admin
// merely tried while it was not saved costs nothing, and a model saved in
// settings is named from the next call on.
//
// Names are sticky: a model that was configured and is then replaced stays
// named until restart, because its series stays exported until then (see
// maxModelLabels) and renaming it would split one series in two. The price is
// that the named set can hold at most maxModelLabels pairs per process life: a
// model configured after that many others were, which does not fit, is
// "other" until restart and logs a warning saying so. Which pairs a config
// names when it holds more than the cap is configuredModelPairs' priority
// order, not call order.
type modelLabelSet struct {
	mu    sync.RWMutex
	named map[modelPair]struct{}
	// warned records pairs already reported as not fitting, so the warning is
	// once per pair per process rather than once per settings write.
	warned map[modelPair]struct{}
	// syncedGen is the source generation + 1 that named was last filled at;
	// zero means never. Atomic so the common case (nothing changed) is one load.
	syncedGen atomic.Uint64
	// source is atomic because syncIfStale reads it before taking mu; only
	// tests ever replace it.
	source atomic.Pointer[modelSource]
	// fixedGen numbers setConfigured calls, tests only.
	fixedGen uint64
}

// modelSource is where the configured models come from. Production reads the
// live config; tests substitute a fixed list.
type modelSource struct {
	generation func() uint64
	configured func() []modelPair
}

func configModelSource() modelSource {
	return modelSource{
		generation: config.Generation,
		configured: func() []modelPair {
			cfg := config.Snapshot()
			return configuredModelPairs(&cfg)
		},
	}
}

var modelLabels = newModelLabelSet()

func newModelLabelSet() *modelLabelSet {
	s := &modelLabelSet{
		named:  map[modelPair]struct{}{},
		warned: map[modelPair]struct{}{},
	}
	src := configModelSource()
	s.source.Store(&src)
	return s
}

// admit returns the label for model: its own name when (provider, model) is a
// configured pair that fits, "other" otherwise.
func (s *modelLabelSet) admit(provider, model string) string {
	if model == modelOther {
		return model
	}
	s.syncIfStale()
	s.mu.RLock()
	_, ok := s.named[modelPair{provider, model}]
	s.mu.RUnlock()
	if ok {
		return model
	}
	return modelOther
}

// syncIfStale names the configured pairs the set does not name yet, when the
// source has changed since the last sync.
func (s *modelLabelSet) syncIfStale() {
	src := s.source.Load()
	gen := src.generation() + 1
	if s.syncedGen.Load() == gen {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.syncedGen.Load() == gen {
		return
	}
	// Read under s.mu so two racing syncs cannot interleave, and after the
	// generation was read, so what is named is at least that generation's
	// state. config.Snapshot takes config's read lock; nothing holds that lock
	// while calling into this package, so there is no lock-order cycle.
	for _, p := range src.configured() {
		if _, ok := s.named[p]; ok {
			continue
		}
		if len(s.named) >= maxModelLabels {
			if _, seen := s.warned[p]; !seen {
				s.warned[p] = struct{}{}
				aiMetricsLog.Warn("ai metrics: configured model cannot be named in the model label; "+
					"it is reported as \"other\" until the process restarts (provider=%q model=%q named_models_cap=%d)",
					p.provider, p.model, maxModelLabels)
			}
			continue
		}
		s.named[p] = struct{}{}
	}
	s.syncedGen.Store(gen)
}

// reset forgets every named pair and goes back to the live config as the
// source; tests only.
func (s *modelLabelSet) reset() {
	s.mu.Lock()
	s.named = map[modelPair]struct{}{}
	s.warned = map[modelPair]struct{}{}
	src := configModelSource()
	s.source.Store(&src)
	s.syncedGen.Store(0)
	s.mu.Unlock()
}

// setConfigured replaces the source with a fixed list of configured pairs and
// marks it changed, so the next label re-syncs: a test calls it again to
// simulate a settings change. Tests only.
func (s *modelLabelSet) setConfigured(pairs ...modelPair) {
	s.mu.Lock()
	s.fixedGen++
	gen := s.fixedGen
	s.source.Store(&modelSource{
		generation: func() uint64 { return gen },
		configured: func() []modelPair { return pairs },
	})
	// Forget the generation the last sync ran at: a test source's counter
	// starts at 1 and the live config's can be any value, so without this a
	// stale syncedGen could equal the new source's generation and skip the
	// re-sync this call exists to force.
	s.syncedGen.Store(0)
	s.mu.Unlock()
}

// Model label values that are not a model name.
const (
	modelOther = "other"
	// modelUnknown is the span's ai.model for a call that sent no model. The
	// metric label folds it to modelOther (see callMeta.normalized).
	modelUnknown = "unknown"
)

// callMeta identifies one AI request for metrics and traces. Every field is a
// small closed or configured value; never a filename, prompt or reply.
type callMeta struct {
	provider string
	model    string
	task     string
	// endpointID is the pool row id of a routed parser. It goes on the span
	// only (ai.endpoint_id), never on a metric.
	endpointID string
	// spanModel is the model name after the whitespace and length fold but
	// before the label-set admission, set by normalized. The cardinality cap
	// protects metric series; a trace attribute is not a series, so the span
	// keeps the real name and a model past the cap is still identifiable there.
	spanModel string
}

// normalized folds a model string that would make a poor label value to a
// fixed one: empty or longer than 64 characters or containing whitespace (free
// text leaking into the model field) is "other"; and a model that is not one of
// the configured models is "other" too (see modelLabelSet). Only the metric
// label is folded this far: spanModel keeps the name that survived the length
// and whitespace folds, and "unknown" for an empty one, so a span still says
// what a label cannot.
func (m callMeta) normalized() callMeta {
	if m.provider == "" {
		m.provider = providerOpenAI
	}
	switch {
	case m.model == "":
		// Not "unknown" as a label of its own: that is one more value per
		// provider on every family, which the series cap has no room for (see
		// maxModelLabels).
		if m.spanModel == "" {
			m.spanModel = modelUnknown
		}
		m.model = modelOther
	case len(m.model) > maxModelLabelLen || strings.ContainsAny(m.model, " \t\r\n"):
		m.model = modelOther
	}
	if m.spanModel == "" {
		m.spanModel = m.model
	}
	m.model = modelLabels.admit(m.provider, m.model)
	return m
}

func (m callMeta) labelList() []attribute.KeyValue {
	return []attribute.KeyValue{attrProvider.String(m.provider), attrModel.String(m.model), attrTask.String(m.task)}
}

func (m callMeta) labels() attribute.Set { return attribute.NewSet(m.labelList()...) }

// spanAttributeModel is the model for the span: the uncapped name when
// normalized ran, the label otherwise.
func (m callMeta) spanAttributeModel() string {
	if m.spanModel != "" {
		return m.spanModel
	}
	return m.model
}

func (m callMeta) spanAttributes() []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String("ai.provider", m.provider),
		attribute.String("ai.model", m.spanAttributeModel()),
		attribute.String("ai.task", m.task),
	}
	if m.endpointID != "" {
		attrs = append(attrs, attribute.String("ai.endpoint_id", m.endpointID))
	}
	return attrs
}

type aiInstruments struct {
	requests     metric.Int64Counter
	duration     metric.Float64Histogram
	tokens       metric.Int64Counter
	failures     metric.Int64Counter
	parseResults metric.Int64Counter
}

// aiMetrics holds the instruments. They are created on first use from the
// global meter provider; a test stores its own (newAIInstruments over a
// private provider) because the global provider binds once per process.
var aiMetrics atomic.Pointer[aiInstruments]

func aiInstrumentsFor() *aiInstruments {
	if m := aiMetrics.Load(); m != nil {
		return m
	}
	m := newAIInstruments(telemetry.Meter("ai"))
	if aiMetrics.CompareAndSwap(nil, m) {
		return m
	}
	return aiMetrics.Load()
}

// newAIInstruments builds the five instruments. The metric package's
// constructors return a no-op instrument alongside any error, so a failure
// degrades to missing series, never a panic or a nil dereference. Counter
// names carry no "_total" (the exporter appends it); the units are bracketed
// annotations, never "1".
func newAIInstruments(m metric.Meter) *aiInstruments {
	ins := &aiInstruments{}
	// A constructor error leaves a usable no-op instrument in place; collect
	// the errors and log them once so a missing series has a stated reason.
	var errs []error
	var err error
	ins.requests, err = m.Int64Counter("ai.requests",
		metric.WithUnit("{request}"),
		metric.WithDescription("Direct AI HTTP attempts, by provider, model, task and outcome. One per attempt: a retry, whether the SDK's own or the retry layer's, is a new attempt."))
	errs = append(errs, err)
	ins.duration, err = m.Float64Histogram("ai.request.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Wall time of one HTTP attempt, by provider, model and task, excluding the SDK's retry backoff."))
	errs = append(errs, err)
	ins.tokens, err = m.Int64Counter("ai.tokens",
		metric.WithUnit("{token}"),
		metric.WithDescription("Tokens the provider reported for direct AI requests (batch-API tokens are not recorded)."))
	errs = append(errs, err)
	ins.failures, err = m.Int64Counter("ai.failures",
		metric.WithUnit("{failure}"),
		metric.WithDescription("Failed AI requests and unusable replies, by reason."))
	errs = append(errs, err)
	ins.parseResults, err = m.Int64Counter("ai.parse_results",
		metric.WithUnit("{result}"),
		metric.WithDescription("Scan-phase AI parse results per item: accepted (usable) or not."))
	errs = append(errs, err)
	if joined := errors.Join(errs...); joined != nil {
		aiMetricsLog.Warn("ai metrics: some instruments could not be created; those series will be missing: %v", joined)
	}
	return ins
}

// RecordParseResult counts one item of an AI parse at its final disposition.
// accepted means the model's result was usable (at least one field merged),
// not that it was high-confidence: nothing compares ParsedMetadata.Confidence.
func RecordParseResult(task string, accepted bool) {
	aiInstrumentsFor().parseResults.Add(context.Background(), 1,
		metric.WithAttributes(attrTask.String(task), attrAccepted.Bool(accepted)))
}

// classifyCall maps the error of one SDK call (nil for success) to the
// ai_requests_total outcome and the ai_failures_total reason. Reply-quality
// errors are the exception: they are produced after a successful call, so
// their outcome is "ok" (see recordReplyFailure) and only the reason is set.
//
// A cancellation has outcome "canceled" and no reason: it is not a failure.
// The reason order is: reply errors, quota 429, plain 429, unreachable,
// deadline, permanent, other.
func classifyCall(err error) (outcome, reason string) {
	if err == nil {
		return outcomeOK, ""
	}
	if r := replyFailureReason(err); r != "" {
		return outcomeOK, r
	}
	if errors.Is(err, context.Canceled) {
		return outcomeCanceled, ""
	}
	status, quota := aiHTTPFailure(err)
	switch {
	case quota:
		reason = reasonQuota
	case status == 429:
		reason = reasonRateLimit
	case isUnreachableError(err):
		reason = reasonUnreachable
	case isDeadline(err):
		reason = reasonTimeout
	case isPermanentAIError(err):
		reason = reasonPermanent
	default:
		reason = reasonOther
	}
	if reason == reasonTimeout {
		return outcomeTimeout, reason
	}
	return outcomeError, reason
}

// isDeadline reports a context deadline or a network timeout (an HTTP client
// timeout is not a context.DeadlineExceeded).
func isDeadline(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
		return true
	}
	return false
}
