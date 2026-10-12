// file: internal/database/pebble_store_fpwinidx_test.go
// version: 1.0.0
// guid: 0b7e3f19-8c2d-4a56-b1e4-7d9a2c6f3e81
// last-edited: 2026-10-11

package database

import (
	"encoding/binary"
	"math/rand/v2"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
)

// Window-print index tests (02-PR8). Prints are synthetic random walks; no
// real audio or library data.

// idxRaw is a synthetic 120 s window print (969 frames) seeded by seed: a
// random walk in which each frame flips each bit of the previous one with
// probability 0.15, so it has ~968 distinct shingles and bands normally.
func idxRaw(seed uint64) []byte {
	rng := rand.New(rand.NewPCG(seed, seed^0x5bd1e995))
	const n = 969
	out := make([]byte, 4*n)
	f := rng.Uint32()
	for i := 0; i < n; i++ {
		if i > 0 {
			var mask uint32
			for bit := 0; bit < 32; bit++ {
				if rng.Float64() < 0.15 {
					mask |= 1 << bit
				}
			}
			f ^= mask
		}
		binary.LittleEndian.PutUint32(out[i*4:], f)
	}
	return out
}

func idxWindow(ref FingerprintWindowRef, kind FingerprintWindowKind, slot int, seed uint64) *FingerprintWindow {
	w := fpwinFixture(ref, kind, slot, 1)
	w.Raw = idxRaw(seed)
	w.Frames = len(w.Raw) / 4
	return w
}

