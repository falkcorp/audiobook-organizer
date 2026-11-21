// file: internal/books/cutover/ai_continuity_test.go
// version: 2.0.0
// guid: 8b14c6e2-47d9-4a30-9e5f-62d1a0c73b58
// last-edited: 2026-11-20

package cutover

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// continuityTree decodes blob the way the planner does (HEAD's transforms, then
// the tree) and runs aiRoutingContinuity over it, returning the rows after the
// merge by id and the report.
func continuityTree(t *testing.T, blob string, hasKey bool) (map[string]map[string]any, []string) {
	t.Helper()
	b, err := headDecodeBlob([]byte(blob), headSeed{HasOpenAIKey: hasKey}, headMarkers{})
	require.NoError(t, err)
	tree, err := decodeTree(b.Fields)
	require.NoError(t, err)
	report := aiRoutingContinuity(tree, hasKey)
	return rowsByID(tree), report
}

// openAILine is the report line about the stored openai row.
func openAILine(t *testing.T, report []string) string {
	t.Helper()
	for _, l := range report {
		if strings.Contains(l, "served openai beyond") {
			return l
		}
	}
	require.Fail(t, "no report line about the openai row", "%v", report)
	return ""
}

func rowsByID(tree map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	list, _ := tree["ai_endpoints"].([]any)
	for _, r := range list {
		m := r.(map[string]any)
		out[m["id"].(string)] = m
	}
	return out
}

func capsOf(row map[string]any) []string { return stringList(row["capabilities"]) }

const whisperOnly = `"ai_endpoints":[{"id":"whisper-1","label":"GPU box","protocol":"whisper_server","url":"http://192.0.2.7:9000","priority":1,"concurrency":1,"enabled":true,"capabilities":["transcribe.batch"]}]`

const fallbackLocal = `"enable_ai_parsing":true,"ai_backend":{"llm_mode":"openai-fallback-local","local_base_url":"http://192.0.2.9:11434/v1","local_llm_model":"synthetic-model"}`

// A stored list with no OpenAI row, routing off, a key, parsing on and the
// openai-fallback-local chain: the legacy path served filename parsing from the
// key first, so the flip adds the row the seed derives with that tick and no
// other (cover vision was dark with the switch off, so it stays dark).
func TestAIRoutingContinuity_AddsTheOpenAIRowTheLegacyPathServed(t *testing.T) {
	rows, report := continuityTree(t, `{`+whisperOnly+`,`+fallbackLocal+`}`, true)
	oa, ok := rows[headAIEndpointIDOpenAI]
	require.True(t, ok, "no openai row was added: %v", rows)
	assert.Equal(t, true, oa["enabled"])
	assert.Equal(t, "openai_api_key", oa["auth_ref"])
	assert.Equal(t, []string{headCapLLMFilenameParse}, capsOf(oa), "a tick beyond what the legacy path served was granted")
	assert.Contains(t, rows, "whisper-1", "the stored row went")
	assert.Contains(t, strings.Join(report, "\n"), "added it")
	assert.NotContains(t, strings.Join(report, "\n"), "192.0.2.7", "the report names a stored value")
}

// The rows the continuity adds are the rows the seed derives, models, URLs,
// priorities and all: a list HEAD seeded and a list the cutover completed are
// the same rows, restricted to the ticks the legacy path served.
func TestAIRoutingContinuity_AddedRowsEqualTheSeedersRows(t *testing.T) {
	const base = `"enable_ai_parsing":true,"openai_api_key":"synthetic-key","filename_parse_model":"synthetic-parse",` +
		`"ai_backend":{"llm_mode":"openai-fallback-local","embedding_mode":"local","local_base_url":"http://192.0.2.9:11434/v1","local_llm_model":"synthetic-chat","local_embedding_model":"synthetic-embed"}`
	seeded, err := headDecodeBlob([]byte(`{`+base+`}`), headSeed{HasOpenAIKey: true}, headMarkers{})
	require.NoError(t, err)
	var want []headAIEndpoint
	require.NoError(t, json.Unmarshal(seeded.Fields["ai_endpoints"], &want))

	rows, _ := continuityTree(t, `{`+whisperOnly+`,`+base+`}`, true)
	for _, w := range want {
		if w.ID != headAIEndpointIDLocalLLM && w.ID != headAIEndpointIDOpenAI {
			continue
		}
		got, ok := rows[w.ID]
		require.True(t, ok, "%s was not added", w.ID)
		enc, _ := json.Marshal(got)
		var g headAIEndpoint
		require.NoError(t, json.Unmarshal(enc, &g))
		served := []string{headCapLLMFilenameParse, headCapEmbedText}
		w.Capabilities = slices.DeleteFunc(slices.Clone(w.Capabilities), func(c string) bool { return !slices.Contains(served, c) })
		slices.Sort(w.Capabilities)
		slices.Sort(g.Capabilities)
		assert.Equal(t, w, g, "%s differs from the seeder's row", w.ID)
	}
}

