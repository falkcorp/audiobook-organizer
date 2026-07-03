// file: internal/plugins/dedup/drain_stale_test.go
// version: 1.0.0
// guid: 84155b4f-53cc-4c81-be5e-7575dd040725
// last-edited: 2026-07-03

// Tests for the dedup.drain-stale op wrapper (DEDUP-1 / CONS-16 / CONS-17).
//
// These wire a REAL dedup.Engine (not the nil-engine buildPlugin helper) over an
// in-memory EmbeddingStore + MockStore, then exercise the op wrapper: dry-run
// writes nothing, apply reclassifies + sets the versioned done-flag, and a
// second apply after the flag is a no-op. OperationDef metadata is asserted too.

package dedup

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildPluginWithEngine wires a Plugin with a real engine over the given stores.
func buildPluginWithEngine(t *testing.T, es *database.EmbeddingStore, ms *database.MockStore) *Plugin {
	t.Helper()
	eng := dedupengine.NewEngine(es, ms, nil, nil, merge.NewService(ms))
	return &Plugin{engine: eng, store: ms, embeddingStore: es}
}

// planBoilerplateCandidate seeds one pending exact candidate whose A-side title
// is boilerplate, so it will always be classified would-purge.
func planBoilerplateCandidate(t *testing.T, es *database.EmbeddingStore) {
	t.Helper()
	require.NoError(t, es.UpsertCandidate(database.DedupCandidate{
		EntityType: "book",
		EntityAID:  "book-a",
		EntityBID:  "book-b",
		Layer:      "exact",
		Status:     "pending",
	}))
}

func drainMockStore(flagSet *bool) *database.MockStore {
	dur := 3600
	books := map[string]*database.Book{
		"book-a": {ID: "book-a", Title: "Opening Credits", Duration: &dur},
		"book-b": {ID: "book-b", Title: "A Real Book", Duration: &dur},
	}
	return &database.MockStore{
		GetBookByIDFunc:   func(id string) (*database.Book, error) { return books[id], nil },
		GetBookFilesFunc:  func(string) ([]database.BookFile, error) { return nil, nil },
		GetSettingFunc: func(key string) (*database.Setting, error) {
			if flagSet != nil && *flagSet && key == drainStaleDoneFlag {
				return &database.Setting{Key: key, Value: "true"}, nil
			}
			return nil, nil
		},
		SetSettingFunc: func(key, value, typ string, isSecret bool) error {
			if key == drainStaleDoneFlag && value == "true" && flagSet != nil {
				*flagSet = true
			}
			return nil
		},
	}
}

// TestDrainStaleOp_Metadata asserts the OperationDef shape.
func TestDrainStaleOp_Metadata(t *testing.T) {
	p := &Plugin{}
	def := p.drainStaleDef()
	assert.Equal(t, "dedup.drain-stale", def.ID)
	assert.Contains(t, def.Capabilities, sdk.CapLibraryRead)
	assert.Contains(t, def.Capabilities, sdk.CapLibraryWrite)

	// Default apply is false (dry-run) when params are empty.
	var params drainStaleParams
	require.NoError(t, json.Unmarshal([]byte(`{}`), &params))
	assert.False(t, params.Apply, "apply must default to false")
}

// TestDrainStaleOp_DryRunWritesNothing asserts apply=false leaves the store as-is.
func TestDrainStaleOp_DryRunWritesNothing(t *testing.T) {
	es := newTestEmbeddingStorePurge(t)
	ms := drainMockStore(nil)
	planBoilerplateCandidate(t, es)

	p := buildPluginWithEngine(t, es, ms)
	params, err := json.Marshal(drainStaleParams{Apply: false})
	require.NoError(t, err)
	require.NoError(t, p.runDrainStale(context.Background(), params, &mockReporter{}))

	cands, _, err := es.ListCandidates(database.CandidateFilter{Limit: 10})
	require.NoError(t, err)
	require.Len(t, cands, 1)
	assert.Equal(t, "pending", cands[0].Status, "dry-run must not mutate candidate status")
}

// TestDrainStaleOp_ApplyReclassifiesAndFlags asserts apply=true reclassifies the
// would-purge row and sets the versioned done-flag; a second apply is a no-op.
func TestDrainStaleOp_ApplyReclassifiesAndFlags(t *testing.T) {
	es := newTestEmbeddingStorePurge(t)
	flag := false
	ms := drainMockStore(&flag)
	planBoilerplateCandidate(t, es)

	p := buildPluginWithEngine(t, es, ms)
	params, err := json.Marshal(drainStaleParams{Apply: true})
	require.NoError(t, err)
	require.NoError(t, p.runDrainStale(context.Background(), params, &mockReporter{}))

	cands, _, err := es.ListCandidates(database.CandidateFilter{Limit: 10})
	require.NoError(t, err)
	require.Len(t, cands, 1)
	assert.Equal(t, "stale-drain", cands[0].Status, "apply must reclassify would-purge row to stale-drain")
	assert.True(t, flag, "apply must set the versioned done-flag")

	// Second apply: flag is set → op returns early without touching the store.
	// Re-plant a fresh pending row and confirm it is NOT reclassified.
	require.NoError(t, es.UpsertCandidate(database.DedupCandidate{
		EntityType: "book",
		EntityAID:  "book-c",
		EntityBID:  "book-d",
		Layer:      "exact",
		Status:     "pending",
	}))
	require.NoError(t, p.runDrainStale(context.Background(), params, &mockReporter{}))

	cands2, _, err := es.ListCandidates(database.CandidateFilter{Status: "pending", Limit: 10})
	require.NoError(t, err)
	// The newly-planted pending row survives untouched — the flag short-circuited.
	found := false
	for _, c := range cands2 {
		if c.EntityAID == "book-c" || c.EntityBID == "book-c" {
			found = true
		}
	}
	assert.True(t, found, "second apply after done-flag must be a no-op (row stays pending)")
}
