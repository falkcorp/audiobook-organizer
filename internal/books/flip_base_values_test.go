// file: internal/books/flip_base_values_test.go
// version: 1.0.0
// guid: 0b6d4e92-7a13-4c58-b9e0-3f81c5a2d746
// last-edited: 2026-11-20

package books

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// TestAppConfigBaseValuesSurvive: the dropped HEAD fields a flipped Config keeps
// a value for (cutover's droppedWithValue, "base" entries) hold it on a real
// flipped boot. Readers of them remain, so a zero here would be a silent change
// (an empty database type, a zero-hour auto-update window, an unnamed model).
func TestAppConfigBaseValuesSurvive(t *testing.T) {
	bootFlippedFresh(t)
	c := config.Snapshot()
	require.Equal(t, "pebble", c.DatabaseType)
	require.Equal(t, 2, c.AutoUpdate.WindowStart)
	require.Equal(t, 5, c.AutoUpdate.WindowEnd)
	require.NotEmpty(t, c.AIBackend.LocalEmbeddingModel)
	require.NotEmpty(t, c.AIBackend.LocalLLMModel)
	// The routed path (05-ai.md:803): the pool source the call sites read says on.
	require.True(t, c.AIEndpointsRouting)
	require.True(t, ai.ConfigPool().Active())
}
