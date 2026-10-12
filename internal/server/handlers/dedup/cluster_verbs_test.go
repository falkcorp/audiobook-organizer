// file: internal/server/handlers/dedup/cluster_verbs_test.go
// version: 1.1.0
// guid: 94c1d7e3-6a20-4f58-b3e9-2d8a5c0f7b14
// last-edited: 2026-10-12

package deduphandler_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	deduphandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/dedup"
)

// The cluster verbs used to report success while swallowing per-pair write
// failures: the only visible trace was a low `candidates_updated` / `dismissed`
// the client had nothing to compare against. They now name the pairs whose
// write failed.

type clusterResp struct {
	Data struct {
		Dismissed         int     `json:"dismissed"`
		Removed           int     `json:"removed"`
		CandidatesUpdated int     `json:"candidates_updated"`
		CandidatesError   string  `json:"candidates_error"`
		Failed            []int64 `json:"failed_candidate_ids"`
		Skipped           []int64 `json:"skipped_candidate_ids"`
	} `json:"data"`
}

func decodeCluster(t *testing.T, body []byte) clusterResp {
	t.Helper()
	var r clusterResp
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatalf("decode: %v; body=%s", err, body)
	}
	return r
}

func failWritesFor(h *deduphandler.Handler, bad ...int64) {
	deduphandler.SetCandidateStatusWriterForTest(h, func(id int64, status string) error {
		for _, b := range bad {
			if id == b {
				return errors.New("write refused")
			}
		}
		return nil
	})
}

func TestLinkDedupCluster_ReportsPairsItCouldNotMark(t *testing.T) {
	h, d := newHandler(t)
	id1, _, _ := insertCandidate(t, d.es, "id1", "id2")
	id2, _, _ := insertCandidate(t, d.es, "id2", "id3")
	d.engine.EXPECT().MergeBooksJournaled(int64(0), mock.Anything, "", mock.Anything).
		Return(&merge.Result{PrimaryID: "id1"}, []string{"t"}, nil).Once()
	failWritesFor(h, id2)

	w := doReq(t, h.LinkDedupCluster, http.MethodPost, "/api/v1/dedup/candidates/link-cluster",
		map[string][]string{"book_ids": {"id1", "id2", "id3"}}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (the books ARE linked); body=%s", w.Code, w.Body.String())
	}
	r := decodeCluster(t, w.Body.Bytes())
	if r.Data.CandidatesUpdated != 1 || len(r.Data.Failed) != 1 || r.Data.Failed[0] != id2 {
		t.Fatalf("updated=%d failed=%v want 1 and [%d] (id1=%d marked); body=%s",
			r.Data.CandidatesUpdated, r.Data.Failed, id2, id1, w.Body.String())
	}
}

func TestLinkDedupCluster_CleanRunReportsNoFailures(t *testing.T) {
	h, d := newHandler(t)
	insertCandidate(t, d.es, "id1", "id2")
	d.engine.EXPECT().MergeBooksJournaled(int64(0), mock.Anything, "", mock.Anything).
		Return(&merge.Result{PrimaryID: "id1"}, []string{"t"}, nil).Once()
	w := doReq(t, h.LinkDedupCluster, http.MethodPost, "/api/v1/dedup/candidates/link-cluster",
		map[string][]string{"book_ids": {"id1", "id2"}}, nil)
	r := decodeCluster(t, w.Body.Bytes())
	if r.Data.CandidatesUpdated != 1 || len(r.Data.Failed) != 0 || r.Data.CandidatesError != "" {
		t.Fatalf("clean run reported trouble; body=%s", w.Body.String())
	}
}

