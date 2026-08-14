// file: internal/server/series_delete_refguard_test.go
// version: 1.0.0
// guid: 5b6c1e07-9a34-4d82-bf16-0c72e9a35d84
// last-edited: 2026-08-14

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The series delete handlers used to guard with GetBooksBySeriesIDCore, the
// counter behind the badge in the UI, which skips trashed and non-primary
// books. Those books still hold the series_id, so a series whose only books
// were in one of those two states counted as zero and was deleted out from
// under them. On production that produced 6,893 series IDs referenced by 13,322
// live books, every one rendering with no series and the name unrecoverable.
//
// Both tests below FAIL against the old filtered guard, which is the point:
// each builds a series that the display counter reports as empty and the
// unfiltered counter reports as referenced.


// TestBulkDeleteSeries_KeepsSeriesWhoseOnlyBookIsTrashed is the exact shape of
// series 160094 on production ("Queen of Fire: A Raven's Shadow Novel"), whose
// single book sits in the trash.
func TestBulkDeleteSeries_KeepsSeriesWhoseOnlyBookIsTrashed(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	store := database.GetGlobalStore()

	trashedOnly, err := store.CreateSeries("Only A Trashed Book", nil)
	require.NoError(t, err)
	genuinelyEmpty, err := store.CreateSeries("Genuinely Empty", nil)
	require.NoError(t, err)

	_, err = store.CreateBook(&database.Book{
		Title:             "Trashed Book",
		SeriesID:          &trashedOnly.ID,
		FilePath:          "/tmp/refguard-trashed.m4b",
		MarkedForDeletion: boolPtr(true),
	})
	require.NoError(t, err)

	w := postJSON(server, "/api/v1/series/bulk-delete", map[string]interface{}{
		"ids": []int{trashedOnly.ID, genuinelyEmpty.ID},
	})
	assert.Equal(t, http.StatusOK, w.Code)

	var wrapper struct {
		Data bulkDeleteResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &wrapper))
	assert.Equal(t, 1, wrapper.Data.Deleted, "only the genuinely empty series may be deleted")
	assert.Equal(t, 1, wrapper.Data.Skipped, "the series holding a trashed book must be skipped")

	survivor, err := store.GetSeriesByID(trashedOnly.ID)
	assert.NoError(t, err)
	require.NotNil(t, survivor, "deleting this series strands its trashed book on a dead series_id")
	assert.Equal(t, "Only A Trashed Book", survivor.Name)

	gone, err := store.GetSeriesByID(genuinelyEmpty.ID)
	assert.NoError(t, err)
	assert.Nil(t, gone, "a series referenced by nothing should still be deleted")
}

// TestBulkDeleteSeries_KeepsSeriesWhoseOnlyBookIsNonPrimary covers the other
// half of the filtered counter: alternate versions of a book are hidden behind
// the primary one, but they still carry the series_id.
func TestBulkDeleteSeries_KeepsSeriesWhoseOnlyBookIsNonPrimary(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	store := database.GetGlobalStore()

	nonPrimaryOnly, err := store.CreateSeries("Only A Duplicate Version", nil)
	require.NoError(t, err)

	_, err = store.CreateBook(&database.Book{
		Title:            "Alternate Version",
		SeriesID:         &nonPrimaryOnly.ID,
		FilePath:         "/tmp/refguard-nonprimary.m4b",
		IsPrimaryVersion: boolPtr(false),
	})
	require.NoError(t, err)

	w := postJSON(server, "/api/v1/series/bulk-delete", map[string]interface{}{
		"ids": []int{nonPrimaryOnly.ID},
	})
	assert.Equal(t, http.StatusOK, w.Code)

	var wrapper struct {
		Data bulkDeleteResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &wrapper))
	assert.Equal(t, 0, wrapper.Data.Deleted)
	assert.Equal(t, 1, wrapper.Data.Skipped)

	survivor, err := store.GetSeriesByID(nonPrimaryOnly.ID)
	assert.NoError(t, err)
	assert.NotNil(t, survivor, "deleting this series strands its non-primary book")
}

// TestDeleteEmptySeries_KeepsSeriesWhoseOnlyBookIsTrashed covers the
// single-series endpoint, which carried the same filtered guard.
func TestDeleteEmptySeries_KeepsSeriesWhoseOnlyBookIsTrashed(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	store := database.GetGlobalStore()

	series, err := store.CreateSeries("Single Endpoint Trashed", nil)
	require.NoError(t, err)
	_, err = store.CreateBook(&database.Book{
		Title:             "Trashed Book Two",
		SeriesID:          &series.ID,
		FilePath:          "/tmp/refguard-single-trashed.m4b",
		MarkedForDeletion: boolPtr(true),
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/series/"+strconv.Itoa(series.ID), nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusConflict, w.Code,
		"a series still referenced by a trashed book must not be deletable")

	survivor, err := store.GetSeriesByID(series.ID)
	assert.NoError(t, err)
	assert.NotNil(t, survivor)
}
