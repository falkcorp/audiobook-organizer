// file: internal/database/pebble_file_provenance_chain_test.go
// version: 1.0.0
// guid: 26bcb28f-89d0-479a-ad50-50541b72d90f
// last-edited: 2026-08-21
//
// The tamper-evidence half of the provenance ledger: the per-chain hash link,
// the store-wide sequence, and the export cursor.
//
// What these pin down is the distinction that makes a verifier usable at all —
// "this row predates chaining" is NOT "this row was tampered with". A verifier
// that cannot tell those apart flags the entire existing library on its first
// run and gets ignored forever after.

package database

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chainKeys returns the raw Pebble keys of one file's chain, in key order, so
// a test can reach past the API and corrupt the store the way a bug would.
func chainKeys(t *testing.T, s *PebbleStore, bookFileID string) [][]byte {
	t.Helper()
	prefix := []byte(fileProvPrefix + bookFileID + ":")
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixEnd(prefix)})
	require.NoError(t, err)
	defer iter.Close()
	var out [][]byte
	for iter.First(); iter.Valid(); iter.Next() {
		out = append(out, append([]byte(nil), iter.Key()...))
	}
	return out
}

func appendN(t *testing.T, s *PebbleStore, bookFileID string, n int) {
	t.Helper()
	base := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	for i := range n {
		require.NoError(t, s.AppendFileEvent(FileEvent{
			BookFileID: bookFileID,
			Path:       "/lib/" + bookFileID + ".m4b",
			Kind:       FileEventObserved,
			At:         base.Add(time.Duration(i) * time.Hour),
			Digest:     FileDigest{SHA256Full: bookFileID + string(rune('a'+i))},
		}))
	}
}

func TestAppendFileEventAssignsAnAscendingStoreWideSequence(t *testing.T) {
	s := newProvStore(t)
	appendN(t, s, "bf1", 2)
	appendN(t, s, "bf2", 2)

	one, err := s.GetFileHistory("bf1")
	require.NoError(t, err)
	two, err := s.GetFileHistory("bf2")
	require.NoError(t, err)

	// Store-wide, not per-chain: bf2's events continue bf1's numbering.
	assert.Equal(t, []uint64{1, 2}, []uint64{one[0].Seq, one[1].Seq})
	assert.Equal(t, []uint64{3, 4}, []uint64{two[0].Seq, two[1].Seq})

	max, err := s.MaxFileEventSeq()
	require.NoError(t, err)
	assert.EqualValues(t, 4, max)
}

func TestAppendFileEventLinksEachEventToItsChainPredecessor(t *testing.T) {
	s := newProvStore(t)
	appendN(t, s, "bf1", 3)

	got, err := s.GetFileHistory("bf1")
	require.NoError(t, err)
	require.Len(t, got, 3)

	assert.Empty(t, got[0].PrevHash, "the first event in a chain links to nothing")
	assert.Equal(t, got[0].Hash, got[1].PrevHash)
	assert.Equal(t, got[1].Hash, got[2].PrevHash)
	for _, e := range got {
		assert.Equal(t, e.ComputeHash(), e.Hash, "stored hash disagrees with content")
	}
}

// Chains are per-file. If bf2's first event linked to bf1's tip, deleting any
// bf1 event would break an unrelated file's chain.
func TestChainsForDifferentFilesDoNotLinkToEachOther(t *testing.T) {
	s := newProvStore(t)
	appendN(t, s, "bf1", 2)
	appendN(t, s, "bf2", 1)

	two, err := s.GetFileHistory("bf2")
	require.NoError(t, err)
	require.Len(t, two, 1)
	assert.Empty(t, two[0].PrevHash)
}

// Callers append out of TIME order routinely — a pre-write observation
// recorded after the fact, an orphan adopted into the middle. The chain
// records the order things were WRITTEN, so this must still verify.
func TestVerifyFileChainAcceptsAChainAppendedOutOfTimeOrder(t *testing.T) {
	s := newProvStore(t)
	base := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	for _, at := range []time.Time{base.Add(2 * time.Hour), base, base.Add(time.Hour)} {
		require.NoError(t, s.AppendFileEvent(FileEvent{
			BookFileID: "bf1", Path: "/lib/a.m4b", Kind: FileEventObserved,
			At: at, Digest: FileDigest{SHA256Full: "h" + at.String()},
		}))
	}

	got, err := s.GetFileHistory("bf1")
	require.NoError(t, err)
	rep := VerifyFileChain(got)
	assert.Equal(t, ChainOK, rep.Verdict, "problems: %v", rep.Problems)
	assert.Equal(t, 3, rep.Chained)
}

