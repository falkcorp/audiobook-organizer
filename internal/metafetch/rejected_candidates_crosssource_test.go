// file: internal/metafetch/rejected_candidates_crosssource_test.go
// version: 1.1.0
// guid: 5d1f7c3a-8e2b-4a96-b0c4-3e7a9d6f2b18
// last-edited: 2026-10-10

package metafetch

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// Owner decision 2026-10-10: a rejected ASIN or ISBN is rejected for the
// book across every source. All fixtures are synthetic.

const audnexusSource = "Audnexus (Audible)"

func TestRejectedSet_IdentifiersAreBookWideAcrossSources(t *testing.T) {
	audible := &MetadataCandidate{Source: "Audible", Title: "Synthetic Field Notes 6", ASIN: "B0SYNXSRC1"}

	t.Run("an Audible ASIN rejection refuses Audnexus's record of that ASIN", func(t *testing.T) {
		m := memRejections{}
		require.NoError(t, RecordRejection(m, "b1", audible))
		set := loadSet(t, m, "b1")
		require.True(t, set.HasCandidate(&MetadataCandidate{Source: audnexusSource, Title: "Synthetic Field Notes 6", ASIN: "b0synxsrc1"}))
		require.True(t, set.HasMetadata(audnexusSource, &metadata.BookMetadata{Title: "Synthetic Field Notes 6 (Unabridged)", ASIN: "B0SYNXSRC1"}),
			"the ASIN decides whatever title the other source gives the product")
		require.False(t, set.HasCandidate(&MetadataCandidate{Source: audnexusSource, Title: "Synthetic Field Notes 6", ASIN: "B0SYNOTHER"}),
			"another ASIN of the same title is not refused")
	})
	t.Run("the index is a positive match only", func(t *testing.T) {
		m := memRejections{}
		require.NoError(t, RecordRejection(m, "b1", audible))
		set := loadSet(t, m, "b1")
		require.False(t, set.HasCandidate(&MetadataCandidate{Source: audnexusSource, Title: "Synthetic Field Notes 6"}),
			"no identifier: the title fallback stays under the rejection's own source")
		require.False(t, set.HasCandidate(&MetadataCandidate{Source: "Synthetic Books", Title: "Synthetic Field Notes 6", ISBN13: "9780306406157"}),
			"an ISBN-only record of another source shares no identifier with an ASIN rejection")
		require.False(t, loadSet(t, m, "b2").HasCandidate(&MetadataCandidate{Source: audnexusSource, Title: "Synthetic Field Notes 6", ASIN: "B0SYNXSRC1"}),
			"another book is not affected")
	})
	t.Run("an ISBN rejection refuses any provider's record of that ISBN, ISBN-10 or -13", func(t *testing.T) {
		m := memRejections{}
		require.NoError(t, RecordRejection(m, "b1", &MetadataCandidate{Source: "Open Library", Title: "Synthetic Field Notes 6", ISBN10: "0-306-40615-2"}))
		set := loadSet(t, m, "b1")
		require.True(t, set.HasCandidate(&MetadataCandidate{Source: "Synthetic Books", Title: "Another Title", ISBN13: "978-0-306-40615-7"}))
		require.True(t, set.HasCandidate(&MetadataCandidate{Source: "Synthetic Books", Title: "Another Title", ISBN: "0306406152"}))
		require.False(t, set.HasCandidate(&MetadataCandidate{Source: "Synthetic Books", Title: "Another Title", ISBN13: "9780000000027"}))
	})
	t.Run("a legacy title-wide key joins no index", func(t *testing.T) {
		m := memRejections{RejectedCandidateStoreKey("b1", "Audible", "Synthetic Field Notes 6"): []byte("1")}
		set := loadSet(t, m, "b1")
		require.True(t, set.HasCandidate(audible))
		require.False(t, set.HasCandidate(&MetadataCandidate{Source: audnexusSource, Title: "Synthetic Field Notes 6", ASIN: "B0SYNXSRC1"}))
	})
	t.Run("LoadAllRejectedCandidates builds the index too", func(t *testing.T) {
		m := memRejections{}
		require.NoError(t, RecordRejection(m, "b1", audible))
		all, err := LoadAllRejectedCandidates(m)
		require.NoError(t, err)
		require.True(t, all["b1"].HasCandidate(&MetadataCandidate{Source: audnexusSource, Title: "x", ASIN: "B0SYNXSRC1"}))
	})
}

