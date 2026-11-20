// file: internal/books/cutover/rename_aibackend_test.go
// version: 1.0.0
// guid: 0F2D9E5B-7C41-4A38-B6D2-3E91A8C47F10
// last-edited: 2026-11-19

package cutover

import (
	"bytes"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Fixture URLs are TEST-NET-1 addresses; the models are Batman names.
const (
	aiURL1 = "http://192.0.2.10:11434/v1"
	aiURL2 = "http://192.0.2.11:11434/v1"
	aiURL3 = "http://192.0.2.12:8080/v1"
)

// aiVals is a values map of JSON strings, from name/value pairs.
func aiVals(kv ...string) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for i := 0; i < len(kv); i += 2 {
		out[kv[i]] = mustJSON(kv[i+1])
	}
	return out
}

// aiCfg is the part of HEAD's config the seed reads for these names.
type aiCfg struct {
	url, llm, emb, chat, embed string
	key                        bool
}

var aiDefChat, aiDefEmbed = aiDefaultModels()

// aiSeed is the list HEAD's seed builds for c, through the frozen
// headBuildMigratedAIEndpoints.
func aiSeed(t testing.TB, c aiCfg) json.RawMessage {
	t.Helper()
	cfg, _ := headDefaultConfig()
	cfg.AIBackend.LocalBaseURL = c.url
	cfg.AIBackend.LLMMode = c.llm
	cfg.AIBackend.EmbeddingMode = c.emb
	cfg.AIBackend.LocalLLMModel = c.chat
	cfg.AIBackend.LocalEmbeddingModel = c.embed
	if c.key {
		cfg.OpenAIAPIKey = "present"
	}
	b, err := json.Marshal(headBuildMigratedAIEndpoints(cfg, c.key))
	require.NoError(t, err)
	return b
}

// aiNorm decodes a list with every capabilities list sorted, so two lists that
// differ only in tick order compare equal (a tick order means nothing).
func aiNorm(t testing.TB, raw json.RawMessage) []any {
	t.Helper()
	var rows []any
	require.NoError(t, json.Unmarshal(raw, &rows))
	for _, r := range rows {
		m := r.(map[string]any)
		if caps, ok := m["capabilities"].([]any); ok {
			sort.Slice(caps, func(i, j int) bool { return caps[i].(string) < caps[j].(string) })
		}
	}
	return rows
}

func aiRow(t testing.TB, list json.RawMessage, id string) map[string]any {
	t.Helper()
	for _, r := range aiNorm(t, list) {
		if m := r.(map[string]any); m["id"] == id {
			return m
		}
	}
	return nil
}

func aiCaps(row map[string]any) []string {
	var out []string
	for _, c := range row["capabilities"].([]any) {
		out = append(out, c.(string))
	}
	return out
}

func aiStr(t testing.TB, m map[string]json.RawMessage, k string) string {
	t.Helper()
	var s string
	require.NoError(t, json.Unmarshal(m[k], &s))
	return s
}

var aiModes = []string{headAIBackendModeDisabled, headAIBackendModeLocal, headAIBackendModeOpenAI, headAIBackendModeOpenAIFallbackLocal}

// ---- Unfold ----

func TestAIBackend_UnfoldEmptyIsHEADDefaults(t *testing.T) {
	for _, in := range []json.RawMessage{nil, json.RawMessage(``), json.RawMessage(`null`), json.RawMessage(`[]`), json.RawMessage(" [ ] ")} {
		got, err := UnfoldAIBackend(in)
		require.NoError(t, err, string(in))
		assert.Equal(t, headAIBackendModeDisabled, aiStr(t, got, "llm_mode"))
		assert.Equal(t, headAIBackendModeDisabled, aiStr(t, got, "embedding_mode"))
		assert.Equal(t, "", aiStr(t, got, "local_base_url"))
		assert.Equal(t, "bge-m3", aiStr(t, got, "local_embedding_model"))
		assert.Equal(t, "qwen2.5:7b-instruct", aiStr(t, got, "local_llm_model"))
		assert.Len(t, got, 5)
	}
}

// Every list HEAD's seed builds reads back as the mode the rows can carry.
func TestAIBackend_UnfoldReadsTheSeed(t *testing.T) {
	for _, url := range []string{"", aiURL1} {
		for _, key := range []bool{false, true} {
			for _, llm := range aiModes {
				for _, emb := range aiModes {
					c := aiCfg{url: url, llm: llm, emb: emb, chat: "gotham-chat", embed: "gotham-embed", key: key}
					got, err := UnfoldAIBackend(aiSeed(t, c))
					require.NoError(t, err)
					// The mode the rows can carry: a cloud route alone keeps
					// the fallback it came from (see llmMode and embMode).
					carried := func(m string) string {
						local, cloud := modeLocal(m) && url != "", modeCloud(m) && key
						if cloud && !local && m == headAIBackendModeOpenAIFallbackLocal {
							return m
						}
						return modeOf(local, cloud)
					}
					wantLLM, wantEmb := carried(llm), carried(emb)
					assert.Equal(t, wantLLM, aiStr(t, got, "llm_mode"), "%+v", c)
					assert.Equal(t, wantEmb, aiStr(t, got, "embedding_mode"), "%+v", c)
					assert.Equal(t, url, aiStr(t, got, "local_base_url"))
					if url != "" {
						assert.Equal(t, "gotham-chat", aiStr(t, got, "local_llm_model"))
						assert.Equal(t, "gotham-embed", aiStr(t, got, "local_embedding_model"))
					} else {
						assert.Equal(t, aiDefChat, aiStr(t, got, "local_llm_model"))
						assert.Equal(t, aiDefEmbed, aiStr(t, got, "local_embedding_model"))
					}
				}
			}
		}
	}
}