func TestVerifyFileChainDetectsAnEventEditedInPlace(t *testing.T) {
	s := newProvStore(t)
	appendN(t, s, "bf1", 3)

	// Rewrite the middle event's path but leave its stored hash alone —
	// exactly what a buggy "fix up the paths" sweep would do.
	keys := chainKeys(t, s, "bf1")
	require.Len(t, keys, 3)
	val, closer, err := s.db.Get(keys[1])
	require.NoError(t, err)
	var e FileEvent
	require.NoError(t, json.Unmarshal(val, &e))
	require.NoError(t, closer.Close())
	e.Path = "/lib/somewhere-else.m4b"
	data, err := json.Marshal(e)
	require.NoError(t, err)
	require.NoError(t, s.db.Set(keys[1], data, pebble.Sync))

	got, err := s.GetFileHistory("bf1")
	require.NoError(t, err)
	rep := VerifyFileChain(got)
	assert.Equal(t, ChainBroken, rep.Verdict)
	assert.NotEmpty(t, rep.Problems)
}

func TestVerifyFileChainDetectsAnEventDeletedFromTheMiddle(t *testing.T) {
	s := newProvStore(t)
	appendN(t, s, "bf1", 3)

	keys := chainKeys(t, s, "bf1")
	require.Len(t, keys, 3)
	require.NoError(t, s.db.Delete(keys[1], pebble.Sync))

	got, err := s.GetFileHistory("bf1")
	require.NoError(t, err)
	require.Len(t, got, 2)
	rep := VerifyFileChain(got)
	assert.Equal(t, ChainBroken, rep.Verdict, "a deleted event left the chain looking intact")
}

// The whole point of the three-state verdict. Events written before chaining
// existed are legitimate; reporting them as tampering would flag the entire
// library on the first run after deploy.
func TestVerifyFileChainReportsPreChainEventsAsUnchainedNotBroken(t *testing.T) {
	legacy := []FileEvent{
		{BookFileID: "bf1", Path: "/lib/a.m4b", Kind: FileEventObserved},
		{BookFileID: "bf1", Path: "/lib/a.m4b", Kind: FileEventTagsWritten},
	}
	rep := VerifyFileChain(legacy)
	assert.Equal(t, ChainUnchained, rep.Verdict)
	assert.Equal(t, 2, rep.Unchained)
	assert.Empty(t, rep.Problems)
}

// A chain that starts unchained and then gains chained events is the real
// upgrade path. The first chained event has no verifiable predecessor, which
// must not read as a break.
func TestVerifyFileChainToleratesTheUpgradeBoundary(t *testing.T) {
	s := newProvStore(t)
	appendN(t, s, "bf1", 2)
	got, err := s.GetFileHistory("bf1")
	require.NoError(t, err)

	mixed := append([]FileEvent{{BookFileID: "bf1", Kind: FileEventObserved}}, got...)
	rep := VerifyFileChain(mixed)
	assert.Equal(t, ChainOK, rep.Verdict, "problems: %v", rep.Problems)
	assert.Equal(t, 1, rep.Unchained)
	assert.Equal(t, 2, rep.Chained)
}

// Without length prefixes, moving a character across a field boundary leaves
// the concatenation identical and the chain becomes trivially forgeable.
func TestCanonicalEncodingIsLengthPrefixed(t *testing.T) {
	a := FileEvent{Kind: FileEventObserved, Detail: "ab", Actor: "c"}
	b := FileEvent{Kind: FileEventObserved, Detail: "a", Actor: "bc"}
	assert.NotEqual(t, a.ComputeHash(), b.ComputeHash(),
		"fields are concatenated without lengths — the hash is forgeable")
}

func TestComputeHashCoversTheSequenceAndTheLink(t *testing.T) {
	base := FileEvent{Kind: FileEventObserved, Path: "/a", Seq: 7, PrevHash: "aa"}
	renumbered := base
	renumbered.Seq = 8
	relinked := base
	relinked.PrevHash = "bb"

	assert.NotEqual(t, base.ComputeHash(), renumbered.ComputeHash(), "seq is outside the digest")
	assert.NotEqual(t, base.ComputeHash(), relinked.ComputeHash(), "prev_hash is outside the digest")
}

