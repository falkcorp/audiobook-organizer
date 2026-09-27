// file: internal/metafetch/owner_replace_apply_test.go
// version: 1.0.0
// guid: 2da7a64b-6955-4983-b67b-61999a88725c
// last-edited: 2026-09-27
//
// Owner ruling 2026-09-27: the review page's bulk buttons get a toggle, "Fill
// empty fields" (default) or "Replace existing". Replace reaches the apply as
// FillOnly false + OwnerReplace; the hashless bulk marker adds
// UnseenCandidate, which keeps the automatic-apply guards.

package metafetch

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// describedFixture is stampFixture with a filled description the candidate's
// ("A description.") differs from.
func describedFixture() (*database.MockStore, **database.Book) {
	store, book, updated := stampFixture(false)
	book.Description = new("The owner's description")
	return store, updated
}

// Fill keeps a filled description; replace (hash-checked bulk pin) and
// replace on the hashless marker both overwrite it.
func TestApplyCandidate_OwnerReplaceOverwritesFilledDescription(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts ApplyOptions
		want string
	}{
		{"fill", ApplyOptions{FillOnly: true}, "The owner's description"},
		{"replace, bulk pin", ApplyOptions{OwnerReplace: true}, "A description."},
		{"replace, owner marker", ApplyOptions{OwnerReplace: true, UnseenCandidate: true}, "A description."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, updated := describedFixture()
			applyIgnoringHistory(t, store, nil, tc.opts)
			got := *updated
			require.NotNil(t, got)
			require.NotNil(t, got.Description)
			require.Equal(t, tc.want, *got.Description)
		})
	}
}

// Replace on the hashless marker is still an apply nobody was shown: a book
// marked "no match" is refused, both before the apply and at commit.
func TestApplyCandidate_UnseenReplaceStillRefusesNoMatch(t *testing.T) {
	store, updated := noMatchFixture()
	_, err := NewService(store).ApplyMetadataCandidateWithOptions("stamp-1", stampCandidate, nil,
		ApplyOptions{OwnerReplace: true, UnseenCandidate: true})
	require.True(t, errors.Is(err, ErrMarkedNoMatch), "unseen replace on a no-match book: %v", err)
	require.Nil(t, *updated, "nothing was committed")

	store2, book, updated2 := stampFixture(false)
	reads := 0
	store2.GetBookByIDFunc = func(string) (*database.Book, error) {
		reads++
		clone := *book
		if reads > 1 { // marked "no match" while the apply ran
			nm := "no_match"
			clone.MetadataReviewStatus = &nm
		}
		return &clone, nil
	}
	_, err = NewService(store2).ApplyMetadataCandidateWithOptions("stamp-1", stampCandidate, nil,
		ApplyOptions{OwnerReplace: true, UnseenCandidate: true})
	require.True(t, errors.Is(err, ErrMarkedNoMatch), "in-lock re-check: %v", err)
	require.Nil(t, *updated2, "nothing was committed over the fresh no_match")
}

// Replace on the hashless marker records the match only when the book ends up
// holding the candidate's title, like any automatic apply; a hash-checked
// bulk replace is hand-picked and records it whatever title is kept.
func TestApplyCandidate_UnseenReplaceKeptTitleRecordsNoMatch(t *testing.T) {
	store, _, updated := stampFixture(true) // locked title: the book keeps "Old Title"
	applyIgnoringHistory(t, store, nil, ApplyOptions{OwnerReplace: true, UnseenCandidate: true})
	got := *updated
	require.NotNil(t, got)
	require.Nil(t, got.MetadataReviewStatus, "nobody was shown this candidate: no match recorded")
	require.Nil(t, got.MetadataSourceHash)

	store, _, updated = stampFixture(true)
	applyIgnoringHistory(t, store, nil, ApplyOptions{OwnerReplace: true})
	requireStamped(t, *updated)
}

// An owner replace overwrote filled fields; its history is the only way to
// revert it, so a failed history write comes back as an error, like an
// owner-reviewed apply.
func TestApplyCandidate_HistoryFailureIsReturnedForOwnerReplace(t *testing.T) {
	pebble, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = pebble.Close() })
	store := &historyFailStore{PebbleStore: pebble, failFetched: true}
	book, err := store.CreateBook(&database.Book{Title: "track01", FilePath: "/library/a.m4b", Format: "m4b"})
	require.NoError(t, err)

	resp, err := NewService(store).ApplyMetadataCandidateWithOptions(book.ID,
		MetadataCandidate{Title: "New Title", Source: "Audible"}, nil, ApplyOptions{OwnerReplace: true})
	require.NotNil(t, resp)
	require.ErrorIs(t, err, ErrApplyHistoryIncomplete)
	require.Contains(t, err.Error(), "owner-replace apply")
}

// The history label names an owner replace, alone or next to a gate override;
// the owner-reviewed-only label is unchanged.
func TestApplyOptionsHistorySource_OwnerReplace(t *testing.T) {
	require.Equal(t, "Audible (owner replace: review bulk apply replaced existing values)",
		ApplyOptions{OwnerReplace: true}.historySource("Audible"))
	require.Equal(t, "Audible (owner-reviewed; certainty gate overridden: score_below_floor; owner replace: review bulk apply replaced existing values)",
		ApplyOptions{OwnerReviewed: true, GateOverride: "score_below_floor", OwnerReplace: true}.historySource("Audible"))
	require.Equal(t, "unknown source (owner replace: review bulk apply replaced existing values)",
		ApplyOptions{OwnerReplace: true}.historySource(""))
	require.Equal(t, "Audible (owner-reviewed; certainty gate overridden: owner_reviewed)",
		ApplyOptions{OwnerReviewed: true}.historySource("Audible"))
}

func TestNormalizeBulkApplyMode(t *testing.T) {
	for in, want := range map[string]string{"": "", "fill": "", "replace": "replace"} {
		got, ok := NormalizeBulkApplyMode(in)
		require.True(t, ok, in)
		require.Equal(t, want, got, in)
	}
	for _, bad := range []string{"Replace", "overwrite", " fill"} {
		_, ok := NormalizeBulkApplyMode(bad)
		require.False(t, ok, bad)
	}
}
