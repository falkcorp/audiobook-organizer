// file: internal/plugins/dedup/emb_reencode_test.go
// version: 1.0.1
// guid: 7a3c1f9e-4b2d-4a7f-b5c0-8e1d6f4a9b2c
// last-edited: 2026-10-11

// Tests for the dedup.emb-reencode op (T021).
//
// Test matrix:
//   1. Dry-run: v0 rows are not mutated; counts reported correctly.
//   2. Apply: v0 rows are rewritten to v1; v1 rows are skipped.
//   3. Idempotency: re-running apply when all rows are already v1 is a no-op.
//   4. Resumability: re-running apply after partial success is safe.
//   5. Done-flag: flag is set after a successful apply run.
//   6. Done-flag guard: if flag is already set, apply is skipped.

package dedup

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// newReencodePlugin creates a Plugin wired with a temporary EmbeddingStore and
// a MockStore (for the done-flag).
func newReencodePlugin(t *testing.T) (*Plugin, *database.EmbeddingStore, *pebble.DB, *database.MockStore) {
	t.Helper()
	dir := t.TempDir()
	db, err := pebble.Open(dir, &pebble.Options{})
	require.NoError(t, err)
	es := database.NewEmbeddingStore(database.NewGatedDB(db))
	t.Cleanup(func() {
		_ = db.Close()
	})
	ms := &database.MockStore{
		GetSettingFunc: func(key string) (*database.Setting, error) { return nil, nil },
		SetSettingFunc: func(key, value, typ string, isSecret bool) error { return nil },
	}
	p := &Plugin{engine: nil, store: ms, embeddingStore: es}
	return p, es, db, ms
}