func TestAdoptOrphanEventsLeavesAVerifiableChain(t *testing.T) {
	s := newProvStore(t)
	base := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)

	// Seen on disk before it had a row...
	require.NoError(t, s.AppendFileEvent(FileEvent{
		Path: "/incoming/a.m4b", Kind: FileEventObserved, At: base,
		Digest: FileDigest{SHA256Full: "pristine"},
	}))
	// ...then imported, and tagged.
	require.NoError(t, s.AppendFileEvent(FileEvent{
		BookFileID: "bf1", Path: "/lib/a.m4b", Kind: FileEventTagsWritten,
		At: base.Add(time.Hour), Digest: FileDigest{SHA256Full: "tagged"},
	}))

	n, err := s.AdoptOrphanEvents("bf1", "pristine")
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	got, err := s.GetFileHistory("bf1")
	require.NoError(t, err)
	require.Len(t, got, 2, "adoption must merge the two halves into one chain")
	rep := VerifyFileChain(got)
	assert.Equal(t, ChainOK, rep.Verdict, "adoption left the chain unverifiable: %v", rep.Problems)
}

// Adoption must re-link in the order events were WRITTEN, because that is the
// order VerifyFileChain walks. Key order is event-time order, and the two come
// apart whenever anything is backdated — which orphan adoption does by
// definition, since it preserves the original At.
//
// Here the orphan is observed at 09:00 but recorded first (seq 1), and the
// book_file's own event is dated 08:00 and recorded second (seq 2). Key order
// is 08:00 then 09:00; write order is the reverse. Re-linking along keys
// produces a chain the verifier reads as broken.
func TestAdoptOrphanEventsRelinksInWriteOrderNotTimeOrder(t *testing.T) {
	s := newProvStore(t)
	base := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)

	require.NoError(t, s.AppendFileEvent(FileEvent{
		Path: "/incoming/a.m4b", Kind: FileEventObserved, At: base.Add(time.Hour),
		Digest: FileDigest{SHA256Full: "pristine"},
	}))
	require.NoError(t, s.AppendFileEvent(FileEvent{
		BookFileID: "bf1", Path: "/lib/a.m4b", Kind: FileEventTagsWritten, At: base,
		Digest: FileDigest{SHA256Full: "tagged"},
	}))

	n, err := s.AdoptOrphanEvents("bf1", "pristine")
	require.NoError(t, err)
	require.Equal(t, 1, n)

	got, err := s.GetFileHistory("bf1")
	require.NoError(t, err)
	require.Len(t, got, 2)
	rep := VerifyFileChain(got)
	assert.Equal(t, ChainOK, rep.Verdict,
		"adoption re-linked along keys, not along write order: %v", rep.Problems)
}

// Adoption MOVES an event to a new key. The sequence index points at keys, so
// a stale pointer would make the export skip a real event forever.
func TestAdoptOrphanEventsRepointsTheSequenceIndex(t *testing.T) {
	s := newProvStore(t)
	require.NoError(t, s.AppendFileEvent(FileEvent{
		Path: "/incoming/a.m4b", Kind: FileEventObserved,
		Digest: FileDigest{SHA256Full: "pristine"},
	}))
	_, err := s.AdoptOrphanEvents("bf1", "pristine")
	require.NoError(t, err)

	rows, err := s.ScanFileEventsBySeq(0, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.EqualValues(t, 1, rows[0].Seq)
	assert.Equal(t, "bf1", rows[0].Event.BookFileID,
		"the sequence index still points at the pre-adoption row")
}

// A dangling sequence slot is the evidence that a row was deleted. Skipping it
// would hide exactly what the index exists to preserve.
func TestScanFileEventsBySeqSurfacesADanglingSlot(t *testing.T) {
	s := newProvStore(t)
	appendN(t, s, "bf1", 3)

	keys := chainKeys(t, s, "bf1")
	require.NoError(t, s.db.Delete(keys[1], pebble.Sync))

	rows, err := s.ScanFileEventsBySeq(0, 0)
	require.NoError(t, err)
	require.Len(t, rows, 3, "the deleted event's sequence slot vanished with it")
	assert.Empty(t, rows[1].Event.Kind, "the dangling slot should read back empty, not fabricated")
}

func TestExportCursorNeverMovesBackwards(t *testing.T) {
	s := newProvStore(t)
	require.NoError(t, s.SetFileProvExportCursor(10))
	require.NoError(t, s.SetFileProvExportCursor(4))

	cur, err := s.GetFileProvExportCursor()
	require.NoError(t, err)
	assert.EqualValues(t, 10, cur, "a rewind would re-append rows the JSONL already holds")
}

func TestExportCursorStartsAtZero(t *testing.T) {
	s := newProvStore(t)
	cur, err := s.GetFileProvExportCursor()
	require.NoError(t, err)
	assert.Zero(t, cur)
}
