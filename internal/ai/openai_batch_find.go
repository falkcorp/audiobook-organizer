// file: internal/ai/openai_batch_find.go
// version: 1.0.0
// guid: 4769ae5d-5c7b-4283-b3f0-132b915706e5
// last-edited: 2026-09-19

package ai

import (
	"context"
	"strconv"
	"time"
)

// scanBatchLookback is how far back FindScanBatch pages through the project's
// batch history. A batch lives 24h and its output 30 days; a scan phase left
// "submitting" is looked up when its op resumes, normally minutes after the
// crash. A week covers a server that stayed down over a long weekend, and
// ListProjectBatches' page cap bounds the walk regardless.
const scanBatchLookback = 7 * 24 * time.Hour

// FindScanBatch returns the id of the batch whose owner metadata names this
// ai.author-scan phase (BatchMetaScanID / BatchMetaScanPhase, written by the
// pipeline at CreateBatch time). It is how a phase whose process died between
// OpenAI accepting the batch and the local write of its id re-attaches instead
// of paying for a second batch. found is false when no batch in the lookback
// window carries the keys. When several do (not expected: a phase is submitted
// once), the most recently created wins.
func (p *OpenAIParser) FindScanBatch(ctx context.Context, scanID int, phaseType string) (string, bool, error) {
	batches, err := p.ListProjectBatches(ctx, time.Now().Add(-scanBatchLookback))
	if err != nil {
		return "", false, err
	}
	want := strconv.Itoa(scanID)
	var best *BatchInfo
	for i := range batches {
		b := &batches[i]
		if b.Metadata[BatchMetaScanID] != want || b.Metadata[BatchMetaScanPhase] != phaseType {
			continue
		}
		if best == nil || b.CreatedAt.After(best.CreatedAt) {
			best = b
		}
	}
	if best == nil {
		return "", false, nil
	}
	return best.ID, true, nil
}
