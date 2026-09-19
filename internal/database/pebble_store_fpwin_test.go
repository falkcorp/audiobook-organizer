// file: internal/database/pebble_store_fpwin_test.go
// version: 1.0.0
// guid: 67f3605a-acce-4382-9555-a1a26ed37eb0
// last-edited: 2026-09-19

package database

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Fingerprint-window storage tests (design PR 2). Reads that matter go through
// a REOPENED store (bookSigEnv.reopen) so an in-process layer cannot
// manufacture a pass for a write that never reached disk.

// fpwinRaw builds a raw print of n frames whose bytes are distinguishable by tag.
func fpwinRaw(tag byte, frames int) []byte {
	return bytes.Repeat([]byte{tag, tag + 1, tag + 2, tag + 3}, frames)
}

func fpwinFixture(ref FingerprintWindowRef, kind FingerprintWindowKind, slot int, tag byte) *FingerprintWindow {
	return &FingerprintWindow{
		Ref:             ref,
		Kind:            kind,
		SlotBP:          slot,
		WindowSet:       "ws1",
		OffsetSec:       float64(slot) / 10000 * 3600,
		LengthSec:       120,
		DecodedSec:      120,
		DurationUsedSec: 3600,
		DurationSource:  "acoustid_fingerprint_duration_sec",
		Frames:          960,
		Raw:             fpwinRaw(tag, 960),
		Algorithm:       2,
		Pipeline:        "ffpcm-s16le-11025-mono/v1",
		FpcalcVersion:   "1.6.0",
		FFmpegVersion:   "8.0.1",
		SourceSize:      123456789,
		SourceMtimeUnix: 1757000000,
		ComputedAt:      time.Date(2026, 9, 19, 3, 5, 0, 0, time.UTC),
		Host:            "server",
		LeaseID:         "lease-1",
	}
}

// fpwinSeedFile creates a book with one file at path and returns (bookID, fileID).
func fpwinSeedFile(t *testing.T, s *PebbleStore, path string, legacyPrint []byte) (string, string) {
	t.Helper()
	b, err := s.CreateBook(&Book{Title: "Window " + path, FilePath: path})
	require.NoError(t, err)
	f := &BookFile{
		BookID:                         b.ID,
		FilePath:                       path,
		Format:                         "m4b",
		AcoustIDFingerprint:            legacyPrint,
		AcoustIDFingerprintDurationSec: 3600,
	}
	require.NoError(t, s.CreateBookFile(f))
	require.NotEmpty(t, f.ID)
	return b.ID, f.ID
}

// fpwinKeysUnder counts raw keys under prefix, straight off Pebble.
func fpwinKeysUnder(t *testing.T, s *PebbleStore, prefix string) []string {
	t.Helper()
	var keys []string
	require.NoError(t, forEachKeyInRange(s.db, []byte(prefix), prefixEnd([]byte(prefix)), func(k, _ []byte) error {
		keys = append(keys, string(k))
		return nil
	}))
	return keys
}

func TestFpwin_KeySchemeMatchesTheDesign(t *testing.T) {
	w := fpwinFixture(FileWindowRef("01FILE"), WindowKindWindow, 5000, 1)
	require.Equal(t, "fpwin:f:01FILE:window:5000", string(fpwinKey(w)))
	h := fpwinFixture(FileWindowRef("01FILE"), WindowKindHead, 0, 1)
	require.Equal(t, "fpwin:f:01FILE:head:0", string(fpwinKey(h)))
	wh := fpwinFixture(FileWindowRef("01FILE"), WindowKindWhole, 0, 1)
	require.Equal(t, "fpwin:f:01FILE:whole:0", string(fpwinKey(wh)))

	p := PathWindowRef("Author/Book/01.m4b")
	require.Regexp(t, `^p:[0-9a-f]{64}$`, string(p))
	require.Equal(t, "fpwin_fail:f:01FILE", string(fpwinFailKey(FileWindowRef("01FILE"))))
}

