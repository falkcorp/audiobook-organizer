// file: internal/metafetch/identity_prior_test.go
// version: 1.0.0
// guid: 9ae53579-716a-44b6-adb8-2944ae90ce16
// last-edited: 2026-10-06

package metafetch

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// A bracket is a note on the credit; the name outside it is the author.
// Only an empty or studio remainder hands the author to a bracket.
func TestSearchAuthorHint_BracketNoteKeepsTheName(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Homer [Fagles]", "Homer"},
		{"Pat Reader [trans. Lee Author]", "Pat Reader"},
		{"Jane Example [XYZ]", "Jane Example"},
		{"GraphicAudio [Jane Example]", "Jane Example"},
		{"[XYZ]", ""},
	} {
		assert.Equal(t, tc.want, SearchAuthorHint(tc.in), tc.in)
	}
}

// A row hashed with no author proves a credit that only CLEANS to "" (a
// bracket tag) through its search fingerprint, never by the hash alone:
// that shortcut is for the system's placeholders. A row fetched for another
// author stays stale; a row the batch fetch wrote for this credit passes.
func TestCachedQueryMatches_CleanedToEmptyCreditNeedsTheFingerprint(t *testing.T) {
	f := newVerdictFixture(t)
	b, err := f.store.CreateBook(&database.Book{Title: "Harbor Lights", FilePath: "/lib/id/harbor-lights.m4b"})
	require.NoError(t, err)
	book := f.book(b.ID)
	other := f.mfs.resolveSearchInputs(book, book.Title, "Kim Writer", "").fingerprint(book.Title)
	for _, live := range []string{"Pat Reader [trans. Lee Author]", "Homer [Fagles]", "[XYZ]"} {
		entry := &MetadataCandidateCache{BookID: b.ID, SourceHash: BatchSourceHash(b.ID, book.Title, ""), SearchFingerprint: other}
		require.ErrorIs(t, f.mfs.ValidateCachedIdentityForBook(entry, book, []string{live}), ErrStaleMetadataCache, live)
	}
	fresh := f.mfs.resolveSearchInputs(book, book.Title, SearchAuthorHint("[XYZ]"), "").fingerprint(book.Title)
	entry := &MetadataCandidateCache{BookID: b.ID, SourceHash: BatchSourceHash(b.ID, book.Title, ""), SearchFingerprint: fresh}
	require.NoError(t, f.mfs.ValidateCachedIdentityForBook(entry, book, []string{"[XYZ]"}))
}

// putRow stores a cache row for book with the given fingerprint and hash.
func putRow(t *testing.T, f *verdictFixture, book *database.Book, hash, fp string, candidates bool) {
	t.Helper()
	now := time.Now().UTC()
	entry := &database.MetadataCandidateCache{BookID: book.ID, FetchedAt: now, SourceHash: hash, SearchFingerprint: fp}
	if candidates {
		raw, err := json.Marshal(map[string]string{"title": book.Title, "author": "Someone"})
		require.NoError(t, err)
		entry.Candidates = []json.RawMessage{raw}
	} else {
		entry.LastEmptyFetchAt = &now
	}
	require.NoError(t, f.store.PutMetadataCache(entry))
}

