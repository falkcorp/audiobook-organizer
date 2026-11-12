// file: core/coretest/ai.go
// version: 1.0.0
// guid: b3e7a914-5d26-4f08-9c1b-6a8d2e0f4c75
// last-edited: 2026-11-12

package coretest

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
)

// FakeAI is the scriptable ai.Backend modules and the router's own tests run
// against (05-ai section 9): one fake endpoint that serves text, embeddings
// and transcription from a script, speaks the batch API from an in-memory
// remote, records every call it receives, and can be told to fail with any
// error, wait, or block until released. All methods are safe for concurrent
// use. It implements ai.Backend, ai.TextGenerator, ai.Embedder,
// ai.Transcriber and ai.BatchBackend.
type FakeAI struct {
	mu sync.Mutex

	caps      ai.BackendCaps
	capsErr   error
	capsCalls int

	text       fakeScript
	embed      fakeScript
	transcribe fakeScript
	calls      []FakeAICall

	batches       map[ai.BatchHandle]*fakeBatch
	nextBatch     int
	loseSubmit    bool
	submitErrs    []error // consumed one per SubmitBatch call; the last repeats
	submitNext    int
	batchItemFunc func(ai.BatchItem) ai.BatchResult
	cancels       []ai.BatchHandle
	batchCalls    int
}

// FakeAICall is one call a FakeAI received.
type FakeAICall struct {
	Kind       ai.Kind
	Model      string
	Text       ai.TextRequest
	Embed      ai.EmbedRequest
	Transcribe ai.TranscribeRequest
}

// FakeAIStep is one scripted answer. Err wins over the response; Latency waits
// (honouring the call's context) before answering; Block, when set, holds the
// call until it is closed or the call's context ends.
type FakeAIStep struct {
	Err     error
	Latency time.Duration
	Block   <-chan struct{}
	Usage   ai.Usage
	// Text, Vectors and Transcript are the kind's reply.
	Text       string
	Vectors    [][]float32
	Transcript string
	Duration   time.Duration
	Model      string // the model the fake reports; the requested one when empty
}

// fakeScript is a queue of steps; the last step repeats once the queue is
// spent, and an empty queue answers with an empty successful reply.
type fakeScript struct {
	steps []FakeAIStep
	next  int
}

func (s *fakeScript) take() FakeAIStep {
	if len(s.steps) == 0 {
		return FakeAIStep{}
	}
	i := min(s.next, len(s.steps)-1)
	s.next++
	return s.steps[i]
}

type fakeBatch struct {
	tags    map[string]string
	items   []ai.BatchItem
	phase   ai.BatchPhase
	results []ai.BatchResult
}

// NewFakeAI is an endpoint with no caps, an empty script for every kind and an
// empty remote batch store.
func NewFakeAI() *FakeAI {
	return &FakeAI{batches: map[ai.BatchHandle]*fakeBatch{}}
}

// ---- configuration ---------------------------------------------------------------

// SetCaps sets what Caps reports.
func (f *FakeAI) SetCaps(c ai.BackendCaps) *FakeAI {
	f.mu.Lock()
	f.caps = c
	f.mu.Unlock()
	return f
}

// FailCaps makes Caps return err (nil restores it).
func (f *FakeAI) FailCaps(err error) *FakeAI {
	f.mu.Lock()
	f.capsErr = err
	f.mu.Unlock()
	return f
}

// CapsCalls is how many times Caps was called.
func (f *FakeAI) CapsCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.capsCalls
}

// ScriptText replaces the text script.
func (f *FakeAI) ScriptText(steps ...FakeAIStep) *FakeAI {
	f.mu.Lock()
	f.text = fakeScript{steps: slices.Clone(steps)}
	f.mu.Unlock()
	return f
}

// ScriptEmbed replaces the embedding script.
func (f *FakeAI) ScriptEmbed(steps ...FakeAIStep) *FakeAI {
	f.mu.Lock()
	f.embed = fakeScript{steps: slices.Clone(steps)}
	f.mu.Unlock()
	return f
}

// ScriptTranscribe replaces the transcription script.
func (f *FakeAI) ScriptTranscribe(steps ...FakeAIStep) *FakeAI {
	f.mu.Lock()
	f.transcribe = fakeScript{steps: slices.Clone(steps)}
	f.mu.Unlock()
	return f
}

// Calls returns the calls received so far, oldest first.
func (f *FakeAI) Calls() []FakeAICall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// CallCount is how many calls of kind were received.
func (f *FakeAI) CallCount(kind ai.Kind) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.Kind == kind {
			n++
		}
	}
	return n
}

// ---- ai.Backend ---------------------------------------------------------------------

// Health reports OK.
func (f *FakeAI) Health(context.Context) integrations.Health {
	return integrations.Health{Status: integrations.HealthOK}
}

