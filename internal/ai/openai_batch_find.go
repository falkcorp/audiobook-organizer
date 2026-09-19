// file: internal/ai/openai_batch_find.go
// version: 1.1.0
// guid: 4769ae5d-5c7b-4283-b3f0-132b915706e5
// last-edited: 2026-09-19

package ai

import (
	"context"
	"time"
)

// FindBatchByMetadata returns the id of the project batch created at or after
// since whose metadata carries every key/value in match. It is how a local
// record whose process died between OpenAI accepting a batch and the write of
// its id re-attaches instead of paying for a second batch.
//
// found=false is returned only for a CONFIRMED absence: the listing reached
// back past since. A listing cut short by its page cap returns
// ErrBatchListTruncated instead, because the batch may sit on a page that was
// never read, and a caller that took "not found" there would resubmit. When
// several batches match (not expected), the most recently created wins.
func (p *OpenAIParser) FindBatchByMetadata(ctx context.Context, match map[string]string, since time.Time) (string, bool, error) {
	batches, err := p.ListProjectBatches(ctx, since)
	if err != nil {
		return "", false, err
	}
	var best *BatchInfo
	for i := range batches {
		b := &batches[i]
		if !metadataMatches(b.Metadata, match) {
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

func metadataMatches(md, match map[string]string) bool {
	if len(match) == 0 {
		return false
	}
	for k, v := range match {
		if md[k] != v {
			return false
		}
	}
	return true
}
