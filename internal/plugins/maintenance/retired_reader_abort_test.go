// file: internal/plugins/maintenance/retired_reader_abort_test.go
// version: 1.0.0
// guid: 7c2e91d4-3b58-4a06-9e1f-d84a5b60c2f7
// last-edited: 2026-10-25

package maintenance

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// storeWithoutRetiredReader is a store that cannot read retired rows: it
// satisfies the repoint and recover ops' narrow store interfaces and nothing
// else, so database.AsCapability finds no database.RetiredBookFileReader on it.
// It holds a missing row, so an op that went on past its lookup would try to
// repair it; every write is counted.
type storeWithoutRetiredReader struct {
	writes atomic.Int32
}

func (s *storeWithoutRetiredReader) GetAllBookFilesCore() ([]database.BookFileCore, error) {
	return []database.BookFileCore{{ID: "f1", BookID: "b1", FilePath: "/lib/missing/gone.mp3", FileSize: 10}}, nil
}

func (s *storeWithoutRetiredReader) GetAllBooksCore(int, int) ([]database.BookCore, error) {
	return nil, nil
}

func (s *storeWithoutRetiredReader) GetBookFiles(string) ([]database.BookFile, error) {
	return nil, nil
}

func (s *storeWithoutRetiredReader) UpdateBookFiles(context.Context, []*database.BookFile, func(int, bool)) (int, error) {
	s.writes.Add(1)
	return 0, errors.New("a store that cannot read retired rows must not be written to")
}

// Both ops build the set of claimed paths from every row, retired ones included,
// and a live-only list would let a repair land on a file a retired version holds.
// On a store that cannot read retired rows they stop with
// database.ErrRetiredReaderUnavailable before they plan or write anything. The
// other fakes in this package model a store with nothing retired (noRetiredRows),
// so none of them reaches this branch.
func TestRetiredReaderAbort_RepointStopsBeforePlanningOrWriting(t *testing.T) {
	store := &storeWithoutRetiredReader{}
	plan, err := planMissingFileRepoint(context.Background(), store, nil, missingFileRepointParams{Apply: true}, &fakeReporter{})
	require.ErrorIs(t, err, database.ErrRetiredReaderUnavailable)
	assert.Contains(t, err.Error(), "load book files")
	assert.Equal(t, repointPlan{}, plan, "nothing may be planned from a claim set that left retired rows out")
	assert.Zero(t, store.writes.Load())
}

func TestRetiredReaderAbort_RecoverStopsBeforePlanningOrWriting(t *testing.T) {
	store := &storeWithoutRetiredReader{}
	plan, err := planRecoverMissingFiles(context.Background(), store, nil, t.TempDir(), recoverMissingParams{Apply: true}, &fakeReporter{})
	require.ErrorIs(t, err, database.ErrRetiredReaderUnavailable)
	assert.Contains(t, err.Error(), "load book files")
	assert.Equal(t, recoverPlan{}, plan, "nothing may be planned from a claim set that left retired rows out")
	assert.Zero(t, store.writes.Load())
}