// Close does nothing.
func (f *FakeAI) Close() error { return nil }

// Caps reports the configured caps.
func (f *FakeAI) Caps(context.Context) (ai.BackendCaps, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.capsCalls++
	if f.capsErr != nil {
		return ai.BackendCaps{}, f.capsErr
	}
	return ai.BackendCaps{
		Models:   slices.Clone(f.caps.Models),
		Features: slices.Clone(f.caps.Features),
		Labels:   slices.Clone(f.caps.Labels),
		Locality: f.caps.Locality,
	}, nil
}

// wait runs a step's latency and block, honouring ctx.
func wait(ctx context.Context, st FakeAIStep) error {
	if st.Latency > 0 {
		t := time.NewTimer(st.Latency)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if st.Block != nil {
		select {
		case <-st.Block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return ctx.Err()
}

// Generate answers from the text script.
func (f *FakeAI) Generate(ctx context.Context, model string, req ai.TextRequest) (ai.TextResponse, error) {
	f.mu.Lock()
	f.calls = append(f.calls, FakeAICall{Kind: ai.KindText, Model: model, Text: req})
	st := f.text.take()
	f.mu.Unlock()
	if err := wait(ctx, st); err != nil {
		return ai.TextResponse{}, err
	}
	if st.Err != nil {
		return ai.TextResponse{}, st.Err
	}
	return ai.TextResponse{Text: st.Text, FinishReason: "stop", Model: modelOr(st.Model, model), Usage: st.Usage}, nil
}

// Embed answers from the embedding script.
func (f *FakeAI) Embed(ctx context.Context, model string, req ai.EmbedRequest) (ai.EmbedResponse, error) {
	f.mu.Lock()
	f.calls = append(f.calls, FakeAICall{Kind: ai.KindEmbed, Model: model, Embed: req})
	st := f.embed.take()
	f.mu.Unlock()
	if err := wait(ctx, st); err != nil {
		return ai.EmbedResponse{}, err
	}
	if st.Err != nil {
		return ai.EmbedResponse{}, st.Err
	}
	return ai.EmbedResponse{Vectors: st.Vectors, Model: modelOr(st.Model, model), Usage: st.Usage}, nil
}

// Transcribe answers from the transcription script.
func (f *FakeAI) Transcribe(ctx context.Context, model string, req ai.TranscribeRequest) (ai.TranscribeResponse, error) {
	f.mu.Lock()
	f.calls = append(f.calls, FakeAICall{Kind: ai.KindTranscribe, Model: model, Transcribe: req})
	st := f.transcribe.take()
	f.mu.Unlock()
	if err := wait(ctx, st); err != nil {
		return ai.TranscribeResponse{}, err
	}
	if st.Err != nil {
		return ai.TranscribeResponse{}, st.Err
	}
	return ai.TranscribeResponse{Text: st.Transcript, Duration: st.Duration, Usage: st.Usage}, nil
}

func modelOr(reported, asked string) string {
	if reported != "" {
		return reported
	}
	return asked
}

// ---- the batch API ---------------------------------------------------------------------

// ErrFakeSubmitLost is what SubmitBatch returns while LoseSubmitResponse is on:
// the remote kept the batch but the caller never heard.
var ErrFakeSubmitLost = errors.New("coretest: the batch was accepted but the response was lost")

// LoseSubmitResponse makes SubmitBatch create the remote batch and then fail
// with ErrFakeSubmitLost, as a dropped connection does: the orphan the poll
// must find by tag.
func (f *FakeAI) LoseSubmitResponse(on bool) *FakeAI {
	f.mu.Lock()
	f.loseSubmit = on
	f.mu.Unlock()
	return f
}

// FailSubmit scripts SubmitBatch's failures: the call consumes one error per
// call, the last repeating, and a nil entry lets that call succeed. Nothing
// reaches the remote store for a call that fails this way (the provider
// refused the batch). It replaces any earlier script; no arguments clears it.
func (f *FakeAI) FailSubmit(errs ...error) *FakeAI {
	f.mu.Lock()
	f.submitErrs, f.submitNext = slices.Clone(errs), 0
	f.mu.Unlock()
	return f
}

// BatchCallCount is how many batch API calls (submit, status, results, cancel,
// find) the endpoint received.
func (f *FakeAI) BatchCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.batchCalls
}

// AnswerBatchItems sets how a finished batch answers each item; the default
// echoes the item's custom ID as the reply text.
func (f *FakeAI) AnswerBatchItems(fn func(ai.BatchItem) ai.BatchResult) *FakeAI {
	f.mu.Lock()
	f.batchItemFunc = fn
	f.mu.Unlock()
	return f
}

// SubmitBatch records a remote batch in progress.
func (f *FakeAI) SubmitBatch(_ context.Context, _ string, items []ai.BatchItem, tags map[string]string) (ai.BatchHandle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batchCalls++
	if len(f.submitErrs) > 0 {
		err := f.submitErrs[min(f.submitNext, len(f.submitErrs)-1)]
		f.submitNext++
		if err != nil {
			return "", err
		}
	}
	f.nextBatch++
	h := ai.BatchHandle(fmt.Sprintf("batch_%d", f.nextBatch))
	f.batches[h] = &fakeBatch{tags: maps.Clone(tags), items: slices.Clone(items), phase: ai.BatchInProgress}
	if f.loseSubmit {
		return "", ErrFakeSubmitLost
	}
	return h, nil
}

// BatchStatus reports the remote batch's phase.
func (f *FakeAI) BatchStatus(_ context.Context, h ai.BatchHandle) (ai.BatchState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batchCalls++
	b, ok := f.batches[h]
	if !ok {
		return ai.BatchState{}, &integrations.Error{Op: "batch status", Class: integrations.ClassNotFound, Err: fmt.Errorf("no batch %s", h)}
	}
	return ai.BatchState{Phase: b.phase, Total: len(b.items), Completed: len(b.items)}, nil
}

// BatchResults yields one result per item of a completed batch.
func (f *FakeAI) BatchResults(_ context.Context, h ai.BatchHandle) (iter.Seq2[ai.BatchResult, error], error) {
	f.mu.Lock()
	f.batchCalls++
	b, ok := f.batches[h]
	if !ok {
		f.mu.Unlock()
		return nil, &integrations.Error{Op: "batch results", Class: integrations.ClassNotFound, Err: fmt.Errorf("no batch %s", h)}
	}
	if b.phase != ai.BatchCompleted {
		f.mu.Unlock()
		return nil, fmt.Errorf("coretest: batch %s is %s, not completed", h, b.phase)
	}
	items, answer := slices.Clone(b.items), f.batchItemFunc
	f.mu.Unlock()
	return func(yield func(ai.BatchResult, error) bool) {
		for _, it := range items {
			var r ai.BatchResult
			if answer != nil {
				r = answer(it)
			} else {
				r = ai.BatchResult{CustomID: it.CustomID, Text: &ai.TextResponse{Text: it.CustomID,
					Usage: ai.Usage{Requests: 1, InputTokens: 10, OutputTokens: 5}}}
			}
			if !yield(r, nil) {
				return
			}
		}
	}, nil
}

// CancelBatch cancels the remote batch and records the call.
func (f *FakeAI) CancelBatch(_ context.Context, h ai.BatchHandle) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batchCalls++
	f.cancels = append(f.cancels, h)
	b, ok := f.batches[h]
	if !ok {
		return &integrations.Error{Op: "batch cancel", Class: integrations.ClassNotFound, Err: fmt.Errorf("no batch %s", h)}
	}
	b.phase = ai.BatchCanceled
	return nil
}

// FindBatches lists the remote batches whose tags contain tags.
func (f *FakeAI) FindBatches(_ context.Context, tags map[string]string) ([]ai.BatchHandle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batchCalls++
	var out []ai.BatchHandle
	for _, h := range slices.Sorted(maps.Keys(f.batches)) {
		match := true
		for k, v := range tags {
			if f.batches[h].tags[k] != v {
				match = false
			}
		}
		if match {
			out = append(out, h)
		}
	}
	return out, nil
}

// CompleteBatch moves the remote batch to Completed.
func (f *FakeAI) CompleteBatch(h ai.BatchHandle) { f.setPhase(h, ai.BatchCompleted) }

// FailBatch moves the remote batch to Failed.
func (f *FakeAI) FailBatch(h ai.BatchHandle) { f.setPhase(h, ai.BatchFailed) }

func (f *FakeAI) setPhase(h ai.BatchHandle, p ai.BatchPhase) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b := f.batches[h]; b != nil {
		b.phase = p
	}
}

// RemoteBatches lists the remote batch handles, in order.
func (f *FakeAI) RemoteBatches() []ai.BatchHandle {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Sorted(maps.Keys(f.batches))
}

// RemoteBatchTags is the tags the remote batch was submitted with.
func (f *FakeAI) RemoteBatchTags(h ai.BatchHandle) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b := f.batches[h]; b != nil {
		return maps.Clone(b.tags)
	}
	return nil
}

// RemoteBatchPhase is the remote batch's phase.
func (f *FakeAI) RemoteBatchPhase(h ai.BatchHandle) (ai.BatchPhase, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b := f.batches[h]; b != nil {
		return b.phase, true
	}
	return 0, false
}

// CancelCalls lists the handles CancelBatch was called with.
func (f *FakeAI) CancelCalls() []ai.BatchHandle {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.cancels)
}
