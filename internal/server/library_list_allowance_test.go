// file: internal/server/library_list_allowance_test.go
// version: 1.0.0
// guid: 6c0f6b0e-2f4b-4a53-9d0e-5a3f1c7d8e21
// last-edited: 2026-10-10

package server

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	audiobookspkg "github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/querygrammar"
	"github.com/falkcorp/audiobook-organizer/internal/searchcache"
)

// TestBuildAudiobookListResponse_OneSlotPerRequest: the Library list runs two
// scans (the page and the total) under a regex filter. buildAudiobookListResponse
// must wrap them in ONE shared pattern allowance, so the request takes exactly
// one slot, not one per scan, and has given it back by the time it returns.
// Without audiobooks.WithSharedSearchAllowance in that function each scan takes
// its own slot and the delta below is 2.
func TestBuildAudiobookListResponse_OneSlotPerRequest(t *testing.T) {
	fx := newSearchCacheServer(t, 60, 8, searchcache.Config{})
	restore := querygrammar.SetPatternSlotsForTesting(1, 10*time.Millisecond)
	defer restore()

	f := audiobookspkg.ListFilters{FieldFilters: []audiobookspkg.FieldFilter{{Field: "title", Value: "/alpha/"}}}
	before := querygrammar.PatternSlotAcquisitions()
	resp, err := fx.srv.buildAudiobookListResponse(context.Background(), 50, 0, "", nil, nil, f, false)
	require.NoError(t, err)
	require.Equal(t, uint64(1), querygrammar.PatternSlotAcquisitions()-before,
		"the page and the total of one request share one pattern slot")
	count, ok := resp["count"].(int)
	require.True(t, ok, "the response carries a total")
	require.Positive(t, count, "the regex matched some of the fixture's titles")

	// The only slot is back: the next request is not refused.
	release, err := querygrammar.AcquirePatternSlot(context.Background())
	require.NoError(t, err, "the request returned its slot")
	release()
}
