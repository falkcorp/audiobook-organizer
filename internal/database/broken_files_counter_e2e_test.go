// file: internal/database/broken_files_counter_e2e_test.go
// version: 1.0.0
// guid: c4f1a7d2-8e63-49b5-a0c1-7d2e9f5b8143
// last-edited: 2026-09-05

package database

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBrokenFilesCounter_ReflectsUpdatedMissingFlag is the ONE test that proves
// the dashboard's Broken Files fix end to end: it crosses the writer→reader
// boundary that every other test stops short of.
//
// The maintenance.mark-missing-files op sets book_file.Missing via UpdateBookFile;
// the dashboard counter reads it back through computeLibraryStats. Those two are
// tested independently elsewhere (the op against a fake, the stats derivation
// against a seeded store), and BOTH pass even if the flag never actually
// propagates from an UpdateBookFile write into the population computeLibraryStats
// scans — memdb strips some fields on the way in (AcoustIDFingerprint,
// IntroTranscription), and if Missing were among them the op would write
// correctly, every unit test would stay green, and the tile would still read 0.
//
// This test writes Missing=true through the real store and asserts the counter
// MOVES from 0 to 1 on BOTH the memdb fast path and the Pebble scan path. The
// 0-baseline is the bogus-value control: without it a counter hard-wired to 1
// would pass.
func TestBrokenFilesCounter_ReflectsUpdatedMissingFlag(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	p, ok := store.(*PebbleStore)
	require.True(t, ok, "expected *PebbleStore from setupPebbleTestDB")

	book, err := store.CreateBook(&Book{Title: "Broken Files E2E", FilePath: "/lib/e2e"})
	require.NoError(t, err)
	require.NoError(t, store.CreateBookFile(&BookFile{
		BookID: book.ID, FilePath: "/lib/e2e/f.mp3", Missing: false,
	}))

	p.WaitForWarmup()

	computeBoth := func(t *testing.T) (memdb, pebble int) {
		t.Helper()
		for _, useMemDB := range []bool{true, false} {
			p.UseMemDB = useMemDB
			stats, err := p.computeLibraryStats()
			require.NoError(t, err)
			if useMemDB {
				memdb = stats.BrokenFiles
			} else {
				pebble = stats.BrokenFiles
			}
		}
		return memdb, pebble
	}

	// Baseline control: a present file is not broken. If this is not 0 the
	// assertion below proves nothing.
	memdb0, pebble0 := computeBoth(t)
	require.Equal(t, 0, memdb0, "baseline (memdb): a present file must not count as broken")
	require.Equal(t, 0, pebble0, "baseline (pebble): a present file must not count as broken")

	// Flip Missing=true through the exact write path the mark-missing op uses.
	files, err := store.GetBookFiles(book.ID)
	require.NoError(t, err)
	require.Len(t, files, 1)
	full := files[0]
	full.Missing = true
	require.NoError(t, store.UpdateBookFile(full.ID, &full))

	// The counter must now report the book on BOTH paths — proving the flag
	// propagated from the write into the population each path scans.
	memdb1, pebble1 := computeBoth(t)
	require.Equal(t, 1, memdb1,
		"memdb path: BrokenFiles must reflect the Missing flag written by UpdateBookFile")
	require.Equal(t, 1, pebble1,
		"pebble path: BrokenFiles must reflect the Missing flag written by UpdateBookFile")
}