func TestAIBackend_UnfoldMalformedPrev(t *testing.T) {
	for name, in := range map[string]string{
		"not a list":          `{"id":"local-llm"}`,
		"a string":            `"x"`,
		"element not object":  `[1]`,
		"null element":        `[null]`,
		"id not a string":     `[{"id":7}]`,
		"local twice":         `[{"id":"local-llm"},{"id":"local-llm"}]`,
		"openai twice":        `[{"id":"openai"},{"id":"openai"}]`,
		"caps not a list":     `[{"id":"local-llm","capabilities":"embed.text"}]`,
		"caps not strings":    `[{"id":"openai","capabilities":[1]}]`,
		"url not a string":    `[{"id":"local-llm","url":5}]`,
		"chat not a string":   `[{"id":"local-llm","chat_model":[]}]`,
		"embed not a string":  `[{"id":"local-llm","embed_model":{}}]`,
		"truncated":           `[{"id":"local-llm"`,
		"openai caps garbage": `[{"id":"openai","capabilities":{"a":1}}]`,
	} {
		_, err := UnfoldAIBackend(json.RawMessage(in))
		require.Error(t, err, name)
		assert.ErrorIs(t, err, ErrConvertInput, name)
		// and the fold refuses the same prev, the same way
		_, ferr := FoldAIBackend(aiVals(), json.RawMessage(in))
		assert.ErrorIs(t, ferr, ErrConvertInput, name)
	}
}

// The seed's list for the fallback with no local URL is one cloud route and a
// priority of 5; it reads as the fallback it came from, so writing "openai"
// back to it is a change (it sets the priority to 20), as HEAD's seed would.
func TestAIBackend_UnfoldCloudRouteAloneReadsFallbackFromPriorityAndBatch(t *testing.T) {
	list := json.RawMessage(`[{"id":"openai","priority":5,"capabilities":["llm.dedup_review","embed.text"]}]`)
	un, err := UnfoldAIBackend(list)
	require.NoError(t, err)
	assert.Equal(t, "openai-fallback-local", aiStr(t, un, "llm_mode"))
	assert.Equal(t, "openai-fallback-local", aiStr(t, un, "embedding_mode"))
	got, err := FoldAIBackend(aiVals("llm_mode", "openai", "embedding_mode", "openai"), list)
	require.NoError(t, err)
	row := aiRow(t, got, "openai")
	assert.Equal(t, float64(20), row["priority"])
	assert.Contains(t, aiCaps(row), "embed.text_batch")
	// A priority that is not a number is no fallback, (a string "5" included).
	un, err = UnfoldAIBackend(json.RawMessage(`[{"id":"openai","priority":"5","capabilities":["llm.dedup_review"]}]`))
	require.NoError(t, err)
	assert.Equal(t, "openai", aiStr(t, un, "llm_mode"))
}

// Another instance's fields never matter to the view, even malformed ones.
func TestAIBackend_UnfoldIgnoresOtherEndpoints(t *testing.T) {
	in := `[{"id":"whisper-9000","url":5,"capabilities":"junk"},{"id":"local-llm","url":"` + aiURL1 + `","chat_model":"a","embed_model":"b","capabilities":["llm.filename_parse"]}]`
	got, err := UnfoldAIBackend(json.RawMessage(in))
	require.NoError(t, err)
	assert.Equal(t, "local", aiStr(t, got, "llm_mode"))
	assert.Equal(t, "a", aiStr(t, got, "local_llm_model"))
}

// A mode is read off ANY of its ticks, and an unrelated tick reads as nothing.
func TestAIBackend_UnfoldModeFromTicks(t *testing.T) {
	row := func(id string, caps ...string) string {
		b, _ := json.Marshal(map[string]any{"id": id, "url": aiURL1, "capabilities": caps})
		return string(b)
	}
	for _, tc := range []struct {
		name, list, llm, emb string
	}{
		{"one local llm tick", "[" + row("local-llm", "llm.author_review") + "]", "local", "disabled"},
		{"embed text local", "[" + row("local-llm", "embed.text") + "]", "disabled", "local"},
		{"cloud rerank", "[" + row("openai", "llm.metadata_rerank") + "]", "openai", "disabled"},
		{"cloud dedup review", "[" + row("openai", "llm.dedup_review") + "]", "openai", "disabled"},
		{"cloud embed text without batch is the seed's fallback", "[" + row("openai", "embed.text") + "]", "disabled", "openai-fallback-local"},
		{"cloud embed text with batch", "[" + row("openai", "embed.text", "embed.text_batch") + "]", "disabled", "openai"},
		{"both", "[" + row("local-llm", "llm.filename_parse", "embed.text") + "," + row("openai", "llm.dedup_review", "embed.text") + "]", "openai-fallback-local", "openai-fallback-local"},
		{"the openai row's own unconditional ticks are no mode", "[" + row("openai", "llm.filename_parse", "llm.audiobook_parse", "llm.cover_art_vision", "llm.author_review", "transcribe.intro_clip") + "]", "disabled", "disabled"},
		{"a tick on another row is nobody's", "[" + row("elsewhere", "llm.filename_parse", "embed.text", "llm.dedup_review") + "]", "disabled", "disabled"},
		{"cloud llm route alone", "[" + row("openai", "llm.dedup_review") + "]", "openai", "disabled"},
		{"batch alone is no embedding mode", "[" + row("openai", "embed.text_batch") + "]", "disabled", "disabled"},
	} {
		got, err := UnfoldAIBackend(json.RawMessage(tc.list))
		require.NoError(t, err, tc.name)
		assert.Equal(t, tc.llm, aiStr(t, got, "llm_mode"), tc.name)
		assert.Equal(t, tc.emb, aiStr(t, got, "embedding_mode"), tc.name)
	}
}