func TestFpwin_RoundTripSurvivesReopen(t *testing.T) {
	env := newBookSigEnv(t)
	_, fileID := fpwinSeedFile(t, env.store, "/lib/a/one.m4b", nil)
	ref := FileWindowRef(fileID)

	want := []*FingerprintWindow{
		fpwinFixture(ref, WindowKindWindow, 9000, 30),
		fpwinFixture(ref, WindowKindWindow, 1000, 10),
		fpwinFixture(ref, WindowKindWindow, 5000, 20),
		fpwinFixture(ref, WindowKindWhole, 0, 40),
	}
	for _, w := range want {
		require.NoError(t, env.store.PutFingerprintWindow(w))
	}

	fresh := env.reopen(t)
	got, err := fresh.GetFingerprintWindows(ref)
	require.NoError(t, err)
	require.Len(t, got, 4)
	// Ordered window-by-slot then whole, and every field survives.
	wantOrder := []*FingerprintWindow{want[1], want[2], want[0], want[3]}
	for i := range got {
		exp := *wantOrder[i]
		exp.SchemaVersion = FingerprintWindowSchemaVersion
		require.Equal(t, exp, got[i], "window %d", i)
	}

	// Put replaces the same (kind, slot) in place.
	repl := fpwinFixture(ref, WindowKindWindow, 5000, 99)
	require.NoError(t, fresh.PutFingerprintWindow(repl))
	got, err = fresh.GetFingerprintWindows(ref)
	require.NoError(t, err)
	require.Len(t, got, 4)
	require.Equal(t, repl.Raw, got[1].Raw)
}

func TestFpwin_PutRefusesInvalidAndOrphanRows(t *testing.T) {
	env := newBookSigEnv(t)
	_, fileID := fpwinSeedFile(t, env.store, "/lib/a/two.m4b", nil)
	ref := FileWindowRef(fileID)

	bad := map[string]*FingerprintWindow{
		"no row":      fpwinFixture(FileWindowRef("01NOSUCHFILE"), WindowKindWindow, 5000, 1),
		"bad kind":    fpwinFixture(ref, FingerprintWindowKind("tail"), 0, 1),
		"slot > 100%": fpwinFixture(ref, WindowKindWindow, 10001, 1),
		"head slot":   fpwinFixture(ref, WindowKindHead, 5000, 1),
		"colon in id": fpwinFixture(FingerprintWindowRef("f:a:b"), WindowKindWindow, 5000, 1),
		"bad ref":     fpwinFixture(FingerprintWindowRef("x:abc"), WindowKindWindow, 5000, 1),
		"ragged raw": func() *FingerprintWindow {
			w := fpwinFixture(ref, WindowKindWindow, 5000, 1)
			w.Raw = w.Raw[:5]
			return w
		}(),
		"virtual head": func() *FingerprintWindow { w := fpwinFixture(ref, WindowKindHead, 0, 1); w.Virtual = true; return w }(),
	}
	for name, w := range bad {
		require.Error(t, env.store.PutFingerprintWindow(w), name)
	}
	require.Empty(t, fpwinKeysUnder(t, env.store, fpwinKeyPrefix), "a refused Put must write nothing")

	// A path ref needs no row.
	require.NoError(t, env.store.PutFingerprintWindow(fpwinFixture(PathWindowRef("x/y.m4b"), WindowKindWindow, 5000, 1)))
}

func TestFpwin_WindowsForFileAddsTheLegacyHead(t *testing.T) {
	env := newBookSigEnv(t)
	legacy := fpwinRaw(200, 960)
	_, withPrint := fpwinSeedFile(t, env.store, "/lib/a/legacy.m4b", legacy)
	_, noPrint := fpwinSeedFile(t, env.store, "/lib/a/fresh.m4b", nil)
	require.NoError(t, env.store.PutFingerprintWindow(fpwinFixture(FileWindowRef(withPrint), WindowKindWindow, 5000, 7)))
	require.NoError(t, env.store.PutFingerprintWindow(fpwinFixture(FileWindowRef(noPrint), WindowKindWindow, 5000, 8)))

	// Wait for memdb, whose rows strip AcoustIDFingerprint: the shim must not
	// read the print from there.
	env.store.WaitForWarmup()
	fresh := env.reopen(t)
	fresh.WaitForWarmup()

	got, err := fresh.WindowsForFile(withPrint)
	require.NoError(t, err)
	require.Len(t, got, 2)
	head := got[0]
	require.True(t, head.Virtual)
	require.Equal(t, WindowKindHead, head.Kind)
	require.Equal(t, 0, head.SlotBP)
	require.Equal(t, LegacyHeadPipeline, head.Pipeline)
	require.Equal(t, FileWindowRef(withPrint), head.Ref)
	require.Equal(t, legacy, head.Raw)
	require.Equal(t, 960, head.Frames)
	require.InDelta(t, 3600, head.DurationUsedSec, 0)
	require.Equal(t, WindowKindWindow, got[1].Kind)
	require.False(t, got[1].Virtual)

	// The virtual head is never stored.
	require.Len(t, fpwinKeysUnder(t, fresh, string(fpwinRefPrefix(FileWindowRef(withPrint)))), 1)
	stored, err := fresh.GetFingerprintWindows(FileWindowRef(withPrint))
	require.NoError(t, err)
	require.Len(t, stored, 1)

	got, err = fresh.WindowsForFile(noPrint)
	require.NoError(t, err)
	require.Len(t, got, 1, "no legacy print, no virtual head")
	require.False(t, got[0].Virtual)
}

