// file: internal/metafetch/rejected_candidates_crosssource_test.go
// version: 1.0.0
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
	m := memRejections{RejectedCandidateStoreKey("b1", "Open Library", "T"): []byte(`{"ids":["isbn:0306406152"]}`)}
	require.True(t, loadSet(t, m, "b1").HasCandidate(&MetadataCandidate{Source: "Synthetic Books", Title: "U", ISBN13: "9780306406157"}))
}

// Undo of a record removes its identifiers from every key of the book.
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
		require.JSONEq(t, `{"ids":["asin:b0synxsrc2"]}`, string(m[audibleKey]))
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
