// file: internal/server/identification_advance_items_options_test.go
// version: 1.0.0
// guid: 4c7e1a95-2b60-4f83-8d29-b0e5a1c3d746
// last-edited: 2026-11-19

package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/identification"
)

// The shared core's pool options are identAdvanceRunOptions read back: the same
// worker count, error mode, progress bounds and label, so the two cannot drift.
func TestIdentAdvanceItemsOptions_MirrorTheRunOptions(t *testing.T) {
	adv := identification.Advance{
		Concurrency: 6,
		Label:       func(m identification.Mark) string { return "label " + m.BookID },
	}
	page := []identification.Mark{{BookID: "book-a", Epoch: 1}, {BookID: "book-b", Epoch: 1}, {BookID: "book-c", Epoch: 1}}

	run := identAdvanceRunOptions(adv, page, 4)
	got := identAdvanceItemsOptions(adv, page, 4)

	assert.Equal(t, 6, got.Concurrency)
	assert.Equal(t, run.Concurrency, got.Concurrency)
	assert.True(t, got.CollectErrors, "one book's failure never stops the others")
	assert.Equal(t, 4, got.ProgressBase, "the bar counts the books advanced so far")
	assert.Equal(t, 7, got.ProgressTotal)
	require.NotNil(t, got.PureLabel)
	assert.Nil(t, got.Label, "the label is pure: it reads only page[i]")
	assert.Equal(t, "label book-b", got.PureLabel(1, len(page)))
}
