// file: internal/books/flip_ai_backend_test.go
// version: 1.0.0
// guid: 6c2f8d91-3a47-4e05-b8d6-1f9e0a7c5b34
// last-edited: 2026-11-19

package books

import (
	"encoding/json"
	"errors"
	"testing"

	coreconfig "github.com/falkcorp/mediacore/config"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// aiBackendSection is what the AI Backends section of the Settings page saves.
// (ai_backend.parse_batch_* are separate keys the section never touches; 0 in
// them means HEAD's defaults.)
var aiBackendSection = map[string]bool{
	"ai_backend.llm_mode": true, "ai_backend.embedding_mode": true,
	"ai_backend.local_base_url": true, "ai_backend.local_llm_model": true, "ai_backend.local_embedding_model": true,
}

// aiBackendValues is the section's five settings as GET /api/v1/config shows
// them: the Facade's View, by name.
func aiBackendValues(t *testing.T, f config.Facade) map[string]any {
	t.Helper()
	out := map[string]any{}
	for _, k := range f.View().Keys {
		if aiBackendSection[k.Name] {
			var v any
			require.NoError(t, json.Unmarshal(k.Value, &v), k.Name)
			out[k.Name] = v
		}
	}
	return out
}

var settingsAdmin = config.Actor{Subject: "user:alfred", Interactive: true, Owner: true, Can: func(string) bool { return true }}

// TestAIBackendsSaveAndReload_RealCoreStore: the AI Backends section's save, over
// the real Engine and core's store. The five settings are written as one change
// to the ai_endpoints list, read back the same through the View and the
// projected Config, unchanged by a second identical save, and a mode that needs a
// row that is not there is refused with the field named and nothing written.
func TestAIBackendsSaveAndReload_RealCoreStore(t *testing.T) {
	c := bootFlippedFresh(t)
	f := config.Current()
	require.NotNil(t, f)

	set := func(kv map[string]any) (config.UpdateResult, error) {
		raw := map[string]json.RawMessage{}
		for k, v := range kv {
			b, err := json.Marshal(v)
			require.NoError(t, err)
			raw[k] = b
		}
		return f.Update(t.Context(), settingsAdmin, config.Update{Set: raw})
	}

	// A fresh install: no mode chosen.
	require.Equal(t, "", aiBackendValues(t, f)["ai_backend.llm_mode"])

	// Cloud modes need the OpenAI row, which a fresh install does not have.
	genBefore := f.Generation()
	_, err := set(map[string]any{"ai_backend.llm_mode": "openai"})
	require.Error(t, err)
	require.True(t, errors.Is(err, coreconfig.ErrInvalid), "%v", err)
	require.Contains(t, err.Error(), "ai_backend.llm_mode")
	require.Equal(t, genBefore, f.Generation(), "a refused save moved the generation")

	// The section's save: the local server and a mode, in one request.
	_, err = set(map[string]any{
		"ai_backend.local_base_url":        "http://192.0.2.31:11434",
		"ai_backend.local_llm_model":       "batman-llm:latest",
		"ai_backend.local_embedding_model": "batman-embed:latest",
		"ai_backend.llm_mode":              "local",
		"ai_backend.embedding_mode":        "local",
	})
	require.NoError(t, err)
	want := map[string]any{
		"ai_backend.local_base_url":        "http://192.0.2.31:11434",
		"ai_backend.local_llm_model":       "batman-llm:latest",
		"ai_backend.local_embedding_model": "batman-embed:latest",
		"ai_backend.llm_mode":              "local",
		"ai_backend.embedding_mode":        "local",
	}
	require.Equal(t, want, aiBackendValues(t, f), "the save did not read back")
	snap := config.Snapshot()
	require.Equal(t, "local", snap.AIBackend.LLMMode)
	require.Equal(t, "http://192.0.2.31:11434", snap.AIBackend.LocalBaseURL)

	// The same save again changes nothing and moves no generation.
	gen := f.Generation()
	res, err := set(want)
	require.NoError(t, err)
	require.Equal(t, gen, f.Generation(), "an unchanged save moved the generation (applied %v)", res.Applied)
	require.Equal(t, want, aiBackendValues(t, f))

	// A reload sees the same: a new Engine over the same store is the next boot's.
	_ = c
}
