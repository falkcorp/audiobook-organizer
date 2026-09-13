// file: internal/config/ai_endpoints_test.go
// version: 1.1.0
// guid: 8b2d4e61-0c7f-4a93-b5d8-2f1e6a9c3b74
// last-edited: 2026-09-13

package config

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/aidispatch"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
)

const testLocalURL = "http://127.0.0.1:11434/v1"

// migrateRows runs the real migration and returns the rows it wrote.
func migrateRows(t *testing.T, blob string, seed aiEndpointsSeed) []AIEndpoint {
	t.Helper()
	out, changed := migrateAIEndpointsBlob(blob, seed)
	require.True(t, changed, "migration did not run")
	var got struct {
		AIEndpoints []AIEndpoint `json:"ai_endpoints"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.NotNil(t, got.AIEndpoints)
	return got.AIEndpoints
}

// withMeasuredGPU simulates the PR 3 probe: every row that requires a GPU is
// given the measured "gpu" label. The migration itself never declares it.
func withMeasuredGPU(rows []AIEndpoint) []AIEndpoint {
	out := slices.Clone(rows)
	for i := range out {
		if out[i].RequireGPU {
			out[i].Labels = append(slices.Clone(out[i].Labels), "gpu")
		}
	}
	return out
}

func candidatesFor(t *testing.T, rows []AIEndpoint, c aidispatch.Capability, opts ...aidispatch.Option) ([]string, []aidispatch.Refusal) {
	t.Helper()
	all := append([]aidispatch.Option{aidispatch.WithSlots(aidispatch.NewSlots()), aidispatch.WithHealth(aidispatch.NewHealth())}, opts...)
	eps, refusals, err := aidispatch.New(DispatchEndpoints(rows), all...).Candidates(c)
	require.NoError(t, err)
	ids := make([]string, 0, len(eps))
	for _, e := range eps {
		ids = append(ids, e.ID)
	}
	return ids, refusals
}

func whisperEnv() []WhisperEndpoint {
	var eps []WhisperEndpoint
	for _, port := range []string{"19848", "19849", "19850", "19851"} {
		eps = append(eps, WhisperEndpoint{
			URL: "http://127.0.0.1:" + port, Concurrency: 1, Label: "mac-" + port,
			Priority: 50, RequireGPU: true, Capabilities: []string{"local", "mac"},
		})
	}
	return eps
}

const (
	localUV  = AIEndpointIDLocalUV
	localLLM = AIEndpointIDLocalLLM
	openai   = AIEndpointIDOpenAI
	none     = ""
)

// TestMigrateAIEndpoints_SelectionMatchesToday is the PR 2 contract: for each
// real blob shape, selection over the migrated rows picks, for every
// capability, the endpoint the corresponding site uses today ("" = the site
// cannot run today either, so the capability fails closed).
//
// Known, deliberate divergences (also in the PR description):
//   - filename_parse / author_review / author_discovery have a scan site that
//     follows llm_mode AND a handler site that always calls OpenAI. The rows
//     follow llm_mode, so in local mode with a key, the handler site would move
//     to local once it routes (PR 6).
//   - dedup_review / author_dedup_batch / embed.text_batch in local mode send
//     OpenAI Batch API requests to Ollama today, which fails; afterwards they
//     fail closed with a named refusal.
//   - an empty local_llm_model leaves local-llm without a chat model, so chat
//     capabilities fail closed (today the local path sends "gpt-5-mini" to
//     Ollama, which has no such model).
func TestMigrateAIEndpoints_SelectionMatchesToday(t *testing.T) {
	cases := []struct {
		name string
		blob string
		seed aiEndpointsSeed
		// want maps capability -> first candidate.
		want map[aidispatch.Capability]string
		// second, when set, is the expected second candidate (failover).
		second map[aidispatch.Capability]string
		// requires is WHISPER_REQUIRES. Today it filters only the remote
		// whisper pool (transcribePool), never the local uv path, so it is
		// applied only in the case that has whisper servers.
		requires []string
	}{
		{
			name: "local-only",
			blob: `{"ai_backend":{"llm_mode":"local","embedding_mode":"local","local_base_url":"` + testLocalURL + `",
				"local_llm_model":"qwen2.5:7b","local_embedding_model":"bge-m3"},
				"embedding":{"enabled":true,"base_url":"` + testLocalURL + `","model":"bge-m3"}}`,
			want: map[aidispatch.Capability]string{
				aidispatch.LLMFilenameParse: localLLM, aidispatch.LLMAudiobookParse: none,
				aidispatch.LLMCoverArtVision: none, aidispatch.LLMAuthorReview: localLLM,
				aidispatch.LLMAuthorDiscovery: localLLM, aidispatch.LLMMetadataRerank: localLLM,
				aidispatch.LLMDedupReview: none, aidispatch.LLMAuthorDedupBatch: none,
				aidispatch.LLMDiagnostics: none, aidispatch.EmbedText: localLLM,
				aidispatch.EmbedTextBatch: none, aidispatch.TranscribeBatch: localUV,
				aidispatch.TranscribeIntroClip: localUV,
			},
		},
		{
			name: "openai-only",
			blob: `{"ai_backend":{"llm_mode":"openai","embedding_mode":"openai"},
				"embedding":{"enabled":true,"model":"text-embedding-3-large"}}`,
			seed: aiEndpointsSeed{HasOpenAIKey: true},
			want: map[aidispatch.Capability]string{
				aidispatch.LLMFilenameParse: openai, aidispatch.LLMAudiobookParse: openai,
				aidispatch.LLMCoverArtVision: openai, aidispatch.LLMAuthorReview: openai,
				aidispatch.LLMAuthorDiscovery: openai, aidispatch.LLMMetadataRerank: openai,
				aidispatch.LLMDedupReview: openai, aidispatch.LLMAuthorDedupBatch: openai,
				aidispatch.LLMDiagnostics: openai, aidispatch.EmbedText: openai,
				aidispatch.EmbedTextBatch: openai, aidispatch.TranscribeBatch: localUV,
				// uv first when on PATH; the whisper-1 fallback is ticked on
				// openai but refused by protocol until PR 3.
				aidispatch.TranscribeIntroClip: localUV,
			},
		},
		{
			name: "openai-fallback-local",
			blob: `{"ai_backend":{"llm_mode":"openai-fallback-local","embedding_mode":"local","local_base_url":"` + testLocalURL + `",
				"local_llm_model":"qwen2.5:7b","local_embedding_model":"bge-m3"},
				"embedding":{"enabled":true,"base_url":"` + testLocalURL + `","model":"bge-m3"}}`,
			seed: aiEndpointsSeed{HasOpenAIKey: true},
			want: map[aidispatch.Capability]string{
				aidispatch.LLMFilenameParse: openai, aidispatch.LLMAudiobookParse: openai,
				aidispatch.LLMCoverArtVision: openai, aidispatch.LLMAuthorReview: openai,
				aidispatch.LLMAuthorDiscovery: openai, aidispatch.LLMMetadataRerank: openai,
				aidispatch.LLMDedupReview: openai, aidispatch.LLMAuthorDedupBatch: openai,
				aidispatch.LLMDiagnostics: openai, aidispatch.EmbedText: localLLM,
				aidispatch.EmbedTextBatch: none, aidispatch.TranscribeBatch: localUV,
				aidispatch.TranscribeIntroClip: localUV,
			},
			// parserChain: OpenAI first, then local.
			second: map[aidispatch.Capability]string{
				aidispatch.LLMFilenameParse: localLLM, aidispatch.LLMMetadataRerank: localLLM,
			},
		},
		{
			name: "whisper env plus blob",
			// The blob's own list and remote URL must lose to the env, as
			// applyEnvAuthoritativeConfig makes them lose at runtime.
			blob: `{"whisper_endpoints":[{"url":"http://192.0.2.10:9000","concurrency":2}],
				"whisper_remote_url":"http://192.0.2.11:9000","embedding":{"enabled":false}}`,
			seed: func() aiEndpointsSeed {
				eps := whisperEnv()
				return aiEndpointsSeed{EnvWhisperEndpoints: &eps}
			}(),
			requires: []string{"gpu"},
			want: map[aidispatch.Capability]string{
				aidispatch.LLMFilenameParse: none, aidispatch.EmbedText: none,
				aidispatch.TranscribeBatch: "whisper-19848", aidispatch.TranscribeIntroClip: localUV,
			},
		},
		{
			name: "empty local model fails closed for chat",
			blob: `{"ai_backend":{"llm_mode":"local","embedding_mode":"local","local_base_url":"` + testLocalURL + `"},
				"embedding":{"enabled":true,"base_url":"` + testLocalURL + `","model":"bge-m3"}}`,
			want: map[aidispatch.Capability]string{
				aidispatch.LLMFilenameParse: none, aidispatch.EmbedText: localLLM,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows := withMeasuredGPU(migrateRows(t, tc.blob, tc.seed))
			for c, want := range tc.want {
				got, refusals := candidatesFor(t, rows, c, aidispatch.WithRequiredLabels(aidispatch.TranscribeBatch, tc.requires...))
				first := none
				if len(got) > 0 {
					first = got[0]
				}
				assert.Equal(t, want, first, "%s: candidates %v refusals %v", c.ID(), got, refusals)
				if s, ok := tc.second[c]; ok {
					require.GreaterOrEqual(t, len(got), 2, "%s: no failover candidate", c.ID())
					assert.Equal(t, s, got[1], "%s failover", c.ID())
				}
			}
		})
	}
}

// TestMigrateAIEndpoints_WhisperRows pins the four env servers: every one is a
// candidate (not just the first), the blob's server is gone, and local uv does
// NOT take batch work while servers exist.
func TestMigrateAIEndpoints_WhisperRows(t *testing.T) {
	eps := whisperEnv()
	rows := migrateRows(t, `{"whisper_endpoints":[{"url":"http://192.0.2.10:9000"}]}`, aiEndpointsSeed{EnvWhisperEndpoints: &eps})
	got, _ := candidatesFor(t, withMeasuredGPU(rows), aidispatch.TranscribeBatch, aidispatch.WithRequiredLabels(aidispatch.TranscribeBatch, "gpu"))
	assert.Equal(t, []string{"whisper-19848", "whisper-19849", "whisper-19850", "whisper-19851"}, got)

	for _, r := range rows {
		assert.NotContains(t, r.URL, "192.0.2.10", "blob whisper server survived an env override")
		if strings.HasPrefix(r.ID, "whisper-") {
			assert.Equal(t, 50, r.Priority)
			assert.Equal(t, 1, r.Concurrency)
			assert.True(t, r.RequireGPU)
			assert.Equal(t, []string{"local", "mac"}, r.Labels, "declared labels must not gain gpu")
		}
	}
	// Without the measured label (no probe yet), whisper_requires=gpu leaves
	// no candidates -- which is why the status API says coverage ignores it.
	got, _ = candidatesFor(t, rows, aidispatch.TranscribeBatch, aidispatch.WithRequiredLabels(aidispatch.TranscribeBatch, "gpu"))
	assert.Empty(t, got)
}

func TestMigrateAIEndpoints_RemoteURLAndDuplicates(t *testing.T) {
	rows := migrateRows(t, `{"whisper_remote_url":"http://192.0.2.5"}`, aiEndpointsSeed{})
	got, _ := candidatesFor(t, rows, aidispatch.TranscribeBatch)
	assert.Equal(t, []string{"whisper-1"}, got, "remote URL without a port becomes whisper-1")

	rows = migrateRows(t, `{"whisper_endpoints":[{"url":"http://192.0.2.5:9000"},{"url":"http://192.0.2.5:9000"},{"url":""},{"url":"http://192.0.2.6:9000"}]}`, aiEndpointsSeed{})
	got, _ = candidatesFor(t, rows, aidispatch.TranscribeBatch)
	assert.Equal(t, []string{"whisper-1", "whisper-2"}, got, "duplicate and empty URLs skipped; shared port falls back to position")
}

// TestMigrateAIEndpoints_Idempotent: the second run is a no-op, an operator's
// empty list is preserved, and only null re-migrates. Mutation-checked: with
// the presence guard removed the second run rewrites the rows.
func TestMigrateAIEndpoints_Idempotent(t *testing.T) {
	blob := `{"ai_backend":{"llm_mode":"local","local_base_url":"` + testLocalURL + `","local_llm_model":"m"}}`
	once, changed := migrateAIEndpointsBlob(blob, aiEndpointsSeed{})
	require.True(t, changed)
	twice, changed := migrateAIEndpointsBlob(once, aiEndpointsSeed{HasOpenAIKey: true})
	assert.False(t, changed, "second run must not re-derive (it would add an openai row here)")
	assert.Equal(t, once, twice)

	emptied := `{"ai_endpoints":[],"ai_backend":{"llm_mode":"local","local_base_url":"` + testLocalURL + `"}}`
	out, changed := migrateAIEndpointsBlob(emptied, aiEndpointsSeed{})
	assert.False(t, changed, "an operator's empty list must survive")
	assert.Equal(t, emptied, out)

	_, changed = migrateAIEndpointsBlob(`{"ai_endpoints":null}`, aiEndpointsSeed{})
	assert.True(t, changed, "null is treated as absent")

	// A blob with nothing configured still writes [] (never null), so the
	// migration cannot re-run on every boot.
	out, changed = migrateAIEndpointsBlob(`{}`, aiEndpointsSeed{})
	require.True(t, changed)
	_, changed = migrateAIEndpointsBlob(out, aiEndpointsSeed{})
	assert.False(t, changed)

	_, changed = migrateAIEndpointsBlob(`not json`, aiEndpointsSeed{})
	assert.False(t, changed)
}

// TestMigrateAIEndpoints_KeepsLegacyFields: the migration only adds a key.
func TestMigrateAIEndpoints_KeepsLegacyFields(t *testing.T) {
	blob := `{"ai_backend":{"llm_mode":"local","local_base_url":"` + testLocalURL + `"},"whisper_remote_url":"http://192.0.2.5:9000","parse_batch_size":8}`
	out, changed := migrateAIEndpointsBlob(blob, aiEndpointsSeed{})
	require.True(t, changed)
	var before, after map[string]any
	require.NoError(t, json.Unmarshal([]byte(blob), &before))
	require.NoError(t, json.Unmarshal([]byte(out), &after))
	delete(after, "ai_endpoints")
	assert.Equal(t, before, after)
}

// TestMigrateAIEndpoints_OnlyBaselineTicked: every ID the migration writes,
// across every shape, is in the golden MigrationBaseline. Together with
// TestTick_RefusesNonBaseline this is the "new capability starts with no
// servers" rule.
func TestMigrateAIEndpoints_OnlyBaselineTicked(t *testing.T) {
	eps := whisperEnv()
	blobs := []struct {
		blob string
		seed aiEndpointsSeed
	}{
		{`{"ai_backend":{"llm_mode":"openai-fallback-local","embedding_mode":"openai","local_base_url":"` + testLocalURL + `","local_llm_model":"m"},"embedding":{"enabled":true,"model":"e"}}`, aiEndpointsSeed{HasOpenAIKey: true, EnvWhisperEndpoints: &eps}},
		{`{"ai_backend":{"llm_mode":"local","embedding_mode":"openai-fallback-local","local_base_url":"` + testLocalURL + `"}}`, aiEndpointsSeed{HasOpenAIKey: true}},
	}
	for _, b := range blobs {
		for _, r := range migrateRows(t, b.blob, b.seed) {
			for _, id := range r.Capabilities {
				c, ok := aidispatch.ParseCapability(id)
				require.True(t, ok, "%s ticked unregistered %q", r.ID, id)
				assert.True(t, aidispatch.InMigrationBaseline(c), "%s ticked non-baseline %q", r.ID, id)
			}
		}
	}
}

// TestTick_RefusesNonBaseline is the mutation target for the baseline rule:
// a capability outside MigrationBaseline (the zero value is the only one a
// test outside aidispatch can build) must never be ticked.
func TestTick_RefusesNonBaseline(t *testing.T) {
	row := AIEndpoint{Capabilities: []string{}}
	tick(&row, aidispatch.Capability{})
	assert.Empty(t, row.Capabilities)
	tick(&row, aidispatch.EmbedText)
	tick(&row, aidispatch.EmbedText)
	assert.Equal(t, []string{aidispatch.EmbedText.ID()}, row.Capabilities, "ticks once, dedups")
}

// TestAIEndpoints_BaselineGoldenUnchanged guards that PR 2 did not widen what
// a migration may tick: the baseline is exactly the 13 PR 1 capabilities.
func TestAIEndpoints_BaselineGoldenUnchanged(t *testing.T) {
	var got []string
	for _, c := range aidispatch.MigrationBaseline {
		got = append(got, c.ID())
	}
	assert.Equal(t, []string{
		"llm.filename_parse", "llm.audiobook_parse", "llm.cover_art_vision", "llm.author_review",
		"llm.author_discovery", "llm.metadata_rerank", "llm.dedup_review", "llm.author_dedup_batch",
		"llm.diagnostics", "embed.text", "embed.text_batch", "transcribe.batch", "transcribe.intro_clip",
	}, got)
}

// TestMigrateAIEndpoints_ProdPreview prints what the migration writes on
// prod's shape (run with -v). Not an assertion of prod's exact model names:
// local_llm_model is a placeholder here.
func TestMigrateAIEndpoints_ProdPreview(t *testing.T) {
	blob := `{"ai_backend":{"llm_mode":"local","local_base_url":"` + testLocalURL + `","local_llm_model":"<prod local_llm_model>",
		"local_embedding_model":"bge-m3","parse_batch_size":8,"parse_batch_workers":1},
		"embedding":{"enabled":true,"base_url":"` + testLocalURL + `","model":"bge-m3"}}`
	for _, key := range []bool{false, true} {
		eps := whisperEnv()
		rows := migrateRows(t, blob, aiEndpointsSeed{HasOpenAIKey: key, EnvWhisperEndpoints: &eps})
		pretty, err := json.MarshalIndent(rows, "", "  ")
		require.NoError(t, err)
		t.Logf("prod preview (openai key present=%v):\n%s", key, pretty)
		for _, spec := range aidispatch.Registry() {
			got, _ := candidatesFor(t, withMeasuredGPU(rows), spec.Capability, aidispatch.WithRequiredLabels(aidispatch.TranscribeBatch, "gpu"))
			t.Logf("  %-24s -> %v", spec.Capability.ID(), got)
		}
	}
}

// --- PUT /config validation --------------------------------------------------

func newEndpointsUpdateService(t *testing.T) *UpdateService {
	t.Helper()
	store := mocks.NewMockStore(t)
	store.On("SetSetting", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	store.On("GetSetting", mock.Anything).Return((*database.Setting)(nil), nil).Maybe()
	return NewUpdateService(store)
}

func putJSON(t *testing.T, us *UpdateService, body string) (int, map[string]any) {
	t.Helper()
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &payload))
	return us.UpdateConfig(context.Background(), payload)
}

func keepEndpoints(t *testing.T) {
	t.Helper()
	orig := Snapshot().AIEndpoints
	t.Cleanup(func() { Mutate(func(c *Config) { c.AIEndpoints = orig }) })
}

const validRow = `{"id":"box","protocol":"openai_compat","url":"http://192.0.2.7:11434/v1","chat_model":"m",
	"concurrency":2,"enabled":true,"capabilities":["llm.filename_parse"],"host_roots":{"root-a":"/mnt/a"}}`

func TestUpdateConfig_AIEndpointsValid(t *testing.T) {
	keepEndpoints(t)
	us := newEndpointsUpdateService(t)
	status, resp := putJSON(t, us, `{"ai_endpoints":[`+validRow+`,
		{"id":"cloud","protocol":"openai_compat","url":"https://api.openai.com/v1","auth_ref":"openai_api_key","capabilities":[]}]}`)
	require.Equal(t, http.StatusOK, status, "resp %v", resp)
	got := Snapshot().AIEndpoints
	require.Len(t, got, 2)
	assert.Equal(t, map[string]string{"root-a": "/mnt/a"}, got[0].HostRoots)
}

// TestUpdateConfig_AIEndpointsRejected: every rule answers 400 and saves nothing.
func TestUpdateConfig_AIEndpointsRejected(t *testing.T) {
	cases := map[string]struct{ body, want string }{
		"nested typo":              {`{"ai_endpoints":[{"id":"a","protocol":"openai_compat","url":"http://192.0.2.1/v1","capabilites":["llm.filename_parse"]}]}`, "capabilites"},
		"unknown capability":       {`{"ai_endpoints":[{"id":"a","protocol":"openai_compat","url":"http://192.0.2.1/v1","capabilities":["llm.not_a_thing"]}]}`, `unknown capability "llm.not_a_thing"`},
		"wildcard":                 {`{"ai_endpoints":[{"id":"a","protocol":"openai_compat","url":"http://192.0.2.1/v1","capabilities":["llm.*"]}]}`, "wildcard"},
		"unknown capability model": {`{"ai_endpoints":[{"id":"a","protocol":"openai_compat","url":"http://192.0.2.1/v1","capability_models":{"llm.nope":"m"}}]}`, "capability_models"},
		"duplicate id":             {`{"ai_endpoints":[{"id":"a","protocol":"openai_compat","url":"http://192.0.2.1/v1"},{"id":"a","protocol":"openai_compat","url":"http://192.0.2.2/v1"}]}`, "duplicate id"},
		"duplicate protocol+url":   {`{"ai_endpoints":[{"id":"a","protocol":"openai_compat","url":"http://192.0.2.1/v1"},{"id":"b","protocol":"openai_compat","url":"HTTP://192.0.2.1/v1/"}]}`, "already used"},
		"negative concurrency":     {`{"ai_endpoints":[{"id":"a","protocol":"openai_compat","url":"http://192.0.2.1/v1","concurrency":-1}]}`, "concurrency"},
		"unknown auth_ref":         {`{"ai_endpoints":[{"id":"a","protocol":"openai_compat","url":"http://192.0.2.1/v1","auth_ref":"hardcover_token"}]}`, "auth_ref"},
		"credentials in url":       {`{"ai_endpoints":[{"id":"a","protocol":"openai_compat","url":"http://u:p@192.0.2.1/v1"}]}`, "credentials"},
		"unknown protocol":         {`{"ai_endpoints":[{"id":"a","protocol":"grpc","url":"http://192.0.2.1"}]}`, "unknown protocol"},
		"missing url":              {`{"ai_endpoints":[{"id":"a","protocol":"whisper_server"}]}`, "url is required"},
		"missing id":               {`{"ai_endpoints":[{"protocol":"local_process"}]}`, "id is required"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			keepEndpoints(t)
			sentinel := []AIEndpoint{{ID: "sentinel", Protocol: "local_process", Capabilities: []string{}}}
			Mutate(func(c *Config) { c.AIEndpoints = sentinel })
			us := newEndpointsUpdateService(t)
			status, resp := putJSON(t, us, tc.body)
			require.Equal(t, http.StatusBadRequest, status, "resp %v", resp)
			// %v, not JSON: the error text contains quotes JSON would escape.
			assert.Contains(t, fmt.Sprintf("%v", resp), tc.want)
			assert.Equal(t, sentinel, Snapshot().AIEndpoints, "rejected PUT changed ai_endpoints")
		})
	}
}

// TestUpdateConfig_StoredUnknownCapabilityKept: a row a newer binary stored
// with an ID this one does not register is kept and does not block an
// unrelated save; selection ignores the ID.
func TestUpdateConfig_StoredUnknownCapabilityKept(t *testing.T) {
	keepEndpoints(t)
	stored := []AIEndpoint{{ID: "future", Protocol: "openai_compat", URL: "http://192.0.2.9/v1", ChatModel: "m",
		Enabled: true, Capabilities: []string{"llm.from_the_future", "llm.filename_parse"}}}
	Mutate(func(c *Config) { c.AIEndpoints = stored })
	origBatch := Snapshot().AIBackend.ParseBatchSize
	t.Cleanup(func() { Mutate(func(c *Config) { c.AIBackend.ParseBatchSize = origBatch }) })

	status, resp := putJSON(t, newEndpointsUpdateService(t), `{"ai_backend":{"parse_batch_size":7}}`)
	require.Equal(t, http.StatusOK, status, "resp %v", resp)
	assert.Equal(t, stored, Snapshot().AIEndpoints)
	assert.Equal(t, []string{"llm.from_the_future"}, stored[0].UnknownCapabilities())
	got, _ := candidatesFor(t, stored, aidispatch.LLMFilenameParse)
	assert.Equal(t, []string{"future"}, got)
}

// TestUpdateConfig_AIEndpointsReplaceNotMerge: a PUT row does not inherit
// fields the stored row at the same index had.
func TestUpdateConfig_AIEndpointsReplaceNotMerge(t *testing.T) {
	keepEndpoints(t)
	Mutate(func(c *Config) {
		c.AIEndpoints = []AIEndpoint{{ID: "box", Protocol: "openai_compat", URL: "http://192.0.2.7/v1",
			HostRoots: map[string]string{"old": "/x"}, CapabilityModels: map[string]string{"llm.filename_parse": "old"}}}
	})
	status, resp := putJSON(t, newEndpointsUpdateService(t), `{"ai_endpoints":[{"id":"box","protocol":"openai_compat","url":"http://192.0.2.7/v1"}]}`)
	require.Equal(t, http.StatusOK, status, "resp %v", resp)
	got := Snapshot().AIEndpoints
	require.Len(t, got, 1)
	assert.Nil(t, got[0].HostRoots)
	assert.Nil(t, got[0].CapabilityModels)
}

// TestMaskSecrets_AIEndpointAuthRef: GET masks auth_ref without touching the
// live config, and PUTting the masked value back resolves to the real name.
func TestMaskSecrets_AIEndpointAuthRef(t *testing.T) {
	keepEndpoints(t)
	us := newEndpointsUpdateService(t)
	live := []AIEndpoint{{ID: "cloud", Protocol: "openai_compat", URL: "https://api.openai.com/v1",
		AuthRef: "openai_api_key", Capabilities: []string{"llm.diagnostics"}, HostRoots: map[string]string{"r": "/a"}}}
	Mutate(func(c *Config) { c.AIEndpoints = live })

	masked := us.MaskSecrets(Snapshot())
	require.Len(t, masked.AIEndpoints, 1)
	assert.Equal(t, database.MaskSecret("openai_api_key"), masked.AIEndpoints[0].AuthRef)
	assert.NotEqual(t, "openai_api_key", masked.AIEndpoints[0].AuthRef)
	masked.AIEndpoints[0].Capabilities[0] = "mutated"
	masked.AIEndpoints[0].HostRoots["r"] = "mutated"
	snap := Snapshot()
	assert.Equal(t, "openai_api_key", snap.AIEndpoints[0].AuthRef, "masking reached the live config")
	assert.Equal(t, "llm.diagnostics", snap.AIEndpoints[0].Capabilities[0])
	assert.Equal(t, "/a", snap.AIEndpoints[0].HostRoots["r"])

	// Round trip: PUT the masked rows back.
	body, err := json.Marshal(map[string]any{"ai_endpoints": us.MaskSecrets(Snapshot()).AIEndpoints})
	require.NoError(t, err)
	status, resp := putJSON(t, us, string(body))
	require.Equal(t, http.StatusOK, status, "resp %v", resp)
	assert.Equal(t, "openai_api_key", Snapshot().AIEndpoints[0].AuthRef)
}

// TestAIEndpointsSeedForLoad covers the load-time seed: a stored key row and
// the env whisper overrides must reach the migration, because the key is never
// in the blob and ApplyEnvAuthoritativeConfig runs after the migration chain.
func TestAIEndpointsSeedForLoad(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	seed := aiEndpointsSeedForLoad(map[string]*database.Setting{})
	assert.Nil(t, seed.EnvWhisperEndpoints, "no env → no whisper override")
	assert.Nil(t, seed.EnvWhisperRemoteURL)

	viper.Set("whisper_endpoints", `[{"url":"http://192.0.2.10:19848"},{"url":"http://192.0.2.10:19849"},`+
		`{"url":"http://192.0.2.10:19850"},{"url":"http://192.0.2.10:19851"}]`)
	viper.Set("whisper_remote_url", "http://192.0.2.11:9000")
	seed = aiEndpointsSeedForLoad(map[string]*database.Setting{
		"openai_api_key": {Key: "openai_api_key", Value: "encrypted-blob", IsSecret: true},
	})
	assert.True(t, seed.HasOpenAIKey, "a stored key row counts even when the live config has no key")
	require.NotNil(t, seed.EnvWhisperEndpoints)
	assert.Len(t, *seed.EnvWhisperEndpoints, 4)
	require.NotNil(t, seed.EnvWhisperRemoteURL)
	assert.Equal(t, "http://192.0.2.11:9000", *seed.EnvWhisperRemoteURL)
	require.NotNil(t, seed.Base)

	seed = aiEndpointsSeedForLoad(map[string]*database.Setting{
		"openai_api_key": {Key: "openai_api_key", Value: ""},
	})
	assert.Equal(t, Snapshot().OpenAIAPIKey != "", seed.HasOpenAIKey, "an empty key row does not count")
}
