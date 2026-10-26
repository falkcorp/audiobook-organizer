// file: internal/server/purge_forget_waiting_route_test.go
// version: 1.0.0
// guid: 7d1c9e42-5b08-4f63-a2e7-c3f40b8a9d15
// last-edited: 2026-10-26

package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
)

// seedPendingJournal writes journal records the way a purge pass does
// (purge_pending_file:<book id>), with the waiting-for-folder mark where the
// caller asks for it. The key and field names are the journal's wire format.
func (f *credGuardFixture) seedPendingJournal(t *testing.T, bookID, path string, waiting bool) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"book_id":            bookID,
		"path":               path,
		"recorded_at":        time.Now().UTC(),
		"waiting_for_folder": waiting,
	})
	require.NoError(t, err)
	require.NoError(t, f.store.SetRaw("purge_pending_file:"+bookID, raw))
}

func (f *credGuardFixture) journalKeys(t *testing.T) []string {
	t.Helper()
	pairs, _, err := f.store.ScanPrefixPage("purge_pending_file:", "", 100)
	require.NoError(t, err)
	keys := make([]string, 0, len(pairs))
	for _, p := range pairs {
		keys = append(keys, p.Key)
	}
	return keys
}

// POST /audiobooks/purge-soft-deleted/forget-waiting-file-records is wired
// behind library.delete: a viewer and a session holding only library.organize
// are refused and the journal is untouched; a session holding exactly
// library.delete deletes the records marked as waiting for a folder (and only
// those) and leaves an audit row naming the actor.
func TestForgetWaitingFileRecordsRoute_PermissionAndEffect(t *testing.T) {
	f := setupCredGuardServer(t)
	_, viewerTok := f.viewerGoneSession(t)
	onlyDelete := f.exactPermGoneSession(t, "only-delete", auth.PermLibraryDelete)
	onlyOrganize := f.exactPermGoneSession(t, "only-organize", auth.PermLibraryOrganize)

	f.seedPendingJournal(t, "waiting-a", "/lib/Gotham/Hush/hush.m4b", true)
	f.seedPendingJournal(t, "waiting-b", "/lib/Gotham/Knightfall/knightfall.m4b", true)
	f.seedPendingJournal(t, "pending-c", "/lib/Gotham/Year One/year-one.m4b", false)
	const route = "/api/v1/audiobooks/purge-soft-deleted/forget-waiting-file-records"

	for name, tok := range map[string]string{"viewer": viewerTok, "only organize": onlyOrganize} {
		w := f.do(t, tok, http.MethodPost, route, nil)
		assert.Equal(t, http.StatusForbidden, w.Code, "%s: %s", name, w.Body.String())
	}
	assert.Len(t, f.journalKeys(t), 3, "a refused request deletes nothing")

	// The listing separates the two counts before anything is forgotten.
	w := f.do(t, f.sessionToken, http.MethodGet, "/api/v1/audiobooks/soft-deleted", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var listing struct {
		Data struct {
			Pending float64 `json:"files_pending_from_earlier_pass"`
			Waiting float64 `json:"files_waiting_for_folder"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &listing))
	assert.Equal(t, float64(1), listing.Data.Pending)
	assert.Equal(t, float64(2), listing.Data.Waiting)

	w = f.do(t, onlyDelete, http.MethodPost, route, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var res struct {
		Data struct {
			Records int `json:"records_forgotten"`
			Paths   int `json:"paths_forgotten"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &res))
	assert.Equal(t, 2, res.Data.Records)
	assert.Equal(t, 2, res.Data.Paths)
	assert.Equal(t, []string{"purge_pending_file:pending-c"}, f.journalKeys(t),
		"only the records marked as waiting for a folder are forgotten")

	// The audit row names the signed-in actor and the counts.
	w = f.do(t, f.sessionToken, http.MethodGet, "/api/v1/activity?type="+activity.TypeTrashForgetPendingFileRecords, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := w.Body.String()
	assert.Contains(t, body, activity.TypeTrashForgetPendingFileRecords)
	assert.Contains(t, body, "synthetic-only-delete")
	assert.Contains(t, body, `"records_forgotten":2`)
}
