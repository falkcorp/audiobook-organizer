// file: internal/server/handlers/metadata.go
// version: 1.1.0
// guid: f6a7b8c9-d0e1-2345-fabc-345678901234
// last-edited: 2026-06-01

package handlers

import (
	"encoding/json"

	"github.com/falkcorp/audiobook-organizer/internal/operations"
)

// BulkMetadataFetchV2Params is the JSON params for the v2 bulk_metadata_fetch op.
// Selection replaces the old BookIDs field: the client sends either
//   - book_ids: an explicit list of IDs (page-level selection), or
//   - filter: a FilterSpec that the server resolves to IDs at run time
//     with IsPrimaryVersion=true always applied.
type BulkMetadataFetchV2Params struct {
	Selection     operations.SelectionSpec `json:"selection"`
	PreferAudible bool                     `json:"prefer_audible"`

	// SkipCached is a POINTER so "absent" stays distinguishable from an
	// explicit false. Absent means TRUE: a bulk fetch should not re-hit
	// providers for books whose cache entry is still fresh, which is the
	// difference between "fetch what we're missing" and "re-fetch the entire
	// library". Pass false explicitly to force a full refresh.
	SkipCached *bool `json:"skip_cached,omitempty"`

	// RunKey identifies a fetch CHAIN. Every OperationResult row is written
	// under it, so a continuation resumes the same ledger as its predecessor.
	// Empty on a user's first dispatch; the op mints one and carries it forward.
	//
	// A fresh dispatch minting a NEW key is load-bearing: it opens a new epoch,
	// which is what stops a completed chain's ledger from turning every future
	// run into a no-op that skips everything and reports success.
	RunKey string `json:"run_key,omitempty"`

	// Continuation is the 0-based link index within a chain. It exists to make
	// a successor's params BYTE-DIFFERENT from the still-running predecessor's:
	// EnqueueOp returns the EXISTING op id for byte-identical params while one
	// is active, so an otherwise-identical successor would be silently
	// swallowed and the chain would stop while looking like it had completed.
	Continuation int `json:"continuation,omitempty"`
}

// ResolveSkipCached applies the "absent means true" rule above.
func (p BulkMetadataFetchV2Params) ResolveSkipCached() bool {
	return p.SkipCached == nil || *p.SkipCached
}

// RatingPatchRequest is the JSON body for PATCH /api/v1/audiobooks/:id/rating.
// Each field is a json.RawMessage so the handler can distinguish null (clear)
// from absent (don't touch) from a numeric value.
type RatingPatchRequest struct {
	Overall     json.RawMessage `json:"overall"`
	Story       json.RawMessage `json:"story"`
	Performance json.RawMessage `json:"performance"`
	Notes       json.RawMessage `json:"notes"`
}
