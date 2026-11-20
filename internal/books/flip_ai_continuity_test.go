// file: internal/books/flip_ai_continuity_test.go
// version: 1.0.0
// guid: 2e9a47d1-b6c3-4f08-8d15-7a30c9e41f62
// last-edited: 2026-11-20

package books

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/aidispatch"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// TestConfigCutover_AIRoutingContinuity is the oracle cell for the flip forcing
// ai_endpoints_routing on: an install HEAD ran with routing OFF, an OpenAI key,
// enable_ai_parsing on and a stored ai_endpoints list with no OpenAI row (one
// whisper server only: HEAD seeds the rows only when the list is absent) still
// has every feature HEAD's legacy path served answered after the flip. In HEAD
// filename parsing in local and openai-fallback-local modes, local embeddings
// and cover vision went to the key and ai_backend.* directly; after the flip
// they reach an endpoint only through a row that ticks them, so the cutover must
// leave one. The cell asks the router (the dispatcher over the flipped Config's
// rows, with the flipped Config's own secret), not the rows' JSON.
func TestConfigCutover_AIRoutingContinuity(t *testing.T) {
	const blob = `{"enable_ai_parsing":true,"openai_api_key":"synthetic-continuity-key",` +
		`"ai_endpoints":[{"id":"whisper-1","label":"GPU box","protocol":"whisper_server","url":"http://192.0.2.7:9000",` +
		`"priority":1,"concurrency":1,"enabled":true,"capabilities":["transcribe.batch"]}]}`
	fx := newFlipFixture(t, func(s *database.PebbleStore) {
		require.NoError(t, s.SetSetting("config_blob", blob, "json", false))
	}, "")
	var flipped config.Config
	fx.runFlipped(t, func(*composed) { flipped = config.Snapshot() })

	require.True(t, flipped.AIEndpointsRouting, "routing is always on after the flip")
	require.NotEmpty(t, flipped.OpenAIAPIKey, "the key did not survive the flip")

	d := aidispatch.New(config.DispatchEndpoints(flipped.AIEndpoints))
	// The sites HEAD's legacy path served from the key with routing off, as the
	// seed's own derivation ticks them for a key holder.
	for name, c := range map[string]aidispatch.Capability{
		"scanner filename parse": aidispatch.LLMFilenameParse,
		"cover vision":           aidispatch.LLMCoverArtVision,
	} {
		eps, _, err := d.Candidates(c)
		require.NoError(t, err, name)
		require.NotEmpty(t, eps, "%s has no endpoint after the flip: the legacy path's feature went dark", name)
		assert.Equal(t, "openai_api_key", eps[0].AuthRef, name)
	}
	// The whisper row HEAD stored is still there, untouched.
	var whisper bool
	for _, r := range flipped.AIEndpoints {
		whisper = whisper || (r.ID == "whisper-1" && r.URL == "http://192.0.2.7:9000")
	}
	assert.True(t, whisper, "the stored whisper row changed")
}