// The warmup scans byte ranges anchored on "book_file:" and "book:" and friends;
// windows must stay out of every one of them. Asserted as a DELTA over every
// phase, so a future prefix that swallowed "fpwin:" fails here even if no row
// were admitted.
func TestFpwin_WarmupNeverReadsTheWindowPrefix(t *testing.T) {
	env := newBookSigEnv(t)
	_, fileID := fpwinSeedFile(t, env.store, "/lib/a/warm.m4b", nil)

	warm := func() (map[string]int, map[string]int64) {
		mem, err := NewMemStore()
		require.NoError(t, err)
		require.NoError(t, mem.WarmFromPebble(context.Background(), env.store))
		_, scanned := mem.LastWarmupCounts()
		bytesScanned, _ := mem.LastWarmupBytes()
		return scanned, bytesScanned
	}
	keysBefore, bytesBefore := warm()

	// Large payloads so a leak into any phase moves its byte count visibly.
	ref := FileWindowRef(fileID)
	for _, slot := range []int{1000, 5000, 9000} {
		w := fpwinFixture(ref, WindowKindWindow, slot, byte(slot/1000))
		w.Raw = fpwinRaw(byte(slot/1000), 256*1024)
		require.NoError(t, env.store.PutFingerprintWindow(w))
	}
	pw := fpwinFixture(PathWindowRef("untracked/x.m4b"), WindowKindWindow, 5000, 9)
	pw.Raw = fpwinRaw(9, 256*1024)
	require.NoError(t, env.store.PutFingerprintWindow(pw))
	require.NoError(t, env.store.db.Set(fpwinFailKey(ref), []byte(`{"reason":"x"}`), nil))

	keysAfter, bytesAfter := warm()
	require.Equal(t, keysBefore, keysAfter, "warmup scanned more keys after windows were written")
	require.Equal(t, bytesBefore, bytesAfter, "warmup read window bytes")
}

// seedWindowsFor writes three windows and a failure tombstone for a file.
func seedWindowsFor(t *testing.T, s *PebbleStore, fileID string) {
	t.Helper()
	ref := FileWindowRef(fileID)
	for _, slot := range []int{1000, 5000, 9000} {
		require.NoError(t, s.PutFingerprintWindow(fpwinFixture(ref, WindowKindWindow, slot, byte(slot/1000))))
	}
	require.NoError(t, s.db.Set(fpwinFailKey(ref), []byte(`{"reason":"x"}`), nil))
}

func requireNoWindowsLeft(t *testing.T, s *PebbleStore, fileID string) {
	t.Helper()
	ref := FileWindowRef(fileID)
	require.Empty(t, fpwinKeysUnder(t, s, string(fpwinRefPrefix(ref))), "windows of deleted book_file %s survived", fileID)
	require.Empty(t, fpwinKeysUnder(t, s, string(fpwinFailKey(ref))), "failure tombstone of deleted book_file %s survived", fileID)
}

func TestFpwin_Cascade_DeleteBookFile(t *testing.T) {
	env := newBookSigEnv(t)
	_, gone := fpwinSeedFile(t, env.store, "/lib/c/gone.m4b", nil)
	_, kept := fpwinSeedFile(t, env.store, "/lib/c/kept.m4b", nil)
	seedWindowsFor(t, env.store, gone)
	seedWindowsFor(t, env.store, kept)

	require.NoError(t, env.store.DeleteBookFile(gone))

	fresh := env.reopen(t)
	requireNoWindowsLeft(t, fresh, gone)
	got, err := fresh.GetFingerprintWindows(FileWindowRef(kept))
	require.NoError(t, err)
	require.Len(t, got, 3, "the cascade reached a neighbouring file")
}

