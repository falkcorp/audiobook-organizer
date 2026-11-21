// file: internal/books/appconfig/ai.go
// version: 1.0.0
// guid: d27b4f90-3c61-4e8a-b5d9-0a6e1c83f742
// last-edited: 2026-11-19

package appconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	coreconfig "github.com/falkcorp/mediacore/config"
)

// foldAIBackend is the new ai_endpoints list for an Update's ai_backend.* values:
// cutover.FoldAIBackend over the list as it stands (or as the same Update already
// sets it). A value the list cannot hold is a field error naming the field (the
// first, when several fail the same way), never a partial write. It returns nil
// when the list comes out as it went in (a form that re-sends the values it was
// given), so the write is a no-op.
func (e *Engine) foldAIBackend(ctx context.Context, vals map[string]json.RawMessage, resSet map[string]json.RawMessage) (json.RawMessage, error) {
	prev, ok := resSet["ai_endpoints"]
	if !ok {
		loaded, err := e.res.Load(ctx, []string{"ai_endpoints"})
		if err != nil {
			return nil, fmt.Errorf("appconfig: reading ai_endpoints: %w", err)
		}
		prev = loaded["ai_endpoints"]
	}
	out, err := cutover.FoldAIBackend(vals, prev)
	if err != nil {
		first := "ai_backend." + slices.Sorted(maps.Keys(vals))[0]
		if errors.Is(err, cutover.ErrConvertInput) || errors.Is(err, cutover.ErrNotRepresentable) {
			return nil, &coreconfig.FieldError{Path: coreconfig.Path(first), Err: err}
		}
		return nil, err
	}
	if bytes.Equal(bytes.TrimSpace(out), bytes.TrimSpace(prev)) {
		return nil, nil
	}
	return out, nil
}
