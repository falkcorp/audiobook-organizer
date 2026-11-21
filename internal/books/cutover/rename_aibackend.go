// file: internal/books/cutover/rename_aibackend.go
// version: 1.0.1
// guid: 6C0A3B0B-B2A8-42A4-9C8B-C6F557EF5F42
// last-edited: 2026-11-19

package cutover

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
)

// The ai_backend.* settings as a view over the ai_endpoints instance list
// (F3-07 e9). HEAD's codec folds these five scalars into ai_endpoints once, at
// first boot (headBuildMigratedAIEndpoints, head_blob.go); the Settings page
// still saves them, and no write route exists for ai_endpoints. UnfoldAIBackend
// reads them back off the list and FoldAIBackend writes them onto it, by the
// mapping that seed uses. The contract is FoldOIDC's: errors are returned and
// wrap ErrConvertInput (malformed input) or ErrNotRepresentable (well formed,
// but the list cannot hold it), a key that is absent leaves the list alone,
// and nothing fails open.
//
// The mapping, from headBuildMigratedAIEndpoints:
//
//   - The row with id "local-llm" exists iff the URL is not empty. It holds
//     local_base_url (url), local_llm_model (chat_model) and
//     local_embedding_model (embed_model).
//   - llm_mode and embedding_mode are capability ticks, never the Enabled flag.
//     A local llm mode ticks llm.filename_parse, llm.author_review,
//     llm.author_discovery and llm.metadata_rerank on local-llm; a cloud one
//     ticks llm.metadata_rerank and llm.dedup_review on the "openai" row, and
//     "openai-fallback-local" does both and sets that row's priority to 5
//     (otherwise 20). With no local row the seed's list for the fallback is a
//     cloud route alone, told from "openai" by that priority (llm) and by the
//     absent embed.text_batch tick (embedding), and read as the fallback.
//     embedding_mode ticks embed.text on local-llm (local) and on openai
//     (cloud), and embed.text_batch on openai for the pure "openai" mode.
//   - Those are the only fields the fold ever writes on the openai row. It
//     never creates or deletes it, and never touches its auth_ref; the OpenAI
//     row exists iff a key does, and no ai_backend key can make one.
//
// Lossy cases, each pinned by TestAIBackend_LossyCases:
//   - A mode is read off the ticks, so it is always an effective mode: HEAD's
//     "" (derive it) is never returned, and writing "" is refused.
//   - A mode that needs a row that is not there is refused, where HEAD's seed
//     silently ticks nothing: local needs the local-llm row ("set the local
//     server URL first"), openai needs the openai row ("set an OpenAI key
//     first"). The intent of a mode that has no row to carry it is not stored,
//     so setting the URL later does not tick the new row for it.
//   - The Enabled flag is neither read nor written: a disabled row still reads
//     as its ticks.
//   - A model that has no local-llm row to live on reads as HEAD's default and
//     may be written only as that default. An empty local_embedding_model is
//     refused: HEAD's seed would fill it from embedding.model, an OpenAI model
//     name, onto a local server. Clearing the URL while a model is named is
//     accepted only if the model is the removed row's own.
//   - Clearing the URL removes the local-llm row with its ticks, label and any
//     field an operator tuned: HEAD has no local row for an empty URL.
//   - Switching into or out of "openai-fallback-local" overwrites the openai
//     row's priority with 5 or 20, as the seed does.
//   - Writing a mode other than the one the list reads leaves ticks that no
//     mode names (operator-added ones) alone, and a write of the value the
//     list already reads changes nothing at all.

// The ai_backend names the view holds.
const (
	aiBackendEmbeddingMode  = "embedding_mode"
	aiBackendLLMMode        = "llm_mode"
	aiBackendLocalBaseURL   = "local_base_url"
	aiBackendLocalEmbModel  = "local_embedding_model"
	aiBackendLocalLLMModel  = "local_llm_model"
	aiBackendOpenAIPriority = 20 // the seed's priority for the openai row
	aiBackendFallbackPrio   = 5  // the seed's priority under openai-fallback-local
)

