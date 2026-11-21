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
// ai_endpoints_routing on: an install HEAD ran with routing OFF in the
// openai-fallback-local chain (an OpenAI key, a local backend, local
// embeddings, enable_ai_parsing on) and a stored ai_endpoints list with no
// OpenAI or local row (one whisper server only: HEAD seeds the rows only when
// the list is absent) still has every feature HEAD's legacy path served
// answered after the flip: the scanner's filename parse (cloud first, then
// local, as the chain ran) and local embeddings. Cover vision was dark in HEAD
// with the switch off (the cover reader refused to run), so it must STAY dark:
// the flip must not start paid vision calls. The cell asks the router (the
// dispatcher over the flipped Config's rows), not the rows' JSON.
func TestConfigCutover_AIRoutingContinuity(t *testing.T) {
	const blob = `{"enable_ai_parsing":true,"openai_api_key":"synthetic-continuity-key",` +
		`"ai_backend":{"llm_mode":"openai-fallback-local","embedding_mode":"local",` +
		`"local_base_url":"http://192.0.2.9:11434/v1","local_llm_model":"synthetic-chat","local_embedding_model":"synthetic-embed"},` +
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

	// Served by HEAD's legacy path, answered after the flip, in the chain's order.
	eps, _, err := d.Candidates(aidispatch.LLMFilenameParse)
	require.NoError(t, err)
	require.Len(t, eps, 2, "filename parse lost an endpoint of the chain the legacy path ran")
	assert.Equal(t, "openai_api_key", eps[0].AuthRef, "the chain tries the cloud first")
	assert.Equal(t, "http://192.0.2.9:11434/v1", eps[1].URL)

	eps, _, err = d.Candidates(aidispatch.EmbedText)
	require.NoError(t, err)
	require.NotEmpty(t, eps, "local embeddings have no endpoint after the flip")
	assert.Equal(t, "http://192.0.2.9:11434/v1", eps[0].URL)

	// Dark in HEAD with the switch off: dark after the flip.
	eps, _, err = d.Candidates(aidispatch.LLMCoverArtVision)
	require.NoError(t, err)
	assert.Empty(t, eps, "cover vision was not served by HEAD with routing off; the flip started it")

	// The whisper row HEAD stored is still there, untouched.
	var whisper bool
	for _, r := range flipped.AIEndpoints {
		whisper = whisper || (r.ID == "whisper-1" && r.URL == "http://192.0.2.7:9000")
	}
	assert.True(t, whisper, "the stored whisper row changed")
}
