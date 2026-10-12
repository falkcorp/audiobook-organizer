// file: internal/plugins/dedup/window_index_build_test.go
// version: 1.0.0
// guid: 5c1a8f37-d29e-4b60-93a4-8e0f6b2d7c15
// last-edited: 2026-10-11

package dedup

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
)

// Synthetic books ("Title 0001") and synthetic random-walk prints only.

func winRaw(seed uint64) []byte {
	rng := rand.New(rand.NewPCG(seed, seed^0x3c6ef372))
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

// seedWindowLibrary makes files books with one file each and three ws1
// windows per file, then wipes the index so the windows look like ones
// written before the write-through existed. It returns the file IDs.
func seedWindowLibrary(t *testing.T, s *database.PebbleStore, files int) []string {
	t.Helper()
	ids := make([]string, 0, files)
	for i := 0; i < files; i++ {
		b, err := s.CreateBook(&database.Book{Title: fmt.Sprintf("Title %04d", i), FilePath: fmt.Sprintf("/lib/t/%04d", i)})
		require.NoError(t, err)
		f := &database.BookFile{BookID: b.ID, FilePath: fmt.Sprintf("/lib/t/%04d/a.m4b", i), Format: "m4b"}
		require.NoError(t, s.CreateBookFile(f))
		for j, slot := range []int{1000, 5000, 9000} {
			require.NoError(t, s.PutFingerprintWindow(&database.FingerprintWindow{
				Ref: database.FileWindowRef(f.ID), Kind: database.WindowKindWindow, SlotBP: slot,
				WindowSet: fingerprint.WindowSetWS1, LengthSec: 120, DurationUsedSec: 3600, DurationSource: "book_file",
				Frames: 969, Raw: winRaw(uint64(i*10 + j + 1)), Algorithm: 2, Pipeline: fingerprint.WindowPipelineID,
				FpcalcVersion: "1.6.0", FFmpegVersion: "8.0.1", ComputedAt: time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC),
			}))
		}
		ids = append(ids, f.ID)
	}
	_, err := s.WipeByPrefixes([]string{"fpwinidx:", "fpwinidx_meta:"})
	require.NoError(t, err)
	return ids
}

func newWindowStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	s, err := database.NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.WaitForWarmup()
	return s
}

// countingWindowStore records how many PutWindowIndex calls run at once.
type countingWindowStore struct {
	*database.PebbleStore
	inFlight, maxInFlight atomic.Int64
}

func (c *countingWindowStore) PutWindowIndex(fileID string) (int, error) {
	n := c.inFlight.Add(1)
	for {
		m := c.maxInFlight.Load()
		if n <= m || c.maxInFlight.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(time.Millisecond) // let workers overlap
	defer c.inFlight.Add(-1)
	return c.PebbleStore.PutWindowIndex(fileID)
}

// cancelAfterReporter reports canceled once `after` items have asked.
type cancelAfterReporter struct {
	fakeReporter
	after int64
	asked atomic.Int64
}

func (r *cancelAfterReporter) IsCanceled() bool { return r.asked.Add(1) > r.after }

func indexSnapshot(t *testing.T, s *database.PebbleStore, ids []string) (keys int64, members map[string][]fingerprint.WindowBand) {
	t.Helper()
	keys, err := s.CountPrefix("fpwinidx:")
	require.NoError(t, err)
	members = make(map[string][]fingerprint.WindowBand, len(ids))
	for _, id := range ids {
		m, ok, err := s.WindowIndexMembers(id)
		require.NoError(t, err)
		if ok {
			members[id] = m
		}
	}
	return keys, members
}

func live(t *testing.T) json.RawMessage {
	t.Helper()
	return json.RawMessage(`{"dry_run":false}`)
}

func TestWindowIndexBuild_PreviewWritesNothing(t *testing.T) {
	s := newWindowStore(t)
	ids := seedWindowLibrary(t, s, 6)
	p := &Plugin{store: s}
	require.NoError(t, p.runWindowIndexBuild(context.Background(), nil, &fakeReporter{}))
	keys, members := indexSnapshot(t, s, ids)
	require.Zero(t, keys, "omitted mode is a preview")
	require.Empty(t, members)
	require.False(t, s.IsWindowIndexBuilt())
	require.Contains(t, windowIndexSizing(742_000), "= 35616000 keys")
}

func TestWindowIndexBuild_ResumableAndCancellable(t *testing.T) {
	const files = 50
	require.Greater(t, windowIndexBuildConcurrency(), 1, "a whole-library CPU loop must not run on one worker")

	// Reference: one uninterrupted build.
	fresh := newWindowStore(t)
	freshIDs := seedWindowLibrary(t, fresh, files)
	require.NoError(t, (&Plugin{store: fresh}).runWindowIndexBuild(context.Background(), live(t), &fakeReporter{}))
	require.True(t, fresh.IsWindowIndexBuilt())
	freshKeys, freshMembers := indexSnapshot(t, fresh, freshIDs)
	require.Len(t, freshMembers, files)

	// Interrupted: cancel after 40 percent of the items, then rerun.
	s := newWindowStore(t)
	ids := seedWindowLibrary(t, s, files)
	counting := &countingWindowStore{PebbleStore: s}
	p := &Plugin{store: counting}
	err := p.runWindowIndexBuild(context.Background(), live(t), &cancelAfterReporter{after: files * 4 / 10})
	require.Error(t, err)
	require.True(t, errors.Is(err, context.Canceled), "a canceled run stops with context.Canceled, got %v", err)
	require.False(t, s.IsWindowIndexBuilt(), "a partial pass never sets the flag")
	partialKeys, partial := indexSnapshot(t, s, ids)
	require.NotEmpty(t, partial, "the canceled run indexed some files")
	require.Less(t, len(partial), files, "and stopped before all of them")

	require.NoError(t, p.runWindowIndexBuild(context.Background(), live(t), &fakeReporter{}))
	require.True(t, s.IsWindowIndexBuilt())
	resumedKeys, resumed := indexSnapshot(t, s, ids)
	t.Logf("window-index-build: fresh build %d band keys / %d member lists; canceled run left %d keys / %d lists; resumed build %d keys / %d lists; max workers in flight %d",
		freshKeys, len(freshMembers), partialKeys, len(partial), resumedKeys, len(resumed), counting.maxInFlight.Load())
	require.Equal(t, freshKeys, resumedKeys, "resumed final key count equals a fresh build's")
	require.Len(t, resumed, files)
	// Files differ between the two stores (IDs), so compare the band sets
	// in seeding order: same prints, same bands.
	for i := range ids {
		require.Equal(t, freshMembers[freshIDs[i]], resumed[ids[i]], "file %d", i)
	}
	require.Greater(t, counting.maxInFlight.Load(), int64(1), "workers ran concurrently")
}