var aiBackendNames = []string{aiBackendEmbeddingMode, aiBackendLLMMode, aiBackendLocalBaseURL, aiBackendLocalEmbModel, aiBackendLocalLLMModel}

// AIBackendNames are the five names UnfoldAIBackend returns and FoldAIBackend
// takes (the HEAD fields ai_backend.<name>), in a stable order.
func AIBackendNames() []string { return slices.Clone(aiBackendNames) }

// The capabilities each mode owns, by the row they sit on.
var (
	aiLocalLLMCaps  = []string{headCapLLMFilenameParse, headCapLLMAuthorReview, headCapLLMAuthorDiscovery, headCapLLMMetadataRerank}
	aiCloudLLMCaps  = []string{headCapLLMMetadataRerank, headCapLLMDedupReview}
	aiEmbedTextCaps = []string{headCapEmbedText}
	aiEmbedBatch    = []string{headCapEmbedTextBatch}
)

// aiEntry is one element of the list. raw is its bytes as read; m is its
// decoded fields, set once the entry is changed.
type aiEntry struct {
	raw   json.RawMessage
	m     map[string]json.RawMessage
	id    string
	dirty bool
}

// aiRows is the decoded list.
type aiRows struct {
	entries []*aiEntry
	local   *aiEntry // the local-llm row, nil if none
	openai  *aiEntry // the openai row, nil if none
}

// aiDefaultModels are HEAD's defaults for local_llm_model and
// local_embedding_model, the values the list reads when it has no local-llm
// row.
var aiDefaultModels = sync.OnceValues(func() (chat, embed string) {
	def, _ := headDefaultConfig()
	return def.AIBackend.LocalLLMModel, def.AIBackend.LocalEmbeddingModel
})

// aiState is what the list says about the five names.
type aiState struct {
	url, chat, embed   string
	llmLocal, llmCloud bool
	embLocal, embCloud bool
	// fallbackPrio is the openai row's priority being the seed's fallback one;
	// batch is embed.text_batch being ticked on it.
	fallbackPrio, batch bool
}

func aiBad(what string) error { return fmt.Errorf("%w: ai-backend: %s", ErrConvertInput, what) }
func aiUnrep(what string) error {
	return fmt.Errorf("%w: ai-backend: %s", ErrNotRepresentable, what)
}

// parseAIRows reads the list. Null, empty and absent are the empty list.
func parseAIRows(in json.RawMessage) (*aiRows, error) {
	rows := &aiRows{}
	if len(bytes.TrimSpace(in)) == 0 {
		return rows, nil
	}
	// A JSON null decodes to a nil slice without error: also the empty list.
	var elems []json.RawMessage
	if err := json.Unmarshal(in, &elems); err != nil {
		return nil, aiBad("want a list of endpoints")
	}
	for _, raw := range elems {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil || m == nil {
			return nil, aiBad("an endpoint is not an object")
		}
		e := &aiEntry{raw: raw}
		if v, has := m["id"]; has && json.Unmarshal(v, &e.id) != nil {
			return nil, aiBad("an endpoint's id is not a string")
		}
		switch e.id {
		case headAIEndpointIDLocalLLM:
			if rows.local != nil {
				return nil, aiBad("the local-llm endpoint is listed twice")
			}
			rows.local = e
		case headAIEndpointIDOpenAI:
			if rows.openai != nil {
				return nil, aiBad("the openai endpoint is listed twice")
			}
			rows.openai = e
		}
		rows.entries = append(rows.entries, e)
	}
	// The two rows the view reads must be well formed whether or not it
	// changes them.
	for _, e := range []*aiEntry{rows.local, rows.openai} {
		if e == nil {
			continue
		}
		if _, err := e.caps(); err != nil {
			return nil, err
		}
	}
	if rows.local != nil {
		for _, k := range []string{"url", "chat_model", "embed_model"} {
			if _, err := rows.local.str(k); err != nil {
				return nil, err
			}
		}
	}
	return rows, nil
}

