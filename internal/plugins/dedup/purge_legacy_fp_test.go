// file: internal/plugins/dedup/purge_legacy_fp_test.go
// version: 1.1.0
// guid: 9e4b7f3a-2c1d-4e8b-b6a5-0d7c9e2f5b8a
// last-edited: 2026-07-06

// Table-driven tests for the dedup.purge-legacy-fp-candidates op (T015).
//
// Test matrix:
//   1. stale row (exact layer, sim=1.0, created pre-cutover, no matching file hash)
//      → dry-run: status unchanged; apply: status becomes "stale-fp"
//   2. genuine hash-dupe row (exact layer, sim=1.0, pre-cutover, MATCHING file hash)
//      → always kept unchanged
//   3. acoustid-layer row (sim=1.0, pre-cutover)
//      → always kept unchanged (excluded from purge by design)
//   4. post-cutover row (exact layer, sim=1.0, created AFTER cutover)
//      → always kept unchanged
//   5. dry-run: stale row is NOT marked (no mutations)
//
// These tests spin up a real EmbeddingStore backed by a temporary PebbleDB and
// a hand-written MockStore for the main store (GetAllBookFiles + GetSetting +
// SetSetting). No network, no full Engine — the purge logic is self-contained.

package dedup

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── test helpers ────────────────────────────────────────────────────────────

// newTestEmbeddingStorePurge creates an isolated EmbeddingStore backed by a
// temporary PebbleDB directory.
func newTestEmbeddingStorePurge(t *testing.T) *database.EmbeddingStore {
	t.Helper()
	dir := t.TempDir()
	db, err := pebble.Open(dir, &pebble.Options{})
	require.NoError(t, err)
	// Access via the exported constructor using shared-DB mode.
	// We own the DB but the store will not close it (owned=false via NewEmbeddingStore).
	// Use the test-only helper from the database package instead.
	_ = db // used via the constructor below
	es := database.NewEmbeddingStore(db)
	t.Cleanup(func() { _ = db.Close() })
	return es
}

// mockReporter is a minimal sdk.Reporter for tests.
// sdk.Reporter is an alias for registry.Reporter which requires Logger() *slog.Logger.
type mockReporter struct{}

var _ sdk.Reporter = (*mockReporter)(nil)

func (m *mockReporter) UpdateProgress(current, total int, message string) error { return nil }
func (m *mockReporter) IsCanceled() bool                                         { return false }
func (m *mockReporter) Logger() *slog.Logger                                     { return slog.Default() }
func (m *mockReporter) Log(level slog.Level, message string, attrs ...slog.Attr) error {
	return nil
}
func (m *mockReporter) Checkpoint(state any) error { return nil }
func (m *mockReporter) RunPhase(ctx context.Context, name string, fn func(context.Context, sdk.Reporter) error) error {
	return fn(ctx, m)
}
func (m *mockReporter) Trigger(ctx context.Context, eventName string, payload any) error {
	return nil
}
func (m *mockReporter) SetCurrentItem(label string) {}

// buildPlugin wires a Plugin with the given stores.
func buildPlugin(t *testing.T, es *database.EmbeddingStore, ms *database.MockStore) *Plugin {
	t.Helper()
	return &Plugin{
		engine:         nil, // not needed for purge op
		store:          ms,
		embeddingStore: es,
	}
}

// ─── table-driven tests ───────────────────────────────────────────────────────

