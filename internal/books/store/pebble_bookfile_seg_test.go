// file: internal/books/store/pebble_bookfile_seg_test.go
// version: 1.0.1
// guid: 2e4f6a8c-b0d2-4e6f-8a0c-b2d4f6a8c0e2
// last-edited: 2026-11-09

// Tests for T020: AcoustIDSeg0..6 drop from Pebble book_file: stored values.
//
// Test coverage:
//  1. marshalBookFileDropSegs — output JSON has no acoustid_seg* keys.
//  2. Back-compat decode: old-format JSON with all 7 seg fields round-trips
//     correctly via json.Unmarshal (struct fields remain).
//  3. CreateBookFile / UpdateBookFile write path: stored bytes lack segs.
//  4. SweepBookFileSegDrop dry-run: counts correct, flag not set.
//  5. SweepBookFileSegDrop apply: rewrites correct rows, flag set.
//  6. SweepBookFileSegDrop resumable: re-run after apply = 0 rewrites.

package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── 1. marshalBookFileDropSegs unit test ─────────────────────────────────────

func TestMarshalBookFileDropSegs_NilsAllSegs(t *testing.T) {
	f := &BookFile{
		ID:           "01TEST",
		BookID:       "book-1",
		FilePath:     "/tmp/test.m4b",
		AcoustIDSeg0: "AQADtAcSRY",
		AcoustIDSeg1: "AQADtAcSRZ",
		AcoustIDSeg2: "AQADtAcSRA",
		AcoustIDSeg3: "AQADtAcSRB",
		AcoustIDSeg4: "AQADtAcSRC",
		AcoustIDSeg5: "AQADtAcSRD",
		AcoustIDSeg6: "AQADtAcSRE",
	}

	data, err := marshalBookFileDropSegs(f)
	require.NoError(t, err)

	raw := string(data)
	for _, seg := range []string{
		`"acoustid_seg0"`, `"acoustid_seg1"`, `"acoustid_seg2"`,
		`"acoustid_seg3"`, `"acoustid_seg4"`, `"acoustid_seg5"`, `"acoustid_seg6"`,
	} {
		if strings.Contains(raw, seg) {
			t.Errorf("serialized JSON still contains %s; want absent", seg)
		}
	}

	// Caller's struct must not be mutated.
	if f.AcoustIDSeg0 != "AQADtAcSRY" {
		t.Errorf("caller struct mutated: AcoustIDSeg0 = %q, want AQADtAcSRY", f.AcoustIDSeg0)
	}
}

func TestMarshalBookFileDropSegs_OtherFieldsPreserved(t *testing.T) {
	f := &BookFile{
		ID:           "01TEST",
		BookID:       "book-1",
		FilePath:     "/tmp/test.m4b",
		FileHash:     "sha256:abc",
		AcoustIDSeg0: "AQADtAcSRY",
	}

	data, err := marshalBookFileDropSegs(f)
	require.NoError(t, err)

	var got BookFile
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, "book-1", got.BookID)
	assert.Equal(t, "/tmp/test.m4b", got.FilePath)
	assert.Equal(t, "sha256:abc", got.FileHash)
	assert.Empty(t, got.AcoustIDSeg0, "AcoustIDSeg0 should be absent in deserialized output")
}

// ── 2. Back-compat decode ─────────────────────────────────────────────────────

func TestBookFileBackCompatDecode_AllSegsPresent(t *testing.T) {
	// Simulate a legacy Pebble row that still carries all 7 seg fields.
	oldJSON := `{
		"id":"01LEGACY",
		"book_id":"bk1",
		"file_path":"/books/a.m4b",
		"acoustid_seg0":"AQADtAcSRY",
		"acoustid_seg1":"AQADtAcSRZ",
		"acoustid_seg2":"AQADtAcSRA",
		"acoustid_seg3":"AQADtAcSRB",
		"acoustid_seg4":"AQADtAcSRC",
		"acoustid_seg5":"AQADtAcSRD",
		"acoustid_seg6":"AQADtAcSRE"
	}`

	var f BookFile
	require.NoError(t, json.Unmarshal([]byte(oldJSON), &f))

	assert.Equal(t, "01LEGACY", f.ID)
	assert.Equal(t, "bk1", f.BookID)
	assert.Equal(t, "AQADtAcSRY", f.AcoustIDSeg0, "Seg0 should survive decode of old-format row")
	assert.Equal(t, "AQADtAcSRZ", f.AcoustIDSeg1)
	assert.Equal(t, "AQADtAcSRA", f.AcoustIDSeg2)
	assert.Equal(t, "AQADtAcSRB", f.AcoustIDSeg3)
	assert.Equal(t, "AQADtAcSRC", f.AcoustIDSeg4)
	assert.Equal(t, "AQADtAcSRD", f.AcoustIDSeg5)
	assert.Equal(t, "AQADtAcSRE", f.AcoustIDSeg6)
}

