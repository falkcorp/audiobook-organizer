// file: internal/server/handlers/duplicates/sibling_journal_list_test.go
// version: 1.0.1
// guid: 97a7db00-9389-4cc7-a6fa-536c1ec63043
// last-edited: 2026-10-20

package duplicates_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/duplicates"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// The list caps its page: limit=0 (or none) is the default of 50, and a
// larger limit is capped at 500.
func TestListSiblingMoveJournals_LimitCap(t *testing.T) {
	f := vptest.New(t)
	for i := range 501 {
		id := fmt.Sprintf("01J%023d", i)
		data, err := json.Marshal(merge.SiblingMoveJournal{ID: id, Status: merge.SiblingJournalApplied})
		require.NoError(t, err)
		require.NoError(t, f.S.SetRaw("merge:sibling-journal:"+id, data))
	}
	ms := merge.NewService(f.S)
	h := duplicates.New(f.S, nil, nil, nil,
		func() duplicates.MergeService { return ms }, nil, nil)
	count := func(url string) int {
		w := doReq(t, h.ListSiblingMoveJournals, http.MethodGet, url, nil)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var env struct {
			Data struct {
				Count int `json:"count"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
		return env.Data.Count
	}
	require.Equal(t, 50, count("/merge/sibling-journal"))
	require.Equal(t, 50, count("/merge/sibling-journal?limit=0"))
	require.Equal(t, 7, count("/merge/sibling-journal?limit=7"))
	require.Equal(t, 500, count("/merge/sibling-journal?limit=100000"))
}
