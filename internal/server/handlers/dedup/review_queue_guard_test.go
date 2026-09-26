// file: internal/server/handlers/dedup/review_queue_guard_test.go
// version: 1.1.0
// guid: cab247f4-53e7-444a-aa35-bc2ca52f380f
// last-edited: 2026-09-26

// Regression tests for the review-queue-only rule on the bulk link endpoints:
// bulk-link must honour the list's source filter, and neither bulk-link nor
// link-series may link a same-path pair or a pinned manual pair, directly or
// through a chain of other links.

package deduphandler_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

type bulkResp struct {
	Data struct {
		Attempted int `json:"attempted"`
		Merged    int `json:"merged"`
		Failed    int `json:"failed"`
		Failures  []struct {
			CandidateID int64  `json:"candidate_id"`
			Reason      string `json:"reason"`
		} `json:"failures"`
	} `json:"data"`
}

func decodeBulk(t *testing.T, body []byte) bulkResp {
	t.Helper()
	var r bulkResp
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatalf("decode: %v; body=%s", err, body)
	}
	return r
}

// wireGuardBooks wires GetBookByID from a fixed map, and
// GetBooksByVersionGroup from the same map's VersionGroupIDs.
func wireGuardBooks(d testDeps, m map[string]*database.Book) {
	d.store.EXPECT().GetBookByID(mock.Anything).RunAndReturn(func(id string) (*database.Book, error) {
		return m[id], nil
	}).Maybe()
	d.store.EXPECT().GetBooksByVersionGroup(mock.Anything).RunAndReturn(func(gid string) ([]database.Book, error) {
		var out []database.Book
		for _, b := range m {
			if b.VersionGroupID != nil && *b.VersionGroupID == gid {
				out = append(out, *b)
			}
		}
		return out, nil
	}).Maybe()
	d.store.EXPECT().GetBookFiles(mock.Anything).Return(nil, nil).Maybe()
	d.engine.EXPECT().ScorePairsForBook(mock.Anything, mock.Anything, mock.Anything).Return(nil, nil).Maybe()
}

