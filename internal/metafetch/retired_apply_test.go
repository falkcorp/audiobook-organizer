// file: internal/metafetch/retired_apply_test.go
// version: 1.0.0
// guid: 91d2a6c4-0e7b-4f35-8a19-2b6c5d7e3f80
// last-edited: 2026-11-17

package metafetch

import (
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// A retired book is in the trash (MarkedForDeletion) or absorbed into another
// book (MergedIntoBookID). Every automatic apply (nobody picked this candidate
// for this book) passes through ApplyMetadataCandidateWithOptions, so that is
// where upgrade, auto-match-transcribed, the version-twin fixer and the bulk
// applies are all stopped. One retirement field per fixture, so a check that
// reads only the other field fails.
type retiredFieldCase struct {
	name   string
	retire func(b *database.Book)
}

var retiredFieldCases = []retiredFieldCase{
	{"soft_deleted", func(b *database.Book) { yes := true; b.MarkedForDeletion = &yes }},
	{"merged_into", func(b *database.Book) { into := "survivor-book-id"; b.MergedIntoBookID = &into }},
}

// The option sets the automatic callers use today.
var automaticApplyOptions = map[string]ApplyOptions{
	"fill_only":                 {FillOnly: true},
	"unseen_overwrite":          {UnseenCandidate: true},
	"rank_upgrade":              {UnseenCandidate: true, RankUpgrade: "rank upgrade: open_library -> audible"},
	"transcription_fill":        {FillOnly: true, RefuseEmptyWrite: true},
	"fill_only_require_history": {FillOnly: true, RequireHistory: true},
}

func TestRetiredApply_AutomaticApplyWritesNothingOnRetiredBook(t *testing.T) {
	for _, rc := range retiredFieldCases {
		for label, opts := range automaticApplyOptions {
			t.Run(rc.name+"/"+label, func(t *testing.T) {
				store, book, updated := stampFixture(false)
				rc.retire(book)
				// The field-state read is the apply body's first step (the
				// lock strip, before it resolves author or series rows or
				// commits). The refusal must come before it, not only at the
				// commit.
				bodyReads := 0
				inner := store.GetMetadataFieldStatesFunc
				store.GetMetadataFieldStatesFunc = func(id string) ([]database.MetadataFieldState, error) {
					bodyReads++
					return inner(id)
				}
				resp, err := NewService(store).ApplyMetadataCandidateWithOptions("stamp-1", stampCandidate, nil, opts)
				require.ErrorIs(t, err, ErrBookRetired, "an automatic apply must refuse a retired book")
				require.Nil(t, resp)
				require.Nil(t, *updated, "nothing was committed")
				require.Zero(t, bodyReads, "the apply body ran before the refusal")
			})
		}
	}
}

// Control: the same automatic apply on a live book commits.
func TestRetiredApply_ControlAutomaticApplyOnLiveBookCommits(t *testing.T) {
	for label, opts := range automaticApplyOptions {
		t.Run(label, func(t *testing.T) {
			store, _, updated := stampFixture(false)
			applyIgnoringHistory(t, store, nil, opts)
			require.NotNil(t, *updated, "control did not commit")
		})
	}
}

// A person picking a candidate for a retired book (the single-book dialog)
// is not an automatic apply and is not refused here.
func TestRetiredApply_HandPickedApplyOnRetiredBookIsUnchanged(t *testing.T) {
	for _, rc := range retiredFieldCases {
		t.Run(rc.name, func(t *testing.T) {
			store, book, updated := stampFixture(false)
			rc.retire(book)
			_, err := NewService(store).ApplyMetadataCandidate("stamp-1", stampCandidate, nil)
			require.NoError(t, err)
			require.NotNil(t, *updated, "the owner's own apply changed")
		})
	}
}

// A book trashed or merged away after the automatic apply's first check read
// the row but before its write: the in-lock re-check refuses the write.
func TestRetiredApply_RetiredDuringApplyIsNotWritten(t *testing.T) {
	for _, rc := range retiredFieldCases {
		t.Run(rc.name, func(t *testing.T) {
			store, book, updated := stampFixture(false)
			reads := 0
			store.GetBookByIDFunc = func(string) (*database.Book, error) {
				reads++
				clone := *book
				if reads > 1 { // every read after the apply's first is the "fresh" row
					rc.retire(&clone)
				}
				return &clone, nil
			}
			_, err := NewService(store).ApplyMetadataCandidateWithOptions("stamp-1", stampCandidate, nil, ApplyOptions{FillOnly: true})
			require.ErrorIs(t, err, ErrBookRetired, "the in-lock refusal is typed")
			require.Nil(t, *updated, "nothing was committed onto the retired row")
		})
	}
}
