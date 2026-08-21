// file: internal/fileops/write_tags_safe_provenance_test.go
// version: 1.0.0
// guid: 9d2f6b83-1e47-4a05-bc39-8f5a0d716e42
// last-edited: 2026-08-21

package fileops

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// recorder captures appended events and can be made to fail.
type recorder struct {
	events []database.FileEvent
	fail   error
}

func (r *recorder) AppendFileEvent(e database.FileEvent) error {
	if r.fail != nil {
		return r.fail
	}
	r.events = append(r.events, e)
	return nil
}

// failingHashUpdater reports an error from UpdateBookFileHashes.
type failingHashUpdater struct{ called bool }

func (f *failingHashUpdater) UpdateBookFileHashes(id, orig, post string) error {
	f.called = true
	return errors.New("column update exploded")
}

func writeTempAudio(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "book.m4b")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	return p
}

func kinds(events []database.FileEvent) []database.FileEventKind {
	out := make([]database.FileEventKind, 0, len(events))
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}

func TestWriteTagsSafeRecordsPreAndPostEvents(t *testing.T) {
	path := writeTempAudio(t, "original-bytes")
	rec := &recorder{}

	orig, post, err := WriteTagsSafe(path, func(tmp string) error {
		return os.WriteFile(tmp, []byte("mutated-bytes"), 0o644)
	}, WriteTagsSafeOptions{
		BookFileID:  "bf1",
		Provenance:  rec,
		Actor:       "test-op",
		Detail:      `author: "" -> "Brandon Sanderson"`,
		TorrentHash: "deadbeef",
	})
	require.NoError(t, err)

	require.Equal(t,
		[]database.FileEventKind{database.FileEventObserved, database.FileEventTagsWritten},
		kinds(rec.events))

	// The digests recorded must be the real before/after hashes, in that order.
	assert.Equal(t, orig, rec.events[0].Digest.SHA256Full)
	assert.Equal(t, post, rec.events[1].Digest.SHA256Full)
	assert.NotEqual(t, orig, post, "the write did not change the bytes; the test proves nothing")

	// Caller-supplied provenance metadata rides along on the events.
	assert.Equal(t, "deadbeef", rec.events[1].Digest.TorrentHash,
		"the torrent hash identifies the source release and must survive the write")
	assert.Equal(t, "test-op", rec.events[1].Actor)
	assert.Equal(t, `author: "" -> "Brandon Sanderson"`, rec.events[1].Detail)
	assert.Equal(t, "bf1", rec.events[1].BookFileID)
}

// The reason the pre-write record is written before the mutation rather than
// after it: if the write fails, the prior state must still be on record.
func TestWriteTagsSafeRecordsThePreWriteStateEvenWhenTheWriteFails(t *testing.T) {
	path := writeTempAudio(t, "original-bytes")
	rec := &recorder{}

	_, _, err := WriteTagsSafe(path, func(tmp string) error {
		return errors.New("tag write blew up")
	}, WriteTagsSafeOptions{BookFileID: "bf1", Provenance: rec})
	require.Error(t, err)

	require.Len(t, rec.events, 1, "the pre-write observation must survive a failed write")
	assert.Equal(t, database.FileEventObserved, rec.events[0].Kind)

	// And the file itself is untouched, so the recorded digest is still true.
	got, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, "original-bytes", string(got))
}

// Provenance alone is reason enough to compute the digests. Before this, the
// hash gate keyed off Store only, so a provenance-only caller silently recorded
// empty hashes.
func TestWriteTagsSafeComputesHashesForProvenanceWithoutAStore(t *testing.T) {
	path := writeTempAudio(t, "original-bytes")
	rec := &recorder{}

	orig, post, err := WriteTagsSafe(path, func(tmp string) error {
		return os.WriteFile(tmp, []byte("mutated-bytes"), 0o644)
	}, WriteTagsSafeOptions{BookFileID: "bf1", Provenance: rec})
	require.NoError(t, err)

	assert.NotEmpty(t, orig)
	assert.NotEmpty(t, post)
	for i, e := range rec.events {
		assert.NotEmpty(t, e.Digest.SHA256Full, "event %d recorded an empty digest", i)
	}
}

// With neither destination configured the expensive full-file hashing is still
// skipped — the performance fix that motivated the gate must survive.
func TestWriteTagsSafeSkipsHashingWithNoStoreAndNoProvenance(t *testing.T) {
	path := writeTempAudio(t, "original-bytes")

	orig, post, err := WriteTagsSafe(path, func(tmp string) error {
		return os.WriteFile(tmp, []byte("mutated-bytes"), 0o644)
	}, WriteTagsSafeOptions{})
	require.NoError(t, err)
	assert.Empty(t, orig)
	assert.Empty(t, post)
}

// A failure to record provenance must never fail the file operation it is
// merely observing.
func TestWriteTagsSafeSucceedsWhenProvenanceRecordingFails(t *testing.T) {
	path := writeTempAudio(t, "original-bytes")
	rec := &recorder{fail: errors.New("ledger unavailable")}

	_, _, err := WriteTagsSafe(path, func(tmp string) error {
		return os.WriteFile(tmp, []byte("mutated-bytes"), 0o644)
	}, WriteTagsSafeOptions{BookFileID: "bf1", Provenance: rec})
	require.NoError(t, err, "a provenance failure must not fail the write")

	got, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, "mutated-bytes", string(got))
}

// The column update is best-effort, but the write itself still succeeds and the
// ledger still holds the record.
func TestWriteTagsSafeSucceedsWhenTheHashColumnUpdateFails(t *testing.T) {
	path := writeTempAudio(t, "original-bytes")
	rec := &recorder{}
	store := &failingHashUpdater{}

	_, post, err := WriteTagsSafe(path, func(tmp string) error {
		return os.WriteFile(tmp, []byte("mutated-bytes"), 0o644)
	}, WriteTagsSafeOptions{BookFileID: "bf1", Store: store, Provenance: rec})
	require.NoError(t, err)

	assert.True(t, store.called, "the column update was not attempted")
	require.Len(t, rec.events, 2)
	assert.Equal(t, post, rec.events[1].Digest.SHA256Full,
		"the ledger must still hold the post-write digest when the columns do not")
}

func TestWriteTagsSafeRecordsFileSizeOnEvents(t *testing.T) {
	path := writeTempAudio(t, "original-bytes")
	rec := &recorder{}

	_, _, err := WriteTagsSafe(path, func(tmp string) error {
		return os.WriteFile(tmp, []byte("a-longer-set-of-mutated-bytes"), 0o644)
	}, WriteTagsSafeOptions{BookFileID: "bf1", Provenance: rec})
	require.NoError(t, err)

	require.Len(t, rec.events, 2)
	assert.EqualValues(t, len("original-bytes"), rec.events[0].Digest.SizeBytes)
	assert.EqualValues(t, len("a-longer-set-of-mutated-bytes"), rec.events[1].Digest.SizeBytes)
}