// ---- Fold: the contract ----

func TestAIBackend_FoldNothingNamedKeepsPrevByteForByte(t *testing.T) {
	prev := json.RawMessage(` [ {"url":"http://192.0.2.50/a?x=1&y=2","id":"whisper-1","protocol":"whisper_server","zzz":1, "capabilities":[]} ,
	{ "id":"local-llm","url":"` + aiURL1 + `","capabilities":["embed.text"],"chat_model":"c","embed_model":"e"} ] `)
	for _, vals := range []map[string]json.RawMessage{nil, {}} {
		got, err := FoldAIBackend(vals, prev)
		require.NoError(t, err)
		assert.True(t, bytes.Equal(prev, got), "got %s", got)
	}
	// An absent prev has nothing to keep: the empty list.
	for _, p := range []json.RawMessage{nil, json.RawMessage(``), json.RawMessage(`  `)} {
		got, err := FoldAIBackend(nil, p)
		require.NoError(t, err)
		assert.JSONEq(t, `[]`, string(got))
	}
	got, err := FoldAIBackend(nil, json.RawMessage(`null`))
	require.NoError(t, err)
	assert.JSONEq(t, `null`, string(got))
}

// Writing back exactly what the list reads changes nothing, byte for byte.
func TestAIBackend_FoldOfUnfoldIsIdentity(t *testing.T) {
	var lists []json.RawMessage
	for _, url := range []string{"", aiURL1} {
		for _, key := range []bool{false, true} {
			for _, llm := range aiModes {
				for _, emb := range aiModes {
					lists = append(lists, aiSeed(t, aiCfg{url: url, llm: llm, emb: emb, chat: "gotham-chat", embed: "gotham-embed", key: key}))
				}
			}
		}
	}
	// A hand-tuned list: a partial set of ticks, an unknown tick, odd layout.
	lists = append(lists, json.RawMessage(`[{"id":"local-llm", "url":"`+aiURL1+`","capabilities":["llm.author_review","x.future"],"label":"mine","auth_ref":"openai_api_key"},
	{"id":"openai","url":"u","capabilities":["embed.text"],"priority":3}]`))
	for _, prev := range lists {
		un, err := UnfoldAIBackend(prev)
		require.NoError(t, err)
		got, err := FoldAIBackend(un, prev)
		require.NoError(t, err, string(prev))
		assert.True(t, bytes.Equal(prev, got), "prev %s\ngot  %s", prev, got)
	}
}

func TestAIBackend_FoldInputIsNotMutated(t *testing.T) {
	prev := aiSeed(t, aiCfg{url: aiURL1, llm: "local", emb: "local", chat: "c", embed: "e", key: true})
	prevCopy := slices.Clone(prev)
	vals := aiVals("llm_mode", "openai-fallback-local", "local_base_url", aiURL2)
	valsCopy := map[string]json.RawMessage{}
	for k, v := range vals {
		valsCopy[k] = slices.Clone(v)
	}
	_, err := FoldAIBackend(vals, prev)
	require.NoError(t, err)
	assert.Equal(t, prevCopy, prev)
	assert.Equal(t, valsCopy, vals)
}

func TestAIBackend_FoldIsIdempotent(t *testing.T) {
	prev := aiSeed(t, aiCfg{url: "", llm: "disabled", emb: "disabled", chat: aiDefChat, embed: aiDefEmbed, key: true})
	vals := aiVals("local_base_url", aiURL1, "llm_mode", "openai-fallback-local", "embedding_mode", "openai", "local_llm_model", "gotham-chat")
	once, err := FoldAIBackend(vals, prev)
	require.NoError(t, err)
	twice, err := FoldAIBackend(vals, once)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(once, twice), "%s\n%s", once, twice)
}

// Every value written reads back, in both directions.
func TestAIBackend_RoundTrip(t *testing.T) {
	prev := aiSeed(t, aiCfg{url: aiURL1, llm: "disabled", emb: "disabled", chat: "gotham-chat", embed: "gotham-embed", key: true})
	for _, llm := range aiModes {
		for _, emb := range aiModes {
			for _, url := range []string{aiURL1, aiURL2} {
				vals := aiVals("llm_mode", llm, "embedding_mode", emb, "local_base_url", url, "local_llm_model", "joker-chat", "local_embedding_model", "joker-embed")
				list, err := FoldAIBackend(vals, prev)
				require.NoError(t, err)
				back, err := UnfoldAIBackend(list)
				require.NoError(t, err)
				for k, v := range vals {
					assert.JSONEq(t, string(v), string(back[k]), "%s after %v", k, vals)
				}
			}
		}
	}
	// And the other way: a list read, written onto a different list, then read.
	other := aiSeed(t, aiCfg{url: "", llm: "disabled", emb: "disabled", chat: aiDefChat, embed: aiDefEmbed, key: true})
	src := aiSeed(t, aiCfg{url: aiURL3, llm: "openai-fallback-local", emb: "local", chat: "robin-chat", embed: "robin-embed", key: true})
	un, err := UnfoldAIBackend(src)
	require.NoError(t, err)
	moved, err := FoldAIBackend(un, other)
	require.NoError(t, err)
	un2, err := UnfoldAIBackend(moved)
	require.NoError(t, err)
	assert.Equal(t, un, un2)
}

