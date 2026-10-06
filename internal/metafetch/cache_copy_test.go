// file: internal/metafetch/cache_copy_test.go
// version: 1.0.0
// guid: 3c9d4b7e-1a62-4f08-b5e3-8d2f6a0c9e41
// last-edited: 2026-10-06

package metafetch

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func copyFixture(t *testing.T) (*database.PebbleStore, *Service, string, string) {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	a, err := st.CreateAuthor("Frank Herbert")
	require.NoError(t, err)
	mk := func() string {
		b, err := st.CreateBook(&database.Book{Title: "Dune", Format: "m4b", FilePath: "/lib/Dune", AuthorID: &a.ID})
		require.NoError(t, err)
		return b.ID
	}
	from, to := mk(), mk()
	raw, err := json.Marshal(MetadataCandidate{Title: "Dune", Author: "Frank Herbert", ASIN: "B002V1OF70", Source: "audible"})
	require.NoError(t, err)
	require.NoError(t, st.PutMetadataCache(&MetadataCandidateCache{BookID: from, FetchedAt: time.Now().UTC(),
		Candidates: []json.RawMessage{raw}, SourceHash: BatchSourceHash(from, "Dune", "Frank Herbert"), FetchedForASIN: "B002V1OF70"}))
	return st, NewService(st), from, to
}

// The copy is re-keyed for the target: it passes the target's identity
// check (a verbatim copy never could, the hash binds the book id), and it
// keeps the candidates, their date and the ASIN they were fetched for.
func TestCopyCandidateCache_RekeysForTarget(t *testing.T) {
	st, svc, from, to := copyFixture(t)
	src, err := st.GetMetadataCache(from)
	require.NoError(t, err)
	target, err := st.GetBookByID(to)
	require.NoError(t, err)
	require.Error(t, svc.ValidateCachedIdentityForBook(&MetadataCandidateCache{BookID: to, SourceHash: src.SourceHash},
		target, []string{"Frank Herbert"}), "a verbatim copy is stale for the target")

	cp, err := svc.CopyCandidateCache(from, to, nil)
	require.NoError(t, err)
	stored, err := st.GetMetadataCache(to)
	require.NoError(t, err)
	require.Equal(t, cp.SourceHash, stored.SourceHash)
	require.NoError(t, svc.ValidateCachedIdentityForBook(stored, target, []string{"Frank Herbert"}))
	require.Equal(t, src.Candidates, stored.Candidates)
	require.True(t, src.FetchedAt.Equal(stored.FetchedAt))
	require.Equal(t, "B002V1OF70", stored.FetchedForASIN)
}

// check refuses the write; a source with no candidates is refused too.
func TestCopyCandidateCache_RefusesOnCheckAndEmptySource(t *testing.T) {
	st, svc, from, to := copyFixture(t)
	refuse := errors.New("refused")
	_, err := svc.CopyCandidateCache(from, to, func(*database.Book, *MetadataCandidateCache) error { return refuse })
	require.ErrorIs(t, err, refuse)
	entry, err := st.GetMetadataCache(to)
	require.NoError(t, err)
	require.Nil(t, entry, "nothing is written when check refuses")

	_, err = svc.CopyCandidateCache(to, from, nil)
	require.ErrorIs(t, err, ErrNoCandidatesToCopy)
}

// ApplyOptions.Guard runs under the write lock: a refusal writes nothing and
// is returned wrapped; CandidateSourceHash is the hash the apply stamps.
func TestApplyOptionsGuard_RefusesCommit(t *testing.T) {
	st, svc, _, to := copyFixture(t)
	cand := MetadataCandidate{Title: "Dune", Author: "Frank Herbert", Narrator: "Scott Brick", ASIN: "B002V1OF70", Source: "audible"}
	refuse := errors.New("guard refused")
	_, err := svc.ApplyMetadataCandidateWithOptions(to, cand, []string{"narrator"},
		ApplyOptions{FillOnly: true, Guard: func(*database.Book) error { return refuse }})
	require.ErrorIs(t, err, refuse)
	b, err := st.GetBookByID(to)
	require.NoError(t, err)
	require.Nil(t, b.Narrator)
	require.Nil(t, b.MetadataReviewStatus)

	calls := 0
	_, err = svc.ApplyMetadataCandidateWithOptions(to, cand, []string{"narrator"},
		ApplyOptions{FillOnly: true, Guard: func(*database.Book) error { calls++; return nil }})
	require.NoError(t, err)
	require.Positive(t, calls)
	b, err = st.GetBookByID(to)
	require.NoError(t, err)
	require.Equal(t, "Scott Brick", *b.Narrator)
	require.Equal(t, CandidateSourceHash(cand), *b.MetadataSourceHash)
}