// openaiPriorityIs reports whether the openai row's priority is n. A priority
// that is not a number is never n; parseAIRows has refused one that is not
// JSON at all.
func (r *aiRows) openaiPriorityIs(n int) bool {
	v, has := r.openai.fields()["priority"]
	if !has {
		return false
	}
	var f float64
	return json.Unmarshal(v, &f) == nil && f == float64(n)
}

// fields decodes the entry's fields.
func (e *aiEntry) fields() map[string]json.RawMessage {
	if e.m == nil {
		_ = json.Unmarshal(e.raw, &e.m) // validated by parseAIRows
	}
	return e.m
}

func (e *aiEntry) str(key string) (string, error) {
	v, has := e.fields()[key]
	if !has || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return "", aiBad("an endpoint's " + key + " is not a string")
	}
	return s, nil
}

func (e *aiEntry) caps() ([]string, error) {
	v, has := e.fields()["capabilities"]
	if !has || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal(v, &out); err != nil {
		return nil, aiBad("an endpoint's capabilities are not a list of strings")
	}
	return out, nil
}

func (e *aiEntry) set(key string, v any) {
	b, err := marshalNoEscape(v)
	if err != nil {
		panic("cutover: marshal of an ai-backend value: " + err.Error()) // strings and ints
	}
	e.fields()[key] = b
	e.dirty = true
}

// tick adds the capabilities in caps that are missing; untick removes every
// copy of them. Neither touches a capability that is not named.
func (e *aiEntry) tick(caps []string) {
	have, _ := e.caps() // validated
	next := slices.Clone(have)
	for _, c := range caps {
		if !slices.Contains(next, c) {
			next = append(next, c)
		}
	}
	if len(next) != len(have) {
		e.set("capabilities", next)
	}
}

func (e *aiEntry) untick(caps []string) {
	have, _ := e.caps()
	next := make([]string, 0, len(have))
	for _, c := range have {
		if !slices.Contains(caps, c) {
			next = append(next, c)
		}
	}
	if len(next) != len(have) {
		e.set("capabilities", next)
	}
}

func hasAny(have []string, caps []string) bool {
	for _, c := range caps {
		if slices.Contains(have, c) {
			return true
		}
	}
	return false
}

// state reads the five names off the list.
func (r *aiRows) state() aiState {
	chat, embed := aiDefaultModels()
	s := aiState{chat: chat, embed: embed}
	if r.local != nil {
		s.url, _ = r.local.str("url")
		s.chat, _ = r.local.str("chat_model")
		s.embed, _ = r.local.str("embed_model")
		have, _ := r.local.caps()
		s.llmLocal = hasAny(have, aiLocalLLMCaps)
		s.embLocal = hasAny(have, aiEmbedTextCaps)
	}
	if r.openai != nil {
		have, _ := r.openai.caps()
		s.llmCloud = hasAny(have, aiCloudLLMCaps)
		s.embCloud = hasAny(have, aiEmbedTextCaps)
		s.batch = hasAny(have, aiEmbedBatch)
		s.fallbackPrio = r.openaiPriorityIs(aiBackendFallbackPrio)
	}
	return s
}

// modeOf names the effective mode two routes make.
func modeOf(local, cloud bool) string {
	switch {
	case local && cloud:
		return headAIBackendModeOpenAIFallbackLocal
	case local:
		return headAIBackendModeLocal
	case cloud:
		return headAIBackendModeOpenAI
	}
	return headAIBackendModeDisabled
}

// llmMode is the mode the llm ticks read as. A cloud route with no local one
// reads as openai-fallback-local when the openai row has the fallback's
// priority: that is the list HEAD's seed builds for the fallback mode with no
// local URL, and reading it as openai would make a write of openai a no-op
// that leaves the priority at 5.
func (s aiState) llmMode() string {
	if s.llmCloud && !s.llmLocal && s.fallbackPrio {
		return headAIBackendModeOpenAIFallbackLocal
	}
	return modeOf(s.llmLocal, s.llmCloud)
}

