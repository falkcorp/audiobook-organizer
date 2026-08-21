// file: internal/database/pebble_file_provenance_test.go
// version: 1.0.0
// guid: 5a3c7e21-8b40-4f96-a2d1-7c9e0b4f6835
// last-edited: 2026-08-21

package database

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newProvStore returns a concrete *PebbleStore, because the provenance methods
// are deliberately not part of the wide Store interface.
func newProvStore(t *testing.T) *PebbleStore {
	t.Helper()
	store, err := NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestAppendFileEventRoundTripsInChronologicalOrder(t *testing.T) {
	s := newProvStore(t)
	base := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)

	// Appended out of order on purpose — the key encodes the timestamp, so the
	// scan must return them by time, not by insertion.
	for _, e := range []FileEvent{
		{BookFileID: "bf1", Path: "/lib/a.m4b", Kind: FileEventTagsWritten, At: base.Add(2 * time.Hour), Digest: FileDigest{SHA256Full: "cc"}},
		{BookFileID: "bf1", Path: "/lib/a.m4b", Kind: FileEventObserved, At: base, Digest: FileDigest{SHA256Full: "aa"}},
		{BookFileID: "bf1", Path: "/lib/a.m4b", Kind: FileEventObserved, At: base.Add(time.Hour), Digest: FileDigest{SHA256Full: "bb"}},
	} {
		require.NoError(t, s.AppendFileEvent(e))
	}

	got, err := s.GetFileHistory("bf1")
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, []string{"aa", "bb", "cc"},
		[]string{got[0].Digest.SHA256Full, got[1].Digest.SHA256Full, got[2].Digest.SHA256Full})
}

// The ledger's entire premise is that it never loses an entry. Two events
// stamped the same nanosecond must both survive; a naive timestamp key would
// silently overwrite the first.
func TestAppendFileEventKeepsBothEventsInTheSameNanosecond(t *testing.T) {
	s := newProvStore(t)
	at := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)

	require.NoError(t, s.AppendFileEvent(FileEvent{
		BookFileID: "bf1", Path: "/lib/a.m4b", Kind: FileEventObserved, At: at,
		Digest: FileDigest{SHA256Full: "first"},
	}))
	require.NoError(t, s.AppendFileEvent(FileEvent{
		BookFileID: "bf1", Path: "/lib/a.m4b", Kind: FileEventTagsWritten, At: at,
		Digest: FileDigest{SHA256Full: "second"},
	}))

	got, err := s.GetFileHistory("bf1")
	require.NoError(t, err)
	require.Len(t, got, 2, "an event was overwritten by one with an identical timestamp")
	assert.Equal(t, "first", got[0].Digest.SHA256Full)
	assert.Equal(t, "second", got[1].Digest.SHA256Full)
}

// This is the payoff over the two-column scheme: a hash recorded BEFORE a tag
// write still resolves to the file afterwards, when the columns have long since
// been overwritten with a different value.
func TestFindFileEventsByHashResolvesASupersededHash(t *testing.T) {
	s := newProvStore(t)
	base := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)

	require.NoError(t, s.AppendFileEvent(FileEvent{
		BookFileID: "bf1", Path: "/lib/a.m4b", Kind: FileEventObserved, At: base,
		Digest: FileDigest{SHA256Full: "before-the-write"},
	}))
	require.NoError(t, s.AppendFileEvent(FileEvent{
		BookFileID: "bf1", Path: "/lib/a.m4b", Kind: FileEventTagsWritten, At: base.Add(time.Second),
		Digest: FileDigest{SHA256Full: "after-the-write"},
	}))

	found, err := s.FindFileEventsByHash("before-the-write")
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, "bf1", found[0].BookFileID)
	assert.Equal(t, FileEventObserved, found[0].Kind)
}