func TestFpwin_Cascade_DeleteBookFilesByIDs(t *testing.T) {
	env := newBookSigEnv(t)
	_, a := fpwinSeedFile(t, env.store, "/lib/c/a.m4b", nil)
	_, b := fpwinSeedFile(t, env.store, "/lib/c/b.m4b", nil)
	_, kept := fpwinSeedFile(t, env.store, "/lib/c/k.m4b", nil)
	for _, id := range []string{a, b, kept} {
		seedWindowsFor(t, env.store, id)
	}

	require.NoError(t, env.store.DeleteBookFilesByIDs([]string{a, b}))

	fresh := env.reopen(t)
	requireNoWindowsLeft(t, fresh, a)
	requireNoWindowsLeft(t, fresh, b)
	got, err := fresh.GetFingerprintWindows(FileWindowRef(kept))
	require.NoError(t, err)
	require.Len(t, got, 3)
}

func TestFpwin_Cascade_DeleteBookFilesForBook(t *testing.T) {
	env := newBookSigEnv(t)
	bookID, first := fpwinSeedFile(t, env.store, "/lib/c/book/01.m4b", nil)
	second := &BookFile{BookID: bookID, FilePath: "/lib/c/book/02.m4b", Format: "m4b"}
	require.NoError(t, env.store.CreateBookFile(second))
	_, other := fpwinSeedFile(t, env.store, "/lib/c/other.m4b", nil)
	for _, id := range []string{first, second.ID, other} {
		seedWindowsFor(t, env.store, id)
	}

	require.NoError(t, env.store.DeleteBookFilesForBook(bookID))

	fresh := env.reopen(t)
	requireNoWindowsLeft(t, fresh, first)
	requireNoWindowsLeft(t, fresh, second.ID)
	got, err := fresh.GetFingerprintWindows(FileWindowRef(other))
	require.NoError(t, err)
	require.Len(t, got, 3)
}

// A failed batch delete (fail-closed on an unresolvable id) must take no
// windows with it either: the cascade is in the same batch as the rows.
func TestFpwin_Cascade_FailedBatchDeleteKeepsWindows(t *testing.T) {
	env := newBookSigEnv(t)
	_, a := fpwinSeedFile(t, env.store, "/lib/c/fc.m4b", nil)
	seedWindowsFor(t, env.store, a)

	require.Error(t, env.store.DeleteBookFilesByIDs([]string{a, "01NOSUCHROW"}))
	got, err := env.store.GetFingerprintWindows(FileWindowRef(a))
	require.NoError(t, err)
	require.Len(t, got, 3)
}

// Updates of a SURVIVING row must never cascade: deleteBookFileSecondaryIndexes
// runs on every update, which is why the cascade is not there.
func TestFpwin_Cascade_UpdateDoesNotTouchWindows(t *testing.T) {
	env := newBookSigEnv(t)
	bookID, id := fpwinSeedFile(t, env.store, "/lib/c/upd.m4b", nil)
	seedWindowsFor(t, env.store, id)

	f, err := env.store.GetBookFileByID(bookID, id)
	require.NoError(t, err)
	f.Duration = 1234
	require.NoError(t, env.store.UpdateBookFile(id, f))

	got, err := env.reopen(t).GetFingerprintWindows(FileWindowRef(id))
	require.NoError(t, err)
	require.Len(t, got, 3)
}

// Windows are keyed by file ID, never book ID, and a move between books keeps
// the file ID — so a move needs no carry-over code. This proves it through the
// real bulk move path.
func TestFpwin_CarryOver_MoveBetweenBooksKeepsWindows(t *testing.T) {
	env := newBookSigEnv(t)
	src, id := fpwinSeedFile(t, env.store, "/lib/m/src.m4b", fpwinRaw(50, 960))
	dst, err := env.store.CreateBook(&Book{Title: "Target", FilePath: "/lib/m/dst"})
	require.NoError(t, err)
	seedWindowsFor(t, env.store, id)

	require.NoError(t, env.store.MoveBookFilesToBookBulk([]BookFileMove{{FileIDs: []string{id}, SourceBookID: src}}, dst.ID))

	fresh := env.reopen(t)
	moved, err := fresh.GetBookFileByID(dst.ID, id)
	require.NoError(t, err)
	require.NotNil(t, moved, "precondition: the row moved")
	got, err := fresh.WindowsForFile(id)
	require.NoError(t, err)
	require.Len(t, got, 4, "legacy head + three windows must follow the moved row")
	require.True(t, got[0].Virtual)
}