// embMode is the mode the embedding ticks read as. A cloud route with no local
// one is openai when embed.text_batch is ticked, and the seed's list for
// openai-fallback-local with no local URL (which ticks embed.text only) when
// it is not, for the reason llmMode gives.
func (s aiState) embMode() string {
	if s.embCloud && !s.embLocal && !s.batch {
		return headAIBackendModeOpenAIFallbackLocal
	}
	return modeOf(s.embLocal, s.embCloud)
}

// UnfoldAIBackend is the ai_backend.* view of the ai_endpoints list: the five
// names, each a JSON string. The URL and the two models are the local-llm
// row's; with no such row the URL is "" and the models are HEAD's defaults. The
// modes are the effective ones the ticks make (see the file comment): a mode is
// local when local-llm carries any of its ticks, cloud when the openai row
// carries any of its own, both being openai-fallback-local, neither disabled.
// The OpenAI row's other fields, and every other endpoint, are ignored.
//
// A list that is not a list of objects, an id or a field the view reads that
// is not a string, capabilities that are not a list of strings, and a repeated
// local-llm or openai id are ErrConvertInput.
func UnfoldAIBackend(endpoints json.RawMessage) (map[string]json.RawMessage, error) {
	rows, err := parseAIRows(endpoints)
	if err != nil {
		return nil, err
	}
	s := rows.state()
	return map[string]json.RawMessage{
		aiBackendEmbeddingMode: mustJSON(s.embMode()),
		aiBackendLLMMode:       mustJSON(s.llmMode()),
		aiBackendLocalBaseURL:  mustJSON(s.url),
		aiBackendLocalEmbModel: mustJSON(s.embed),
		aiBackendLocalLLMModel: mustJSON(s.chat),
	}, nil
}

