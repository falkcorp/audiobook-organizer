// file: internal/plugins/maintenance/ident_index_test.go
// version: 1.0.0
// guid: 484bb658-22bc-46f4-b1b8-80b4bc6c33ea
// last-edited: 2026-10-11

package maintenance

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

type fakeIdentRebuilder struct {
	rep        database.IdentIndexReport
	st         database.IdentRebuildStats
	err        error
	rebuilt    bool
	verifyRuns int
}

func (f *fakeIdentRebuilder) VerifyIdentIndex(context.Context) (database.IdentIndexReport, error) {
	f.verifyRuns++
	return f.rep, f.err
}

func (f *fakeIdentRebuilder) RebuildIdentIndex(context.Context) (database.IdentRebuildStats, error) {
	f.rebuilt = true
	return f.st, f.err
}

func TestIdentIndexOp_PreviewWritesNothingAndReports(t *testing.T) {
	f := &fakeIdentRebuilder{rep: database.IdentIndexReport{Ready: true, Books: 10, RowsWrong: 2, BadKeys: 1, CounterDrift: 3}}
	rep := &fakeReporter{}
	require.NoError(t, previewIdentIndex(context.Background(), f, rep))
	require.False(t, f.rebuilt, "a preview never rebuilds")
	require.Len(t, rep.logs, 1)
	require.Contains(t, rep.logs[0], "rows_wrong=2")
	require.Contains(t, rep.logs[0], "counter_drift=3")
	require.Contains(t, rep.logs[0], "dry_run=false")
}

func TestIdentIndexOp_RebuildFailsOnUndecodableAndError(t *testing.T) {
	f := &fakeIdentRebuilder{st: database.IdentRebuildStats{Books: 4, Undecodable: 1}}
	err := rebuildIdentIndex(context.Background(), f, &fakeReporter{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot be decoded")

	boom := errors.New("boom")
	err = rebuildIdentIndex(context.Background(), &fakeIdentRebuilder{err: boom}, &fakeReporter{})
	require.ErrorIs(t, err, boom)
}

// TestIdentIndexOp_RealStore: the real store satisfies the capability, a
// clean index previews as clean, and a rebuild over it fixes nothing.
func TestIdentIndexOp_RealStore(t *testing.T) {
	var _ identIndexRebuilder = (*database.PebbleStore)(nil)
	s, err := database.NewPebbleStoreInMemory("identop_" + ulid.Make().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.WaitForWarmup()
	for _, title := range []string{"Title 000001", "Title 000002", "Title 000003"} {
		_, err := s.CreateBook(&database.Book{Title: title, FilePath: "/lib/op/" + title + ".m4b", Format: "m4b"})
		require.NoError(t, err)
	}
	rep := &fakeReporter{}
	require.NoError(t, previewIdentIndex(context.Background(), s, rep))
	require.True(t, strings.Contains(rep.logs[0], "matches its 3 books"), rep.logs[0])
	rep = &fakeReporter{}
	require.NoError(t, rebuildIdentIndex(context.Background(), s, rep))
	require.Contains(t, rep.logs[0], "books=3 rows_fixed=0")
}