// ---- Fold: what is written, and what never is ----

// A row of every kind, as an operator may have left it, to check what a fold
// leaves alone.
func aiHandList() json.RawMessage {
	return json.RawMessage(`[
 {"zz_unknown":{"a":[1,2]},"id":"whisper-9000","label":"Mac","protocol":"whisper_server","url":"http://192.0.2.60:9000/a?x=1&y=2","priority":1,"concurrency":2,"enabled":true,"capabilities":["transcribe.batch"]},
 {"id":"openai","label":"OpenAI","protocol":"openai_compat","url":"https://api.openai.com/v1","chat_model":"gpt","embed_model":"emb","priority":20,"concurrency":4,"enabled":true,"capabilities":["llm.filename_parse","llm.cover_art_vision","x.future","embed.text","embed.text_batch"],"features":["batch_api"],"auth_ref":"openai_api_key","capability_models":{"llm.dedup_review":"m"},"host_roots":{"r":"/p"}},
 {"id":"local-llm","label":"mine","protocol":"openai_compat","url":"` + aiURL1 + `","chat_model":"c","embed_model":"e","priority":10,"concurrency":4,"enabled":true,"capabilities":["llm.filename_parse","llm.author_review","llm.author_discovery","llm.metadata_rerank","embed.text","operator.added"],"auth_ref":"openai_api_key","x_extra":{"k":"v"}}
]`)
}

func fields(t testing.TB, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &m))
	return m
}

func elems(t testing.TB, raw json.RawMessage) []json.RawMessage {
	t.Helper()
	var e []json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &e))
	return e
}

func TestAIBackend_FoldLeavesOtherEndpointsByteForByte(t *testing.T) {
	prev := aiHandList()
	whisper := elems(t, prev)[0]
	for _, vals := range []map[string]json.RawMessage{
		aiVals("local_base_url", aiURL2),
		aiVals("llm_mode", "openai"),
		aiVals("embedding_mode", "disabled", "llm_mode", "disabled"),
		aiVals("local_base_url", ""),
		aiVals("local_llm_model", "joker-chat", "local_embedding_model", "joker-embed"),
	} {
		got, err := FoldAIBackend(vals, prev)
		require.NoError(t, err)
		assert.True(t, bytes.Contains(got, whisper), "the whisper row's bytes are kept, %v", vals)
		assert.Contains(t, string(got), `x=1&y=2`, "an & is not escaped")
	}
}

func TestAIBackend_FoldTouchesOnlyWhatTheModeOwnsOnTheOpenAIRow(t *testing.T) {
	prev := aiHandList()
	before := fields(t, elems(t, prev)[1])
	// llm_mode openai-fallback-local (from local): cloud ticks and priority 5.
	got, err := FoldAIBackend(aiVals("llm_mode", "openai-fallback-local"), prev)
	require.NoError(t, err)
	row := fields(t, elems(t, got)[1])
	for k, v := range before {
		if k == "capabilities" || k == "priority" {
			continue
		}
		assert.Equal(t, string(v), string(row[k]), "field %s is kept", k)
	}
	assert.Equal(t, `"openai_api_key"`, string(row["auth_ref"]))
	assert.JSONEq(t, `5`, string(row["priority"]))
	assert.ElementsMatch(t, []string{"llm.filename_parse", "llm.cover_art_vision", "x.future", "embed.text", "embed.text_batch", "llm.metadata_rerank", "llm.dedup_review"}, aiCaps(aiRow(t, got, "openai")))

	// Back out of fallback: the cloud ticks and the priority go, nothing else.
	back, err := FoldAIBackend(aiVals("llm_mode", "local"), got)
	require.NoError(t, err)
	assert.JSONEq(t, `20`, string(fields(t, elems(t, back)[1])["priority"]))
	assert.ElementsMatch(t, []string{"llm.filename_parse", "llm.cover_art_vision", "x.future", "embed.text", "embed.text_batch"}, aiCaps(aiRow(t, back, "openai")))
}

func TestAIBackend_FoldPriorityOnlyMovesAtTheFallbackBoundary(t *testing.T) {
	prev := json.RawMessage(`[{"id":"local-llm","url":"` + aiURL1 + `","capabilities":[]},{"id":"openai","priority":42,"capabilities":[]}]`)
	prio := func(list json.RawMessage) string { return string(fields(t, elems(t, list)[1])["priority"]) }
	a, err := FoldAIBackend(aiVals("llm_mode", "openai"), prev)
	require.NoError(t, err)
	assert.Equal(t, "42", prio(a), "disabled to openai keeps a tuned priority")
	b, err := FoldAIBackend(aiVals("llm_mode", "local"), a)
	require.NoError(t, err)
	assert.Equal(t, "42", prio(b))
	c, err := FoldAIBackend(aiVals("llm_mode", "openai-fallback-local"), b)
	require.NoError(t, err)
	assert.Equal(t, "5", prio(c))
	d, err := FoldAIBackend(aiVals("llm_mode", "disabled"), c)
	require.NoError(t, err)
	assert.Equal(t, "20", prio(d))
	// The embedding mode never moves it.
	e, err := FoldAIBackend(aiVals("embedding_mode", "openai-fallback-local"), prev)
	require.NoError(t, err)
	assert.Equal(t, "42", prio(e))
}

