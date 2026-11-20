// file: internal/books/cutover/ai_continuity_test.go
// version: 1.0.0
// guid: 8b14c6e2-47d9-4a30-9e5f-62d1a0c73b58
// last-edited: 2026-11-20

package cutover

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// continuityRows decodes blob the way the planner does and returns the rows
// after aiRoutingContinuity, with its report.
func continuityRows(t *testing.T, blob string, hasKey bool) (map[string]headAIEndpoint, []string) {
	t.Helper()
	b, err := headDecodeBlob([]byte(blob), headSeed{HasOpenAIKey: hasKey}, headMarkers{})
	require.NoError(t, err)
	report := aiRoutingContinuity(&b, hasKey)
	var rows []headAIEndpoint
	require.NoError(t, json.Unmarshal(b.Fields["ai_endpoints"], &rows))
	out := map[string]headAIEndpoint{}
	for _, r := range rows {
		out[r.ID] = r
	}
	return out, report
}

const whisperOnly = `"ai_endpoints":[{"id":"whisper-1","label":"GPU box","protocol":"whisper_server","url":"http://192.0.2.7:9000","priority":1,"concurrency":1,"enabled":true,"capabilities":["transcribe.batch"]}]`

// A stored list with no OpenAI row, routing off, a key and parsing on: the
// legacy path served filename parsing from the key, so the flip adds the row
// the seed would have derived, with the same ticks.
func TestAIRoutingContinuity_AddsTheOpenAIRowTheLegacyPathServed(t *testing.T) {
	rows, report := continuityRows(t, `{`+whisperOnly+`,"enable_ai_parsing":true,"ai_backend":{"llm_mode":"openai"}}`, true)
	oa, ok := rows[headAIEndpointIDOpenAI]
	require.True(t, ok, "no openai row was added: %v", rows)
	assert.True(t, oa.Enabled)
	assert.Equal(t, "openai_api_key", oa.AuthRef)
	assert.Contains(t, oa.Capabilities, headCapLLMFilenameParse)
	assert.Contains(t, rows, "whisper-1", "the stored row went")
	require.Len(t, report, 1)
	assert.Contains(t, report[0], "added it")
	assert.NotContains(t, strings.Join(report, "\n"), "192.0.2.7", "the report names a stored value")
}

// A stored OpenAI row that ticks nothing, or is disabled: the legacy path
// ignored both while routing was off, so the flip ticks and enables it, and
// changes nothing else on the row.
func TestAIRoutingContinuity_TicksAndEnablesAStoredRow(t *testing.T) {
	blob := `{"ai_endpoints":[{"id":"openai","label":"mine","protocol":"openai_compat","url":"https://llm.example.test/v1","chat_model":"x","priority":7,"concurrency":2,"enabled":false,"capabilities":["embed.text"],"auth_ref":"openai_api_key"}],"enable_ai_parsing":true}`
	rows, report := continuityRows(t, blob, true)
	oa := rows[headAIEndpointIDOpenAI]
	assert.True(t, oa.Enabled)
	assert.Contains(t, oa.Capabilities, "embed.text", "an existing tick went")
	assert.Contains(t, oa.Capabilities, headCapLLMFilenameParse)
	assert.Equal(t, "https://llm.example.test/v1", oa.URL)
	assert.Equal(t, 7, oa.Priority)
	assert.Equal(t, "mine", oa.Label)
	require.Len(t, report, 1)
	assert.Contains(t, report[0], "enabled the row")
}

// With routing on in HEAD the stored rows were the truth: nothing moves.
func TestAIRoutingContinuity_RoutingOnTouchesNothing(t *testing.T) {
	blob := `{` + whisperOnly + `,"ai_endpoints_routing":true,"enable_ai_parsing":true}`
	rows, report := continuityRows(t, blob, true)
	assert.Empty(t, report)
	assert.Len(t, rows, 1)
}

// An install HEAD seeded itself derives the same rows: the merge changes
// nothing, so the report is empty and a second pass is a no-op.
func TestAIRoutingContinuity_SeededListIsAFixedPoint(t *testing.T) {
	rows, report := continuityRows(t, `{"enable_ai_parsing":true}`, true)
	assert.Empty(t, report, "the seed's own rows were changed")
	require.Contains(t, rows, headAIEndpointIDOpenAI)
	b, err := headDecodeBlob([]byte(`{"enable_ai_parsing":true}`), headSeed{HasOpenAIKey: true}, headMarkers{})
	require.NoError(t, err)
	assert.Empty(t, aiRoutingContinuity(&b, true))
	assert.Empty(t, aiRoutingContinuity(&b, true))
}

// A local-LLM install: llm_mode local with a base URL and no local row stored
// gets the row and the chat ticks the legacy path used.
func TestAIRoutingContinuity_LocalModeAddsTheLocalRow(t *testing.T) {
	blob := `{` + whisperOnly + `,"ai_backend":{"llm_mode":"local","local_base_url":"http://192.0.2.9:11434/v1"}}`
	rows, report := continuityRows(t, blob, false)
	l, ok := rows[headAIEndpointIDLocalLLM]
	require.True(t, ok, "%v", rows)
	assert.Contains(t, l.Capabilities, headCapLLMFilenameParse)
	assert.NotContains(t, rows, headAIEndpointIDOpenAI, "no key: no OpenAI row")
	require.Len(t, report, 1)
	assert.False(t, slices.ContainsFunc(report, func(s string) bool { return strings.Contains(s, "192.0.2.9") }), "the report names a URL")
}
