// file: internal/books/cutover/rename_aibackend_parity_test.go
// version: 1.0.0
// guid: 91B7C3E4-2D58-4F06-A1E9-5C7D0B3F8A62
// last-edited: 2026-11-19

package cutover

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The parity square. HEAD's seed (headBuildMigratedAIEndpoints, frozen in this
// package) is a function of the config; FoldAIBackend writes a change onto
// the list instead of rebuilding it. For every config A whose modes the list
// can carry and every write B:
//
//	Fold(B, seed(A)) == seed(A with B applied)
//
// up to the order of a row's ticks, or the fold refuses. A refusal is
// expected exactly where B asks for what the list cannot carry (a mode whose
// row the result lacks, a model with no row to live on). The oracle below
// states that independently of the fold's own code.

type aiWrite struct {
	url, llm, emb, chat, embed *string
}

func sp(s string) *string { return &s }

func (w aiWrite) values() map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for k, p := range map[string]*string{
		"local_base_url": w.url, "llm_mode": w.llm, "embedding_mode": w.emb,
		"local_llm_model": w.chat, "local_embedding_model": w.embed,
	} {
		if p != nil {
			out[k] = mustJSON(*p)
		}
	}
	return out
}

func (w aiWrite) apply(a aiCfg) aiCfg {
	if w.url != nil {
		a.url = *w.url
	}
	if w.llm != nil {
		a.llm = *w.llm
	}
	if w.emb != nil {
		a.emb = *w.emb
	}
	if w.chat != nil {
		a.chat = *w.chat
	}
	if w.embed != nil {
		a.embed = *w.embed
	}
	return a
}

// representable: the list can carry the modes and models of a config. With no
// URL there is no row, so no mode a local server serves and no model but HEAD's
// default is stored (TestAIBackend_LossyCases).
func (a aiCfg) representable() bool {
	if a.url == "" && (a.chat != aiDefChat || a.embed != aiDefEmbed) {
		return false
	}
	if a.url == "" && (modeLocal(a.llm) || modeLocal(a.emb)) {
		return false
	}
	if !a.key && (modeCloud(a.llm) || modeCloud(a.emb)) {
		return false
	}
	return true
}

// refused is the oracle: whether B on A asks for what the list cannot carry.
func refused(a aiCfg, w aiWrite) bool {
	r := w.apply(a)
	// A fallback written back after the URL is cleared stays what the cloud
	// route alone reads as, so it is no change and no refusal.
	carried := func(was string, set *string) bool {
		return w.url != nil && *w.url == "" && a.url != "" && a.key &&
			was == headAIBackendModeOpenAIFallbackLocal && *set == was
	}
	if w.llm != nil && !carried(a.llm, w.llm) && ((modeLocal(*w.llm) && r.url == "") || (modeCloud(*w.llm) && !r.key)) {
		return true
	}
	if w.emb != nil && !carried(a.emb, w.emb) && ((modeLocal(*w.emb) && r.url == "") || (modeCloud(*w.emb) && !r.key)) {
		return true
	}
	if r.url == "" {
		// A model has nowhere to live unless it is HEAD's default or the
		// removed row's own.
		cleared := w.url != nil && a.url != ""
		for _, m := range []struct {
			set       *string
			dflt, was string
		}{{w.chat, aiDefChat, a.chat}, {w.embed, aiDefEmbed, a.embed}} {
			if m.set != nil && *m.set != m.dflt && !(cleared && *m.set == m.was) {
				return true
			}
		}
	}
	return false
}

func TestAIBackend_ParitySquare(t *testing.T) {
	urls := []*string{nil, sp(""), sp(aiURL1), sp(aiURL2)}
	modes := []*string{nil, sp("disabled"), sp("local"), sp("openai"), sp("openai-fallback-local")}
	chats := []*string{nil, sp(aiDefChat), sp("joker-chat")}
	embeds := []*string{nil, sp(aiDefEmbed), sp("joker-embed")}
	if testing.Short() {
		// Short runs (make ci, -race) take the models that matter: unnamed,
		// and one that is neither HEAD's default nor anything on the row.
		chats, embeds = []*string{nil, sp("joker-chat")}, []*string{nil, sp("joker-embed")}
	}

	var as []aiCfg
	for _, url := range []string{"", aiURL1} {
		for _, key := range []bool{false, true} {
			for _, llm := range aiModes {
				for _, emb := range aiModes {
					for _, embed := range []string{aiDefEmbed, "gotham-embed"} {
						c := aiCfg{url: url, llm: llm, emb: emb, chat: "gotham-chat", embed: embed, key: key}
						if url == "" {
							c.chat = aiDefChat
						}
						if c.representable() {
							as = append(as, c)
						}
					}
				}
			}
		}
	}
	require.Greater(t, len(as), 40)

	var checked, refusals int
	for _, a := range as {
		prev := aiSeed(t, a)
		for _, u := range urls {
			for _, l := range modes {
				for _, e := range modes {
					for _, c := range chats {
						for _, em := range embeds {
							w := aiWrite{u, l, e, c, em}
							got, err := FoldAIBackend(w.values(), prev)
							if refused(a, w) {
								require.ErrorIs(t, err, ErrNotRepresentable, "A=%+v B=%+v", a, w)
								refusals++
								continue
							}
							require.NoError(t, err, "A=%+v B=%+v", a, w)
							want := aiSeed(t, w.apply(a))
							require.Equal(t, aiNorm(t, want), aiNorm(t, got), "A=%+v B=%s", a, w.values())
							checked++
						}
					}
				}
			}
		}
	}
	t.Logf("%d configs, %d writes equal to HEAD's seed, %d refused", len(as), checked, refusals)
	assert.Greater(t, checked, 5000)
	assert.Greater(t, refusals, 500)
}

// The seed's own invariant, over HEAD's own function: the local row exists iff
// the URL is not empty, so the view agrees on every config the seed builds.
func TestAIBackend_ParityRowExistsIffURL(t *testing.T) {
	for _, url := range []string{"", aiURL1} {
		list := aiSeed(t, aiCfg{url: url, llm: "local", emb: "local", chat: "c", embed: "e", key: true})
		assert.Equal(t, url != "", aiRow(t, list, "local-llm") != nil)
	}
}

// Fold never panics or half-writes on arbitrary names and values.
func TestAIBackend_FoldRejectsWithoutSideEffects(t *testing.T) {
	prev := aiHandList()
	for _, vals := range []map[string]json.RawMessage{
		aiVals("llm_mode", "disabled", "local_embedding_model", ""),
		aiVals("llm_mode", "disabled", "local_base_url", "http://u:p@192.0.2.1/"),
	} {
		got, err := FoldAIBackend(vals, prev)
		require.Error(t, err)
		assert.Nil(t, got)
		assert.True(t, errors.Is(err, ErrNotRepresentable))
	}
}