// ── 3. Write path: stored bytes lack segs ────────────────────────────────────

func TestCreateBookFile_StoredValueLacksSegs(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	ps := store.(*PebbleStore)

	book := &Book{Title: "Test", FilePath: "/tmp/t.m4b"}
	created, err := ps.CreateBook(book)
	require.NoError(t, err)

	f := &BookFile{
		BookID:       created.ID,
		FilePath:     "/tmp/t.m4b",
		AcoustIDSeg0: "AQADtAcSRY",
		AcoustIDSeg1: "AQADtAcSRZ",
	}
	require.NoError(t, ps.CreateBookFile(f))

	// Read the raw Pebble value and verify no seg keys.
	key := []byte("book_file:" + f.BookID + ":" + f.ID)
	val, closer, err := ps.db.Get(key)
	require.NoError(t, err)
	raw := string(val)
	closer.Close()

	assert.NotContains(t, raw, `"acoustid_seg0"`,
		"stored JSON must not contain acoustid_seg0")
	assert.NotContains(t, raw, `"acoustid_seg1"`,
		"stored JSON must not contain acoustid_seg1")
}

func TestUpdateBookFile_StoredValueLacksSegs(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	ps := store.(*PebbleStore)

	book := &Book{Title: "Test", FilePath: "/tmp/u.m4b"}
	created, err := ps.CreateBook(book)
	require.NoError(t, err)

	f := &BookFile{BookID: created.ID, FilePath: "/tmp/u.m4b"}
	require.NoError(t, ps.CreateBookFile(f))

	// Update with seg values — they must still be absent in the stored bytes.
	f.AcoustIDSeg0 = "AQADtAcSRY"
	f.AcoustIDSeg3 = "AQADtAcSRB"
	require.NoError(t, ps.UpdateBookFile(f.ID, f))

	key := []byte("book_file:" + f.BookID + ":" + f.ID)
	val, closer, err := ps.db.Get(key)
	require.NoError(t, err)
	raw := string(val)
	closer.Close()

	assert.NotContains(t, raw, `"acoustid_seg0"`)
	assert.NotContains(t, raw, `"acoustid_seg3"`)
}

// ── 4+5+6. SweepBookFileSegDrop ─────────────────────────────────────────────

// writeLegacyBookFileRow writes a raw Pebble row with segment fields present,
// bypassing the new marshalBookFileDropSegs path to simulate pre-T020 data.
func writeLegacyBookFileRow(t *testing.T, ps *PebbleStore, f *BookFile) {
	t.Helper()
	data, err := json.Marshal(f)
	require.NoError(t, err, "marshal legacy row")
	key := []byte("book_file:" + f.BookID + ":" + f.ID)
	require.NoError(t, ps.db.Set(key, data, nil), "write legacy row")
}

