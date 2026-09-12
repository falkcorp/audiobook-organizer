// file: internal/server/handlers/audiobooks/applied_filters_test.go
// version: 1.0.0
// guid: 9c906f15-83b7-485f-ab8c-42b4336a6bdf
// last-edited: 2026-09-11

package audiobookshandler_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TASK-098 (TODO.md L7736): the /audiobooks list response did not say which
// filters the server actually applied -- only items/count/limit/offset. A
// caller (or the frontend rendering filter chips) had to guess from what it
// sent, which can diverge from what the server accepted (an unrecognized or
// stripped field, a per-user filter, a quick-query fast path). These tests
// pin applied_filters as ADDITIVE ground truth: it never changes which books
// are returned, and it is always present -- an empty array, never omitted or
// null, when no filters were sent.

// TestListAudiobooks_AppliedFilters_EchoesFieldFilters asserts a filters=
// JSON param round-trips into the response's applied_filters array with the
// same field/value.
func TestListAudiobooks_AppliedFilters_EchoesFieldFilters(t *testing.T) {
	h, _ := newHandler(t)
	c, w := newCtx("GET", `/audiobooks?filters=[{"field":"title","value":"Dune"}]`, nil, nil)
	h.ListAudiobooks(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var body struct {
		Data struct {
			AppliedFilters []map[string]string `json:"applied_filters"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Contains(t, body.Data.AppliedFilters, map[string]string{"field": "title", "value": "Dune"})
}

// TestListAudiobooks_AppliedFilters_EmptyWhenNoFiltersSent is the
// anti-over-suppression guard: a known-good, unfiltered request must still
// return applied_filters as [] (present, not omitted and not null) with the
// new guard active.
func TestListAudiobooks_AppliedFilters_EmptyWhenNoFiltersSent(t *testing.T) {
	h, _ := newHandler(t)
	c, w := newCtx("GET", "/audiobooks", nil, nil)
	h.ListAudiobooks(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var body struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	raw, ok := body.Data["applied_filters"]
	require.True(t, ok, "applied_filters key must be present even when no filters were sent")
	require.NotEqual(t, "null", string(raw), "applied_filters must be [], never null")

	var applied []map[string]string
	require.NoError(t, json.Unmarshal(raw, &applied))
	require.Empty(t, applied)
}

// TestListAudiobooks_AppliedFilters_LibraryState pins the acceptance-criteria
// example: library_state=imported (a simple query param, not a filters=
// entry) must also appear in applied_filters, since the server applied it
// too.
func TestListAudiobooks_AppliedFilters_LibraryState(t *testing.T) {
	h, _ := newHandler(t)
	c, w := newCtx("GET", "/audiobooks?library_state=imported", nil, nil)
	h.ListAudiobooks(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var body struct {
		Data struct {
			AppliedFilters []map[string]string `json:"applied_filters"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Contains(t, body.Data.AppliedFilters, map[string]string{"field": "library_state", "value": "imported"})
}

// TestListAudiobooks_AppliedFilters_ExistingKeysUnchanged is the additive
// guarantee: adding applied_filters must not rename, remove, or otherwise
// disturb items/count/limit/offset.
func TestListAudiobooks_AppliedFilters_ExistingKeysUnchanged(t *testing.T) {
	h, d := newHandler(t)
	d.rec.listResp = map[string]any{"items": []any{}, "count": 0, "limit": 50, "offset": 0}
	c, w := newCtx("GET", "/audiobooks", nil, nil)
	h.ListAudiobooks(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var body struct {
		Data struct {
			Items  []any `json:"items"`
			Count  int   `json:"count"`
			Limit  int   `json:"limit"`
			Offset int   `json:"offset"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, 0, body.Data.Count)
	require.Equal(t, 50, body.Data.Limit)
	require.Equal(t, 0, body.Data.Offset)
}