func TestAIBackend_FoldEmbeddingBatchIsPureOpenAIOnly(t *testing.T) {
	prev := json.RawMessage(`[{"id":"local-llm","url":"` + aiURL1 + `","capabilities":[]},{"id":"openai","capabilities":[]}]`)
	caps := func(list json.RawMessage, id string) []string { return aiCaps(aiRow(t, list, id)) }
	a, err := FoldAIBackend(aiVals("embedding_mode", "openai"), prev)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"embed.text", "embed.text_batch"}, caps(a, "openai"))
	assert.Empty(t, caps(a, "local-llm"))
	b, err := FoldAIBackend(aiVals("embedding_mode", "openai-fallback-local"), a)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"embed.text"}, caps(b, "openai"))
	assert.ElementsMatch(t, []string{"embed.text"}, caps(b, "local-llm"))
	c, err := FoldAIBackend(aiVals("embedding_mode", "local"), b)
	require.NoError(t, err)
	assert.Empty(t, caps(c, "openai"))
	assert.ElementsMatch(t, []string{"embed.text"}, caps(c, "local-llm"))
}

// A fold never creates or removes the openai row, and refuses a cloud mode
// with none, with words the SPA can show.
func TestAIBackend_FoldNeverMakesOrDropsTheOpenAIRow(t *testing.T) {
	prev := aiSeed(t, aiCfg{url: aiURL1, llm: "local", emb: "local", chat: "c", embed: "e", key: false})
	require.Nil(t, aiRow(t, prev, "openai"))
	for _, vals := range []map[string]json.RawMessage{
		aiVals("llm_mode", "openai"),
		aiVals("llm_mode", "openai-fallback-local"),
		aiVals("embedding_mode", "openai"),
		aiVals("embedding_mode", "openai-fallback-local"),
	} {
		_, err := FoldAIBackend(vals, prev)
		require.ErrorIs(t, err, ErrNotRepresentable, "%v", vals)
		assert.Contains(t, err.Error(), "set an OpenAI key first")
	}
	// Writes that need no openai row pass, and leave it absent.
	got, err := FoldAIBackend(aiVals("llm_mode", "disabled", "embedding_mode", "disabled", "local_base_url", aiURL2), prev)
	require.NoError(t, err)
	assert.Nil(t, aiRow(t, got, "openai"))
	// And with the row present nothing removes it, whatever the write.
	withKey := aiSeed(t, aiCfg{url: aiURL1, llm: "openai", emb: "openai", chat: "c", embed: "e", key: true})
	got, err = FoldAIBackend(aiVals("llm_mode", "disabled", "embedding_mode", "disabled", "local_base_url", ""), withKey)
	require.NoError(t, err)
	require.NotNil(t, aiRow(t, got, "openai"))
	assert.Equal(t, "openai_api_key", aiRow(t, got, "openai")["auth_ref"])
}

// Credentials on any row survive every kind of write, byte for byte.
func TestAIBackend_FoldNeverDropsOrRewritesACredentialReference(t *testing.T) {
	prev := aiHandList()
	var authRefs []string
	for _, e := range elems(t, prev) {
		if v, ok := fields(t, e)["auth_ref"]; ok {
			authRefs = append(authRefs, string(v))
		}
	}
	require.Len(t, authRefs, 2)
	for _, vals := range []map[string]json.RawMessage{
		aiVals("llm_mode", "disabled", "embedding_mode", "disabled"),
		aiVals("llm_mode", "openai-fallback-local", "embedding_mode", "openai"),
		aiVals("local_base_url", aiURL2, "local_llm_model", "joker-chat"),
		aiVals("local_embedding_model", "joker-embed"),
	} {
		got, err := FoldAIBackend(vals, prev)
		require.NoError(t, err, "%v", vals)
		var after []string
		for _, e := range elems(t, got) {
			if v, ok := fields(t, e)["auth_ref"]; ok {
				after = append(after, string(v))
			}
		}
		assert.Equal(t, authRefs, after, "%v", vals)
	}
	// Clearing the URL removes the local row and its reference with it; the
	// openai row's stays.
	got, err := FoldAIBackend(aiVals("local_base_url", ""), prev)
	require.NoError(t, err)
	assert.Nil(t, aiRow(t, got, "local-llm"))
	assert.Equal(t, "openai_api_key", aiRow(t, got, "openai")["auth_ref"])
	// A new row never carries one.
	none := aiSeed(t, aiCfg{url: "", llm: "disabled", emb: "disabled", chat: aiDefChat, embed: aiDefEmbed, key: true})
	got, err = FoldAIBackend(aiVals("local_base_url", aiURL1), none)
	require.NoError(t, err)
	_, has := aiRow(t, got, "local-llm")["auth_ref"]
	assert.False(t, has)
}

func TestAIBackend_FoldKeepsTheLocalRowsOwnFieldsAndTicksNoModeOwns(t *testing.T) {
	prev := aiHandList()
	got, err := FoldAIBackend(aiVals("local_base_url", aiURL2, "llm_mode", "disabled", "embedding_mode", "disabled", "local_llm_model", "m1", "local_embedding_model", "m2"), prev)
	require.NoError(t, err)
	row := aiRow(t, got, "local-llm")
	assert.Equal(t, "mine", row["label"])
	assert.Equal(t, map[string]any{"k": "v"}, row["x_extra"])
	assert.Equal(t, aiURL2, row["url"])
	assert.Equal(t, "m1", row["chat_model"])
	assert.Equal(t, "m2", row["embed_model"])
	assert.Equal(t, []string{"operator.added"}, aiCaps(row))
}

// ---- Fold: making and removing the local row ----