// A row merge collapses two rows for one file; the donor's windows move to the
// keeper, the keeper's own windows win on a collision, and the donor keeps
// nothing (not even its tombstone).
func TestFpwin_CarryOver_RowMergeMovesDonorWindows(t *testing.T) {
	env := newBookSigEnv(t)
	bookID, keeper := fpwinSeedFile(t, env.store, "/lib/r/track.m4b", nil)
	donor := &BookFile{BookID: bookID, FilePath: "/lib/r/track.m4b", Format: "m4b"}
	require.NoError(t, env.store.CreateBookFile(donor))

	keeperRef, donorRef := FileWindowRef(keeper), FileWindowRef(donor.ID)
	require.NoError(t, env.store.PutFingerprintWindow(fpwinFixture(keeperRef, WindowKindWindow, 5000, 100)))
	for _, slot := range []int{1000, 5000, 9000} {
		require.NoError(t, env.store.PutFingerprintWindow(fpwinFixture(donorRef, WindowKindWindow, slot, byte(slot/1000))))
	}
	require.NoError(t, env.store.db.Set(fpwinFailKey(donorRef), []byte(`{}`), nil))

	n, err := env.store.CarryOverFingerprintWindows(donorRef, keeperRef)
	require.NoError(t, err)
	require.Equal(t, 2, n, "slot 5000 already existed on the keeper")

	fresh := env.reopen(t)
	got, err := fresh.GetFingerprintWindows(keeperRef)
	require.NoError(t, err)
	require.Len(t, got, 3)
	for _, w := range got {
		require.Equal(t, keeperRef, w.Ref, "carried row must be re-referenced")
	}
	require.Equal(t, fpwinRaw(100, 960), got[1].Raw, "the keeper's own window must win")
	requireNoWindowsLeft(t, fresh, donor.ID)

	// Refused onto a row that does not exist; the donor keeps its windows.
	require.NoError(t, fresh.PutFingerprintWindow(fpwinFixture(donorRef, WindowKindWindow, 1000, 1)))
	_, err = fresh.CarryOverFingerprintWindows(donorRef, FileWindowRef("01NOSUCHROW"))
	require.Error(t, err)
	left, err := fresh.GetFingerprintWindows(donorRef)
	require.NoError(t, err)
	require.Len(t, left, 1)
}

func TestFpwin_CarryOver_RepointPathRefToFileRef(t *testing.T) {
	env := newBookSigEnv(t)
	_, id := fpwinSeedFile(t, env.store, "/lib/p/found.m4b", nil)
	pref := PathWindowRef("p/found.m4b")
	require.NoError(t, env.store.PutFingerprintWindow(fpwinFixture(pref, WindowKindWindow, 5000, 5)))

	n, err := env.store.CarryOverFingerprintWindows(pref, FileWindowRef(id))
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Empty(t, fpwinKeysUnder(t, env.store, string(fpwinRefPrefix(pref))))
	got, err := env.store.GetFingerprintWindows(FileWindowRef(id))
	require.NoError(t, err)
	require.Len(t, got, 1)
}

func TestFpwin_DeleteFingerprintWindows(t *testing.T) {
	env := newBookSigEnv(t)
	_, id := fpwinSeedFile(t, env.store, "/lib/d/x.m4b", nil)
	seedWindowsFor(t, env.store, id)
	n, err := env.store.DeleteFingerprintWindows(FileWindowRef(id))
	require.NoError(t, err)
	require.Equal(t, 3, n)
	requireNoWindowsLeft(t, env.store, id)
	// The book_file row itself is untouched.
	require.NotNil(t, env.store.lookupBookFileByIDIndex(id))
}

func TestFpwin_UndecodableRowIsAnError(t *testing.T) {
	env := newBookSigEnv(t)
	_, id := fpwinSeedFile(t, env.store, "/lib/u/x.m4b", nil)
	require.NoError(t, env.store.db.Set([]byte("fpwin:f:"+id+":window:5000"), []byte("{not json"), nil))
	_, err := env.store.GetFingerprintWindows(FileWindowRef(id))
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "undecodable"))
}
