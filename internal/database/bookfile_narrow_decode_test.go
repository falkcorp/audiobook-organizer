// file: internal/database/bookfile_narrow_decode_test.go
// version: 1.0.0
// guid: 9d3e5b71-6c42-4f08-b1a7-2e8c0f4d6a93
// last-edited: 2026-10-14

package database

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"
)

// narrowDecodeFile is a book file with every heavy field populated, so a decode
// that kept any of them (or dropped a Core field) shows up.
func narrowDecodeFile(bookID, name string, disc, track int) *BookFile {
	return &BookFile{
		BookID:              bookID,
		FilePath:            "/synthetic/" + bookID + "/" + name + ".m4b",
		DiscNumber:          disc,
		TrackNumber:         track,
		Duration:            90,
		FileSize:            2_000,
		FileHash:            "hash-" + name,
		RawTags:             map[string]string{"album": "Synthetic " + name},
		AcoustIDFingerprint: []byte("synthetic-fingerprint-bytes-" + name),
		AcoustIDSeg0:        "seg0-" + name,
		AcoustIDSeg1:        "seg1-" + name,
	}
}

// TestDecodeBookFileCoreMatchesCore: decoding straight into BookFileCore must
// give exactly what decoding the full BookFile and projecting it gives.
func TestDecodeBookFileCoreMatchesCore(t *testing.T) {
	f := narrowDecodeFile("book-1", "a", 1, 2)
	f.ID = "file-1"
	data, err := json.Marshal(f)
	require.NoError(t, err)

	var full BookFile
	require.NoError(t, json.Unmarshal(data, &full))
	require.NotEmpty(t, full.AcoustIDFingerprint, "fixture sanity: the stored row carries a fingerprint")

	narrow, err := decodeBookFileCore(data)
	require.NoError(t, err)
	require.Equal(t, full.Core(), narrow)
}

// TestDecodeBookFileCoreNeverDecodesFingerprint is the proof the blob is
// skipped rather than decoded and discarded: a row whose fingerprint is not
// valid base64 fails the full decode and still decodes narrow.
func TestDecodeBookFileCoreNeverDecodesFingerprint(t *testing.T) {
	data, err := json.Marshal(narrowDecodeFile("book-1", "a", 0, 1))
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	require.Contains(t, raw, "acoustid_fingerprint")
	raw["acoustid_fingerprint"] = "!! not base64 !!"
	corrupt, err := json.Marshal(raw)
	require.NoError(t, err)

	var full BookFile
	require.Error(t, json.Unmarshal(corrupt, &full), "control: the full decode must trip on the bad blob")

	narrow, err := decodeBookFileCore(corrupt)
	require.NoError(t, err)
	require.Equal(t, "book-1", narrow.BookID)
}

