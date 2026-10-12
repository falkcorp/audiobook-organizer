// file: internal/server/handlers/audiobooks/export_test.go
// version: 1.0.0
// guid: 0b6c7d1e-4f92-4a38-8e15-d2a9c3b7f640
// last-edited: 2026-10-11

package audiobookshandler

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/fileops"
)

// SwapWriteMetadataBack replaces the file write-back of PUT /audiobooks/:id
// and returns a func that restores it.
func SwapWriteMetadataBack(fn func(ctx context.Context, path string, md map[string]any, cfg fileops.OperationConfig) error) (restore func()) {
	old := writeMetadataBack
	writeMetadataBack = fn
	return func() { writeMetadataBack = old }
}

// WriteBackWarning exposes writeBackWarning.
func WriteBackWarning(err error) string { return writeBackWarning(err) }