// A URL where there is no local row makes the row HEAD's seed makes, ticked
// for the modes written.
func TestAIBackend_FoldCreatesTheSeedsLocalRow(t *testing.T) {
	for _, key := range []bool{false, true} {
		for _, llm := range aiModes {
			for _, emb := range aiModes {
				prev := aiSeed(t, aiCfg{url: "", llm: "disabled", emb: "disabled", chat: aiDefChat, embed: aiDefEmbed, key: key})
				if (modeCloud(llm) || modeCloud(emb)) && !key {
					continue
				}
				got, err := FoldAIBackend(aiVals("local_base_url", aiURL1, "llm_mode", llm, "embedding_mode", emb, "local_llm_model", "gotham-chat", "local_embedding_model", "gotham-embed"), prev)
				require.NoError(t, err)
				want := aiSeed(t, aiCfg{url: aiURL1, llm: llm, emb: emb, chat: "gotham-chat", embed: "gotham-embed", key: key})
				// The seed puts it first; its ticks come in the seed's order.
				assert.Equal(t, aiNorm(t, want)[0], aiNorm(t, got)[0], "key=%v llm=%s emb=%s", key, llm, emb)
				assert.Equal(t, headAIEndpointIDLocalLLM, elemID(t, elems(t, got)[0]))
			}
		}
	}
}

func elemID(t testing.TB, raw json.RawMessage) string {
	t.Helper()
	var s string
	require.NoError(t, json.Unmarshal(fields(t, raw)["id"], &s))
	return s
}

func TestAIBackend_FoldCreatedRowUsesHEADsDefaultModelsWhenNoneNamed(t *testing.T) {
	prev := aiSeed(t, aiCfg{url: "", llm: "disabled", emb: "disabled", chat: aiDefChat, embed: aiDefEmbed, key: false})
	got, err := FoldAIBackend(aiVals("local_base_url", aiURL1), prev)
	require.NoError(t, err)
	want := aiSeed(t, aiCfg{url: aiURL1, llm: "disabled", emb: "disabled", chat: aiDefChat, embed: aiDefEmbed})
	assert.Equal(t, aiNorm(t, want), aiNorm(t, got))
	row := aiRow(t, got, "local-llm")
	assert.Equal(t, aiDefChat, row["chat_model"])
	assert.Equal(t, aiDefEmbed, row["embed_model"])
}

func TestAIBackend_FoldCreatesIntoAnEmptyOrAbsentList(t *testing.T) {
	for _, prev := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(`[]`)} {
		got, err := FoldAIBackend(aiVals("local_base_url", aiURL1), prev)
		require.NoError(t, err)
		assert.Len(t, elems(t, got), 1)
		assert.Equal(t, aiURL1, aiRow(t, got, "local-llm")["url"])
	}
}

func TestAIBackend_FoldClearingTheURLRemovesTheRow(t *testing.T) {
	prev := aiSeed(t, aiCfg{url: aiURL1, llm: "local", emb: "local", chat: "c", embed: "e", key: true})
	got, err := FoldAIBackend(aiVals("local_base_url", ""), prev)
	require.NoError(t, err)
	assert.Nil(t, aiRow(t, got, "local-llm"))
	want := aiSeed(t, aiCfg{url: "", llm: "local", emb: "local", chat: "c", embed: "e", key: true})
	assert.Equal(t, aiNorm(t, want), aiNorm(t, got), "the same list HEAD's seed builds for an empty URL")
	un, err := UnfoldAIBackend(got)
	require.NoError(t, err)
	assert.Equal(t, "", aiStr(t, un, "local_base_url"))
	assert.Equal(t, aiDefChat, aiStr(t, un, "local_llm_model"))
	// Clearing it when there is none is a write of what the list reads.
	again, err := FoldAIBackend(aiVals("local_base_url", ""), got)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(got, again))
	// Removing the only row leaves the empty list, not null.
	only, err := FoldAIBackend(aiVals("local_base_url", ""), json.RawMessage(`[{"id":"local-llm","url":"`+aiURL1+`","capabilities":[]}]`))
	require.NoError(t, err)
	assert.Equal(t, `[]`, string(only))
}

func TestAIBackend_FoldClearingTheURLWithItsOwnModelsIsAccepted(t *testing.T) {
	prev := aiSeed(t, aiCfg{url: aiURL1, llm: "disabled", emb: "disabled", chat: "gotham-chat", embed: "gotham-embed", key: false})
	got, err := FoldAIBackend(aiVals("local_base_url", "", "local_llm_model", "gotham-chat", "local_embedding_model", "gotham-embed"), prev)
	require.NoError(t, err)
	assert.Nil(t, aiRow(t, got, "local-llm"))
	_, err = FoldAIBackend(aiVals("local_base_url", "", "local_llm_model", "joker-chat"), prev)
	assert.ErrorIs(t, err, ErrNotRepresentable, "a different model has nowhere to go")
	_, err = FoldAIBackend(aiVals("local_base_url", "", "llm_mode", "local"), aiSeed(t, aiCfg{url: aiURL1, llm: "local", emb: "disabled", chat: "c", embed: "e"}))
	assert.ErrorIs(t, err, ErrNotRepresentable, "a local mode with no URL")
}

// ---- Fold: refusals ----

