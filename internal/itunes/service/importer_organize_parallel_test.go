// file: internal/itunes/service/importer_organize_parallel_test.go
// version: 1.0.2
// guid: 3f9a1c7e-2b6d-4a58-9e0f-7c1d5b8a4e2f
// last-edited: 2026-07-16

package itunesservice

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// concurrencyTrackingOrganizer is a BookOrganizer test double for
// CONC-10 that also implements the (unexported, structurally-matched)
// targetPather interface organizeDestKey type-asserts for
// (GenerateTargetPath / GenerateTargetDirPath), so organizeImportedBooks'
// destination-collision guard is actually exercised by the test below.
//
// It derives the "destination" purely from book.Title (mirroring
// organizer.Organizer's real title/author-driven naming pattern) and
// tracks, per destination key, how many OrganizeBook calls are
// concurrently in flight. If organizeImportedBooks' per-destination
// locking were missing or broken, two books sharing a title would be
// able to run OrganizeBook concurrently and maxInFlight for that key
// would exceed 1 — exactly the silent-corruption class of bug the
// lock exists to prevent.
type concurrencyTrackingOrganizer struct {
	mu          sync.Mutex
	inFlight    map[string]int
	maxInFlight map[string]int
	calls       int
}

func newConcurrencyTrackingOrganizer() *concurrencyTrackingOrganizer {
	return &concurrencyTrackingOrganizer{
		inFlight:    map[string]int{},
		maxInFlight: map[string]int{},
	}
}

func (o *concurrencyTrackingOrganizer) destFor(book *database.Book) string {
	return "/organized/" + book.Title + ".m4b"
}

func (o *concurrencyTrackingOrganizer) OrganizeBook(book *database.Book) (string, string, error) {
	key := o.destFor(book)

	o.mu.Lock()
	o.calls++
	o.inFlight[key]++
	if o.inFlight[key] > o.maxInFlight[key] {
		o.maxInFlight[key] = o.inFlight[key]
	}
	o.mu.Unlock()

	// Hold the "critical section" open long enough that, absent the
	// destLocks guard, a second goroutine racing on the same key would
	// almost certainly overlap with this one.
	time.Sleep(5 * time.Millisecond)

	o.mu.Lock()
	o.inFlight[key]--
	o.mu.Unlock()

	return key, "copy", nil
}

func (o *concurrencyTrackingOrganizer) OrganizeBookDirectory(book *database.Book, segmentPaths []string) (string, map[string]string, error) {
	return "", nil, fmt.Errorf("not exercised by this test: all books are single-file")
}

func (o *concurrencyTrackingOrganizer) GenerateTargetPath(book *database.Book) (string, error) {
	return o.destFor(book), nil
}

func (o *concurrencyTrackingOrganizer) GenerateTargetDirPath(book *database.Book) (string, error) {
	return "/organized/" + book.Title, nil
}

var _ BookOrganizer = (*concurrencyTrackingOrganizer)(nil)

// buildOrganizeFixture returns N "imported" books, split across
// distinctTitles distinct titles (so books sharing a title exercise the
// destLocks collision guard) plus a mock store wired to answer the
// GetAllBooks/GetBookFiles/UpdateBook calls organizeImportedBooks makes.
func buildOrganizeFixture(t *testing.T, n, distinctTitles int) ([]database.Book, *dbmocks.MockStore) {
	t.Helper()

	imported := "imported"
	src := "/mnt/itunes/Library.xml"

	books := make([]database.Book, n)
	for i := 0; i < n; i++ {
		title := fmt.Sprintf("Title-%d", i%distinctTitles)
		books[i] = database.Book{
			ID:                 fmt.Sprintf("book-%d", i),
			Title:              title,
			FilePath:           fmt.Sprintf("/mnt/itunes/imported/%d.m4b", i),
			LibraryState:       &imported,
			ITunesImportSource: &src,
		}
	}

	core := make([]database.BookCore, len(books))
	for i := range books {
		core[i] = books[i].Core()
	}

	m := dbmocks.NewMockStore(t)
	m.EXPECT().GetAllBooksCore(0, 0).Return(core, nil)
	for i := range books {
		id := books[i].ID
		b := books[i]
		// organizeImportedBooks hydrates each Core-filtered candidate via
		// GetBookByID before organizing/writing back (DUAL-adjacent: the
		// destination path is derived from Author/Series, heavy fields not
		// present on BookCore).
		m.EXPECT().GetBookByID(id).Return(&b, nil)
		// organizeDestKey and organizeOneBook each call GetBookFiles once
		// per book (two calls total) — the mock permits unlimited calls
		// by default (no .Once()), matching that.
		m.EXPECT().GetBookFiles(id).Return(nil, nil)
		m.EXPECT().UpdateBook(id, mock.Anything).Return(&database.Book{}, nil)
	}
	return books, m
}