func TestCanonicalISBN(t *testing.T) {
	require.Equal(t, "9780306406157", canonicalISBN("0-306-40615-2"))
	require.Equal(t, "9780306406157", canonicalISBN("978-0-306-40615-7"))
	require.Equal(t, "9780000000019", canonicalISBN("000000001X"), "a check digit X converts")
	require.Equal(t, "abc", canonicalISBN("abc"), "not an ISBN: left as normalised")
	// A stored value written before ISBNs were canonicalised still matches.
	m := memRejections{RejectedCandidateStoreKey("b1", "Open Library", "T"): RejectionValue(nil, []string{"isbn:0306406152"})}
	require.True(t, loadSet(t, m, "b1").HasCandidate(&MetadataCandidate{Source: "Synthetic Books", Title: "U", ISBN13: "9780306406157"}))
}

// N1: an "ISBN" label in front of the number is dropped, and the "10"/"13"
// after it only when what is left is then an ISBN-10 or ISBN-13 long.
func TestCanonicalISBN_Labels(t *testing.T) {
	for in, want := range map[string]string{
		"ISBN 0306406152":            "9780306406157",
		"ISBN-13: 978-0-306-40615-7": "9780306406157",
		"ISBN-10: 0-306-40615-2":     "9780306406157",
		"isbn13 9780306406157":       "9780306406157",
		// An ISBN-10 that starts with 10 keeps its digits.
		"ISBN 1000000000": canonicalISBN("1000000000"),
		"ISBN 1300000000": canonicalISBN("1300000000"),
	} {
		require.Equal(t, want, canonicalISBN(in), in)
	}
	require.NotEqual(t, canonicalISBN("0000000000"), canonicalISBN("ISBN 1000000000"))
	m := memRejections{}
	require.NoError(t, RecordRejection(m, "b1", &MetadataCandidate{Source: "Open Library", Title: "T", ISBN: "ISBN 0306406152"}))
	require.True(t, loadSet(t, m, "b1").HasCandidate(&MetadataCandidate{Source: "Synthetic Books", Title: "U", ISBN13: "9780306406157"}))
}

// S3, owner decision 2026-10-10 (ASIN and ISBN rejections apply across all
// sources): an Audible rejection stores its record's ISBN as well as its
// ASIN, so another provider's record carrying that audiobook ISBN is
// refused too.
func TestRecordRejection_AudibleRejectionAlsoRejectsItsISBNAcrossSources(t *testing.T) {
	x := &MetadataCandidate{Source: "Audible", Title: "Synthetic Field Notes 7", ASIN: "B0SYNISBN1", ISBN13: "9780306406157"}
	m := memRejections{}
	require.NoError(t, RecordRejection(m, "b1", x))
	set := loadSet(t, m, "b1")
	require.True(t, set.HasCandidate(&MetadataCandidate{Source: "Google Books", Title: "Field Notes Seven", ISBN10: "0-306-40615-2"}),
		"another source's record of the rejected record's ISBN is refused")
	require.True(t, set.HasCandidate(&MetadataCandidate{Source: audnexusSource, Title: "Synthetic Field Notes 7", ASIN: "B0SYNISBN1"}))
	require.False(t, set.HasCandidate(&MetadataCandidate{Source: "Audible", Title: "Synthetic Field Notes 7", ASIN: "B0SYNISBN2", ISBN13: "9780000000027"}),
		"the other edition of the title (its own ASIN and ISBN) is not refused")
	require.False(t, set.HasCandidate(&MetadataCandidate{Source: "Audible", Title: "Synthetic Field Notes 7", ISBN13: "9780000000027"}),
		"same source+title, no ASIN, another ISBN: a different edition, now that the rejection names an ISBN")
	require.False(t, set.HasCandidate(&MetadataCandidate{Source: "Google Books", Title: "Synthetic Field Notes 7", ISBN13: "9780000000027"}))
}