// FoldAIBackend writes the ai_backend.* values onto the ai_endpoints list prev
// and returns the new list. values is decoded JSON keyed by HEAD name, every
// value a string, and a name it does not hold leaves its part of the list as
// prev has it. A name whose value is the one prev already reads changes
// nothing, and a write that changes nothing returns prev as it is. Arguments
// are in FoldOIDC's order (values, prev).
//
// What each name owns, and the only things the fold writes (see the file
// comment for the mapping):
//
//   - local_base_url: the local-llm row's url. A URL when there is no such row
//     creates it exactly as HEAD's seed does (id local-llm, its label, protocol
//     openai_compat, priority 10, concurrency 4, enabled, the models named or
//     HEAD's defaults) at the front of the list; "" removes it.
//   - local_llm_model, local_embedding_model: the row's chat_model and
//     embed_model.
//   - llm_mode, embedding_mode: the ticks the mode owns on the local-llm row
//     and on the openai row, and the openai row's priority. Nothing else on
//     the openai row is written, it is never created or removed, and its
//     auth_ref is never read.
//
// Every other endpoint is carried over byte for byte. In an endpoint that
// changes, every field the fold does not own keeps its value (its keys come
// back in sorted order); capabilities no mode owns stay ticked.
//
// What is refused with ErrNotRepresentable, none of it echoing a value: a
// mode that is not one of disabled, local, openai and openai-fallback-local;
// a local mode with no local-llm row ("set the local server URL first") and a
// cloud mode with no openai row ("set an OpenAI key first"); a URL that is
// blank, does not parse, embeds credentials, or is another endpoint's of the
// same protocol; a model named when there is no local-llm row to hold it,
// unless it is HEAD's default; an empty local_embedding_model that would
// change the row. A name that is not an ai_backend name, or a value that is
// not a string, and a prev that is malformed (as UnfoldAIBackend reads it),
// are ErrConvertInput.
func FoldAIBackend(values map[string]json.RawMessage, prev json.RawMessage) (json.RawMessage, error) {
	named := map[string]string{}
	for k, raw := range values {
		if !slices.Contains(aiBackendNames, k) {
			return nil, aiBad("not an ai_backend fold name")
		}
		var s string
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &s) != nil {
			return nil, aiBad("want a string")
		}
		named[k] = s
	}
	for _, k := range []string{aiBackendEmbeddingMode, aiBackendLLMMode} {
		if m, ok := named[k]; ok && !slices.Contains([]string{headAIBackendModeDisabled, headAIBackendModeLocal, headAIBackendModeOpenAI, headAIBackendModeOpenAIFallbackLocal}, m) {
			return nil, aiUnrep("not a backend mode")
		}
	}
	rows, err := parseAIRows(prev)
	if err != nil {
		return nil, err
	}
	cur := rows.state()
	defChat, defEmbed := aiDefaultModels()
	removed := map[*aiEntry]bool{}
	var created *aiEntry

	// The URL, which makes or removes the local-llm row.
	urlCleared := false
	if u, ok := named[aiBackendLocalBaseURL]; ok {
		switch {
		case u == "":
			if rows.local != nil {
				removed[rows.local] = true
				rows.local = nil
				urlCleared = true
			}
		case strings.TrimSpace(u) == "":
			return nil, aiUnrep("the local server URL is blank")
		default:
			protocol := headProtocolOpenAICompat
			if rows.local != nil {
				if protocol, err = rows.local.str("protocol"); err != nil {
					return nil, err
				}
			}
			if err := aiCheckURL(rows, u, protocol); err != nil {
				return nil, err
			}
			if rows.local != nil {
				if u != cur.url {
					rows.local.set("url", u)
				}
				break
			}
			// HEAD's row with HEAD's default models; the models named in this
			// write, and the modes, are applied to it below like any row's.
			b, err := marshalNoEscape(headAIEndpoint{
				ID: headAIEndpointIDLocalLLM, Label: "Local LLM (migrated from ai_backend.local_base_url)",
				Protocol: headProtocolOpenAICompat, URL: u, ChatModel: defChat, EmbedModel: defEmbed,
				Priority: 10, Concurrency: 4, Enabled: true, Capabilities: []string{},
			})
			if err != nil {
				return nil, aiBad("the local-llm endpoint does not encode")
			}
			created = &aiEntry{raw: b, id: headAIEndpointIDLocalLLM, dirty: true}
			rows.local = created
		}
	}

	// The models, onto the row that holds them.
	for _, k := range []string{aiBackendLocalLLMModel, aiBackendLocalEmbModel} {
		v, ok := named[k]
		if !ok {
			continue
		}
		field, was, dflt := "chat_model", cur.chat, defChat
		if k == aiBackendLocalEmbModel {
			field, was, dflt = "embed_model", cur.embed, defEmbed
		}
		if rows.local == nil {
			// Nowhere to live: only HEAD's default (what the list reads), or
			// the removed row's own value, is not new information.
			if v == dflt || (urlCleared && v == was) {
				continue
			}
			return nil, aiUnrep("set the local server URL first")
		}
		now, err := rows.local.str(field)
		if err != nil {
			return nil, err
		}
		if v == now {
			continue
		}
		if k == aiBackendLocalEmbModel && v == "" {
			return nil, aiUnrep("the local embedding model is empty")
		}
		rows.local.set(field, v)
	}

	// The modes, against what the list reads now.
	if m, ok := named[aiBackendLLMMode]; ok {
		w := rows.state()
		if m != w.llmMode() {
			needLocal, needCloud := modeLocal(m), modeCloud(m)
			if err := aiNeedRows(rows, needLocal, needCloud); err != nil {
				return nil, err
			}
			if rows.local != nil {
				if needLocal {
					rows.local.tick(aiLocalLLMCaps)
				} else {
					rows.local.untick(aiLocalLLMCaps)
				}
			}
			if rows.openai != nil {
				if needCloud {
					rows.openai.tick(aiCloudLLMCaps)
				} else {
					rows.openai.untick(aiCloudLLMCaps)
				}
				fallback := m == headAIBackendModeOpenAIFallbackLocal
				if wasFallback := w.llmMode() == headAIBackendModeOpenAIFallbackLocal; wasFallback != fallback {
					if fallback {
						rows.openai.set("priority", aiBackendFallbackPrio)
					} else {
						rows.openai.set("priority", aiBackendOpenAIPriority)
					}
				}
			}
		}
	}
	if m, ok := named[aiBackendEmbeddingMode]; ok {
		w := rows.state()
		if m != w.embMode() {
			needLocal, needCloud := modeLocal(m), modeCloud(m)
			if err := aiNeedRows(rows, needLocal, needCloud); err != nil {
				return nil, err
			}
			if rows.local != nil {
				if needLocal {
					rows.local.tick(aiEmbedTextCaps)
				} else {
					rows.local.untick(aiEmbedTextCaps)
				}
			}
			if rows.openai != nil {
				if needCloud {
					rows.openai.tick(aiEmbedTextCaps)
				} else {
					rows.openai.untick(aiEmbedTextCaps)
				}
				if m == headAIBackendModeOpenAI {
					rows.openai.tick(aiEmbedBatch)
				} else {
					rows.openai.untick(aiEmbedBatch)
				}
			}
		}
	}

	// Assemble: prev's elements as they were, the changed ones re-encoded.
	changed := len(removed) > 0 || created != nil
	for _, e := range rows.entries {
		changed = changed || e.dirty
	}
	if !changed {
		if len(bytes.TrimSpace(prev)) == 0 {
			return json.RawMessage(`[]`), nil
		}
		return prev, nil
	}
	var out bytes.Buffer
	out.WriteByte('[')
	n := 0
	emit := func(e *aiEntry) error {
		b := e.raw
		if e.dirty && e != created {
			var err error
			if b, err = marshalNoEscape(e.fields()); err != nil {
				return aiBad("an endpoint does not encode")
			}
		}
		if n > 0 {
			out.WriteByte(',')
		}
		n++
		out.Write(b)
		return nil
	}
	if created != nil {
		// The seed puts the local row first. Its ticks, if a mode gave it any,
		// were written into fields().
		if created.m != nil {
			created.raw, _ = marshalNoEscape(created.m)
		}
		if err := emit(created); err != nil {
			return nil, err
		}
	}
	for _, e := range rows.entries {
		if removed[e] {
			continue
		}
		if err := emit(e); err != nil {
			return nil, err
		}
	}
	out.WriteByte(']')
	return json.RawMessage(out.Bytes()), nil
}

