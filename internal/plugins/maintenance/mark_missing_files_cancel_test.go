// file: internal/plugins/maintenance/mark_missing_files_cancel_test.go
// version: 1.0.0
// guid: 5c8e2a47-3d19-4b6f-a0e4-7f12b9d36c85
// last-edited: 2026-10-14

package maintenance

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/stretchr/testify/require"
)

// touchReporter is a fakeReporter that counts TouchLiveness stamps (the 5-minute
// watchdog's clock) and can run a hook on the Nth one. RunItems stamps liveness
// through UpdateProgress, not TouchLiveness, so every count here is one this
// op's own code made: 2 + one per books page in the plan phase, then one per 64
// rows of a book's pre-write stat pass.
type touchReporter struct {
	fakeReporter
	touches atomic.Int64
	onTouch func(n int64)
}

func (r *touchReporter) TouchLiveness() {
	n := r.touches.Add(1)
	if r.onTouch != nil {
		r.onTouch(n)
	}
}

var _ registry.LivenessToucher = (*touchReporter)(nil)

// goneRows builds n rows of one book whose files never exist, plus the matching
// full records so the apply phase can rehydrate them.
func goneRows(t *testing.T, bookID string, n int) *markFakeStore {
	t.Helper()
	dir := t.TempDir()
	st := &markFakeStore{full: map[string][]database.BookFile{}}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("f%04d", i)
		path := filepath.Join(dir, id+".mp3") // never written
		st.cores = append(st.cores, database.BookFileCore{ID: id, BookID: bookID, FilePath: path})
		st.full[bookID] = append(st.full[bookID], database.BookFile{ID: id, BookID: bookID, FilePath: path})
	}
	return st
}

func TestMarkMissing_PreCanceledRunDoesNothing(t *testing.T) {
	st := goneRows(t, "b1", 3)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := planMarkMissingFiles(ctx, st, nil, markMissingParams{Apply: true}, &touchReporter{})
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, st.updates, "a canceled run must write nothing")
	require.Zero(t, st.getFilesCalls.Load())
}

// The books paging loop is a store read per page that runs before the first
// RunItems item. Cancel must stop it between pages, not after every page.
func TestMarkMissing_CancelStopsBookPaging(t *testing.T) {
	st := goneRows(t, "b1", 1)
	for i := 0; i < 3*bookPageSize; i++ {
		st.books = append(st.books, database.BookCore{ID: fmt.Sprintf("book%05d", i)})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var pages atomic.Int64
	st.onBooksPage = func(offset int) {
		if pages.Add(1) == 2 {
			cancel()
		}
	}

	_, err := planMarkMissingFiles(ctx, st, nil, markMissingParams{}, &touchReporter{})
	require.ErrorIs(t, err, context.Canceled)
	require.EqualValues(t, 2, pages.Load(), "paging must stop after the page that saw the cancel")
}

// The watchdog budget is 5 minutes. The plan phase's own loops must stamp it:
// once before and after the one unbounded read of every book_file row, and once
// per books page.
func TestMarkMissing_PlanPhaseStampsLiveness(t *testing.T) {
	st := goneRows(t, "b1", 1)
	for i := 0; i < 2*bookPageSize+1; i++ { // 3 pages
		st.books = append(st.books, database.BookCore{ID: fmt.Sprintf("book%05d", i)})
	}
	rep := &touchReporter{}
	_, err := planMarkMissingFiles(context.Background(), st, nil, markMissingParams{}, rep)
	require.NoError(t, err)
	require.EqualValues(t, 2+3, rep.touches.Load(), "2 around the row read + 1 per books page")
}

// A book can hold ~1,500 rows and the pre-write stat pass is the longest stretch
// between two stand-down renewals, so it stamps every bookFileStatLivenessEvery
// rows (the first included).
func TestMarkMissing_ApplyStatPassStampsLiveness(t *testing.T) {
	rows := 2*bookFileStatLivenessEvery + 2 // beats at 0, 64, 128
	st := goneRows(t, "b1", rows)
	rep := &touchReporter{}
	plan, err := planMarkMissingFiles(context.Background(), st, nil, markMissingParams{Apply: true}, rep)
	require.NoError(t, err)
	require.Equal(t, rows, plan.MarkedMissing)
	// 3 plan-phase stamps (2 around the row read + 1 books page), 3 stat-pass
	// beats (rows 0, 64, 128) and one per written row (writeBookFileBatch).
	require.EqualValues(t, 3+3+rows, rep.touches.Load())
}

// Cancel during a book's pre-write stat pass must end the pass at the next beat
// and skip the rehydrate and the write, instead of statting the rest of a
// ~1,500-row book first. The cancel is raised on the first stat-pass beat (the
// 4th stamp: 2 around the row read, 1 for the single books page).
func TestMarkMissing_CancelStopsApplyStatPass(t *testing.T) {
	st := goneRows(t, "b1", 2*bookFileStatLivenessEvery+2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rep := &touchReporter{}
	rep.onTouch = func(n int64) {
		if n == 4 {
			cancel()
		}
	}

	_, err := planMarkMissingFiles(ctx, st, nil, markMissingParams{Apply: true}, rep)
	_ = err // RunItems may or may not surface the cancel for the last item; the writes are the contract
	require.Empty(t, st.updates, "nothing may be written after Cancel")
	require.Zero(t, st.getFilesCalls.Load(), "the stat pass must stop at the cancel, before the rehydrate")
	require.EqualValues(t, 4, rep.touches.Load(), "no stat-pass beat after the cancel")
}

// What the survivor inherited from the retired maintenance.fix-book-file-paths
// job (04-P14d): its gate, its 4h budget and both library capabilities. The
// undeclared defaults were any scan.trigger caller and 120 minutes.
func TestMarkMissingFilesDef_CarriesRetiredJobBehaviour(t *testing.T) {
	def := (&Plugin{}).markMissingFilesDef()
	require.Equal(t, "maintenance.mark-missing-files", def.ID)
	require.Equal(t, []auth.Permission{auth.PermSettingsManage}, def.Permissions)
	require.GreaterOrEqual(t, def.Timeout, 4*time.Hour)
	require.Contains(t, def.Capabilities, registry.CapLibraryRead)
	require.Contains(t, def.Capabilities, registry.CapLibraryWrite)
}