// TestPurgeLegacyFP is the main table-driven test for the purge op.
// Each sub-test plants a specific scenario and asserts the final status.
func TestPurgeLegacyFP(t *testing.T) {
	sim100 := 1.0
	sim095 := 0.95

	// We use a custom cutover that is in the FUTURE relative to the "pre-cutover"
	// candidates, so that we can insert candidates with time.Now() and still have
	// them fall before the cutover. We do this by setting cutover = now+1h and
	// "post-cutover" candidates use a plantAfter flag implemented differently.
	//
	// Simpler approach: set cutover to a fixed future time (year 2099) for most
	// test cases so that any candidate inserted now is "pre-cutover". For the
	// post-cutover test case, use cutover = 1 year ago so that now is post-cutover.

	const (
		futureCutover = "2099-01-01T00:00:00Z" // all now-inserted rows are pre-cutover
		pastCutover   = "2020-01-01T00:00:00Z"  // all now-inserted rows are post-cutover
	)

	tests := []struct {
		name           string
		layer          string
		similarity     *float64
		cutoverParam   string // RFC3339 cutover to pass in params
		fileHashA      string // BookFile FileHash for entity A (empty = no files)
		fileHashB      string // BookFile FileHash for entity B (empty = no files)
		sharedHash     bool   // if true, A and B share the same hash (genuine dupe)
		apply          bool
		wantStatus     string // expected final status
	}{
		{
			name:         "stale_row_apply",
			layer:        "exact",
			similarity:   &sim100,
			cutoverParam: futureCutover,
			fileHashA:    "hashA-unique",
			fileHashB:    "hashB-unique",
			sharedHash:   false,
			apply:        true,
			wantStatus:   "stale-fp",
		},
		{
			name:         "stale_row_dry_run",
			layer:        "exact",
			similarity:   &sim100,
			cutoverParam: futureCutover,
			fileHashA:    "hashA-dry",
			fileHashB:    "hashB-dry",
			sharedHash:   false,
			apply:        false,
			wantStatus:   "pending", // dry-run: no mutation
		},
		{
			name:         "genuine_hash_dupe_kept",
			layer:        "exact",
			similarity:   &sim100,
			cutoverParam: futureCutover,
			fileHashA:    "shared-hash",
			fileHashB:    "shared-hash", // same hash on both sides → genuine dupe
			sharedHash:   true,
			apply:        true,
			wantStatus:   "pending", // NOT stale — genuine hash match
		},
		{
			name:         "acoustid_layer_kept",
			layer:        "acoustid",
			similarity:   &sim100,
			cutoverParam: futureCutover,
			fileHashA:    "",
			fileHashB:    "",
			sharedHash:   false,
			apply:        true,
			wantStatus:   "pending", // acoustid layer excluded by design
		},
		{
			name:         "post_cutover_kept",
			layer:        "exact",
			similarity:   &sim100,
			cutoverParam: pastCutover, // cutover is in the past → now-inserted row is post-cutover
			fileHashA:    "hashA-post",
			fileHashB:    "hashB-post",
			sharedHash:   false,
			apply:        true,
			wantStatus:   "pending", // post-cutover → not stale
		},
		{
			name:         "non_perfect_sim_kept",
			layer:        "exact",
			similarity:   &sim095, // sim != 1.0
			cutoverParam: futureCutover,
			fileHashA:    "hashA-095",
			fileHashB:    "hashB-095",
			sharedHash:   false,
			apply:        true,
			wantStatus:   "pending", // sim != 1.0 → not a legacy exact candidate
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			es := newTestEmbeddingStorePurge(t)

			// Build file-hash fixtures for the mock store.
			var bookFiles []database.BookFileCore
			if tc.fileHashA != "" {
				bookFiles = append(bookFiles, database.BookFileCore{
					ID:       "file-a",
					BookID:   "book-a",
					FileHash: tc.fileHashA,
				})
			}
			if tc.fileHashB != "" {
				// If sharedHash, use the same hash string for B's BookFile so
				// hasFileHashMatch will find a match.
				bHash := tc.fileHashB
				if tc.sharedHash {
					bHash = tc.fileHashA // same hash → genuine dupe
				}
				bookFiles = append(bookFiles, database.BookFileCore{
					ID:       "file-b",
					BookID:   "book-b",
					FileHash: bHash,
				})
			}

			ms := &database.MockStore{
				GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) {
					return bookFiles, nil
				},
				GetSettingFunc: func(key string) (*database.Setting, error) {
					return nil, nil // flag not set
				},
				SetSettingFunc: func(key, value, typ string, isSecret bool) error {
					return nil
				},
			}

			// Plant the candidate.
			cand := database.DedupCandidate{
				EntityType: "book",
				EntityAID:  "book-a",
				EntityBID:  "book-b",
				Layer:      tc.layer,
				Similarity: tc.similarity,
				Status:     "pending",
			}
			require.NoError(t, es.UpsertCandidate(cand))

			// Build and run the op.
			p := buildPlugin(t, es, ms)
			params, err := json.Marshal(purgeLegacyFPParams{
				Apply:       tc.apply,
				CutoverDate: tc.cutoverParam,
			})
			require.NoError(t, err)

			err = p.runPurgeLegacyFP(context.Background(), params, &mockReporter{})
			require.NoError(t, err)

			// Assert final status.
			candidates, _, err := es.ListCandidates(database.CandidateFilter{Limit: 10})
			require.NoError(t, err)
			require.Len(t, candidates, 1, "should have exactly one candidate")
			assert.Equal(t, tc.wantStatus, candidates[0].Status,
				"final candidate status mismatch in %s", tc.name)
		})
	}
}

// TestPurgeLegacyFP_FlagSkip verifies that when the done-flag is already set
// and apply=true is requested, the op returns immediately without marking rows.
func TestPurgeLegacyFP_FlagSkip(t *testing.T) {
	sim100 := 1.0
	es := newTestEmbeddingStorePurge(t)

	ms := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) { return nil, nil },
		GetSettingFunc: func(key string) (*database.Setting, error) {
			if key == purgeLegacyFPDoneFlag {
				return &database.Setting{Key: key, Value: "true"}, nil // flag set
			}
			return nil, nil
		},
		SetSettingFunc: func(key, value, typ string, isSecret bool) error { return nil },
	}

	// Plant a stale candidate.
	cand := database.DedupCandidate{
		EntityType: "book",
		EntityAID:  "book-a",
		EntityBID:  "book-b",
		Layer:      "exact",
		Similarity: &sim100,
		Status:     "pending",
	}
	require.NoError(t, es.UpsertCandidate(cand))

	p := buildPlugin(t, es, ms)
	params, err := json.Marshal(purgeLegacyFPParams{
		Apply:       true,
		CutoverDate: "2099-01-01T00:00:00Z",
	})
	require.NoError(t, err)

	require.NoError(t, p.runPurgeLegacyFP(context.Background(), params, &mockReporter{}))

	// Row must still be pending — the flag skip prevented any marking.
	candidates, _, err := es.ListCandidates(database.CandidateFilter{Limit: 10})
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	assert.Equal(t, "pending", candidates[0].Status, "flag-skipped run must not mark rows")
}