// S4: Undo removes the undone record's rejection only. Another source's
// rejection that shares an ISBN with it is the owner's separate decision
// and stays, with its ISBN still refused.
func TestRemoveRejection_KeepsAnotherRecordSharingAnISBN(t *testing.T) {
	x := &MetadataCandidate{Source: "Audible", Title: "Synthetic Field Notes 9", ASIN: "B0SYNUNDO1", ISBN13: "9780306406157"}
	y := &MetadataCandidate{Source: "Open Library", Title: "Synthetic Field Notes Nine", ISBN10: "0306406152"}
	otherWithI := &MetadataCandidate{Source: "Hardcover", Title: "Anything", ISBN13: "9780306406157"}
	m := memRejections{}
	require.NoError(t, RecordRejection(m, "b1", x))
	require.NoError(t, RecordRejection(m, "b1", y))

	changed, err := RemoveRejection(m, "b1", x)
	require.NoError(t, err)
	require.True(t, changed)
	require.NotContains(t, m, RejectedCandidateStoreKey("b1", "Audible", "Synthetic Field Notes 9"), "X's own rejection is gone")
	require.Contains(t, m, RejectedCandidateStoreKey("b1", "Open Library", "Synthetic Field Notes Nine"), "Y's rejection was deleted by X's undo")
	set := loadSet(t, m, "b1")
	require.True(t, set.HasCandidate(y), "Y stays rejected")
	require.True(t, set.HasCandidate(otherWithI), "Y's ISBN stays refused across sources")
	require.True(t, set.HasCandidate(x), "X carries Y's rejected ISBN, so it is still refused (still_rejected)")
	require.False(t, set.HasCandidate(&MetadataCandidate{Source: audnexusSource, Title: "Synthetic Field Notes 9", ASIN: "B0SYNUNDO1"}),
		"X's ASIN is no longer refused")
}

// S4: undo from the Audnexus row takes the Audible rejection of that ASIN
// out whole, its ISBN with it.
func TestRemoveRejection_ViaAudnexusTakesTheRecordsISBNToo(t *testing.T) {
	x := &MetadataCandidate{Source: "Audible", Title: "Synthetic Field Notes 9", ASIN: "B0SYNUNDO1", ISBN13: "9780306406157"}
	keep := &MetadataCandidate{Source: "Audible", Title: "Synthetic Field Notes 9", ASIN: "B0SYNUNDO2", ISBN13: "9780000000027"}
	for name, row := range map[string]*MetadataCandidate{
		"row with the ASIN only":     {Source: audnexusSource, Title: "Synthetic Field Notes 9", ASIN: "B0SYNUNDO1"},
		"row with the ASIN and ISBN": {Source: audnexusSource, Title: "Synthetic Field Notes 9", ASIN: "b0synundo1", ISBN10: "0306406152"},
	} {
		t.Run(name, func(t *testing.T) {
			m := memRejections{}
			require.NoError(t, RecordRejection(m, "b1", x))
			require.NoError(t, RecordRejection(m, "b1", keep))
			changed, err := RemoveRejection(m, "b1", row)
			require.NoError(t, err)
			require.True(t, changed)
			set := loadSet(t, m, "b1")
			require.False(t, set.HasCandidate(x), "the Audible row is no longer refused")
			require.False(t, set.HasCandidate(row))
			require.False(t, set.HasCandidate(&MetadataCandidate{Source: "Hardcover", Title: "Anything", ISBN13: "9780306406157"}),
				"the undone record's ISBN left with its ASIN")
			require.True(t, set.HasCandidate(keep), "the other record of the key stays rejected")
		})
	}
}