func TestSweepBookFileSegDrop_DryRun(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	ps := store.(*PebbleStore)

	book := &Book{Title: "T", FilePath: "/tmp/s.m4b"}
	created, err := ps.CreateBook(book)
	require.NoError(t, err)

	// Write one legacy row (with segs) and one clean row (without segs).
	legacyFile := &BookFile{
		ID: "01LEGACY00000000000000000", BookID: created.ID,
		FilePath:     "/books/legacy.m4b",
		AcoustIDSeg0: "AQADtAcSRY",
		AcoustIDSeg1: "AQADtAcSRZ",
	}
	writeLegacyBookFileRow(t, ps, legacyFile)

	cleanFile := &BookFile{
		ID: "01CLEAN000000000000000000", BookID: created.ID,
		FilePath: "/books/clean.m4b",
		FileHash: "sha256:clean",
	}
	writeLegacyBookFileRow(t, ps, cleanFile)

	// Dry-run.
	res, err := ps.SweepBookFileSegDrop(context.Background(), true, 100, nil)
	require.NoError(t, err)

	assert.Equal(t, 2, res.Total, "total rows")
	assert.Equal(t, 1, res.Rewrite, "dry-run should identify 1 row to rewrite")
	assert.Equal(t, 1, res.Skipped, "dry-run should skip 1 clean row")
	assert.Equal(t, 0, res.Errors)

	// Dry-run must NOT have set the flag.
	setting, settingErr := ps.GetSetting("bookfile_seg_drop_v1_done")
	if settingErr != nil && !errors.Is(settingErr, ErrSettingNotFound) {
		t.Fatalf("unexpected GetSetting error: %v", settingErr)
	}
	if setting != nil && setting.Value == "true" {
		t.Error("completion flag must NOT be set after dry-run")
	}

	// Dry-run must NOT have modified the legacy row.
	key := []byte("book_file:" + legacyFile.BookID + ":" + legacyFile.ID)
	val, closer, err := ps.db.Get(key)
	require.NoError(t, err)
	raw := string(val)
	closer.Close()
	assert.Contains(t, raw, `"acoustid_seg0"`,
		"dry-run must not rewrite the legacy row")
}

func TestSweepBookFileSegDrop_Apply(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	ps := store.(*PebbleStore)

	book := &Book{Title: "A", FilePath: "/tmp/a.m4b"}
	created, err := ps.CreateBook(book)
	require.NoError(t, err)

	// Write two legacy rows and one already-clean row.
	for i, seg := range []string{"AQADtAcSRY", "AQADtAcSRZ"} {
		id := [25]byte{'0', '1', 'L', byte('A' + i)}
		for j := 4; j < 25; j++ {
			id[j] = '0'
		}
		f := &BookFile{
			ID: string(id[:]), BookID: created.ID,
			FilePath:     "/books/legacy" + string(rune('A'+i)) + ".m4b",
			AcoustIDSeg0: seg,
		}
		writeLegacyBookFileRow(t, ps, f)
	}
	cleanFile := &BookFile{
		ID: "01CLEAN000000000000000000", BookID: created.ID,
		FilePath: "/books/clean.m4b",
	}
	writeLegacyBookFileRow(t, ps, cleanFile)

	// Apply sweep.
	res, err := ps.SweepBookFileSegDrop(context.Background(), false, 100, nil)
	require.NoError(t, err)

	assert.Equal(t, 3, res.Total)
	assert.Equal(t, 2, res.Rewrite, "2 legacy rows should be rewritten")
	assert.Equal(t, 1, res.Skipped, "1 clean row should be skipped")
	assert.Equal(t, 0, res.Errors)

	// After apply, legacy rows must not have seg keys in stored bytes.
	for i := range []int{0, 1} {
		id := [25]byte{'0', '1', 'L', byte('A' + i)}
		for j := 4; j < 25; j++ {
			id[j] = '0'
		}
		key := []byte("book_file:" + created.ID + ":" + string(id[:]))
		val, closer, rawErr := ps.db.Get(key)
		require.NoError(t, rawErr)
		raw := string(val)
		closer.Close()
		assert.NotContains(t, raw, `"acoustid_seg0"`,
			"rewritten row must not contain acoustid_seg0")
	}
}

func TestSweepBookFileSegDrop_Resumable(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	ps := store.(*PebbleStore)

	book := &Book{Title: "R", FilePath: "/tmp/r.m4b"}
	created, err := ps.CreateBook(book)
	require.NoError(t, err)

	f := &BookFile{
		ID: "01LEGACY00000000000000001", BookID: created.ID,
		FilePath:     "/books/rfile.m4b",
		AcoustIDSeg0: "AQADtAcSRY",
	}
	writeLegacyBookFileRow(t, ps, f)

	// First apply.
	res1, err := ps.SweepBookFileSegDrop(context.Background(), false, 100, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, res1.Rewrite)

	// Second apply (resumable — same row is now clean, should be skipped).
	res2, err := ps.SweepBookFileSegDrop(context.Background(), false, 100, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, res2.Rewrite, "re-run must produce 0 rewrites (idempotent)")
	assert.Equal(t, 1, res2.Skipped)
}
