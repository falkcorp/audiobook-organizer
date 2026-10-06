// file: internal/server/handlers/metadata/handler_asin_conflict_test.go
// version: 1.0.0
// guid: 4a8e2f63-1c9d-4b07-95e3-d6b0c7a1f248
// last-edited: 2026-10-05

package metadatahandler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	metadatahandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/metadata"
)

// A book's cached candidates survive an ASIN change, so the single-book dialog
// can show a kept candidate that names another ASIN, or one with no ASIN that
// was fetched for an ASIN the book no longer carries. The search flags both
// per candidate; the apply refuses the conflict unless the person overrides it
// for the ASIN they were shown.

func asinBook(asin string) *database.Book {
	return &database.Book{ID: "b1", Title: "A Title", ASIN: &asin}
}

func rawCandidates(t *testing.T, cs ...metafetch.MetadataCandidate) []json.RawMessage {
	t.Helper()
	out := make([]json.RawMessage, len(cs))
	for i, c := range cs {
		b, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = b
	}
	return out
}

type checkedResult struct {
	Title      string `json:"title"`
	ASIN       string `json:"asin"`
	ApplyCheck *struct {
		ASINConflict  bool   `json:"asin_conflict"`
		IdentityStale bool   `json:"identity_stale"`
		BookASIN      string `json:"book_asin"`
		Detail        string `json:"detail"`
	} `json:"apply_check"`
}

func TestSearchAudiobookMetadata_FlagsKeptCandidatesAfterASINReplace(t *testing.T) {
	h, d := newHandler(t)
	entry := &metafetch.MetadataCandidateCache{BookID: "b1", FetchedAt: time.Now(), FetchedForASIN: "B00OLDASIN",
		Candidates: rawCandidates(t,
			metafetch.MetadataCandidate{Title: "Other Record", ASIN: "B00OTHERAS"},
			metafetch.MetadataCandidate{Title: "No ASIN"},
			metafetch.MetadataCandidate{Title: "The Applied One", ASIN: "b00newasin"},
		)}
	d.mfs.EXPECT().GetCachedCandidates("b1").Return(entry, true, nil)
	d.store.EXPECT().GetBookByID("b1").Return(asinBook("B00NEWASIN"), nil)

	w := doReq(h.SearchAudiobookMetadata, http.MethodPost, "/audiobooks/b1/search-metadata", nil, idParam("b1"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Data struct {
			Results []checkedResult `json:"results"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	res := body.Data.Results
	if len(res) != 3 {
		t.Fatalf("results = %d, want 3: %s", len(res), w.Body.String())
	}
	if c := res[0].ApplyCheck; c == nil || !c.ASINConflict || c.BookASIN != "B00NEWASIN" || !strings.Contains(c.Detail, "B00OTHERAS") {
		t.Errorf("conflicting candidate: apply_check %+v, want asin_conflict naming both ASINs", c)
	}
	if res[0].Title != "Other Record" || res[0].ASIN != "B00OTHERAS" {
		t.Errorf("the candidate's own fields must stay flat: %+v", res[0])
	}
	if c := res[1].ApplyCheck; c == nil || c.ASINConflict || !c.IdentityStale {
		t.Errorf("ASIN-less kept candidate: apply_check %+v, want identity_stale only", c)
	}
	if res[2].ApplyCheck != nil {
		t.Errorf("the candidate carrying the book's ASIN must carry no check, got %+v", res[2].ApplyCheck)
	}
}

func applyBody(cand metafetch.MetadataCandidate, override string) map[string]any {
	b := map[string]any{"candidate": cand, "fields": []string{"title"}}
	if override != "" {
		b["override_asin_conflict"] = override
	}
	return b
}

// Without an override the conflicting apply is refused before anything is
// written (strict mocks: no preflight, no apply, no enqueue, no submit), with
// the fields the dialog offers the override from. An override given for
// another ASIN than the book carries now is refused the same way.
func TestApplyAudiobookMetadata_RefusesASINConflictWithoutOverride(t *testing.T) {
	cand := metafetch.MetadataCandidate{Title: "Other Record", ASIN: "B00OTHERAS"}
	for name, override := range map[string]string{"no override": "", "override for another ASIN": "B00STALEASN"} {
		t.Run(name, func(t *testing.T) {
			h, d := newHandler(t)
			d.store.EXPECT().GetBookByID("b1").Return(asinBook("B00NEWASIN"), nil)
			w := doReq(h.ApplyAudiobookMetadata, http.MethodPost, "/audiobooks/b1/apply-metadata", applyBody(cand, override), idParam("b1"))
			if w.Code != http.StatusConflict {
				t.Fatalf("want 409, got %d: %s", w.Code, w.Body.String())
			}
			var body struct {
				Reason        string `json:"reason"`
				BookASIN      string `json:"book_asin"`
				CandidateASIN string `json:"candidate_asin"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Reason != applygate.ReasonASINConflict || body.BookASIN != "B00NEWASIN" || body.CandidateASIN != "B00OTHERAS" {
				t.Fatalf("409 body %+v", body)
			}
			if len(d.rec.publishedEvents) != 0 {
				t.Fatal("a refused apply must publish nothing")
			}
		})
	}
}