func TestAIBackend_FoldRefusals(t *testing.T) {
	withRow := aiSeed(t, aiCfg{url: aiURL1, llm: "local", emb: "local", chat: "gotham-chat", embed: "gotham-embed", key: true})
	noRow := aiSeed(t, aiCfg{url: "", llm: "disabled", emb: "disabled", chat: aiDefChat, embed: aiDefEmbed, key: true})
	other := json.RawMessage(`[{"id":"elsewhere","protocol":"openai_compat","url":"` + aiURL3 + `/","capabilities":[]},` +
		`{"id":"mac","protocol":"whisper_server","url":"` + aiURL2 + `","capabilities":[]}]`)
	for _, tc := range []struct {
		name string
		vals map[string]json.RawMessage
		prev json.RawMessage
		is   error
		msg  string // a word the SPA can show
	}{
		{"unknown name", aiVals("parse_batch_size", "3"), withRow, ErrConvertInput, ""},
		{"a name of another family", aiVals("oauth_enabled", "x"), withRow, ErrConvertInput, ""},
		{"a non-string", map[string]json.RawMessage{"llm_mode": json.RawMessage(`true`)}, withRow, ErrConvertInput, ""},
		{"a null", map[string]json.RawMessage{"local_base_url": json.RawMessage(`null`)}, withRow, ErrConvertInput, ""},
		{"not JSON", map[string]json.RawMessage{"local_base_url": json.RawMessage(`{`)}, withRow, ErrConvertInput, ""},
		{"unknown mode", aiVals("llm_mode", "cloud"), withRow, ErrNotRepresentable, "mode"},
		{"empty mode", aiVals("embedding_mode", ""), withRow, ErrNotRepresentable, "mode"},
		{"mode in the wrong case", aiVals("llm_mode", "Local"), withRow, ErrNotRepresentable, "mode"},
		{"local mode, no row", aiVals("llm_mode", "local"), noRow, ErrNotRepresentable, "set the local server URL first"},
		{"local embedding, no row", aiVals("embedding_mode", "openai-fallback-local"), noRow, ErrNotRepresentable, "set the local server URL first"},
		{"blank URL", aiVals("local_base_url", "   "), withRow, ErrNotRepresentable, "blank"},
		{"URL does not parse", aiVals("local_base_url", "http://192.0.2.1:bad/"), withRow, ErrNotRepresentable, "parse"},
		{"URL with credentials", aiVals("local_base_url", "http://robin:pennyworth@192.0.2.9/v1"), withRow, ErrNotRepresentable, "credentials"},
		{"URL with a user only", aiVals("local_base_url", "http://robin@192.0.2.9/v1"), noRow, ErrNotRepresentable, "credentials"},
		{"URL of another openai_compat row (new row)", aiVals("local_base_url", strings.ToUpper(aiURL3)), other, ErrNotRepresentable, "already has"},
		{"URL of another openai_compat row (existing row)", aiVals("local_base_url", aiURL3), json.RawMessage(`[{"id":"local-llm","protocol":"openai_compat","url":"` + aiURL1 + `","capabilities":[]},{"id":"elsewhere","protocol":"openai_compat","url":"` + aiURL3 + `","capabilities":[]}]`), ErrNotRepresentable, "already has"},
		{"model with no row", aiVals("local_llm_model", "joker-chat"), noRow, ErrNotRepresentable, "set the local server URL first"},
		{"embed model with no row", aiVals("local_embedding_model", "joker-embed"), noRow, ErrNotRepresentable, "set the local server URL first"},
		{"empty embed model on the row", aiVals("local_embedding_model", ""), withRow, ErrNotRepresentable, "empty"},
		{"empty embed model on a new row", aiVals("local_base_url", aiURL1, "local_embedding_model", ""), noRow, ErrNotRepresentable, "empty"},
	} {
		got, err := FoldAIBackend(tc.vals, tc.prev)
		require.Error(t, err, tc.name)
		assert.Nil(t, got, tc.name)
		assert.ErrorIs(t, err, tc.is, tc.name)
		assert.Contains(t, err.Error(), tc.msg, tc.name)
	}
	// What the same names accept.
	for name, tc := range map[string]struct {
		vals map[string]json.RawMessage
		prev json.RawMessage
	}{
		"the same URL on a whisper row (another protocol)":     {aiVals("local_base_url", aiURL2), other},
		"HEAD's default model with no row":                     {aiVals("local_llm_model", aiDefChat, "local_embedding_model", aiDefEmbed), noRow},
		"an empty chat model on the row (HEAD's seed has one)": {aiVals("local_llm_model", ""), withRow},
		"the empty embed model the row already has":            {aiVals("local_embedding_model", ""), json.RawMessage(`[{"id":"local-llm","url":"` + aiURL1 + `","capabilities":[]}]`)},
		"the row's own URL (not another endpoint's)":           {aiVals("local_base_url", aiURL1), withRow},
	} {
		_, err := FoldAIBackend(tc.vals, tc.prev)
		assert.NoError(t, err, name)
	}
}

// An error never carries a value a caller sent or a list held.
func TestAIBackend_ErrorsNeverEchoValues(t *testing.T) {
	secret := "pennyworth-secret"
	for _, tc := range []struct {
		vals map[string]json.RawMessage
		prev json.RawMessage
	}{
		{aiVals("local_base_url", "http://robin:"+secret+"@192.0.2.9/v1"), nil},
		{aiVals("llm_mode", secret), nil},
		{aiVals("local_base_url", "  "), nil},
		{aiVals("local_base_url", "http://192.0.2.1:"+secret+"/"), nil},
		{map[string]json.RawMessage{secret: json.RawMessage(`"x"`)}, nil},
		{map[string]json.RawMessage{"llm_mode": json.RawMessage(`"` + secret + ``)}, nil},
		{nil, json.RawMessage(`[{"id":7,"x":"` + secret + `"}]`)},
		{nil, json.RawMessage(`[{"id":"local-llm","url":"` + aiURL1 + `","capabilities":["` + secret + `",1]}]`)},
		{aiVals("local_base_url", aiURL3), json.RawMessage(`[{"id":"` + secret + `","protocol":"openai_compat","url":"` + aiURL3 + `"}]`)},
	} {
		_, err := FoldAIBackend(tc.vals, tc.prev)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), secret)
		assert.NotContains(t, err.Error(), "192.0.2")
	}
}

