// file: internal/server/handlers/metadata/handler_background_apply_test.go
// version: 1.1.0
// guid: 6e2b9d47-3f81-4c0a-b5d6-1a7e8c4f9b23
// last-edited: 2026-10-07

package metadatahandler_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// The Search Metadata dialog closes as soon as the reviewer is done and sends
// its one staged pick with background:true. The request must answer 202 at
// once and hand the apply to the durable queued op, stamped like a scan
// handoff, without running any of the apply inline (strict mocks: no
// preflight, no apply, no file job).

func backgroundBody(cand metafetch.MetadataCandidate, override string) map[string]any {
	b := applyBody(cand, override)
	b["background"] = true
	return b
}

func TestApplyAudiobookMetadata_BackgroundQueuesStampedOpAndAnswers202(t *testing.T) {
	h, d := newHandler(t)
	q := &fakeEnqueuer{}
	h.SetQueuedApplyEnqueuer(q)
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1", Title: "Current"}, nil)
	d.mfs.EXPECT().ApplyEditMark("b1").Return(int64(7), nil)

	w := doReq(h.ApplyAudiobookMetadata, http.MethodPost, "/audiobooks/b1/apply-metadata",
		backgroundBody(metafetch.MetadataCandidate{Title: "Synthetic Title"}, ""), idParam("b1"))
	if w.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", w.Code, w.Body.String())
	}
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data["queued"] != true || env.Data["background"] != true || env.Data["operation_id"] != "op-queued-1" {
		t.Fatalf("202 body = %v, want queued+background+operation_id", env.Data)
	}
	if len(q.calls) != 1 {
		t.Fatalf("enqueued %d ops, want exactly 1", len(q.calls))
	}
	c := q.calls[0]
	if c.BookID != "b1" || c.Candidate == nil || c.Candidate.Title != "Synthetic Title" ||
		len(c.Fields) != 1 || c.Fields[0] != "title" {
		t.Fatalf("queued apply %+v does not carry the request's pick", c)
	}
	if c.EditMark != 7 || !strings.HasPrefix(c.ApplyBatchID, "apply-queued-") {
		t.Fatalf("queued without its edit mark / batch id: mark=%d batch=%q", c.EditMark, c.ApplyBatchID)
	}
}

// The ASIN conflict is still refused synchronously: nothing is queued to fail
// later out of sight.
func TestApplyAudiobookMetadata_BackgroundStillRefusesASINConflictAt409(t *testing.T) {
	h, d := newHandler(t)
	q := &fakeEnqueuer{}
	h.SetQueuedApplyEnqueuer(q)
	d.store.EXPECT().GetBookByID("b1").Return(asinBook("B00NEWASIN"), nil)

	w := doReq(h.ApplyAudiobookMetadata, http.MethodPost, "/audiobooks/b1/apply-metadata",
		backgroundBody(metafetch.MetadataCandidate{Title: "Other", ASIN: "B00OTHERAS"}, ""), idParam("b1"))
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", w.Code, w.Body.String())
	}
	if len(q.calls) != 0 {
		t.Fatalf("a refused conflict was queued: %+v", q.calls)
	}
}

// With no queued op wired the flag cannot be honoured; the apply runs in the
// request as before rather than being dropped.
func TestApplyAudiobookMetadata_BackgroundWithoutQueuerAppliesInline(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1"}, nil)
	d.mfs.EXPECT().RenamePreflight("b1", mock.Anything, mock.Anything).Return(nil)
	d.mfs.EXPECT().ApplyMetadataCandidate("b1", mock.Anything, mock.Anything).
		Return(&metafetch.FetchMetadataResponse{Message: "applied", Book: &database.Book{ID: "b1"}}, nil)
	d.pool.EXPECT().Submit("b1", mock.Anything).Return(true)

	w := doReq(h.ApplyAudiobookMetadata, http.MethodPost, "/audiobooks/b1/apply-metadata",
		backgroundBody(metafetch.MetadataCandidate{Title: "Synthetic Title"}, ""), idParam("b1"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}
