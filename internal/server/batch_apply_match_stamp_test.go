// file: internal/server/batch_apply_match_stamp_test.go
// version: 1.0.0
// guid: 118151c9-2974-4fad-96ae-08925b5c5f9f
// last-edited: 2026-09-14
//
// A batch row's options decide whether a kept (locked) title blocks the match
// record: a row the owner approved in the review lane records it, an
// automatic row does not.

package server

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

func TestBatchApplyOptions_LockedTitleMatchRecord(t *testing.T) {
	for _, tc := range []struct {
		name        string
		plan        cachedApplyPlan
		wantStamped bool
	}{
		{"review-lane approval", cachedApplyPlan{Pinnable: true, ReviewApproved: true}, true},
		{"gate lifted by owner review", cachedApplyPlan{Pinnable: true, OwnerReviewed: true}, true},
		{"automatic batch row", cachedApplyPlan{Pinnable: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			book := &database.Book{ID: "b-lock", Title: "Old Title"}
			var updated *database.Book
			store := &database.MockStore{
				GetBookByIDFunc: func(string) (*database.Book, error) { c := *book; return &c, nil },
				UpdateBookFunc: func(_ string, b *database.Book) (*database.Book, error) {
					c := *b
					updated = &c
					return &c, nil
				},
				GetMetadataFieldStatesFunc: func(string) ([]database.MetadataFieldState, error) {
					v := `"Curated Title"`
					return []database.MetadataFieldState{{Field: database.FieldKeyTitle, OverrideValue: &v}}, nil
				},
			}
			cand := metafetch.MetadataCandidate{Title: "Candidate Title", Description: "d", ASIN: "B0STAMP002", Source: "audible"}
			_, err := metafetch.NewService(store).ApplyMetadataCandidateWithOptions(book.ID, cand, nil, tc.plan.applyOptions())
			if err != nil && !errors.Is(err, metafetch.ErrApplyHistoryIncomplete) {
				t.Fatalf("apply: %v", err)
			}
			if updated == nil {
				t.Fatal("no row committed")
			}
			if stamped := updated.MetadataReviewStatus != nil && updated.MetadataSource != nil && updated.MetadataSourceHash != nil; stamped != tc.wantStamped {
				t.Fatalf("match recorded = %v, want %v (status %v source %v hash %v)", stamped, tc.wantStamped,
					updated.MetadataReviewStatus, updated.MetadataSource, updated.MetadataSourceHash)
			}
		})
	}
}
