// file: internal/server/handlers/dedup/manual_candidates_test.go
// version: 1.1.0
// guid: 3f2b9c1e-6a47-4d8e-b5c0-9e1d7a4f2c68
// last-edited: 2026-09-26

package deduphandler_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

type manualResp struct {
	Data struct {
		DryRun  bool           `json:"dry_run"`
		Summary map[string]int `json:"summary"`
		Results []struct {
			BookA          string                   `json:"book_a"`
			BookB          string                   `json:"book_b"`
			Outcome        string                   `json:"outcome"`
			Code           string                   `json:"code"`
			Pinned         bool                     `json:"pinned"`
			VersionGroupID string                   `json:"version_group_id"`
			Candidate      *database.DedupCandidate `json:"candidate"`
		} `json:"results"`
	} `json:"data"`
}

func decodeManual(t *testing.T, body []byte) manualResp {
	t.Helper()
	var r manualResp
	require.NoError(t, json.Unmarshal(body, &r), string(body))
	return r
}

// books wires GetBookByID for a fixed set of books; any other id is not found.
func books(d testDeps, bs ...*database.Book) {
	known := map[string]*database.Book{}
	for _, b := range bs {
		known[b.ID] = b
	}
	d.store.EXPECT().GetBookByID(mock.Anything).RunAndReturn(func(id string) (*database.Book, error) {
		return known[id], nil
	}).Maybe()
}

func allCandidates(t *testing.T, es *database.EmbeddingStore) []database.DedupCandidate {
	t.Helper()
	cs, _, err := es.ListCandidates(database.CandidateFilter{Limit: 1000})
	require.NoError(t, err)
	return cs
}

func pairBody(dryRun *bool, pairs ...[2]string) map[string]any {
	ps := make([]map[string]string, 0, len(pairs))
	for _, p := range pairs {
		ps = append(ps, map[string]string{"book_a": p[0], "book_b": p[1], "reason": "shell"})
	}
	body := map[string]any{"pairs": ps}
	if dryRun != nil {
		body["dry_run"] = *dryRun
	}
	return body
}

func boolp(b bool) *bool { return &b }

const manualURL = "/api/v1/dedup/candidates"