// Rows in the wild carry either the full or the chunked hash. Indexing only one
// would leave half the library unresolvable.
func TestFindFileEventsByHashIndexesBothFullAndChunkedDigests(t *testing.T) {
	s := newProvStore(t)

	require.NoError(t, s.AppendFileEvent(FileEvent{
		BookFileID: "bf1", Path: "/lib/a.m4b", Kind: FileEventObserved,
		At:     time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC),
		Digest: FileDigest{SHA256Full: "full-digest", SHA256Chunk: "chunk-digest"},
	}))

	for _, h := range []string{"full-digest", "chunk-digest"} {
		found, err := s.FindFileEventsByHash(h)
		require.NoError(t, err, h)
		require.Len(t, found, 1, "hash %q did not resolve", h)
		assert.Equal(t, "bf1", found[0].BookFileID)
	}
}

func TestAppendFileEventRejectsAnUnadoptableOrphan(t *testing.T) {
	s := newProvStore(t)

	// No BookFileID and no full hash: nothing could ever match it back to a
	// row, so it would be write-only data.
	err := s.AppendFileEvent(FileEvent{
		Path: "/outside/a.m4b", Kind: FileEventObserved,
		Digest: FileDigest{SizeBytes: 123},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sha256_full")
}

func TestAppendFileEventRequiresAKind(t *testing.T) {
	s := newProvStore(t)
	err := s.AppendFileEvent(FileEvent{BookFileID: "bf1", Path: "/lib/a.m4b"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kind")
}

// A file seen on disk before it has a row, then imported, must read back as one
// continuous history rather than two disconnected halves.
func TestAdoptOrphanEventsJoinsPreImportObservationsToTheChain(t *testing.T) {
	s := newProvStore(t)
	base := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)

	// Observed outside the library, before any row existed.
	require.NoError(t, s.AppendFileEvent(FileEvent{
		Path: "/mnt/bigdata/books/abooks/a.m4b", Kind: FileEventObserved, At: base,
		Digest: FileDigest{SHA256Full: "pristine"},
	}))
	// Then imported and given a row.
	require.NoError(t, s.AppendFileEvent(FileEvent{
		BookFileID: "bf1", Path: "/lib/a.m4b", Kind: FileEventImported, At: base.Add(time.Hour),
		Digest: FileDigest{SHA256Full: "pristine"},
	}))

	// Before adoption the pre-import observation is not in the chain.
	pre, err := s.GetFileHistory("bf1")
	require.NoError(t, err)
	require.Len(t, pre, 1)

	n, err := s.AdoptOrphanEvents("bf1", "pristine")
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	post, err := s.GetFileHistory("bf1")
	require.NoError(t, err)
	require.Len(t, post, 2, "the pre-import observation was not adopted")
	assert.Equal(t, FileEventObserved, post[0].Kind)
	assert.Equal(t, "/mnt/bigdata/books/abooks/a.m4b", post[0].Path,
		"adoption must preserve where the file was originally seen")
	assert.Equal(t, "bf1", post[0].BookFileID)

	// Adoption moves rather than copies, so a second call finds nothing.
	again, err := s.AdoptOrphanEvents("bf1", "pristine")
	require.NoError(t, err)
	assert.Equal(t, 0, again, "orphan rows were copied, not moved")
}

func TestAdoptOrphanEventsIsANoOpWhenNothingMatches(t *testing.T) {
	s := newProvStore(t)
	n, err := s.AdoptOrphanEvents("bf1", "no-such-hash")
	require.NoError(t, err)
	assert.Zero(t, n)
}

func TestGetFileHistoryRejectsAnEmptyID(t *testing.T) {
	s := newProvStore(t)
	_, err := s.GetFileHistory("")
	require.Error(t, err)
}

func TestFileDigestIsZero(t *testing.T) {
	assert.True(t, FileDigest{}.IsZero())
	assert.False(t, FileDigest{SizeBytes: 1}.IsZero())
	assert.False(t, FileDigest{TorrentHash: "abc"}.IsZero())
}