// Only the suspect-author question changed: a row's candidates are still the
// book's (served, and accepted by the apply gate); its "nothing found" did
// not ask the title-only question and is re-asked.
func TestFingerprint_SuspectFlagFlipKeepsCandidates(t *testing.T) {
	f := newVerdictFixture(t)
	a, err := f.store.CreateAuthor("Stephen King")
	require.NoError(t, err)
	b, err := f.store.CreateBook(&database.Book{Title: "The Stephen King Collection", FilePath: "/lib/id/sk.m4b", AuthorID: &a.ID})
	require.NoError(t, err)
	book := f.book(b.ID)
	in := f.mfs.resolveSearchInputs(book, book.Title, "Stephen King", "")
	require.True(t, in.parsed.SuspectAuthor)
	before := in.fingerprintWith(book.Title, false)
	hash := BatchSourceHash(b.ID, book.Title, "Stephen King")

	putRow(t, f, book, hash, before, true)
	entry, verdict, _ := f.mfs.CachedBatchVerdict(f.book(b.ID), book.Title, "Stephen King")
	require.Equal(t, BatchVerdictFreshCandidates, verdict)
	require.NoError(t, f.mfs.ValidateCachedIdentityForBook(entry, f.book(b.ID), []string{"Stephen King"}))

	putRow(t, f, book, hash, before, false)
	_, verdict, _ = f.mfs.CachedBatchVerdict(f.book(b.ID), book.Title, "Stephen King")
	require.Equal(t, BatchVerdictNone, verdict, "an empty answer that did not ask the title alone is re-asked")
	require.False(t, f.mfs.SearchFingerprintCurrent(before, f.book(b.ID), book.Title, "Stephen King"))
}

// A row fetched before 2026-10-06 asked the same book's questions read the
// old way (no author cleaning, no title normalisation). Its candidates stay
// valid, and a pre-2026-09-28 no-author row still proves its author through
// that fingerprint; only its "nothing found" is re-asked.
func TestFingerprint_PrePRRowStillProvesItsIdentity(t *testing.T) {
	f := newVerdictFixture(t)
	for _, tc := range []struct{ title, author string }{
		{"Onward", "zzJane Example"},
		{"Wasteland Tales 2_ More Example Stories", "John Sample"},
		{"Meeting Point (2017)", "Jane Example"},
	} {
		t.Run(tc.title, func(t *testing.T) {
			a, err := f.store.CreateAuthor(tc.author)
			require.NoError(t, err)
			b, err := f.store.CreateBook(&database.Book{Title: tc.title, FilePath: "/lib/id/" + tc.title + ".m4b", AuthorID: &a.ID})
			require.NoError(t, err)
			book := f.book(b.ID)
			live, err := database.LiveBookAuthorNames(f.store, book)
			require.NoError(t, err)
			old := f.mfs.resolveSearchInputsWith(book, book.Title, legacyAuthorHint(live[0]), "", &resolveOpts{prePR: true}).fingerprintWith(book.Title, false)
			require.NotEqual(t, old, f.mfs.resolveSearchInputs(book, book.Title, SearchAuthorHint(live[0]), "").fingerprint(book.Title), "fixture: the rules changed this book's questions")

			putRow(t, f, book, BatchSourceHash(b.ID, book.Title, ""), old, true)
			entry, verdict, _ := f.mfs.CachedBatchVerdict(f.book(b.ID), book.Title, SearchAuthorHint(live[0]))
			require.Equal(t, BatchVerdictFreshCandidates, verdict)
			require.NoError(t, f.mfs.ValidateCachedIdentityForBook(entry, f.book(b.ID), live))
		})
	}
}

// An authority read fault does not change which questions a row is judged
// against: a fingerprint written while the lists answered is still current
// while they fault.
func TestFingerprint_AuthorityFaultIsNoChange(t *testing.T) {
	book := &database.Book{ID: "b1", Title: "Ring Harbor"}
	svc := fanoutHarness(t, book)
	stored := svc.resolveSearchInputs(book, book.Title, "Ring Harbor", "").fingerprint(book.Title)
	mock, ok := svc.db.(*database.MockStore)
	require.True(t, ok)
	mock.GetRawFunc = func(string) ([]byte, error) { return nil, errors.New("pebble: closed") }
	faulted := svc.resolveSearchInputs(book, book.Title, "Ring Harbor", "")
	require.NotEqual(t, stored, faulted.fingerprint(book.Title), "fixture: a fault alone changes the resolution")
	assert.Equal(t, fingerprintCurrent, svc.matchSearchFingerprint(stored, book, book.Title, "Ring Harbor", ""))
}