// TestGetBookFilesForIDsPebbleScan_NarrowDecodeMatchesFullRead covers both
// arms of the Pebble fallback (per-book ranges, and the whole-table scan used
// above perBookRangeMaxIDs) against the full-decode GetBookFiles.
func TestGetBookFilesForIDsPebbleScan_NarrowDecodeMatchesFullRead(t *testing.T) {
	p, err := NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	p.WaitForWarmup()

	for _, id := range []string{"nd-book-1", "nd-book-2"} {
		_, err := p.CreateBook(&Book{ID: id, Title: "Synthetic " + id, FilePath: "/synthetic/" + id, Format: "m4b"})
		require.NoError(t, err)
	}
	// Created out of order, so the per-book arm's sort is exercised.
	for _, f := range []*BookFile{
		narrowDecodeFile("nd-book-1", "c", 1, 3),
		narrowDecodeFile("nd-book-1", "a", 1, 1),
		narrowDecodeFile("nd-book-1", "b", 1, 2),
		narrowDecodeFile("nd-book-2", "d", 1, 1),
	} {
		require.NoError(t, p.CreateBookFile(f))
	}

	want := map[string][]BookFileCore{}
	for _, id := range []string{"nd-book-1", "nd-book-2"} {
		full, err := p.GetBookFiles(id)
		require.NoError(t, err)
		require.NotEmpty(t, full[0].AcoustIDFingerprint, "fixture sanity: a full read carries the fingerprint")
		for i := range full {
			want[id] = append(want[id], full[i].Core())
		}
	}

	t.Run("per-book ranges", func(t *testing.T) {
		got, err := p.getBookFilesForIDsPebbleScan([]string{"nd-book-1", "nd-book-2", "nd-book-1", "", "nd-missing"})
		require.NoError(t, err)
		require.Equal(t, want, got)
		require.Equal(t, []int{1, 2, 3}, []int{got["nd-book-1"][0].TrackNumber, got["nd-book-1"][1].TrackNumber, got["nd-book-1"][2].TrackNumber},
			"the per-book arm keeps GetBookFiles' disc, track, path order")
	})

	t.Run("whole-table scan", func(t *testing.T) {
		ids := []string{"nd-book-1", "nd-book-2"}
		for i := 0; len(ids) <= perBookRangeMaxIDs; i++ {
			ids = append(ids, fmt.Sprintf("nd-filler-%d", i))
		}
		got, err := p.getBookFilesForIDsPebbleScan(ids)
		require.NoError(t, err)
		require.Len(t, got, 2)
		// The scan arm returns key order, not the sorted order; compare as sets.
		for id, files := range want {
			require.ElementsMatch(t, files, got[id])
		}
	})
}

// TestGetBookFilesForIDsPebbleScan_DoesNotDecodeStoredFingerprint corrupts the
// stored fingerprint of one row in Pebble itself. Only a fallback that never
// decodes the blob can still return that row, so this fails if either arm goes
// back to decoding a full BookFile.
func TestGetBookFilesForIDsPebbleScan_DoesNotDecodeStoredFingerprint(t *testing.T) {
	p, err := NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	p.WaitForWarmup()

	_, err = p.CreateBook(&Book{ID: "nd-book-9", Title: "Synthetic nd-book-9", FilePath: "/synthetic/nd-book-9", Format: "m4b"})
	require.NoError(t, err)
	f := narrowDecodeFile("nd-book-9", "z", 1, 1)
	require.NoError(t, p.CreateBookFile(f))
	// Read back before corrupting, so want carries the stored (zone-decoded)
	// timestamps rather than the in-memory ones.
	stored, err := p.GetBookFiles("nd-book-9")
	require.NoError(t, err)
	require.Len(t, stored, 1)
	want := stored[0].Core()

	key := []byte("book_file:nd-book-9:" + f.ID)
	val, closer, err := p.db.Get(key)
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(val, &raw))
	require.NoError(t, closer.Close())
	raw["acoustid_fingerprint"] = "!! not base64 !!"
	corrupt, err := json.Marshal(raw)
	require.NoError(t, err)
	require.NoError(t, p.db.Set(key, corrupt, pebble.Sync))

	_, err = p.GetBookFiles("nd-book-9")
	require.Error(t, err, "control: the full-decode read must trip on the corrupted blob")

	ids := []string{"nd-book-9"}
	for _, arm := range []struct {
		name string
		ids  []string
	}{
		{"per-book ranges", ids},
		{"whole-table scan", func() []string {
			all := append([]string(nil), ids...)
			for i := 0; len(all) <= perBookRangeMaxIDs; i++ {
				all = append(all, fmt.Sprintf("nd-filler-%d", i))
			}
			return all
		}()},
	} {
		t.Run(arm.name, func(t *testing.T) {
			got, err := p.getBookFilesForIDsPebbleScan(arm.ids)
			require.NoError(t, err)
			require.Len(t, got["nd-book-9"], 1)
			require.Equal(t, want, got["nd-book-9"][0])
		})
	}
}