// idxKeysOf returns the fpwinidx: keys whose file ID is fileID.
func idxKeysOf(t *testing.T, s *PebbleStore, fileID string) []string {
	t.Helper()
	var out []string
	for _, k := range fpwinKeysUnder(t, s, fpwinIdxKeyPrefix) {
		if k[fpwinIdxFileIDOffset:] == fileID {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// wantBands is the band set the index must hold for these windows.
func wantBands(t *testing.T, raws ...[]byte) []fingerprint.WindowBand {
	t.Helper()
	ws := make([]FingerprintWindow, 0, len(raws))
	for _, r := range raws {
		ws = append(ws, FingerprintWindow{Kind: WindowKindWindow, Raw: r})
	}
	bands, err := fileWindowBands(ws)
	require.NoError(t, err)
	return bands
}

func requireIndexed(t *testing.T, s *PebbleStore, fileID string, want []fingerprint.WindowBand) {
	t.Helper()
	got, indexed, err := s.WindowIndexMembers(fileID)
	require.NoError(t, err)
	require.True(t, indexed, "file %s has no member list", fileID)
	require.Equal(t, want, got)
	keys := idxKeysOf(t, s, fileID)
	require.Len(t, keys, len(want), "one index row per member")
	for _, b := range want {
		require.Contains(t, keys, string(fpwinIdxKey(b, fileID)))
	}
}

func requireNotIndexed(t *testing.T, s *PebbleStore, fileID string) {
	t.Helper()
	_, indexed, err := s.WindowIndexMembers(fileID)
	require.NoError(t, err)
	require.False(t, indexed, "file %s still has a member list", fileID)
	require.Empty(t, idxKeysOf(t, s, fileID), "file %s still has index rows", fileID)
}

func TestPutFingerprintWindow_WritesIndexInSameBatch(t *testing.T) {
	env := newBookSigEnv(t)
	_, id := fpwinSeedFile(t, env.store, "/lib/idx/a.m4b", nil)
	ref := FileWindowRef(id)

	require.NoError(t, env.store.PutFingerprintWindow(idxWindow(ref, WindowKindWindow, 1000, 1)))
	require.NoError(t, env.store.PutFingerprintWindow(idxWindow(ref, WindowKindWindow, 5000, 2)))
	fresh := env.reopen(t)
	requireIndexed(t, fresh, id, wantBands(t, idxRaw(1), idxRaw(2)))
	require.Len(t, idxKeysOf(t, fresh, id), 2*fingerprint.WindowBandCount)

	// Replacing slot 5000 drops its old rows without recomputing the old
	// print (the member list names them) and adds the new ones.
	require.NoError(t, fresh.PutFingerprintWindow(idxWindow(ref, WindowKindWindow, 5000, 3)))
	requireIndexed(t, fresh, id, wantBands(t, idxRaw(1), idxRaw(3)))

	// Head and whole rows are stored but never banded.
	require.NoError(t, fresh.PutFingerprintWindow(idxWindow(ref, WindowKindWhole, 0, 4)))
	require.NoError(t, fresh.PutFingerprintWindow(idxWindow(ref, WindowKindHead, 0, 5)))
	requireIndexed(t, fresh, id, wantBands(t, idxRaw(1), idxRaw(3)))

	// A window with nothing to band still records the file as indexed.
	_, flat := fpwinSeedFile(t, fresh, "/lib/idx/flat.m4b", nil)
	require.NoError(t, fresh.PutFingerprintWindow(fpwinFixture(FileWindowRef(flat), WindowKindWindow, 5000, 9)))
	got, indexed, err := fresh.WindowIndexMembers(flat)
	require.NoError(t, err)
	require.True(t, indexed)
	require.Empty(t, got)

	// p: refs are not indexed.
	require.NoError(t, fresh.PutFingerprintWindow(idxWindow(PathWindowRef("idx/untracked.m4b"), WindowKindWindow, 5000, 6)))
	for _, k := range fpwinKeysUnder(t, fresh, fpwinIdxMetaKeyPrefix) {
		require.False(t, strings.Contains(k, "p:"), "a path ref was indexed: %s", k)
	}

	// ReplaceFingerprintWindows (the backfill's write) rewrites the set.
	require.NoError(t, fresh.ReplaceFingerprintWindows(ref, []FingerprintWindow{*idxWindow(ref, WindowKindWindow, 9000, 7)}))
	requireIndexed(t, fresh, id, wantBands(t, idxRaw(7)))

	// PutWindowIndex rebuilds the same rows from the stored windows.
	n, err := fresh.PutWindowIndex(id)
	require.NoError(t, err)
	require.Equal(t, fingerprint.WindowBandCount, n)
	requireIndexed(t, fresh, id, wantBands(t, idxRaw(7)))
}

func TestDeleteFingerprintWindows_RemovesIndexRows(t *testing.T) {
	env := newBookSigEnv(t)
	_, id := fpwinSeedFile(t, env.store, "/lib/idx/d.m4b", nil)
	_, kept := fpwinSeedFile(t, env.store, "/lib/idx/kept.m4b", nil)
	_, cascaded := fpwinSeedFile(t, env.store, "/lib/idx/cascade.m4b", nil)
	_, failed := fpwinSeedFile(t, env.store, "/lib/idx/failed.m4b", nil)
	for i, f := range []string{id, kept, cascaded, failed} {
		require.NoError(t, env.store.PutFingerprintWindow(idxWindow(FileWindowRef(f), WindowKindWindow, 5000, uint64(10+i))))
	}

	n, err := env.store.DeleteFingerprintWindows(FileWindowRef(id))
	require.NoError(t, err)
	require.Equal(t, 1, n)
	requireNotIndexed(t, env.store, id)

	// The book_file delete cascade takes the index with the windows.
	require.NoError(t, env.store.DeleteBookFile(cascaded))
	requireNotIndexed(t, env.store, cascaded)

	// A durable failure drops the windows, so their rows go too.
	require.NoError(t, env.store.RecordFingerprintWindowFailure(&FingerprintWindowFailure{Ref: FileWindowRef(failed), Reason: "too_short"}))
	requireNotIndexed(t, env.store, failed)

	fresh := env.reopen(t)
	requireNotIndexed(t, fresh, id)
	requireIndexed(t, fresh, kept, wantBands(t, idxRaw(11)))
}

func TestCarryOverFingerprintWindows_MovesIndexRows(t *testing.T) {
	env := newBookSigEnv(t)
	bookID, keeper := fpwinSeedFile(t, env.store, "/lib/idx/track.m4b", nil)
	donor := &BookFile{BookID: bookID, FilePath: "/lib/idx/track.m4b", Format: "m4b"}
	require.NoError(t, env.store.CreateBookFile(donor))
	keeperRef, donorRef := FileWindowRef(keeper), FileWindowRef(donor.ID)

	require.NoError(t, env.store.PutFingerprintWindow(idxWindow(keeperRef, WindowKindWindow, 5000, 20)))
	for i, slot := range []int{1000, 5000, 9000} {
		require.NoError(t, env.store.PutFingerprintWindow(idxWindow(donorRef, WindowKindWindow, slot, uint64(21+i))))
	}

	moved, err := env.store.CarryOverFingerprintWindows([]FingerprintWindowRef{donorRef}, keeperRef)
	require.NoError(t, err)
	require.Equal(t, 2, moved, "the keeper's own slot 5000 wins")

	fresh := env.reopen(t)
	// Keeper: its own 5000 plus the donor's 1000 and 9000.
	requireIndexed(t, fresh, keeper, wantBands(t, idxRaw(20), idxRaw(21), idxRaw(23)))
	requireNotIndexed(t, fresh, donor.ID)

	// An untracked p: candidate repointed onto a file is indexed then.
	_, found := fpwinSeedFile(t, fresh, "/lib/idx/found.m4b", nil)
	pref := PathWindowRef("idx/found.m4b")
	require.NoError(t, fresh.PutFingerprintWindow(idxWindow(pref, WindowKindWindow, 5000, 30)))
	_, err = fresh.CarryOverFingerprintWindows([]FingerprintWindowRef{pref}, FileWindowRef(found))
	require.NoError(t, err)
	requireIndexed(t, fresh, found, wantBands(t, idxRaw(30)))
}

func TestWindowIndexProbe_TwoBandHitsOnly(t *testing.T) {
	env := newBookSigEnv(t)
	_, a := fpwinSeedFile(t, env.store, "/lib/idx/probe-a.m4b", nil)
	_, b := fpwinSeedFile(t, env.store, "/lib/idx/probe-b.m4b", nil)
	require.NoError(t, env.store.PutFingerprintWindow(idxWindow(FileWindowRef(a), WindowKindWindow, 5000, 40)))
	require.NoError(t, env.store.PutFingerprintWindow(idxWindow(FileWindowRef(b), WindowKindWindow, 5000, 41)))
	bandsA := wantBands(t, idxRaw(40))
	require.Len(t, bandsA, fingerprint.WindowBandCount)

	got, err := env.store.WindowIndexProbe(bandsA, 0)
	require.NoError(t, err)
	require.Equal(t, map[string]int{a: fingerprint.WindowBandCount}, got, "every band of A hits A; unrelated B is not a candidate")

	got, err = env.store.WindowIndexProbe(bandsA[:1], 0)
	require.NoError(t, err)
	require.Empty(t, got, "one band hit is below the floor")

	got, err = env.store.WindowIndexProbe([]fingerprint.WindowBand{bandsA[0], bandsA[0], bandsA[1]}, 0)
	require.NoError(t, err)
	require.Equal(t, map[string]int{a: 2}, got, "two distinct band hits; a repeated band counts once")

	// max keeps the strongest candidates.
	bandsB := wantBands(t, idxRaw(41))
	mixed := append(append([]fingerprint.WindowBand{}, bandsA...), bandsB[:3]...)
	got, err = env.store.WindowIndexProbe(mixed, 1)
	require.NoError(t, err)
	require.Equal(t, map[string]int{a: fingerprint.WindowBandCount}, got)

	// A stop-word key (more postings than the cap) is skipped, not walked.
	hot := fingerprint.WindowBand{Band: 0, Hash: 0xfeed}
	hot2 := fingerprint.WindowBand{Band: 1, Hash: 0xbeef}
	for i := 0; i <= windowIndexMaxPostings; i++ {
		id := "01HOT" + strings.Repeat("0", 4) + string(rune('A'+i%26)) + strings.Repeat("x", i/26)
		require.NoError(t, env.store.db.Set(fpwinIdxKey(hot, id), nil, nil))
	}
	require.NoError(t, env.store.db.Set(fpwinIdxKey(hot2, a), nil, nil))
	require.NoError(t, env.store.db.Set(fpwinIdxKey(hot, a), nil, nil))
	got, err = env.store.WindowIndexProbe([]fingerprint.WindowBand{hot, hot2, bandsA[0]}, 0)
	require.NoError(t, err)
	require.Equal(t, map[string]int{a: 2}, got, "the stop word adds no hits; the other two bands do")
}

func TestListWindowIndexFiles_KeysOnly(t *testing.T) {
	env := newBookSigEnv(t)
	_, a := fpwinSeedFile(t, env.store, "/lib/idx/l-a.m4b", nil)
	_, b := fpwinSeedFile(t, env.store, "/lib/idx/l-b.m4b", nil)
	_, none := fpwinSeedFile(t, env.store, "/lib/idx/l-none.m4b", nil)
	for _, slot := range []int{1000, 5000, 9000} {
		require.NoError(t, env.store.PutFingerprintWindow(idxWindow(FileWindowRef(a), WindowKindWindow, slot, 50)))
	}
	require.NoError(t, env.store.PutFingerprintWindow(idxWindow(FileWindowRef(b), WindowKindWindow, 5000, 51)))
	require.NoError(t, env.store.PutFingerprintWindow(idxWindow(PathWindowRef("idx/p.m4b"), WindowKindWindow, 5000, 52)))
	ids, err := env.store.ListWindowIndexFiles()
	require.NoError(t, err)
	want := []string{a, b}
	sort.Strings(want)
	require.Equal(t, want, ids)
	require.NotContains(t, ids, none)
}