// A stored OpenAI row that is disabled with only other ticks the legacy path
// did not serve (cover vision here): ticking filename parsing is fine, but
// enabling the row would start the vision calls HEAD never made, so it stays
// disabled and the report says why.
func TestAIRoutingContinuity_DoesNotEnableARowThatWouldStartMore(t *testing.T) {
	blob := `{"ai_endpoints":[{"id":"openai","label":"mine","protocol":"openai_compat","url":"https://llm.example.test/v1","chat_model":"x","priority":7,"concurrency":2,"enabled":false,"capabilities":["llm.cover_art_vision"],"auth_ref":"openai_api_key"}],` + fallbackLocal + `}`
	rows, report := continuityTree(t, blob, true)
	oa := rows[headAIEndpointIDOpenAI]
	assert.Equal(t, false, oa["enabled"], "enabling the row would have started cover vision")
	assert.Contains(t, capsOf(oa), headCapLLMFilenameParse)
	assert.Contains(t, capsOf(oa), "llm.cover_art_vision", "an existing tick went")
	assert.Equal(t, "https://llm.example.test/v1", oa["url"])
	assert.EqualValues(t, "7", oa["priority"].(json.Number).String())
	line := openAILine(t, report)
	assert.Contains(t, line, "stays disabled")
	assert.Contains(t, line, "llm.cover_art_vision")
}

// A disabled stored row whose every tick is something the legacy path served is
// enabled: nothing else can start.
func TestAIRoutingContinuity_EnablesARowThatStartsNothingElse(t *testing.T) {
	blob := `{"ai_endpoints":[{"id":"openai","label":"mine","protocol":"openai_compat","url":"https://llm.example.test/v1","chat_model":"x","priority":7,"concurrency":2,"enabled":false,"capabilities":[],"auth_ref":"openai_api_key","note_from_a_newer_build":"kept"}],` + fallbackLocal + `}`
	rows, report := continuityTree(t, blob, true)
	oa := rows[headAIEndpointIDOpenAI]
	assert.Equal(t, true, oa["enabled"])
	assert.Equal(t, []string{headCapLLMFilenameParse}, capsOf(oa))
	assert.Equal(t, "kept", oa["note_from_a_newer_build"], "a field this build does not know was dropped")
	assert.Contains(t, openAILine(t, report), "enabled the row")
}

// Features dark under HEAD stay dark: explicit openai mode, parsing off, a
// disabled mode and openai embeddings were never routed, so no row is added or
// ticked for them.
func TestAIRoutingContinuity_DarkFeaturesStayDark(t *testing.T) {
	for name, blob := range map[string]string{
		"explicit openai mode":  `{` + whisperOnly + `,"enable_ai_parsing":true,"ai_backend":{"llm_mode":"openai"}}`,
		"parsing off":           `{` + whisperOnly + `,"enable_ai_parsing":false,"ai_backend":{"llm_mode":"local","local_base_url":"http://192.0.2.9:11434/v1"}}`,
		"disabled mode":         `{` + whisperOnly + `,"enable_ai_parsing":true,"ai_backend":{"llm_mode":"disabled"}}`,
		"openai embedding mode": `{` + whisperOnly + `,"enable_ai_parsing":false,"ai_backend":{"llm_mode":"disabled","embedding_mode":"openai"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			rows, report := continuityTree(t, blob, true)
			assert.Empty(t, report)
			assert.Len(t, rows, 1, "a row appeared: %v", rows)
		})
	}
}

// With routing on in HEAD the stored rows were the truth: nothing moves.
func TestAIRoutingContinuity_RoutingOnTouchesNothing(t *testing.T) {
	rows, report := continuityTree(t, `{`+whisperOnly+`,"ai_endpoints_routing":true,`+fallbackLocal+`}`, true)
	assert.Empty(t, report)
	assert.Len(t, rows, 1)
}

// An install HEAD seeded itself derives the same rows: the merge changes
// nothing, and a second pass is a no-op.
func TestAIRoutingContinuity_SeededListIsAFixedPoint(t *testing.T) {
	blob := `{` + fallbackLocal + `}`
	b, err := headDecodeBlob([]byte(blob), headSeed{HasOpenAIKey: true}, headMarkers{})
	require.NoError(t, err)
	tree, err := decodeTree(b.Fields)
	require.NoError(t, err)
	before, _ := json.Marshal(tree["ai_endpoints"])
	assert.Empty(t, aiRoutingContinuity(tree, true), "the seed's own rows were changed")
	after, _ := json.Marshal(tree["ai_endpoints"])
	assert.True(t, bytes.Equal(before, after))
	assert.Empty(t, aiRoutingContinuity(tree, true))
}

// A local-LLM install: llm_mode local with a base URL and no local row stored
// gets the row and the chat tick the legacy path used, and embeddings when the
// embedding mode is local.
func TestAIRoutingContinuity_LocalModeAddsTheLocalRow(t *testing.T) {
	blob := `{` + whisperOnly + `,"enable_ai_parsing":true,"ai_backend":{"llm_mode":"local","embedding_mode":"local","local_base_url":"http://192.0.2.9:11434/v1"}}`
	rows, report := continuityTree(t, blob, false)
	l, ok := rows[headAIEndpointIDLocalLLM]
	require.True(t, ok, "%v", rows)
	caps := capsOf(l)
	slices.Sort(caps)
	assert.Equal(t, []string{headCapEmbedText, headCapLLMFilenameParse}, caps)
	assert.NotContains(t, rows, headAIEndpointIDOpenAI, "no key: no OpenAI row")
	require.Len(t, report, 1)
	assert.False(t, slices.ContainsFunc(report, func(s string) bool { return strings.Contains(s, "192.0.2.9") }), "the report names a URL")
}
