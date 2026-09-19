// file: internal/ai/author_dedup_job.go
// version: 1.1.0
// guid: 41243eb9-5540-41ac-8494-d0b58348a093
// last-edited: 2026-09-19

package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/ai/aijobs"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// AuthorDedupJobType is the aijobs type of maintenance.ai-dedup-batch's
// whole-library author dedup batch.
//
// It goes through aijobs rather than polling its own batch because aijobs is
// what makes the batch survive a restart: the job row is written before the
// batch is created and names it in metadata, the batch poller collects it
// whenever it completes (with or without the op that submitted it), and the
// callback is run until it applies, and never again after.
const AuthorDedupJobType = "author_dedup_full"

var authorDedupLog = logger.New("ai.author-dedup-job")

// AuthorDedupResultSink applies one completed author dedup batch. sourceID
// identifies the submitting run and is identical on every replay of the same
// job, so a sink keyed on it is idempotent.
type AuthorDedupResultSink func(ctx context.Context, sourceID string, suggestions []AuthorDiscoverySuggestion) error

var (
	authorDedupSinkMu sync.RWMutex
	authorDedupSink   AuthorDedupResultSink
)

// SetAuthorDedupResultSink registers where completed author dedup batches are
// applied. The server sets it at startup; until it does, completions fail
// their apply and aijobs retries them rather than dropping the results.
func SetAuthorDedupResultSink(sink AuthorDedupResultSink) {
	authorDedupSinkMu.Lock()
	defer authorDedupSinkMu.Unlock()
	authorDedupSink = sink
}

// authorDedupJobPayload is what the job row stores for the callback.
type authorDedupJobPayload struct {
	SourceID string                 `json:"source_id"`
	Inputs   []AuthorDiscoveryInput `json:"inputs"`
}

// SubmitAuthorDedupJob submits the whole author list as one aijobs batch and
// returns the job id. sourceID names the submitting run (see
// AuthorDedupResultSink).
func (p *OpenAIParser) SubmitAuthorDedupJob(ctx context.Context, store database.AIJobsStore, sourceID string, inputs []AuthorDiscoveryInput) (string, error) {
	if !p.enabled {
		return "", fmt.Errorf("OpenAI parser is not enabled")
	}
	if len(inputs) == 0 {
		return "", fmt.Errorf("no inputs provided")
	}
	body, err := authorDedupFullBody(p.metadataReviewModel(), inputs)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(authorDedupJobPayload{SourceID: sourceID, Inputs: inputs})
	if err != nil {
		return "", fmt.Errorf("marshal payload: %w", err)
	}
	return aijobs.Submit(ctx, aijobs.Deps{Store: store, Client: &AIJobsBatchClient{Parser: p}}, aijobs.SubmitRequest{
		Type:        AuthorDedupJobType,
		ItemCount:   1,
		PayloadJSON: payload,
		Build:       func(int) (aijobs.BatchRequest, error) { return aijobs.BatchRequest{Body: body, MaxTokens: 16000}, nil },
	})
}

// authorDedupJobCallback decodes a completed batch and applies it through the
// registered sink.
func authorDedupJobCallback(ctx context.Context, itemsJSON []byte, results []aijobs.RowResult) (int, int, []database.AIJobRowError, error) {
	var payload authorDedupJobPayload
	if err := json.Unmarshal(itemsJSON, &payload); err != nil {
		return 0, 0, nil, fmt.Errorf("decode payload: %w", err)
	}

	var suggestions []AuthorDiscoverySuggestion
	var rowErrors []database.AIJobRowError
	ok, bad := 0, 0
	for _, r := range results {
		if r.Err != "" {
			bad++
			rowErrors = append(rowErrors, database.AIJobRowError{CustomID: r.CustomID, Error: r.Err})
			continue
		}
		var parsed struct {
			Suggestions []AuthorDiscoverySuggestion `json:"suggestions"`
		}
		if err := json.Unmarshal([]byte(r.Content), &parsed); err != nil {
			bad++
			rowErrors = append(rowErrors, database.AIJobRowError{CustomID: r.CustomID, Error: "decode: " + err.Error()})
			continue
		}
		ok++
		suggestions = append(suggestions, parsed.Suggestions...)
	}

	if ok == 0 {
		// No row carried a usable answer (all decode failures, truncation, or
		// OpenAI row errors). Recording that would put an empty "complete"
		// scan in the review queue and supersede the last real one; failing
		// the apply lets aijobs retry and then mark the job failed.
		return ok, bad, rowErrors, fmt.Errorf("no usable rows in author dedup batch (%d failed)", bad)
	}

	authorDedupSinkMu.RLock()
	sink := authorDedupSink
	authorDedupSinkMu.RUnlock()
	if sink == nil {
		return ok, bad, rowErrors, fmt.Errorf("author dedup result sink not set")
	}
	if err := sink(ctx, payload.SourceID, suggestions); err != nil {
		return ok, bad, rowErrors, fmt.Errorf("apply author dedup results: %w", err)
	}
	authorDedupLog.Info("applied %d author dedup suggestions from run %s (%d rows ok, %d failed)", len(suggestions), payload.SourceID, ok, bad)
	return ok, bad, rowErrors, nil
}

func init() {
	aijobs.Register(AuthorDedupJobType, authorDedupJobCallback)
}
