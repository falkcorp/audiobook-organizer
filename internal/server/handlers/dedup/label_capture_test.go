// file: internal/server/handlers/dedup/label_capture_test.go
// version: 1.0.0
// guid: 9b3d1f57-4c20-4e8a-bf16-2a7e9c5d013a
// last-edited: 2026-06-18

package deduphandler_test

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
)

// allowLabelCaptureReads lets the best-effort label-capture path call the
// DedupStore book readers any number of times (including zero) without failing
// the strict testify mock. Returns books with one sizeable file so BuildExample
// produces a populated example.
func allowLabelCaptureReads(d testDeps) {
	d.store.EXPECT().GetBookByID(mock.Anything).
		Return(&database.Book{ID: "x", Title: "T"}, nil).Maybe()
	d.store.EXPECT().GetBookFiles(mock.Anything).
		Return([]database.BookFile{{FilePath: "/lib/a.m4b", FileSize: 5 << 20, Duration: 3600}}, nil).Maybe()
	// The label-write path now (re)snapshots the pair's ScoreBreakdown via the
	// engine's shared scorer. It is best-effort; allow it any number of times
	// (incl. zero) and return an empty (zero-signal) result so nothing is
	// persisted onto the example unless a test wires a richer return.
	d.engine.EXPECT().ScorePairsForBook(mock.Anything, mock.Anything, mock.Anything).
		Return(nil, nil).Maybe()
}

func TestDismissDedupCandidate_RecordsHumanNotDupLabel(t *testing.T) {
	h, d := newHandler(t)
	allowLabelCaptureReads(d)
	id, _, _ := insertCandidate(t, d.es, "book-aaa", "book-bbb")

	w := doReq(t, h.DismissDedupCandidate, http.MethodPost,
		"/api/v1/dedup/candidates/"+strconv.FormatInt(id, 10)+"/dismiss", nil,
		gin.Params{{Key: "id", Value: strconv.FormatInt(id, 10)}})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}

	ex, err := d.es.GetLabeledExample(id)
	if err != nil {
		t.Fatalf("GetLabeledExample: %v", err)
	}
	if ex == nil {
		t.Fatal("expected a labeled example after dismiss, got nil")
	}
	if ex.Label != "not_dup" || ex.LabelSource != "human" || ex.LabelReason != "user_dismiss" {
		t.Fatalf("label=%q source=%q reason=%q; want not_dup/human/user_dismiss", ex.Label, ex.LabelSource, ex.LabelReason)
	}
	if ex.DecidedAt == "" {
		t.Error("expected DecidedAt to be stamped")
	}
}

func TestMergeDedupCandidate_RecordsHumanTrueDupLabel(t *testing.T) {
	h, d := newHandler(t)
	allowLabelCaptureReads(d)
	id, aID, bID := insertCandidate(t, d.es, "book-aaa", "book-bbb")
	d.merge.EXPECT().MergeBooks([]string{aID, bID}, "").Return(&merge.Result{PrimaryID: aID}, nil).Once()
	d.engine.EXPECT().CleanupCandidatesAfterMerge(mock.Anything).Return(0).Once()

	w := doReq(t, h.MergeDedupCandidate, http.MethodPost,
		"/api/v1/dedup/candidates/"+strconv.FormatInt(id, 10)+"/merge", nil,
		gin.Params{{Key: "id", Value: strconv.FormatInt(id, 10)}})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}

	ex, err := d.es.GetLabeledExample(id)
	if err != nil {
		t.Fatalf("GetLabeledExample: %v", err)
	}
	if ex == nil {
		t.Fatal("expected a labeled example after merge, got nil")
	}
	if ex.Label != "true_dup" || ex.LabelSource != "human" || ex.LabelReason != "user_merge" {
		t.Fatalf("label=%q source=%q reason=%q; want true_dup/human/user_merge", ex.Label, ex.LabelSource, ex.LabelReason)
	}
}

// TestDismissDedupCandidate_LabelCaptureBestEffort proves capture failure (the
// store cannot resolve the books) never breaks the dismiss action: the request
// still succeeds and simply no label is written.
func TestDismissDedupCandidate_LabelCaptureBestEffort(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID(mock.Anything).Return(nil, assertErr{}).Maybe()
	d.store.EXPECT().GetBookFiles(mock.Anything).Return(nil, assertErr{}).Maybe()
	id, _, _ := insertCandidate(t, d.es, "book-aaa", "book-bbb")

	w := doReq(t, h.DismissDedupCandidate, http.MethodPost,
		"/api/v1/dedup/candidates/"+strconv.FormatInt(id, 10)+"/dismiss", nil,
		gin.Params{{Key: "id", Value: strconv.FormatInt(id, 10)}})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 even when capture fails; body=%s", w.Code, w.Body.String())
	}
	// A build error means no example is written — and crucially, no panic/500.
	ex, err := d.es.GetLabeledExample(id)
	if err != nil {
		t.Fatalf("GetLabeledExample: %v", err)
	}
	if ex != nil {
		t.Fatalf("expected no label when capture failed, got %+v", ex)
	}
}

type assertErr struct{}

func (assertErr) Error() string { return "store unavailable" }
