// file: internal/server/purge_soft_deleted_op_test.go
// version: 1.0.0
// guid: 6a1c8e53-0d72-4f9b-b3e4-9c5a2d7f1e08
// last-edited: 2026-10-22

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	audiobookspkg "github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// purgeOpReporter records what the op reports: progress, log lines and the
// stored result. Its methods run from purge worker goroutines, so it locks.
type purgeOpReporter struct {
	opsregistry.Reporter // nil: any method not overridden below panics
	mu                   sync.Mutex
	progress             [][2]int
	logs                 []string
	result               any
}

func (r *purgeOpReporter) OpID() string { return "op-purge-test" }
func (r *purgeOpReporter) UpdateProgress(done, total int, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress = append(r.progress, [2]int{done, total})
	return nil
}
func (r *purgeOpReporter) Log(_ slog.Level, message string, _ ...slog.Attr) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, message)
	return nil
}
func (r *purgeOpReporter) SetResult(v any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.result = v
	return nil
}

func purgeOpWorld(t *testing.T, trashed ...string) (*Server, *database.PebbleStore) {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	st.WaitForWarmup()
	t.Cleanup(func() { _ = st.Close() })
	for _, title := range trashed {
		marked := true
		at := time.Now().Add(-time.Hour)
		_, err := st.CreateBook(&database.Book{ID: title, Title: title, MarkedForDeletion: &marked, MarkedForDeletionAt: &at})
		require.NoError(t, err)
	}
	return &Server{store: st, audiobookService: audiobookspkg.NewAudiobookService(st)}, st
}

func runPurgeOp(t *testing.T, s *Server, params string) (*purgeOpReporter, error) {
	t.Helper()
	rep := &purgeOpReporter{}
	err := s.runPurgeSoftDeletedOp(context.Background(), json.RawMessage(params), rep)
	return rep, err
}

func purgeResultOf(t *testing.T, rep *purgeOpReporter) *audiobookspkg.PurgeResult {
	t.Helper()
	res, ok := rep.result.(*audiobookspkg.PurgeResult)
	require.True(t, ok, "the op must store a *PurgeResult, got %T", rep.result)
	return res
}

func purgeBookGone(t *testing.T, st *database.PebbleStore, id string) bool {
	t.Helper()
	b, err := st.GetBookByID(id)
	require.NoError(t, err)
	return b == nil
}

// An enqueue that does not name a mode is a preview (owner rule 2026-09-25):
// nothing is purged, and the stored result says so.
func TestPurgeSoftDeletedOp_DefaultsToPreview(t *testing.T) {
	s, st := purgeOpWorld(t, "a", "b")

	rep, err := runPurgeOp(t, s, `{}`)
	require.NoError(t, err)
	res := purgeResultOf(t, rep)
	assert.True(t, res.DryRun)
	assert.Equal(t, 2, res.Attempted)
	assert.Equal(t, 2, res.Purgeable)
	assert.Zero(t, res.Purged)
	assert.False(t, purgeBookGone(t, st, "a"))
	assert.False(t, purgeBookGone(t, st, "b"))
}

// dry_run=false purges, stores the full breakdown as the op result, and logs
// the one-line summary naming every count.
func TestPurgeSoftDeletedOp_LivePurgeStoresResultAndLogsSummary(t *testing.T) {
	s, st := purgeOpWorld(t, "a", "b")
	require.NoError(t, st.CreateBookFile(&database.BookFile{BookID: "b", FilePath: "/x/b.m4b", Format: "m4b"}))

	rep, err := runPurgeOp(t, s, `{"dry_run":false}`)
	require.NoError(t, err)
	res := purgeResultOf(t, rep)
	assert.False(t, res.DryRun)
	assert.Equal(t, 1, res.Purged)
	assert.Equal(t, 1, res.SkippedOwnsFiles)
	assert.Equal(t, []string{"b"}, res.SkippedOwnsFilesIDs)
	assert.True(t, purgeBookGone(t, st, "a"))
	assert.False(t, purgeBookGone(t, st, "b"))

	require.NotEmpty(t, rep.logs)
	line := rep.logs[0]
	assert.True(t, strings.HasPrefix(line, "Purged 1 of 2 soft-deleted books"), line)
	assert.Contains(t, line, "kept 1: still own book_file rows")
	assert.Contains(t, line, "undo of a combine still possible")

	require.NotEmpty(t, rep.progress)
	assert.Equal(t, [2]int{2, 2}, rep.progress[len(rep.progress)-1])
}

func TestPurgeSoftDeletedOp_RejectsConflictingModeSpellings(t *testing.T) {
	s, st := purgeOpWorld(t, "a")
	_, err := runPurgeOp(t, s, `{"dry_run":true,"dryRun":false}`)
	require.Error(t, err)
	assert.False(t, purgeBookGone(t, st, "a"), "a refused run purges nothing")
}

// The stored result caps its id lists (prod's owns-files list alone is ~50k
// ids) and says so; the counts stay exact.
func TestPurgeResult_CappedKeepsCountsAndMarksTruncation(t *testing.T) {
	r := &audiobookspkg.PurgeResult{SkippedOwnsFiles: 500}
	for i := 0; i < 500; i++ {
		r.SkippedOwnsFilesIDs = append(r.SkippedOwnsFilesIDs, fmt.Sprintf("id%d", i))
	}
	c := r.Capped(purgeResultCap)
	assert.Len(t, c.SkippedOwnsFilesIDs, purgeResultCap)
	assert.True(t, c.Truncated)
	assert.Equal(t, 500, c.SkippedOwnsFiles)
	assert.Len(t, r.SkippedOwnsFilesIDs, 500, "the original is not modified")
	assert.False(t, (&audiobookspkg.PurgeResult{SkippedOwnsFilesIDs: []string{"x"}}).Capped(5).Truncated)
}
