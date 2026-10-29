// file: internal/server/transcription_runtime_gate_test.go
// version: 1.0.0
// guid: 5d0c7e92-3b41-4a86-9f1e-c27a6b8d4e03
// last-edited: 2026-10-29

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	maintenanceplugin "github.com/falkcorp/audiobook-organizer/internal/plugins/maintenance"
)

// The auto-match-transcribed op applies without the bulk gate, so its
// transcription agreement must also fit the book's runtime: both the search
// half (so the dry run counts the book) and the apply half (so a real run
// never writes it) check the files as they are now. The cached candidate
// carries no stamp here -- the gate is live, not a read of a stamp.
func TestTranscriptionAutoMatch_RuntimeGate(t *testing.T) {
	cases := []struct {
		name    string
		candSec int
		files   func(string) ([]database.BookFile, error)
		wantOK  bool
	}{
		{name: "candidate fits the book", candSec: fixtureTenHourSec, wantOK: true},
		{name: "book is a fragment of a much longer candidate", candSec: 100000},
		{name: "candidate runtime unknown", candSec: -1},
		{name: "book has no files", candSec: fixtureTenHourSec,
			files: func(string) ([]database.BookFile, error) { return nil, nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bookID := "book-rt"
			book := &database.Book{ID: bookID, Title: ""}
			cand := metafetch.MetadataCandidate{Title: "The Court of Owls", Author: "Bruce Wayne", Score: 0.95, Source: "test"}
			if tc.candSec >= 0 {
				cand.DurationSec = tc.candSec
			}
			entry := marshalCandidateCache(t, bookID, cand)
			store, updateCalls := newTOCTOUCacheStore(t, book, entry, entry)
			withCreatableAuthors(store)
			if tc.files != nil {
				store.GetBookFilesFunc = tc.files
			}
			s := &Server{store: store, metadataFetchService: metafetch.NewService(store)}

			top, found, err := s.SearchTranscriptionCandidate(context.Background(), bookID, "", "")
			if err != nil || !found {
				t.Fatalf("SearchTranscriptionCandidate() = (found=%v, err=%v), want found: the dry run must still count the book", found, err)
			}
			if top.RuntimeUnverified == tc.wantOK {
				t.Fatalf("RuntimeUnverified = %v, want %v", top.RuntimeUnverified, !tc.wantOK)
			}

			applyErr := s.ApplyTranscriptionCandidate(context.Background(), bookID, cand.Title, cand.Author)
			if tc.wantOK {
				if applyErr != nil || len(*updateCalls) == 0 {
					t.Fatalf("ApplyTranscriptionCandidate() = %v with %d writes, want a write", applyErr, len(*updateCalls))
				}
				return
			}
			if !errors.Is(applyErr, maintenanceplugin.ErrTranscriptionRuntimeUnverified) {
				t.Fatalf("ApplyTranscriptionCandidate() = %v, want ErrTranscriptionRuntimeUnverified", applyErr)
			}
			if len(*updateCalls) != 0 {
				t.Fatalf("UpdateBook calls = %d, want 0", len(*updateCalls))
			}
		})
	}
}

// measuredTenHourBooks is fakeBooks whose book has one measured 10 h file.
type measuredTenHourBooks struct{ fakeBooks }

func (measuredTenHourBooks) GetBookFiles(id string) ([]database.BookFile, error) {
	return []database.BookFile{fixtureTenHourFile(id)}, nil
}

// The bulk apply lowers its score floor for a candidate the audio intro
// confirms. The runtime check behind that is LIVE: a cached candidate with no
// transcription stamp (fetched before the gate existed, or before the book's
// files changed) on a book that is a fragment of a much longer one is refused
// as transcription_runtime_unverified, and the same candidate with a fitting
// runtime applies.
func TestBulkApplyPlanners_TranscriptionConfirmationNeedsLiveRuntime(t *testing.T) {
	const heard = "The Court of Owls"
	for _, tc := range []struct {
		name      string
		candSec   int
		wantAllow bool
	}{
		{"runtime fits", fixtureTenHourSec, true},
		{"fragment of a much longer candidate", 100000, false},
		{"candidate runtime unknown", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dur := fixtureTenHourSec
			book := &database.Book{ID: "b1", Title: "", TranscribedTitle: new(heard),
				FilePath: "/lib/Bruce Wayne/Unknown Title/book.m4b", Duration: &dur}
			cand := metafetch.MetadataCandidate{Title: heard, Author: "Bruce Wayne", Score: 0.87, DurationSec: tc.candSec, Source: "Audible"}
			blob, err := json.Marshal(cand)
			if err != nil {
				t.Fatal(err)
			}
			stale := fmt.Errorf("%w: book b1 (stored a, current b)", metafetch.ErrStaleMetadataCache)
			svc := &fakeApplySvc{candidates: []json.RawMessage{blob}, identityErr: stale, queryMatches: map[string]bool{heard: true}}
			plan := planCachedApply(svc, measuredTenHourBooks{fakeBooks{"b1": book}}, "b1", nil, nil)
			if plan.Gate == nil {
				t.Fatalf("gate did not run: %+v", plan)
			}
			if plan.Gate.Allowed != tc.wantAllow {
				t.Fatalf("allowed = %v (reason %q: %s), want %v", plan.Gate.Allowed, plan.Gate.Reason, plan.Gate.Detail, tc.wantAllow)
			}
			if !tc.wantAllow && plan.Gate.Reason != applygate.ReasonTranscriptionRuntimeUnverified {
				t.Errorf("reason = %q (%s), want %q", plan.Gate.Reason, plan.Gate.Detail, applygate.ReasonTranscriptionRuntimeUnverified)
			}
		})
	}
}