// A body with no dry_run key must NOT write: the default is a dry run.
func TestEnqueueManual_OmittedDryRunWritesNothing(t *testing.T) {
	h, d := newHandler(t)
	books(d, &database.Book{ID: "a"}, &database.Book{ID: "b"})

	w := doReq(t, h.EnqueueManualDedupCandidates, http.MethodPost, manualURL, pairBody(nil, [2]string{"a", "b"}), nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	r := decodeManual(t, w.Body.Bytes())
	require.True(t, r.Data.DryRun)
	require.Equal(t, 1, r.Data.Summary["created"])
	require.Equal(t, "created", r.Data.Results[0].Outcome)
	require.Empty(t, allCandidates(t, d.es), "dry run must not write")
	require.Empty(t, *d.dirtyReasons)
}

func TestEnqueueManual_ExplicitDryRunTrueWritesNothing(t *testing.T) {
	h, d := newHandler(t)
	books(d, &database.Book{ID: "a"}, &database.Book{ID: "b"})

	w := doReq(t, h.EnqueueManualDedupCandidates, http.MethodPost, manualURL, pairBody(boolp(true), [2]string{"a", "b"}), nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Empty(t, allCandidates(t, d.es))
}

func TestEnqueueManual_ApplyCreatesReviewableCandidate(t *testing.T) {
	h, d := newHandler(t)
	books(d, &database.Book{ID: "a"}, &database.Book{ID: "b"})

	w := doReq(t, h.EnqueueManualDedupCandidates, http.MethodPost, manualURL, pairBody(boolp(false), [2]string{"b", "a"}), nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	r := decodeManual(t, w.Body.Bytes())
	require.False(t, r.Data.DryRun)
	require.Equal(t, 1, r.Data.Summary["created"])

	cs := allCandidates(t, d.es)
	require.Len(t, cs, 1)
	c := cs[0]
	require.Equal(t, "book", c.EntityType)
	require.Equal(t, "a", c.EntityAID)
	require.Equal(t, "b", c.EntityBID)
	require.Equal(t, "pending", c.Status)
	require.Equal(t, database.CandidateLayerManual, c.Layer)
	require.Equal(t, database.CandidateSourceManual, c.Source)
	require.Equal(t, "shell", c.SourceNote)
	require.Equal(t, []string{"manual_candidates"}, *d.dirtyReasons)

	// It shows up in the review list like a scanner candidate, tagged manual.
	lw := doReq(t, h.ListDedupCandidates, http.MethodGet, "/api/v1/dedup/candidates?status=pending&source=manual", nil, nil)
	require.Equal(t, http.StatusOK, lw.Code, lw.Body.String())
	var list struct {
		Data struct {
			Candidates []map[string]any `json:"candidates"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(lw.Body.Bytes(), &list), lw.Body.String())
	require.Len(t, list.Data.Candidates, 1)
	require.Equal(t, "manual", list.Data.Candidates[0]["layer"])
	require.Equal(t, "manual", list.Data.Candidates[0]["source"])
	require.Equal(t, "", list.Data.Candidates[0]["ai_advice_verdict"], "no advice before the LLM reviews it")

	// LLM advice recorded on the pinned row reaches the review list.
	require.NoError(t, d.es.RecordCandidateLLMAdvice(c.ID, "duplicate", "[high] same book"))
	lw = doReq(t, h.ListDedupCandidates, http.MethodGet, "/api/v1/dedup/candidates?status=pending&source=manual", nil, nil)
	require.Equal(t, http.StatusOK, lw.Code, lw.Body.String())
	require.NoError(t, json.Unmarshal(lw.Body.Bytes(), &list), lw.Body.String())
	require.Len(t, list.Data.Candidates, 1)
	require.Equal(t, "duplicate", list.Data.Candidates[0]["ai_advice_verdict"])
	require.Equal(t, "[high] same book", list.Data.Candidates[0]["ai_advice_reason"])
	require.NotNil(t, list.Data.Candidates[0]["ai_advice_at"])
	require.Equal(t, "pending", list.Data.Candidates[0]["status"])
}

func TestEnqueueManual_Idempotent(t *testing.T) {
	h, d := newHandler(t)
	books(d, &database.Book{ID: "a"}, &database.Book{ID: "b"})

	body := pairBody(boolp(false), [2]string{"a", "b"})
	first := doReq(t, h.EnqueueManualDedupCandidates, http.MethodPost, manualURL, body, nil)
	require.Equal(t, http.StatusOK, first.Code)
	firstID := decodeManual(t, first.Body.Bytes()).Data.Results[0].Candidate.ID

	second := doReq(t, h.EnqueueManualDedupCandidates, http.MethodPost, manualURL, body, nil)
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	r := decodeManual(t, second.Body.Bytes())
	require.Equal(t, "already_open", r.Data.Results[0].Outcome)
	require.Equal(t, firstID, r.Data.Results[0].Candidate.ID)
	require.Len(t, allCandidates(t, d.es), 1)
}

// An open scanner row for the pair is returned, and pinned so purges keep it.
func TestEnqueueManual_PinsExistingScannerCandidate(t *testing.T) {
	h, d := newHandler(t)
	books(d, &database.Book{ID: "a"}, &database.Book{ID: "b"})
	id, _, _ := insertCandidate(t, d.es, "a", "b")

	w := doReq(t, h.EnqueueManualDedupCandidates, http.MethodPost, manualURL, pairBody(boolp(false), [2]string{"a", "b"}), nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	r := decodeManual(t, w.Body.Bytes())
	require.Equal(t, "already_open", r.Data.Results[0].Outcome)
	require.True(t, r.Data.Results[0].Pinned)
	require.Equal(t, 1, r.Data.Summary["pinned"])

	got, err := d.es.GetCandidateByID(id)
	require.NoError(t, err)
	require.Equal(t, "embedding", got.Layer)
	require.True(t, database.IsManualCandidate(*got))
}

func TestEnqueueManual_RejectsInvalidPairs(t *testing.T) {
	h, d := newHandler(t)
	grp := "vg-1"
	books(d,
		&database.Book{ID: "a"}, &database.Book{ID: "b"},
		&database.Book{ID: "v1", VersionGroupID: &grp}, &database.Book{ID: "v2", VersionGroupID: &grp},
	)

	w := doReq(t, h.EnqueueManualDedupCandidates, http.MethodPost, manualURL, pairBody(boolp(false),
		[2]string{"a", "a"},       // same_book
		[2]string{"a", ""},        // missing_id
		[2]string{"a", "missing"}, // book_not_found
		[2]string{"v1", "v2"},     // same_version_group
		[2]string{"a", "b"},       // ok
		[2]string{"b", "a"},       // duplicate_in_request
	), nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	r := decodeManual(t, w.Body.Bytes())
	codes := make([]string, 0, len(r.Data.Results))
	for _, res := range r.Data.Results {
		codes = append(codes, res.Code)
	}
	require.Equal(t, []string{"same_book", "missing_id", "book_not_found", "same_version_group", "", "duplicate_in_request"}, codes)
	require.Equal(t, "vg-1", r.Data.Results[3].VersionGroupID)
	require.Equal(t, 5, r.Data.Summary["rejected"])
	require.Equal(t, 1, r.Data.Summary["created"])
	require.Len(t, allCandidates(t, d.es), 1, "only the valid pair is written")
}

func TestEnqueueManual_RefusesSoftDeletedBook(t *testing.T) {
	h, d := newHandler(t)
	deleted := true
	books(d, &database.Book{ID: "a"}, &database.Book{ID: "gone", MarkedForDeletion: &deleted})

	w := doReq(t, h.EnqueueManualDedupCandidates, http.MethodPost, manualURL, pairBody(boolp(false), [2]string{"a", "gone"}), nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	r := decodeManual(t, w.Body.Bytes())
	require.Equal(t, "rejected", r.Data.Results[0].Outcome)
	require.Equal(t, "book_deleted", r.Data.Results[0].Code)
	require.Empty(t, allCandidates(t, d.es))
}

// A store read error is reported as lookup_failed, never as "not found".
func TestEnqueueManual_LookupErrorIsNotNotFound(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID("a").Return(&database.Book{ID: "a"}, nil).Maybe()
	d.store.EXPECT().GetBookByID("b").Return(nil, errors.New("pebble: closed")).Maybe()

	w := doReq(t, h.EnqueueManualDedupCandidates, http.MethodPost, manualURL, pairBody(boolp(false), [2]string{"a", "b"}), nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "lookup_failed", decodeManual(t, w.Body.Bytes()).Data.Results[0].Code)
}

func TestEnqueueManual_BadBodies(t *testing.T) {
	h, _ := newHandler(t)
	for name, body := range map[string]any{
		"no_pairs":    map[string]any{"dry_run": false},
		"empty_pairs": map[string]any{"pairs": []any{}},
	} {
		w := doReq(t, h.EnqueueManualDedupCandidates, http.MethodPost, manualURL, body, nil)
		require.Equalf(t, http.StatusBadRequest, w.Code, "%s: %s", name, w.Body.String())
	}
	for name, raw := range map[string]string{
		"empty":     ``,
		"malformed": `{"pairs": [`,
		"wrong_dry": `{"pairs": [{"book_a":"a","book_b":"b"}], "dry_run": "false"}`,
	} {
		w := w2rawReq(t, h.EnqueueManualDedupCandidates, manualURL, raw, true)
		require.Equalf(t, http.StatusBadRequest, w.Code, "%s: %s", name, w.Body.String())
	}
}

func TestEnqueueManual_NoEmbedStore(t *testing.T) {
	h, _ := newHandler(t, noEmbed)
	w := doReq(t, h.EnqueueManualDedupCandidates, http.MethodPost, manualURL, pairBody(nil, [2]string{"a", "b"}), nil)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
}