// A posted {"source":"manual"} was dropped by the JSON binding, so the link
// covered every pending book candidate instead of the manual ones the list
// showed. Now the filter narrows the set, and the manual row in it is refused
// (review queue only), so nothing is linked.
func TestBulkLinkDedupCandidates_SourceFilterHonoured(t *testing.T) {
	h, d := newHandler(t)
	wireGuardBooks(d, map[string]*database.Book{
		"man-a": {ID: "man-a"}, "man-b": {ID: "man-b"},
		"scan-a": {ID: "scan-a"}, "scan-b": {ID: "scan-b"},
	})
	insertCandidate(t, d.es, "scan-a", "scan-b")
	if _, err := d.es.EnqueueManualCandidate("book", "man-a", "man-b", "check"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	// No MergeJournaled expectation: any merge fails the mock.

	w := doReq(t, h.BulkLinkDedupCandidates, http.MethodPost, "/api/v1/dedup/candidates/bulk-link",
		map[string]any{"source": "manual"}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
	r := decodeBulk(t, w.Body.Bytes())
	if r.Data.Attempted != 1 {
		t.Fatalf("attempted=%d want 1: the source filter must narrow the set; body=%s", r.Data.Attempted, w.Body.String())
	}
	if r.Data.Merged != 0 || r.Data.Failed != 1 || !strings.HasPrefix(r.Data.Failures[0].Reason, "manual") {
		t.Fatalf("the manual pair must be refused, not linked; body=%s", w.Body.String())
	}
}

// both_unmatched is a book-level filter the endpoint cannot express; it is
// refused rather than silently dropped.
func TestBulkLinkDedupCandidates_BothUnmatchedRefused(t *testing.T) {
	h, d := newHandler(t)
	insertCandidate(t, d.es, "a", "b")
	w := doReq(t, h.BulkLinkDedupCandidates, http.MethodPost, "/api/v1/dedup/candidates/bulk-link",
		map[string]any{"both_unmatched": true}, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400; body=%s", w.Code, w.Body.String())
	}
}

// A and B share a cleaned path and have no candidate between them. Linking
// A–C and then B–C would put A and B in one version group, so the second link
// is refused. Exactly one merge runs.
func TestBulkLinkDedupCandidates_RefusesTransitiveSamePathLink(t *testing.T) {
	h, d := newHandler(t)
	path := "/lib/Author/Book/32.m4b"
	wireGuardBooks(d, map[string]*database.Book{
		"tp-a": {ID: "tp-a", FilePath: path},
		"tp-b": {ID: "tp-b", FilePath: path},
		"tp-c": {ID: "tp-c", FilePath: "/lib/Other/c.m4b"},
	})
	insertCandidate(t, d.es, "tp-a", "tp-c")
	insertCandidate(t, d.es, "tp-b", "tp-c")
	d.engine.EXPECT().MergeJournaled(mock.Anything, mock.Anything, mock.Anything, "", mock.Anything).
		Return(&merge.Result{PrimaryID: "tp-c"}, "dedup:automerge:k", nil).Once()

	w := doReq(t, h.BulkLinkDedupCandidates, http.MethodPost, "/api/v1/dedup/candidates/bulk-link", map[string]any{}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
	r := decodeBulk(t, w.Body.Bytes())
	if r.Data.Merged != 1 || r.Data.Failed != 1 || !strings.HasPrefix(r.Data.Failures[0].Reason, "same_path") {
		t.Fatalf("want 1 linked and 1 refused as same_path; body=%s", w.Body.String())
	}
}

// Same shape for link-series: A–C and B–C union A and B into one cluster
// although the A–B pair itself is refused. The cluster must not be linked;
// a separate clean pair in the same series (the control) still is.
func TestLinkDedupCandidateSeries_RefusesClusterJoiningSamePathPair(t *testing.T) {
	h, d := newHandler(t)
	sid := 7
	path := "/lib/Author/Book/32.m4b"
	wireGuardBooks(d, map[string]*database.Book{
		"sa": {ID: "sa", FilePath: path, SeriesID: &sid},
		"sb": {ID: "sb", FilePath: path, SeriesID: &sid},
		"sc": {ID: "sc", FilePath: "/lib/c.m4b", SeriesID: &sid},
		"sx": {ID: "sx", FilePath: "/lib/x.m4b", SeriesID: &sid},
		"sy": {ID: "sy", FilePath: "/lib/y.m4b", SeriesID: &sid},
	})
	insertCandidate(t, d.es, "sa", "sb")
	insertCandidate(t, d.es, "sa", "sc")
	insertCandidate(t, d.es, "sb", "sc")
	insertCandidate(t, d.es, "sx", "sy")
	var linkedSets [][]string
	d.engine.EXPECT().MergeBooksJournaled(int64(0), mock.Anything, "", mock.Anything).
		RunAndReturn(func(_ int64, ids []string, _ string, _ string) (*merge.Result, []string, error) {
			linkedSets = append(linkedSets, append([]string(nil), ids...))
			return &merge.Result{PrimaryID: ids[0]}, nil, nil
		}).Maybe()

	w := doReq(t, h.LinkDedupCandidateSeries, http.MethodPost, "/api/v1/dedup/candidates/link-series", map[string]int{"series_id": sid}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
	for _, set := range linkedSets {
		if slices.Contains(set, "sa") && slices.Contains(set, "sb") {
			t.Fatalf("a set joining the same-path pair was linked: %v", set)
		}
	}
	if len(linkedSets) != 1 || !slices.Contains(linkedSets[0], "sx") {
		t.Fatalf("control cluster {sx,sy} should be the only link; got %v", linkedSets)
	}
	if !strings.Contains(w.Body.String(), "same_path") {
		t.Fatalf("the refused cluster must be reported; body=%s", w.Body.String())
	}
}

// A pinned manual pair in the series is never linked by link-series.
func TestLinkDedupCandidateSeries_SkipsManualCandidate(t *testing.T) {
	h, d := newHandler(t)
	sid := 9
	wireGuardBooks(d, map[string]*database.Book{
		"ma": {ID: "ma", FilePath: "/lib/ma.m4b", SeriesID: &sid},
		"mb": {ID: "mb", FilePath: "/lib/mb.m4b", SeriesID: &sid},
	})
	insertCandidate(t, d.es, "ma", "mb")
	if _, err := d.es.EnqueueManualCandidate("book", "ma", "mb", ""); err != nil {
		t.Fatalf("pin: %v", err)
	}
	// No MergeBooksJournaled expectation: any link fails the mock.

	w := doReq(t, h.LinkDedupCandidateSeries, http.MethodPost, "/api/v1/dedup/candidates/link-series", map[string]int{"series_id": sid}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "manual") {
		t.Fatalf("the refused manual pair must be reported; body=%s", w.Body.String())
	}
}

// Linking into an EXISTING version group joins every member of that group.
// eg-b sits in group G with eg-a, which is not in the request at all, and
// eg-a is at the same cleaned path as eg-t. Linking (eg-b, eg-t) would put eg-a and eg-t (one
// cleaned path) into one group, so it is refused although the pair itself is
// at two different paths.
func TestBulkLinkDedupCandidates_RefusesLinkIntoGroupHoldingSamePathTwin(t *testing.T) {
	h, d := newHandler(t)
	path := "/lib/Author/Book/32.m4b"
	g := "vg-1"
	wireGuardBooks(d, map[string]*database.Book{
		"eg-a": {ID: "eg-a", FilePath: path, VersionGroupID: &g},
		"eg-b": {ID: "eg-b", FilePath: "/lib/Author/Book/other.m4b", VersionGroupID: &g},
		"eg-t": {ID: "eg-t", FilePath: path},
	})
	insertCandidate(t, d.es, "eg-b", "eg-t")
	d.engine.EXPECT().MergeJournaled(mock.Anything, mock.Anything, mock.Anything, "", mock.Anything).
		Return(&merge.Result{PrimaryID: "eg-b"}, "dedup:automerge:k", nil).Maybe()

	w := doReq(t, h.BulkLinkDedupCandidates, http.MethodPost, "/api/v1/dedup/candidates/bulk-link", map[string]any{}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
	r := decodeBulk(t, w.Body.Bytes())
	if r.Data.Merged != 0 || r.Data.Failed != 1 || !strings.HasPrefix(r.Data.Failures[0].Reason, "same_path: eg-a and eg-t") {
		t.Fatalf("want the link into the group refused as same_path eg-a/eg-t; body=%s", w.Body.String())
	}
}

// A same-path pair a human grouped EARLIER is already in one group; linking a
// third, clean book into that group does not join it, so it is not refused.
func TestBulkLinkDedupCandidates_AllowsLinkIntoGroupWithExistingSamePathPair(t *testing.T) {
	h, d := newHandler(t)
	path := "/lib/Author/Book/32.m4b"
	g := "vg-2"
	wireGuardBooks(d, map[string]*database.Book{
		"ok-a": {ID: "ok-a", FilePath: path, VersionGroupID: &g},
		"ok-b": {ID: "ok-b", FilePath: path, VersionGroupID: &g},
		"ok-c": {ID: "ok-c", FilePath: "/lib/Other/c.m4b"},
	})
	insertCandidate(t, d.es, "ok-a", "ok-c")
	d.engine.EXPECT().MergeJournaled(mock.Anything, mock.Anything, mock.Anything, "", mock.Anything).
		Return(&merge.Result{PrimaryID: "ok-a"}, "dedup:automerge:k", nil).Once()

	w := doReq(t, h.BulkLinkDedupCandidates, http.MethodPost, "/api/v1/dedup/candidates/bulk-link", map[string]any{}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
	r := decodeBulk(t, w.Body.Bytes())
	if r.Data.Merged != 1 || r.Data.Failed != 0 {
		t.Fatalf("want the clean link into the group allowed; body=%s", w.Body.String())
	}
}

// link-series: a cluster whose book is already grouped with a pinned manual
// partner of another cluster member is refused.
func TestLinkDedupCandidateSeries_RefusesClusterJoiningGroupedManualPair(t *testing.T) {
	h, d := newHandler(t)
	sid := 11
	g := "vg-3"
	wireGuardBooks(d, map[string]*database.Book{
		"gm-a": {ID: "gm-a", FilePath: "/lib/a.m4b", SeriesID: &sid, VersionGroupID: &g},
		"gm-p": {ID: "gm-p", FilePath: "/lib/p.m4b", VersionGroupID: &g},
		"gm-c": {ID: "gm-c", FilePath: "/lib/c.m4b", SeriesID: &sid},
	})
	insertCandidate(t, d.es, "gm-a", "gm-c")
	if _, err := d.es.EnqueueManualCandidate("book", "gm-p", "gm-c", "look"); err != nil {
		t.Fatalf("pin: %v", err)
	}
	var linkedSets [][]string
	d.engine.EXPECT().MergeBooksJournaled(int64(0), mock.Anything, "", mock.Anything).
		RunAndReturn(func(_ int64, ids []string, _ string, _ string) (*merge.Result, []string, error) {
			linkedSets = append(linkedSets, append([]string(nil), ids...))
			return &merge.Result{PrimaryID: ids[0]}, nil, nil
		}).Maybe()

	w := doReq(t, h.LinkDedupCandidateSeries, http.MethodPost, "/api/v1/dedup/candidates/link-series", map[string]int{"series_id": sid}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
	if len(linkedSets) != 0 {
		t.Fatalf("a cluster joining a grouped manual pair was linked: %v", linkedSets)
	}
	if !strings.Contains(w.Body.String(), "manual: gm-c and gm-p") {
		t.Fatalf("the refused cluster must be reported; body=%s", w.Body.String())
	}
}
