// file: internal/ai/reply_failure_metrics_test.go
// version: 1.0.0
// guid: 7c2e9a41-5b3d-4f68-a1d0-3e8b6f9c2a57
// last-edited: 2026-11-20

package ai

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/aidispatch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// emptyChoicesReply is a completion with no choices: the endpoint answered
// "ok" and said nothing.
const emptyChoicesReply = `{"id":"test","object":"chat.completion","created":1,"model":"test-model","choices":[],` +
	`"usage":{"prompt_tokens":11,"completion_tokens":0,"total_tokens":11}}`

// TestUnusableReplyCountsAsReplyParseFailure pins that every bad reply the
// model returns, typed or once untyped, lands in ai.failures{reason=reply_parse}
// next to its "ok" request, so the "Reply-quality failures by task" panel is
// not structurally zero. maxRetries is 0 so each site makes exactly one attempt
// (DoWithRetry's backoff base is fixed at 2 s).
func TestUnusableReplyCountsAsReplyParseFailure(t *testing.T) {
	ctx := context.Background()
	bat := []AuthorDedupInput{{Index: 0, CanonicalName: "Bruce Wayne"}}
	disc := []AuthorDiscoveryInput{{ID: 1, Name: "Selina Kyle"}}
	cands := []MetadataLLMCandidate{{Index: 0, Title: "The Long Halloween"}}
	q := MetadataLLMQuery{Title: "The Long Halloween"}

	cases := []struct {
		name  string
		task  string
		reply string
		call  func(p *OpenAIParser) error
	}{
		{"author dedup review, not JSON", TaskAuthorDedupReview, chatReply("not json", 11, 7),
			func(p *OpenAIParser) error { _, err := p.reviewAuthorBatch(ctx, bat); return err }},
		{"author discovery, not JSON", TaskAuthorDiscovery, chatReply("not json", 11, 7),
			func(p *OpenAIParser) error { _, err := p.discoverAuthorBatch(ctx, disc); return err }},
		{"metadata review, not JSON", TaskMetadataReview, chatReply("not json", 11, 7),
			func(p *OpenAIParser) error { _, err := p.scoreMetadataBatch(ctx, q, cands); return err }},
		{"filename parse, no choices", TaskFilenameParse, emptyChoicesReply,
			func(p *OpenAIParser) error { _, err := p.ParseFilename(ctx, "Year One"); return err }},
		{"audiobook parse, no choices", TaskAudiobookParse, emptyChoicesReply,
			func(p *OpenAIParser) error {
				_, err := p.ParseAudiobook(ctx, AudiobookContext{FilePath: "Year One"})
				return err
			}},
		{"batch parse, no choices", TaskBatchParse, emptyChoicesReply,
			func(p *OpenAIParser) error { _, err := p.ParseBatch(ctx, []string{"Year One"}); return err }},
		{"cover art, no choices", TaskCoverArt, emptyChoicesReply,
			func(p *OpenAIParser) error { _, err := p.ParseCoverArt(ctx, []byte{1, 2, 3}, "image/png"); return err }},
		{"author dedup review, no choices", TaskAuthorDedupReview, emptyChoicesReply,
			func(p *OpenAIParser) error { _, err := p.reviewAuthorBatch(ctx, bat); return err }},
		{"author discovery, no choices", TaskAuthorDiscovery, emptyChoicesReply,
			func(p *OpenAIParser) error { _, err := p.discoverAuthorBatch(ctx, disc); return err }},
		{"metadata review, no choices", TaskMetadataReview, emptyChoicesReply,
			func(p *OpenAIParser) error { _, err := p.scoreMetadataBatch(ctx, q, cands); return err }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tel := installAITelemetry(t)
			srv := newStubEndpoint(t, tc.reply, nil)
			p := newTestParser(t, srv)
			p.maxRetries = 0
			err := tc.call(p)
			if err == nil {
				t.Fatal("an unusable reply was accepted")
			}
			if _, ok := errors.AsType[*ReplyParseError](err); !ok {
				t.Fatalf("error is not a *ReplyParseError: %v", err)
			}
			rm := tel.collect(t)
			if got := counterSum(rm, "ai.requests", map[string]string{"task": tc.task, "outcome": "ok"}); got != 1 {
				t.Errorf("ai.requests{task=%s,outcome=ok} = %d, want 1", tc.task, got)
			}
			if got := counterSum(rm, "ai.failures", map[string]string{"task": tc.task, "reason": "reply_parse"}); got != 1 {
				t.Errorf("ai.failures{task=%s,reason=reply_parse} = %d, want 1", tc.task, got)
			}
			if got := counterSum(rm, "ai.requests", map[string]string{"task": tc.task, "outcome": "error"}); got != 0 {
				t.Errorf("ai.requests{task=%s,outcome=error} = %d, want 0", tc.task, got)
			}
			// One failure in total: an empty-choices reply must not be counted a
			// second time by the parse-failure record after the retry closure.
			if got := counterSum(rm, "ai.failures", map[string]string{"reason": "reply_parse"}); got != 1 {
				t.Errorf("total ai.failures{reason=reply_parse} = %d, want 1", got)
			}
		})
	}
}

// An empty-choices reply from the cover-text endpoint counts as a reply_parse
// failure, and is still the sentinel and still a Quality error: the routed
// reader's failover depends on both.
func TestCoverTextNoChoicesRecordsReplyParseAndStaysQuality(t *testing.T) {
	tel := installAITelemetry(t)
	srv := newStubEndpoint(t, emptyChoicesReply, nil)
	tp := newTestPool(visionEP("pool-node", srv.URL+"/v1", 10))
	_, err := NewRoutedCoverTextReader(tp.PoolSource).ReadCoverText(context.Background(), []byte{1}, "image/jpeg")
	if err == nil {
		t.Fatal("ReadCoverText accepted a reply with no choices")
	}
	if !errors.Is(err, errNoCoverTextChoices) {
		t.Errorf("error no longer satisfies errors.Is(errNoCoverTextChoices): %v", err)
	}
	if _, ok := errors.AsType[*aidispatch.QualityError](err); !ok {
		t.Errorf("error is not a Quality error: %v", err)
	}
	if got := srv.requests.Load(); got != 1 {
		t.Errorf("endpoint asked %d times, want 1 (a quality failure is not retried on a peer)", got)
	}
	rm := tel.collect(t)
	if got := counterSum(rm, "ai.failures", map[string]string{"task": TaskCoverText, "reason": "reply_parse"}); got != 1 {
		t.Errorf("ai.failures{task=cover_text,reason=reply_parse} = %d, want 1", got)
	}
	if got := counterSum(rm, "ai.requests", map[string]string{"task": TaskCoverText, "outcome": "ok"}); got != 1 {
		t.Errorf("ai.requests{task=cover_text,outcome=ok} = %d, want 1", got)
	}
}

// A reply the model returned but we could not use is retried like any other
// transient error, and is never promoted to a PermanentError.
func TestDoWithRetry_ReplyParseErrorIsRetriedNotPermanent(t *testing.T) {
	calls := 0
	err := DoWithRetry(context.Background(), 3, time.Millisecond, func() error {
		calls++
		return newReplyParseError(errors.New("no response from OpenAI"), "")
	})
	require.Error(t, err)
	assert.Equal(t, 3, calls, "a bad reply must exhaust its attempts")
	var perm *PermanentError
	assert.False(t, errors.As(err, &perm), "a bad reply is not permanent")
	_, isReply := errors.AsType[*ReplyParseError](err)
	assert.True(t, isReply)
}