// With an override for the ASIN the book carries, the apply runs and records
// the override as owner-reviewed over asin_conflict.
func TestApplyAudiobookMetadata_OverrideAppliesAndRecordsIt(t *testing.T) {
	h, d := newHandler(t)
	cand := metafetch.MetadataCandidate{Title: "Other Record", ASIN: "B00OTHERAS"}
	d.store.EXPECT().GetBookByID("b1").Return(asinBook("B00NEWASIN"), nil)
	d.mfs.EXPECT().RenamePreflight("b1", mock.Anything, []string{"title"}).Return(nil)
	d.mfs.EXPECT().ApplyMetadataCandidateWithOptions("b1", mock.Anything, []string{"title"},
		metafetch.ApplyOptions{OwnerReviewed: true, GateOverride: applygate.ReasonASINConflict}).
		Return(&metafetch.FetchMetadataResponse{Message: "applied", Source: "audible", Book: &database.Book{ID: "b1"}}, nil)
	d.wb.EXPECT().Enqueue("b1").Return()
	d.pool.EXPECT().Submit("b1", mock.Anything).Return(true)
	w := doReq(h.ApplyAudiobookMetadata, http.MethodPost, "/audiobooks/b1/apply-metadata", applyBody(cand, " b00newasin "), idParam("b1"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}

// A candidate that agrees with the book (or carries no ASIN) is the ordinary
// hand-picked apply, override or not.
func TestApplyAudiobookMetadata_NoConflictIsTheOrdinaryApply(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID("b1").Return(asinBook("B00NEWASIN"), nil)
	d.mfs.EXPECT().RenamePreflight("b1", mock.Anything, mock.Anything).Return(nil)
	d.mfs.EXPECT().ApplyMetadataCandidate("b1", mock.Anything, mock.Anything).
		Return(&metafetch.FetchMetadataResponse{Message: "applied", Book: &database.Book{ID: "b1"}}, nil)
	d.wb.EXPECT().Enqueue("b1").Return()
	d.pool.EXPECT().Submit("b1", mock.Anything).Return(true)
	w := doReq(h.ApplyAudiobookMetadata, http.MethodPost, "/audiobooks/b1/apply-metadata",
		applyBody(metafetch.MetadataCandidate{Title: "No ASIN"}, "B00NEWASIN"), idParam("b1"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}

// The queued apply (behind a scan) re-checks the book as it is when it runs:
// a conflict without a matching override refuses there too, and a matching
// override applies with the queued batch id.
func TestRunQueuedApply_ChecksASINConflictWhenItRuns(t *testing.T) {
	q := metadatahandler.QueuedApply{
		Kind: metadatahandler.QueuedApplyCandidate, BookID: "b1",
		Candidate: &metafetch.MetadataCandidate{Title: "Other Record", ASIN: "B00OTHERAS"},
		EditMark:  42, ApplyBatchID: "apply-queued-own",
	}
	t.Run("refused", func(t *testing.T) {
		h, d := newHandler(t)
		d.mfs.EXPECT().ApplyEditsSince("b1", int64(42), "apply-queued-own").Return(metafetch.QueuedApplyEdits{}, nil)
		d.store.EXPECT().GetBookByID("b1").Return(asinBook("B00NEWASIN"), nil)
		err := h.RunQueuedApply(context.Background(), q, nil)
		if err == nil || !strings.Contains(err.Error(), "conflicts") {
			t.Fatalf("want an ASIN-conflict refusal, got %v", err)
		}
	})
	t.Run("overridden", func(t *testing.T) {
		h, d := newHandler(t)
		qo := q
		qo.OverrideASINConflict = "B00NEWASIN"
		d.mfs.EXPECT().ApplyEditsSince("b1", int64(42), "apply-queued-own").Return(metafetch.QueuedApplyEdits{}, nil)
		d.store.EXPECT().GetBookByID("b1").Return(asinBook("B00NEWASIN"), nil)
		d.mfs.EXPECT().RenamePreflight("b1", mock.Anything, mock.Anything).Return(nil)
		d.mfs.EXPECT().ApplyMetadataCandidateWithOptions("b1", mock.Anything, mock.Anything,
			metafetch.ApplyOptions{BatchID: "apply-queued-own", OwnerReviewed: true, GateOverride: applygate.ReasonASINConflict}).
			Return(&metafetch.FetchMetadataResponse{}, nil)
		d.wb.EXPECT().Enqueue("b1").Maybe()
		d.pool.EXPECT().Submit("b1", mock.Anything).Return(true)
		if err := h.RunQueuedApply(context.Background(), qo, nil); err != nil {
			t.Fatalf("RunQueuedApply: %v", err)
		}
	})
}