func TestRejectDedupCluster_NamedPairsOnly(t *testing.T) {
	// The reviewer confirmed the pairs on the card. A third pair among the same
	// books that the current filter hides must NOT be dismissed.
	h, d := newHandler(t)
	allowLabelCaptureReads(d)
	shown1, _, _ := insertCandidate(t, d.es, "id1", "id2")
	shown2, _, _ := insertCandidate(t, d.es, "id2", "id3")
	hidden, _, _ := insertCandidate(t, d.es, "id1", "id3")
	outside, _, _ := insertCandidate(t, d.es, "id1", "id9") // not wholly in the cluster

	w := doReq(t, h.RejectDedupCluster, http.MethodPost, "/api/v1/dedup/candidates/reject-cluster",
		map[string]any{
			"book_ids":      []string{"id1", "id2", "id3"},
			"candidate_ids": []int64{shown1, shown2, outside},
		}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
	r := decodeCluster(t, w.Body.Bytes())
	if r.Data.Dismissed != 2 || len(r.Data.Skipped) != 1 || r.Data.Skipped[0] != outside {
		t.Fatalf("dismissed=%d skipped=%v want 2 and [%d]; body=%s", r.Data.Dismissed, r.Data.Skipped, outside, w.Body.String())
	}
	for id, want := range map[int64]string{shown1: "dismissed", shown2: "dismissed", hidden: "pending", outside: "pending"} {
		c, err := d.es.GetCandidateByID(id)
		if err != nil || c == nil || c.Status != want {
			t.Fatalf("candidate %d: status=%v err=%v want %s", id, c, err, want)
		}
	}
}

func TestRejectDedupCluster_NamedPairsReportWriteFailures(t *testing.T) {
	h, d := newHandler(t)
	allowLabelCaptureReads(d)
	a, _, _ := insertCandidate(t, d.es, "id1", "id2")
	b, _, _ := insertCandidate(t, d.es, "id2", "id3")
	failWritesFor(h, b)

	w := doReq(t, h.RejectDedupCluster, http.MethodPost, "/api/v1/dedup/candidates/reject-cluster",
		map[string]any{"book_ids": []string{"id1", "id2", "id3"}, "candidate_ids": []int64{a, b}}, nil)
	r := decodeCluster(t, w.Body.Bytes())
	if r.Data.Dismissed != 1 || len(r.Data.Failed) != 1 || r.Data.Failed[0] != b {
		t.Fatalf("dismissed=%d failed=%v want 1 and [%d]; body=%s", r.Data.Dismissed, r.Data.Failed, b, w.Body.String())
	}
}

func TestRejectDedupCluster_WholeSetPathReportsWriteFailures(t *testing.T) {
	h, d := newHandler(t)
	allowLabelCaptureReads(d)
	a, _, _ := insertCandidate(t, d.es, "id1", "id2")
	insertCandidate(t, d.es, "id2", "id3")
	failWritesFor(h, a)

	w := doReq(t, h.RejectDedupCluster, http.MethodPost, "/api/v1/dedup/candidates/reject-cluster",
		map[string][]string{"book_ids": {"id1", "id2", "id3"}}, nil)
	r := decodeCluster(t, w.Body.Bytes())
	if r.Data.Dismissed != 1 || len(r.Data.Failed) != 1 || r.Data.Failed[0] != a {
		t.Fatalf("dismissed=%d failed=%v want 1 and [%d]; body=%s", r.Data.Dismissed, r.Data.Failed, a, w.Body.String())
	}
}

func TestRemoveFromDedupCluster_ReportsEdgesItCouldNotDismiss(t *testing.T) {
	h, d := newHandler(t)
	a, _, _ := insertCandidate(t, d.es, "id1", "id3")
	b, _, _ := insertCandidate(t, d.es, "id2", "id3")
	failWritesFor(h, b)

	w := doReq(t, h.RemoveFromDedupCluster, http.MethodPost, "/api/v1/dedup/candidates/remove-from-cluster",
		map[string]any{"cluster_book_ids": []string{"id1", "id2", "id3"}, "remove_book_ids": []string{"id3"}}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
	r := decodeCluster(t, w.Body.Bytes())
	if r.Data.Dismissed != 1 || len(r.Data.Failed) != 1 || r.Data.Failed[0] != b {
		t.Fatalf("dismissed=%d failed=%v want 1 and [%d] (edge %d dismissed); body=%s",
			r.Data.Dismissed, r.Data.Failed, b, a, w.Body.String())
	}
}

func TestRejectDedupCluster_NamedPairReadErrorIsFailedNotSkipped(t *testing.T) {
	// A read that errors says nothing about the pair's status: it may well still
	// be pending, so it is reported as failed (retryable), not skipped.
	h, d := newHandler(t)
	allowLabelCaptureReads(d)
	ok, _, _ := insertCandidate(t, d.es, "id1", "id2")
	bad, _, _ := insertCandidate(t, d.es, "id2", "id3")
	deduphandler.SetCandidateReaderForTest(h, func(id int64) (*database.DedupCandidate, error) {
		if id == bad {
			return nil, errors.New("read refused")
		}
		return d.es.GetCandidateByID(id)
	})

	w := doReq(t, h.RejectDedupCluster, http.MethodPost, "/api/v1/dedup/candidates/reject-cluster",
		map[string]any{"book_ids": []string{"id1", "id2", "id3"}, "candidate_ids": []int64{ok, bad}}, nil)
	r := decodeCluster(t, w.Body.Bytes())
	if r.Data.Dismissed != 1 || len(r.Data.Failed) != 1 || r.Data.Failed[0] != bad || len(r.Data.Skipped) != 0 {
		t.Fatalf("dismissed=%d failed=%v skipped=%v want 1, [%d], []; body=%s",
			r.Data.Dismissed, r.Data.Failed, r.Data.Skipped, bad, w.Body.String())
	}
}

func TestRejectDedupCluster_NamedPairsAreCapped(t *testing.T) {
	h, _ := newHandler(t)
	w := doReq(t, h.RejectDedupCluster, http.MethodPost, "/api/v1/dedup/candidates/reject-cluster",
		map[string]any{"book_ids": []string{"id1", "id2"}, "candidate_ids": make([]int64, 2001)}, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestRejectDedupCluster_NamedNonBookPairIsSkipped(t *testing.T) {
	h, d := newHandler(t)
	allowLabelCaptureReads(d)
	if err := d.es.UpsertCandidate(database.DedupCandidate{
		EntityType: "author", EntityAID: "id1", EntityBID: "id2", Layer: "exact", Status: "pending",
	}); err != nil {
		t.Fatalf("UpsertCandidate: %v", err)
	}
	list, _, err := d.es.ListCandidates(database.CandidateFilter{EntityType: "author", Limit: 10})
	if err != nil || len(list) != 1 {
		t.Fatalf("seed author pair: %v %d", err, len(list))
	}
	id := list[0].ID

	w := doReq(t, h.RejectDedupCluster, http.MethodPost, "/api/v1/dedup/candidates/reject-cluster",
		map[string]any{"book_ids": []string{"id1", "id2"}, "candidate_ids": []int64{id}}, nil)
	r := decodeCluster(t, w.Body.Bytes())
	if r.Data.Dismissed != 0 || len(r.Data.Skipped) != 1 {
		t.Fatalf("author pair must be skipped; body=%s", w.Body.String())
	}
	c, _ := d.es.GetCandidateByID(id)
	if c == nil || c.Status != "pending" {
		t.Fatalf("author pair was dismissed: %+v", c)
	}
}
