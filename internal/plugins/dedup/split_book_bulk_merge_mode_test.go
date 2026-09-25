// file: internal/plugins/dedup/split_book_bulk_merge_mode_test.go
// version: 1.0.0
// guid: 2a8c5e1f-7d39-4b64-9f07-e3b6d4a1c852
// last-edited: 2026-09-25

package dedup

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
)

// dedup.split-book-bulk-merge took a plain `DryRun bool` until 2026-09-25, so
// a body with items and no dry_run -- anything other than the HTTP handler,
// which always set it -- merged and soft-deleted books for real. The mode now
// goes through opmode.ResolveDryRun; a conflicting body proves both spellings
// reach it, and it must be refused before the item/store checks.
func TestSplitBookBulkMerge_ModeRoutesThroughOpmode(t *testing.T) {
	p := &Plugin{}
	for _, body := range []string{
		`{"items":[],"dry_run":false,"dryRun":true}`,
		`{"items":[],"dry_run":true,"dryRun":false}`,
	} {
		err := p.runSplitBookBulkMerge(context.Background(), json.RawMessage(body), &fakeReporter{})
		if err == nil || !strings.Contains(err.Error(), "disagree") {
			t.Fatalf("body %s: want a dry_run/dryRun disagreement error, got %v", body, err)
		}
	}
}

// TestBulkSplitBookMergeParams_OmittedIsPreview pins the params shape: an
// omitted mode decodes to nil (which ResolveDryRun maps to preview), never to
// a false that would read as "apply".
func TestBulkSplitBookMergeParams_OmittedIsPreview(t *testing.T) {
	var params dedupengine.BulkSplitBookMergeParams
	if err := json.Unmarshal([]byte(`{"items":[{"candidate_id":"c","book_ids":["a","b"],"keep_id":"a"}]}`), &params); err != nil {
		t.Fatal(err)
	}
	if params.DryRun != nil || params.DryRunCamel != nil {
		t.Fatalf("omitted mode must decode to nil, got dry_run=%v dryRun=%v", params.DryRun, params.DryRunCamel)
	}
}
