// file: internal/quarantine/auto_quarantine_errors_test.go
// version: 1.0.0
// guid: 3af135eb-9bc7-4793-982e-1920f18299b2
// last-edited: 2026-10-03

package quarantine

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// autoQuarantineStore implements only what AutoQuarantineFailedScans reads.
// The embedded nil Store panics on anything else, so a pass that reached
// QuarantineBook (which needs many more methods) would fail the test loudly.
type autoQuarantineStore struct {
	Store
	books   []database.BookCore
	listErr error
	counts  map[string]int
	readErr map[string]error
}

func (s *autoQuarantineStore) GetAllBooksCore(_, _ int) ([]database.BookCore, error) {
	return s.books, s.listErr
}

func (s *autoQuarantineStore) GetScanFailCount(pathHash string) (int, error) {
	return s.counts[pathHash], s.readErr[pathHash]
}

type autoQuarantineCounters struct{ list, read, move int64 }

func snapshotAutoQuarantine() autoQuarantineCounters {
	return autoQuarantineCounters{
		autoQuarantineListErrCount.Load(), autoQuarantineReadErrCount.Load(), autoQuarantineMoveErrCount.Load(),
	}
}

func (b autoQuarantineCounters) delta() autoQuarantineCounters {
	n := snapshotAutoQuarantine()
	return autoQuarantineCounters{n.list - b.list, n.read - b.read, n.move - b.move}
}

// TestAutoQuarantine_ReadErrorIsCountedNotTreatedAsZero: a counter read that
// fails used to be "n, _ :=" -- a zero count, so a store that could not be
// read silently quarantined nothing. Each failed read must be counted, and the
// pass must carry on with the books it can read.
func TestAutoQuarantine_ReadErrorIsCountedNotTreatedAsZero(t *testing.T) {
	store := &autoQuarantineStore{
		books: []database.BookCore{
			{ID: "b-err", FilePath: "/lib/a.mp3"},
			{ID: "b-ok", FilePath: "/lib/b.mp3"},
		},
		counts:  map[string]int{database.ScanFailKey("/lib/b.mp3"): 1}, // under threshold
		readErr: map[string]error{database.ScanFailKey("/lib/a.mp3"): errors.New("pebble: closed")},
	}
	before := snapshotAutoQuarantine()

	NewQuarantineService(store, &config.Config{}, nil).AutoQuarantineFailedScans()

	require.Equal(t, autoQuarantineCounters{read: 1}, before.delta())
}

// TestAutoQuarantine_ListErrorIsCounted: a failed listing skips the whole
// pass, which used to be a bare return.
func TestAutoQuarantine_ListErrorIsCounted(t *testing.T) {
	store := &autoQuarantineStore{listErr: errors.New("boom")}
	before := snapshotAutoQuarantine()

	NewQuarantineService(store, &config.Config{}, nil).AutoQuarantineFailedScans()

	require.Equal(t, autoQuarantineCounters{list: 1}, before.delta())
}
