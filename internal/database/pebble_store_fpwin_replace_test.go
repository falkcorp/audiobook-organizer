// file: internal/database/pebble_store_fpwin_replace_test.go
// version: 1.0.0
// guid: ceab4355-b275-47dc-b83a-9f47b07279ce
// last-edited: 2026-09-19

package database

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Replace-style window writes used by acoustid.window-backfill (design PR 4).
// Reads that matter go through a reopened store.

func TestFpwin_ReplaceDropsSlotsTheNewPlanLacksAndTheTombstone(t *testing.T) {
	env := newBookSigEnv(t)
	_, id := fpwinSeedFile(t, env.store, "/lib/r/a.m4b", nil)
	seedWindowsFor(t, env.store, id) // slots 1000/5000/9000 + a tombstone
	ref := FileWindowRef(id)

	// The file shrank below 600 s: the new plan has slot 5000 only.
	w := *fpwinFixture(ref, WindowKindWindow, 5000, 0x70)
	require.NoError(t, env.store.ReplaceFingerprintWindows(ref, []FingerprintWindow{w}))

	s := env.reopen(t)
	got, err := s.GetFingerprintWindows(ref)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, 5000, got[0].SlotBP)
	require.Equal(t, w.Raw, got[0].Raw)
	fail, err := s.GetFingerprintWindowFailure(ref)
	require.NoError(t, err)
	require.Nil(t, fail, "a successful replace must clear the tombstone")
}

func TestFpwin_ReplaceRefusesMismatchedRefEmptySetAndOrphan(t *testing.T) {
	env := newBookSigEnv(t)
	_, id := fpwinSeedFile(t, env.store, "/lib/r/b.m4b", nil)
	ref := FileWindowRef(id)

	other := *fpwinFixture(FileWindowRef("someone-else"), WindowKindWindow, 5000, 1)
	require.Error(t, env.store.ReplaceFingerprintWindows(ref, []FingerprintWindow{other}))
	require.Error(t, env.store.ReplaceFingerprintWindows(ref, nil))

	orphan := FileWindowRef("no-such-file")
	w := *fpwinFixture(orphan, WindowKindWindow, 5000, 1)
	require.Error(t, env.store.ReplaceFingerprintWindows(orphan, []FingerprintWindow{w}))
	require.Empty(t, fpwinKeysUnder(t, env.store, fpwinKeyPrefix))
}

func TestFpwin_RecordFailureDropsWindowsAndRoundTrips(t *testing.T) {
	env := newBookSigEnv(t)
	_, id := fpwinSeedFile(t, env.store, "/lib/r/c.m4b", nil)
	seedWindowsFor(t, env.store, id)
	ref := FileWindowRef(id)

	at := time.Date(2026, 9, 19, 3, 15, 0, 0, time.UTC)
	require.NoError(t, env.store.RecordFingerprintWindowFailure(&FingerprintWindowFailure{
		Ref: ref, Reason: "ffmpeg", Detail: "moov atom not found", SlotBP: 5000,
		WindowSet: "ws1", Pipeline: "ffpcm-s16le-11025-mono/v1",
		FpcalcVersion: "1.6.0", FFmpegVersion: "8.0.1",
		SourceSize: 42, SourceMtimeUnix: 1757000000, FailedAt: at, Host: "server",
	}))

	s := env.reopen(t)
	got, err := s.GetFingerprintWindows(ref)
	require.NoError(t, err)
	require.Empty(t, got, "stale windows must not sit beside a tombstone")
	fail, err := s.GetFingerprintWindowFailure(ref)
	require.NoError(t, err)
	require.NotNil(t, fail)
	require.Equal(t, "ffmpeg", fail.Reason)
	require.Equal(t, int64(42), fail.SourceSize)
	require.Equal(t, FingerprintWindowSchemaVersion, fail.SchemaVersion)
	require.True(t, fail.FailedAt.Equal(at))
}

func TestFpwin_RecordFailureRefusesOrphanAndEmptyReason(t *testing.T) {
	env := newBookSigEnv(t)
	_, id := fpwinSeedFile(t, env.store, "/lib/r/d.m4b", nil)
	require.Error(t, env.store.RecordFingerprintWindowFailure(&FingerprintWindowFailure{Ref: FileWindowRef(id)}))
	require.Error(t, env.store.RecordFingerprintWindowFailure(&FingerprintWindowFailure{Ref: FileWindowRef("gone"), Reason: "ffmpeg"}))
	require.Empty(t, fpwinKeysUnder(t, env.store, fpwinFailKeyPrefix))
}

func TestFpwin_GetFailureNoneIsNilNil(t *testing.T) {
	env := newBookSigEnv(t)
	_, id := fpwinSeedFile(t, env.store, "/lib/r/e.m4b", nil)
	fail, err := env.store.GetFingerprintWindowFailure(FileWindowRef(id))
	require.NoError(t, err)
	require.Nil(t, fail)
}