// TestOrganizeImportedBooks_ParallelMatchesSerial_NoDestinationRace runs
// organizeImportedBooks twice — once forced sequential
// (organizeConcurrencyOverride=1) and once forced parallel
// (organizeConcurrencyOverride=8, comfortably more than runtime.NumCPU()
// on most CI runners so the worker pool actually overlaps workers) —
// against fixtures with colliding titles, and asserts:
//  1. both runs organize every book (same result set, order-independent);
//  2. the parallel run's organizer never sees more than one concurrent
//     OrganizeBook call for the same destination key (the destLocks
//     collision guard actually serializes colliding books).
//
// Run with `go test -race` per the task brief; the concurrent map/int
// accesses inside concurrencyTrackingOrganizer plus organizeImportedBooks'
// own shared counters make this a meaningful race-detector target.
func TestOrganizeImportedBooks_ParallelMatchesSerial_NoDestinationRace(t *testing.T) {
	const totalBooks = 12
	const distinctTitles = 4 // 3 books share each title → guaranteed collisions

	// --- Sequential run -----------------------------------------------
	seqBooks, seqStore := buildOrganizeFixture(t, totalBooks, distinctTitles)
	seqOrg := newConcurrencyTrackingOrganizer()
	seqImp := &Importer{
		store:                       seqStore,
		organizerFactory:            func() BookOrganizer { return seqOrg },
		organizeConcurrencyOverride: 1,
	}
	seqStatus := &itunesImportStatus{}
	seqLog := logger.New("test-seq")

	seqImp.organizeImportedBooks(context.Background(), seqStatus, seqLog)

	require.Equal(t, totalBooks, seqOrg.calls, "sequential run must organize every imported book")
	for key, max := range seqOrg.maxInFlight {
		require.LessOrEqualf(t, max, 1, "sequential run must never see concurrent OrganizeBook calls for %s", key)
	}
	for _, b := range seqBooks {
		require.NotNil(t, b.LibraryState)
	}

	// --- Parallel run ---------------------------------------------------
	_, parStore := buildOrganizeFixture(t, totalBooks, distinctTitles)
	parOrg := newConcurrencyTrackingOrganizer()
	parImp := &Importer{
		store:                       parStore,
		organizerFactory:            func() BookOrganizer { return parOrg },
		organizeConcurrencyOverride: 8,
	}
	parStatus := &itunesImportStatus{}
	parLog := logger.New("test-par")

	parImp.organizeImportedBooks(context.Background(), parStatus, parLog)

	require.Equal(t, totalBooks, parOrg.calls, "parallel run must organize every imported book — same result set as serial")

	// The core correctness assertion: even with Concurrency=8 (>> the
	// number of distinct destinations), no destination ever had more than
	// one OrganizeBook call in flight at once.
	require.Len(t, parOrg.maxInFlight, distinctTitles, "expected one destination key per distinct title")
	for key, max := range parOrg.maxInFlight {
		require.LessOrEqualf(t, max, 1, "destLocks must serialize concurrent OrganizeBook calls for colliding destination %s (data-race / corruption risk otherwise)", key)
	}

	// Same failure/success counts as the sequential run (order-independent
	// "same result set" check from the task brief).
	require.Equal(t, 0, seqStatus.Failed, "sequential run: no book should fail to organize in this fixture")
	require.Equal(t, 0, parStatus.Failed, "parallel run: no book should fail to organize in this fixture")
}

// TestOrganizeImportedBooks_EmptyList exercises the zero-books path (no
// imported books) for both concurrency settings — RunItems must be a
// no-op and must not panic on an empty slice.
func TestOrganizeImportedBooks_EmptyList(t *testing.T) {
	m := dbmocks.NewMockStore(t)
	m.EXPECT().GetAllBooksCore(0, 0).Return(nil, nil)

	imp := &Importer{store: m, organizeConcurrencyOverride: 4}
	status := &itunesImportStatus{}
	log := logger.New("test-empty")

	imp.organizeImportedBooks(context.Background(), status, log)

	require.Equal(t, 0, status.Failed)
}
