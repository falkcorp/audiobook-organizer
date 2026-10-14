// file: internal/server/author_scan_op_test.go
// version: 1.1.0
// guid: 5b0d7e32-9c41-4a86-b3f5-71e2a9c04d68
// last-edited: 2026-10-14

package server

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// maintenance.author-dedup-scan (merged into dedup.author-scan, 04-P14a) once
// discarded GetAllAuthorBookCounts' error, ranked every group against an empty
// map and reported success. The test moved with the survivor: a failed count must
// fail the run.
func TestAuthorScanOp_FailsWhenBookCountsFail(t *testing.T) {
	countErr := errors.New("count backend unavailable")
	s := &Server{store: &database.MockStore{
		GetAllAuthorsFunc: func() ([]database.Author, error) {
			return []database.Author{{ID: 1, Name: "Alice Example"}, {ID: 2, Name: "Alice  Example"}}, nil
		},
		GetAllAuthorBookCountsFunc: func() (map[int]int, error) { return nil, countErr },
	}}
	reg := w3decodeReg(t)
	require.NoError(t, s.RegisterAuthorDedupScanOp(reg))
	def, ok := reg.Def("dedup.author-scan")
	require.True(t, ok)

	err := def.Run(context.Background(), json.RawMessage(`{}`), &catalogTestReporter{})
	require.ErrorIs(t, err, countErr)
}

// A successful scan caches the groups the duplicates page reads back. This
// replaces the success test that went with the deleted maintenance copy.
func TestAuthorScanOp_SucceedsAndCachesGroups(t *testing.T) {
	s := &Server{
		store: &database.MockStore{
			GetAllAuthorsFunc: func() ([]database.Author, error) {
				return []database.Author{{ID: 1, Name: "Alice Example"}, {ID: 2, Name: "Alice  Example"}}, nil
			},
			GetAllAuthorBookCountsFunc: func() (map[int]int, error) { return map[int]int{1: 3, 2: 1}, nil },
		},
		dedupCache: cache.NewWithLimit[gin.H]("dedup-test", time.Hour, 10),
	}
	reg := w3decodeReg(t)
	require.NoError(t, s.RegisterAuthorDedupScanOp(reg))
	def, ok := reg.Def("dedup.author-scan")
	require.True(t, ok)

	require.NoError(t, def.Run(context.Background(), nil, &catalogTestReporter{}))

	got, ok := s.dedupCache.Get("author-duplicates")
	require.True(t, ok, "the scan must cache its groups under author-duplicates")
	require.Equal(t, 1, got["count"])
}