func modeLocal(m string) bool {
	return m == headAIBackendModeLocal || m == headAIBackendModeOpenAIFallbackLocal
}

func modeCloud(m string) bool {
	return m == headAIBackendModeOpenAI || m == headAIBackendModeOpenAIFallbackLocal
}

// aiNeedRows refuses a mode whose rows are not in the list.
func aiNeedRows(rows *aiRows, local, cloud bool) error {
	if local && rows.local == nil {
		return aiUnrep("set the local server URL first")
	}
	if cloud && rows.openai == nil {
		return aiUnrep("set an OpenAI key first")
	}
	return nil
}

// aiCheckURL refuses a URL validateAIEndpoints would: one that embeds
// credentials, or that another endpoint of the same protocol already has. The
// local-llm row itself is not another endpoint.
func aiCheckURL(rows *aiRows, u, protocol string) error {
	p, err := url.Parse(u)
	if err != nil {
		return aiUnrep("the local server URL does not parse")
	}
	if p.User != nil {
		return aiUnrep("the local server URL must not embed credentials")
	}
	want := normalizeAIURL(u)
	for _, e := range rows.entries {
		if e == rows.local {
			continue
		}
		proto, err := e.str("protocol")
		if err != nil {
			return err
		}
		other, err := e.str("url")
		if err != nil {
			return err
		}
		if proto == protocol && other != "" && normalizeAIURL(other) == want {
			return aiUnrep("another endpoint already has this URL")
		}
	}
	return nil
}

// normalizeAIURL is config's normalizeEndpointURL: trimmed, case-folded,
// without a trailing slash.
func normalizeAIURL(u string) string {
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(u)), "/")
}

// marshalNoEscape is json.Marshal without HTML escaping, so a URL with an &
// keeps its bytes (RawMessage values are compacted but not escaped either).
func marshalNoEscape(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}