// Undo from the Audnexus row removes the Audible rejection of the same
// ASIN: one product, served by both.
func TestRemoveRejection_RemovesIdentifiersAcrossSources(t *testing.T) {
	x := &MetadataCandidate{Source: "Audible", Title: "Synthetic Field Notes 6", ASIN: "B0SYNXSRC1"}
	y := &MetadataCandidate{Source: "Audible", Title: "Synthetic Field Notes 6", ASIN: "B0SYNXSRC2"}
	viaAudnexus := &MetadataCandidate{Source: audnexusSource, Title: "Synthetic Field Notes 6", ASIN: "B0SYNXSRC1"}
	audibleKey := RejectedCandidateStoreKey("b1", "Audible", "Synthetic Field Notes 6")
	legacyKey := RejectedCandidateStoreKey("b1", "Google Books", "Synthetic Field Notes 6")

	t.Run("the last identifier: the other source's key is deleted, not widened", func(t *testing.T) {
		m := memRejections{legacyKey: []byte("1")}
		require.NoError(t, RecordRejection(m, "b1", x))
		changed, err := RemoveRejection(m, "b1", viaAudnexus)
		require.NoError(t, err)
		require.True(t, changed)
		require.NotContains(t, m, audibleKey, "a key left with no identifier must be deleted (an empty value would read as every record)")
		require.Equal(t, []byte("1"), m[legacyKey], "another source's title-wide rejection is left alone")
		set := loadSet(t, m, "b1")
		require.False(t, set.HasCandidate(viaAudnexus))
		require.False(t, set.HasCandidate(x), "the Audible record of the ASIN is un-rejected too")
		require.False(t, set.HasCandidate(y))
	})
	t.Run("other identifiers of the key stay", func(t *testing.T) {
		m := memRejections{}
		require.NoError(t, RecordRejection(m, "b1", x))
		require.NoError(t, RecordRejection(m, "b1", y))
		changed, err := RemoveRejection(m, "b1", viaAudnexus)
		require.NoError(t, err)
		require.True(t, changed)
		require.Equal(t, RejectionValue(nil, CandidateIdentifiers("B0SYNXSRC2", "", "", "")), m[audibleKey])
		set := loadSet(t, m, "b1")
		require.False(t, set.HasCandidate(x))
		require.True(t, set.HasCandidate(y), "the other rejected record stays rejected")
	})
	t.Run("nothing of the record's: unchanged", func(t *testing.T) {
		m := memRejections{}
		require.NoError(t, RecordRejection(m, "b1", y))
		changed, err := RemoveRejection(m, "b1", viaAudnexus)
		require.NoError(t, err)
		require.False(t, changed)
		require.True(t, loadSet(t, m, "b1").HasCandidate(y))
	})
}

// S2: a rejection is written holding the book's write stripe, the stripe an
// apply re-reads the rejections under before its commit, so the two are
// ordered.
func TestRejectionWrites_HoldTheBookWriteStripe(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(RejectionStore, string) error
	}{
		{"RecordRejection", func(s RejectionStore, id string) error {
			return RecordRejection(s, id, &MetadataCandidate{Source: "Audible", Title: "Synthetic Field Notes 6", ASIN: "B0SYNXSRC1"})
		}},
		{"RemoveRejection", func(s RejectionStore, id string) error {
			_, err := RemoveRejection(s, id, &MetadataCandidate{Source: "Audible", Title: "Synthetic Field Notes 6", ASIN: "B0SYNXSRC1"})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newVerdictFixture(t)
			b, err := f.store.CreateBook(&database.Book{Title: "Synthetic Field Notes 6", FilePath: "/lib/sfn6/book.m4b"})
			require.NoError(t, err)

			held, release := make(chan struct{}), make(chan struct{})
			go func() {
				_, _ = f.store.ModifyBook(b.ID, func(*database.Book) error {
					close(held)
					<-release
					return database.ErrSkipBookWrite
				})
			}()
			<-held
			done := make(chan error, 1)
			go func() { done <- tc.write(f.store, b.ID) }()
			select {
			case err := <-done:
				close(release)
				t.Fatalf("%s finished (err=%v) while the book's write stripe was held", tc.name, err)
			case <-time.After(150 * time.Millisecond):
			}
			close(release)
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(10 * time.Second):
				t.Fatalf("%s never finished after the stripe was released", tc.name)
			}
		})
	}
}
