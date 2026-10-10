// file: internal/server/search_busy_retry_test.go
// version: 1.0.0
// guid: 13af3e69-f0fb-4d12-aaec-7d9d1f01dbf0
// last-edited: 2026-10-10

package server

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/querygrammar"
)

func shortBusyWait(t *testing.T) {
	t.Helper()
	saved := searchBusyRetryWait
	searchBusyRetryWait = func() time.Duration { return time.Millisecond }
	t.Cleanup(func() { searchBusyRetryWait = saved })
}

// TestRetryWhileSearchBusy: a background resolve waits out busy slots, up
// to searchBusyRetries tries; a spent budget (TooSlowError) is a fact about
// the filter and fails at once; a cancelled ctx stops the waiting.
func TestRetryWhileSearchBusy(t *testing.T) {
	shortBusyWait(t)
	busy := &querygrammar.BusyError{Slots: 1, Waited: time.Second}

	t.Run("busy twice then ok", func(t *testing.T) {
		calls := 0
		ids, err := retryWhileSearchBusy(context.Background(), func() ([]string, error) {
			calls++
			if calls < 3 {
				return nil, busy
			}
			return []string{"b1"}, nil
		})
		require.NoError(t, err)
		require.Equal(t, []string{"b1"}, ids)
		require.Equal(t, 3, calls)
	})
	t.Run("too slow is not retried", func(t *testing.T) {
		calls := 0
		_, err := retryWhileSearchBusy(context.Background(), func() ([]string, error) {
			calls++
			return nil, &querygrammar.TooSlowError{Budget: time.Second}
		})
		var slow *querygrammar.TooSlowError
		require.ErrorAs(t, err, &slow)
		require.Equal(t, 1, calls)
	})
	t.Run("busy every time gives up after the last try", func(t *testing.T) {
		calls := 0
		_, err := retryWhileSearchBusy(context.Background(), func() ([]string, error) {
			calls++
			return nil, busy
		})
		var b *querygrammar.BusyError
		require.ErrorAs(t, err, &b)
		require.Equal(t, searchBusyRetries, calls)
	})
	t.Run("other errors are not retried", func(t *testing.T) {
		calls := 0
		_, err := retryWhileSearchBusy(context.Background(), func() ([]string, error) {
			calls++
			return nil, errors.New("bad filter")
		})
		require.Error(t, err)
		require.Equal(t, 1, calls)
	})
	t.Run("cancelled ctx stops waiting", func(t *testing.T) {
		searchBusyRetryWait = func() time.Duration { return time.Hour }
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		start := time.Now()
		_, err := retryWhileSearchBusy(ctx, func() ([]string, error) {
			calls++
			cancel()
			return nil, busy
		})
		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, 1, calls)
		require.Less(t, time.Since(start), time.Second)
	})
}

// TestBatchFetchCandidates_BusySlotsIs503: a regex selection resolved while
// every pattern slot is held answers 503 with Retry-After, as the Library and
// Review lists do -- not 400, which would tell the client its filter is wrong.
func TestBatchFetchCandidates_BusySlotsIs503(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	store := s.storeForWiring()
	mfs := metafetch.NewService(store)
	mfs.SetOverrideSources([]metadata.MetadataSource{&countingSource{name: "SrcA"}})
	s.metadataFetchService = mfs
	_, err := store.CreateBook(&database.Book{Title: "Synthetic One", FilePath: "/lib/synthetic/one.m4b"})
	require.NoError(t, err)

	restore := querygrammar.SetPatternSlotsForTesting(1, 10*time.Millisecond)
	defer restore()
	release, err := querygrammar.AcquirePatternSlot(context.Background())
	require.NoError(t, err)

	post := func() *httptest.ResponseRecorder {
		gin.SetMode(gin.TestMode)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/metadata/batch-fetch-candidates",
			bytes.NewBufferString(`{"selection":{"filter":{"field_filters":[{"field":"title","value":"/synthetic/"}]}}}`))
		c.Request.Header.Set("Content-Type", "application/json")
		s.handleBatchFetchCandidates(c)
		return w
	}
	w := post()
	require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
	require.Equal(t, "2", w.Header().Get("Retry-After"))

	release()
	w = post()
	require.NotEqual(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
}