// ---- Lossy cases ----

func TestAIBackend_LossyCases(t *testing.T) {
	t.Run("HEAD's unset mode reads as the effective one, and cannot be written back", func(t *testing.T) {
		un, err := UnfoldAIBackend(json.RawMessage(`[]`))
		require.NoError(t, err)
		assert.Equal(t, "disabled", aiStr(t, un, "llm_mode"))
		_, err = FoldAIBackend(aiVals("llm_mode", ""), nil)
		assert.ErrorIs(t, err, ErrNotRepresentable)
	})
	t.Run("the intent of a mode with no row is not stored", func(t *testing.T) {
		// HEAD's seed: a local mode with an URL set later ticks the row. Here
		// the mode was refused, or never had a row, so it is not remembered.
		prev := aiSeed(t, aiCfg{url: "", llm: "local", emb: "local", chat: aiDefChat, embed: aiDefEmbed})
		got, err := FoldAIBackend(aiVals("local_base_url", aiURL1), prev)
		require.NoError(t, err)
		assert.Empty(t, aiCaps(aiRow(t, got, "local-llm")))
		seed := aiSeed(t, aiCfg{url: aiURL1, llm: "local", emb: "local", chat: aiDefChat, embed: aiDefEmbed})
		assert.NotEmpty(t, aiCaps(aiRow(t, seed, "local-llm")), "HEAD's seed ticks them")
	})
	t.Run("a disabled row still reads as its ticks", func(t *testing.T) {
		un, err := UnfoldAIBackend(json.RawMessage(`[{"id":"local-llm","url":"` + aiURL1 + `","enabled":false,"capabilities":["embed.text"]}]`))
		require.NoError(t, err)
		assert.Equal(t, "local", aiStr(t, un, "embedding_mode"))
		got, err := FoldAIBackend(aiVals("embedding_mode", "disabled"), json.RawMessage(`[{"id":"local-llm","url":"`+aiURL1+`","enabled":false,"capabilities":["embed.text"]}]`))
		require.NoError(t, err)
		assert.Equal(t, false, aiRow(t, got, "local-llm")["enabled"], "the flag is not written")
	})
	t.Run("clearing the URL drops what an operator tuned on the row", func(t *testing.T) {
		got, err := FoldAIBackend(aiVals("local_base_url", ""), aiHandList())
		require.NoError(t, err)
		assert.Nil(t, aiRow(t, got, "local-llm"))
	})
	t.Run("a model with no row reads as HEAD's default", func(t *testing.T) {
		un, err := UnfoldAIBackend(json.RawMessage(`[]`))
		require.NoError(t, err)
		assert.Equal(t, "bge-m3", aiStr(t, un, "local_embedding_model"))
		assert.Equal(t, "qwen2.5:7b-instruct", aiStr(t, un, "local_llm_model"))
	})
	t.Run("an empty embedding model is refused where HEAD's seed fills it from embedding.model", func(t *testing.T) {
		cfg, _ := headDefaultConfig()
		cfg.AIBackend.LocalBaseURL = aiURL1
		cfg.AIBackend.LocalEmbeddingModel = ""
		rows := headBuildMigratedAIEndpoints(cfg, false)
		assert.Equal(t, cfg.Embedding.Model, rows[0].EmbedModel, "HEAD's seed puts embedding.model on the local row")
		_, err := FoldAIBackend(aiVals("local_base_url", aiURL1, "local_embedding_model", ""), json.RawMessage(`[]`))
		assert.ErrorIs(t, err, ErrNotRepresentable)
	})
	t.Run("the fallback priority is overwritten at the boundary", func(t *testing.T) {
		prev := json.RawMessage(`[{"id":"local-llm","url":"` + aiURL1 + `","capabilities":[]},{"id":"openai","priority":42,"capabilities":[]}]`)
		got, err := FoldAIBackend(aiVals("llm_mode", "openai-fallback-local"), prev)
		require.NoError(t, err)
		assert.Equal(t, float64(5), aiRow(t, got, "openai")["priority"])
	})
	t.Run("a write of the mode the ticks already read leaves odd ticks alone", func(t *testing.T) {
		prev := json.RawMessage(`[{"id":"local-llm","url":"` + aiURL1 + `","capabilities":["llm.author_review"]}]`)
		got, err := FoldAIBackend(aiVals("llm_mode", "local"), prev)
		require.NoError(t, err)
		assert.Equal(t, []string{"llm.author_review"}, aiCaps(aiRow(t, got, "local-llm")), "not topped up to HEAD's four")
	})
	t.Run("a URL change keeps its ticks and a changed mode tops a partial set up", func(t *testing.T) {
		prev := json.RawMessage(`[{"id":"local-llm","url":"` + aiURL1 + `","capabilities":["llm.author_review"]}]`)
		got, err := FoldAIBackend(aiVals("llm_mode", "disabled"), prev)
		require.NoError(t, err)
		assert.Empty(t, aiCaps(aiRow(t, got, "local-llm")))
	})
}