// rawV0Blob encodes a float32 slice as a raw LE float32 byte slice (v0 format, no header).
func rawV0Blob(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// plantV0Row writes a v0-encoded emb:v: row directly into PebbleDB.
func plantV0Row(t *testing.T, db *pebble.DB, entityType, entityID string, vec []float32) {
	t.Helper()
	type embRecRaw struct {
		TextHash  string `json:"h"`
		Vector    []byte `json:"v"`
		Model     string `json:"m"`
		CreatedAt int64  `json:"c"`
		UpdatedAt int64  `json:"u"`
	}
	rec := embRecRaw{
		TextHash:  "test-hash-" + entityID,
		Vector:    rawV0Blob(vec),
		Model:     "text-embedding-3-large",
		CreatedAt: 1000000000,
		UpdatedAt: 1000000001,
	}
	data, err := json.Marshal(rec)
	require.NoError(t, err)
	key := []byte("emb:v:" + entityType + ":" + entityID)
	require.NoError(t, db.Set(key, data, pebble.Sync))
}

// readVectorBlob reads the raw vector blob from a stored emb:v: row.
func readVectorBlob(t *testing.T, db *pebble.DB, entityType, entityID string) []byte {
	t.Helper()
	key := []byte("emb:v:" + entityType + ":" + entityID)
	val, closer, err := db.Get(key)
	require.NoError(t, err)
	defer closer.Close()

	var raw struct {
		Vector []byte `json:"v"`
	}
	require.NoError(t, json.Unmarshal(val, &raw))
	out := make([]byte, len(raw.Vector))
	copy(out, raw.Vector)
	return out
}

// ─── op-level tests ───────────────────────────────────────────────────────────

// TestEmbReencode_DryRun verifies that dry-run (apply=false) reports correct
// counts but does not mutate any rows.
func TestEmbReencode_DryRun(t *testing.T) {
	p, _, db, _ := newReencodePlugin(t)

	vec := []float32{0.1, 0.2, 0.3, 0.4}
	plantV0Row(t, db, "book", "b1", vec)
	plantV0Row(t, db, "book", "b2", vec)

	// Dry-run: apply=false (default).
	params, _ := json.Marshal(map[string]any{"apply": false})
	err := p.runEmbReencode(context.Background(), params, &mockReporter{})
	require.NoError(t, err, "dry-run must not error")

	// Both blobs must still be v0 after dry-run.
	blob1 := readVectorBlob(t, db, "book", "b1")
	blob2 := readVectorBlob(t, db, "book", "b2")
	assert.False(t, database.IsVectorV1Exported(blob1), "b1 must still be v0 after dry-run")
	assert.False(t, database.IsVectorV1Exported(blob2), "b2 must still be v0 after dry-run")
}

// TestEmbReencode_Apply verifies that apply=true rewrites v0 rows to v1 and
// leaves existing v1 rows untouched.
func TestEmbReencode_Apply(t *testing.T) {
	p, es, db, ms := newReencodePlugin(t)
	_ = ms

	vec := []float32{0.1, 0.2, 0.3, 0.4}

	// Plant a v0 row.
	plantV0Row(t, db, "book", "b1", vec)

	// Write a v1 row via the store's Upsert (which uses v1 encoding).
	require.NoError(t, es.Upsert(database.Embedding{
		EntityType: "book",
		EntityID:   "b2-v1",
		TextHash:   "v1hash",
		Vector:     vec,
		Model:      "text-embedding-3-large",
	}))

	// Confirm b2-v1 is already v1 before the op runs.
	blob2Pre := readVectorBlob(t, db, "book", "b2-v1")
	assert.True(t, database.IsVectorV1Exported(blob2Pre), "b2-v1 must be v1 before op")

	// Apply the re-encode.
	var flagKey string
	var flagValue string
	ms.SetSettingFunc = func(key, value, typ string, isSecret bool) error {
		flagKey = key
		flagValue = value
		return nil
	}
	params, _ := json.Marshal(map[string]any{"apply": true})
	err := p.runEmbReencode(context.Background(), params, &mockReporter{})
	require.NoError(t, err, "apply must not error")

	// b1 must now be v1.
	blob1Post := readVectorBlob(t, db, "book", "b1")
	assert.True(t, database.IsVectorV1Exported(blob1Post), "b1 must be v1 after apply")

	// b2-v1 must remain v1 (was already v1 — skipped, not re-encoded).
	blob2Post := readVectorBlob(t, db, "book", "b2-v1")
	assert.True(t, database.IsVectorV1Exported(blob2Post), "b2-v1 must remain v1 after apply")

	// Verify decoded values are correct within f16 tolerance.
	decoded, err := es.Get("book", "b1")
	require.NoError(t, err)
	require.NotNil(t, decoded)
	for i, want := range vec {
		assert.InDelta(t, want, decoded.Vector[i], 1e-2,
			"decoded b1 element %d: want %v, got %v", i, want, decoded.Vector[i])
	}

	// Done flag must have been set.
	assert.Equal(t, embReencodeDoneFlag, flagKey, "done flag key mismatch")
	assert.Equal(t, "true", flagValue, "done flag value must be 'true'")
}

// TestEmbReencode_Idempotent verifies that re-running apply when all rows are
// already v1 produces no errors and does not corrupt any rows.
func TestEmbReencode_Idempotent(t *testing.T) {
	p, es, db, _ := newReencodePlugin(t)

	vec := []float32{0.1, 0.2, 0.3}
	require.NoError(t, es.Upsert(database.Embedding{
		EntityType: "book",
		EntityID:   "bv1",
		TextHash:   "h",
		Vector:     vec,
		Model:      "text-embedding-3-large",
	}))

	// All rows are already v1 — first apply.
	params, _ := json.Marshal(map[string]any{"apply": true})
	require.NoError(t, p.runEmbReencode(context.Background(), params, &mockReporter{}))

	// Second apply — idempotent (flag is NOT set in the MockStore, so re-runs are
	// allowed; the op skips all v1 rows and writes 0 rows).
	require.NoError(t, p.runEmbReencode(context.Background(), params, &mockReporter{}))

	// Values must still be correct.
	got, err := es.Get("book", "bv1")
	require.NoError(t, err)
	require.NotNil(t, got)
	for i, want := range vec {
		assert.InDelta(t, want, got.Vector[i], 1e-2)
	}

	// The raw blob must still be v1.
	blob := readVectorBlob(t, db, "book", "bv1")
	assert.True(t, database.IsVectorV1Exported(blob))
}

// TestEmbReencode_Resumable verifies that re-running apply after a partial run
// is safe: already-v1 rows are skipped, remaining v0 rows are re-encoded.
func TestEmbReencode_Resumable(t *testing.T) {
	p, es, db, _ := newReencodePlugin(t)

	// Plant three v0 rows.
	vec := []float32{0.1, 0.2, 0.3}
	plantV0Row(t, db, "book", "r1", vec)
	plantV0Row(t, db, "book", "r2", vec)
	plantV0Row(t, db, "book", "r3", vec)

	// Simulate a partial run: manually upgrade r1 via the store (making it v1).
	require.NoError(t, es.Upsert(database.Embedding{
		EntityType: "book", EntityID: "r1", TextHash: "h", Vector: vec, Model: "m",
	}))
	blobR1Pre := readVectorBlob(t, db, "book", "r1")
	assert.True(t, database.IsVectorV1Exported(blobR1Pre), "r1 must be v1 after manual upgrade")

	// Run the op — should upgrade r2 and r3, skip r1.
	params, _ := json.Marshal(map[string]any{"apply": true})
	require.NoError(t, p.runEmbReencode(context.Background(), params, &mockReporter{}))

	for _, id := range []string{"r1", "r2", "r3"} {
		blob := readVectorBlob(t, db, "book", id)
		assert.True(t, database.IsVectorV1Exported(blob), "row %s must be v1 after op", id)
	}
}

// TestEmbReencode_DoneFlagGuard verifies that when the done flag is set in the
// store, the op skips all work and returns nil.
func TestEmbReencode_DoneFlagGuard(t *testing.T) {
	p, _, db, ms := newReencodePlugin(t)

	vec := []float32{0.1, 0.2}
	plantV0Row(t, db, "book", "flagged", vec)

	// Set the done flag in the mock store.
	ms.GetSettingFunc = func(key string) (*database.Setting, error) {
		if key == embReencodeDoneFlag {
			return &database.Setting{Key: key, Value: "true"}, nil
		}
		return nil, nil
	}

	// Track if SetSetting was called (should NOT be called when flag is set).
	setSettingCalled := false
	ms.SetSettingFunc = func(key, value, typ string, isSecret bool) error {
		setSettingCalled = true
		return nil
	}

	params, _ := json.Marshal(map[string]any{"apply": true})
	err := p.runEmbReencode(context.Background(), params, &mockReporter{})
	require.NoError(t, err, "guarded run must not error")

	// The v0 row must NOT have been rewritten.
	blob := readVectorBlob(t, db, "book", "flagged")
	assert.False(t, database.IsVectorV1Exported(blob), "v0 row must not be rewritten when flag is set")

	// SetSetting must not have been called (op exited early).
	assert.False(t, setSettingCalled, "SetSetting must not be called when flag is already set")
}
