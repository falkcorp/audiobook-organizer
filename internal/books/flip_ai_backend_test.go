// file: internal/books/flip_ai_backend_test.go
// version: 1.0.0
// guid: 6c2f8d91-3a47-4e05-b8d6-1f9e0a7c5b34
// last-edited: 2026-11-19

package books

import (
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	coreconfig "github.com/falkcorp/mediacore/config"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/books/appconfig"
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

	// A reload sees the same: a new Engine over the same core store and residual
	// blob builds its projection from what was stored, as the next boot does.
	in := c.mod.gapFillIn.Load()
	require.NotNil(t, in, "the boot bound no Facade")
	fresh := appconfig.New(in.acc, config.Project,
		appconfig.WithBase(func() config.Config { return appConfigBase(c.mod.dbPath) }),
		appconfig.WithResidual(&configResidual{store: c.store, acc: in.acc}))
	require.NoError(t, fresh.Start(t.Context()))
	require.Equal(t, want, aiBackendValues(t, fresh), "a rebuilt projection lost the save")
	reloaded := fresh.Snapshot()
	require.Equal(t, snap.AIBackend, reloaded.AIBackend)
	require.Equal(t, "local", reloaded.AIBackend.EmbeddingMode)
}

// TestUnchangedSave_OverTheRealCoreStore: a save that sends back every value the
// page just read is accepted whole and changes nothing: no generation moved, no
// value different afterwards. The one refusal is a key an operator layer forces
// (database_path: core.storage.path is never stored), which core refuses even with
// the value it holds; the page drops such a key from its diff, so the test names
// the set and drops it the same way. A new read-only key fails here until it is
// named, which is when the page needs to know about it too.
func TestUnchangedSave_OverTheRealCoreStore(t *testing.T) {
	bootFlippedFresh(t)
	f := config.Current()
	require.NotNil(t, f)
	// Core keeps a fail-closed key's value as a stored row even when it equals the
	// default (05-config 1.3.5: the stored row is what the fail-closed rule weighs
	// against an operator value), so sending one back writes its row and moves the
	// generation. That is by design and in the restrictive direction; they are
	// checked below on their own, and this set is exactly those two.
	failClosed := map[string]bool{"enable_auth": true, "basic_auth_enabled": true}
	set := map[string]json.RawMessage{}
	for _, k := range f.View().Keys {
		if k.Class != config.KeyClassKey || k.Value == nil || k.LockedBy != "" || failClosed[k.Name] {
			continue
		}
		set[k.Name] = k.Value
	}
	require.NotEmpty(t, set)
	sent := maps.Clone(set)
	gen := f.Generation()
	var readOnly []string
	for range 3 {
		_, err := f.Update(t.Context(), settingsAdmin, config.Update{Set: set})
		if err == nil {
			break
		}
		require.True(t, errors.Is(err, config.ErrKeyReadOnly) || errors.Is(err, coreconfig.ErrUnknownKey), "an unchanged save was refused: %v", err)
		names := err.Error()[strings.LastIndex(err.Error(), ": ")+2:]
		for _, n := range strings.Split(names, ", ") {
			require.Contains(t, set, n)
			delete(set, n)
			delete(sent, n)
			readOnly = append(readOnly, n)
		}
	}
	// maintenance.* maps to core.tasks.windows.nightly.*, which core's tasks module
	// registers; the composed boot has none, so core refuses them as unknown while
	// the View still lists them from the projection's defaults. A production boot
	// registers them.
	slices.Sort(readOnly)
	require.Equal(t, []string{"database_path", "maintenance.enabled", "maintenance.window_end", "maintenance.window_start"}, readOnly,
		"the set of keys core refuses to rewrite changed")
	require.Equal(t, gen, f.Generation(), "an unchanged save moved the generation")
	for _, k := range f.View().Keys {
		if want, ok := sent[k.Name]; ok {
			require.JSONEq(t, string(want), string(k.Value), k.Name)
		}
	}
	// A fail-closed key sent back unchanged keeps its value.
	for name := range failClosed {
		var want json.RawMessage
		for _, k := range f.View().Keys {
			if k.Name == name {
				want = k.Value
			}
		}
		require.NotNil(t, want, name)
		_, err := f.Update(t.Context(), settingsAdmin, config.Update{Set: map[string]json.RawMessage{name: want}})
		require.NoError(t, err, name)
		for _, k := range f.View().Keys {
			if k.Name == name {
				require.JSONEq(t, string(want), string(k.Value), name)
			}
		}
	}
}
