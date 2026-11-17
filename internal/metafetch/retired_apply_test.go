// file: internal/metafetch/retired_apply_test.go
// version: 1.0.2
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

// Options that overwrite but are not automatic(): the review page's bulk
// "Replace existing" (OwnerReplace, FillOnly false, UnseenCandidate false)
// and a bare ApplyOptions{}. Neither says a retired book may be written, so
// both refuse one, at the start and again under the lock. (Round 2 of #4694:
// the backstop keyed on automatic() and let these through.)
var overwritingApplyOptions = map[string]ApplyOptions{
	"bulk_replace":          {OwnerReplace: true, RequireHistory: true},
	"bulk_replace_reviewed": {OwnerReplace: true, OwnerReviewed: true, GateOverride: "owner_reviewed", RequireHistory: true},
	"bare_options":          {},
}

func TestRetiredApply_OverwritingApplyWithoutAllowRetiredRefusesRetiredBook(t *testing.T) {
	for _, rc := range retiredFieldCases {
		for label, opts := range overwritingApplyOptions {
			t.Run(rc.name+"/"+label, func(t *testing.T) {
				store, book, updated := stampFixture(false)
				rc.retire(book)
				// As for the automatic options: the refusal comes at the start,
				// before the apply body reads field states and resolves or
				// creates author and series rows. A check that only refused at
				// the commit would leave those rows behind for a book that was
				// never going to be written.
				bodyReads := 0
				inner := store.GetMetadataFieldStatesFunc
				store.GetMetadataFieldStatesFunc = func(id string) ([]database.MetadataFieldState, error) {
					bodyReads++
					return inner(id)
				}
				resp, err := NewService(store).ApplyMetadataCandidateWithOptions("stamp-1", stampCandidate, nil, opts)
				require.ErrorIs(t, err, ErrBookRetired)
				require.Nil(t, resp)
				require.Nil(t, *updated, "nothing was committed")
				require.Zero(t, bodyReads, "the apply body ran before the refusal")
			})
		}
	}
}

// The under-lock half: the book is live when the apply first reads it and
// retired by the time the write takes the lock.
func TestRetiredApply_OverwritingApplyRetiredDuringApplyIsNotWritten(t *testing.T) {
	for _, rc := range retiredFieldCases {
		for label, opts := range overwritingApplyOptions {
			t.Run(rc.name+"/"+label, func(t *testing.T) {
				store, book, updated := stampFixture(false)
				reads := 0
				store.GetBookByIDFunc = func(string) (*database.Book, error) {
					reads++
					clone := *book
					if reads > 1 {
						rc.retire(&clone)
					}
					return &clone, nil
				}
				_, err := NewService(store).ApplyMetadataCandidateWithOptions("stamp-1", stampCandidate, nil, opts)
				require.ErrorIs(t, err, ErrBookRetired, "the in-lock refusal is typed")
				require.Nil(t, *updated, "nothing was committed onto the retired row")
			})
		}
	}
}

// AllowRetired is the one thing that lets a retired book be written: a single
// review row (overwriting, owner-reviewed) and the single-book dialog set it.
// Both the start check and the under-lock check read it, so a book that is
// retired only by the time the lock is taken is still written for them.
func TestRetiredApply_AllowRetiredWritesRetiredBook(t *testing.T) {
	rowApprove := ApplyOptions{AllowRetired: true, OwnerReviewed: true, GateOverride: "owner_reviewed", RequireHistory: true}
	for _, rc := range retiredFieldCases {
		for label, opts := range map[string]ApplyOptions{"dialog": {AllowRetired: true}, "row_pin": rowApprove} {
			t.Run(rc.name+"/"+label+"/retired_at_start", func(t *testing.T) {
				store, book, updated := stampFixture(false)
				rc.retire(book)
				applyIgnoringHistory(t, store, nil, opts)
				require.NotNil(t, *updated, "a person-picked apply of a retired book must write")
			})
			t.Run(rc.name+"/"+label+"/retired_during_apply", func(t *testing.T) {
				store, book, updated := stampFixture(false)
				reads := 0
				store.GetBookByIDFunc = func(string) (*database.Book, error) {
					reads++
					clone := *book
					if reads > 1 {
						rc.retire(&clone)
					}
					return &clone, nil
				}
				applyIgnoringHistory(t, store, nil, opts)
				require.NotNil(t, *updated, "the person's apply is not refused under the lock")
			})
		}
	}
}

// The no-match check is a different rule from the retired one and keeps
// keying on automatic(): AllowRetired must not lift a "no match" refusal of an
// automatic apply, and a plain overwrite still overrides the owner's mark.
func TestRetiredApply_AllowRetiredDoesNotLiftNoMatch(t *testing.T) {
	noMatch := "no_match"
	store, book, updated := stampFixture(false)
	book.MetadataReviewStatus = &noMatch
	_, err := NewService(store).ApplyMetadataCandidateWithOptions("stamp-1", stampCandidate, nil, ApplyOptions{FillOnly: true, AllowRetired: true})
	require.ErrorIs(t, err, ErrMarkedNoMatch)
	require.Nil(t, *updated)
}
