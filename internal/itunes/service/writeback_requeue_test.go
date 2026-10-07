// file: internal/itunes/service/writeback_requeue_test.go
// version: 1.1.0
// guid: 3e9b5d71-6a2c-4f08-8b4e-d1c7a5f2e9b3
// last-edited: 2026-10-07
//
// Tests for the write-back requeue planners and the checked enqueue methods.
// Synthetic fixtures only: libraries are built in Go, books are invented.

package itunesservice

import (
	"context"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
)

func pidBytes(t *testing.T, s string) [8]byte {
	t.Helper()
	var out [8]byte
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 8 {
		t.Fatalf("bad pid %q", s)
	}
	copy(out[:], b)
	return out
}

func fixtureTrack(t *testing.T, pid, name, loc string) itunes.ITLTrack {
	return itunes.ITLTrack{PersistentID: pidBytes(t, pid), Name: name, Album: name, Genre: "Audiobook", Location: loc}
}

func boolp(v bool) *bool { return &v }

// requeueFixture is a small synthetic library and DB:
//
//	bk-a  pid ...a1  in library, unchanged
//	bk-b  pid ...a2  in library, title differs (metadata)
//	bk-c  pid ...a3  in library, location differs
//	bk-d  pid ...a4  NOT in library (a rebuild "add")
//	bk-e  pid ...a5  in library, differs, but non-primary (never scanned)
//	         pid ...a9  in library, no book claims it (a rebuild "remove")
func requeueFixture(t *testing.T) (*database.MockStore, *itunes.ITLLibrary) {
	t.Helper()
	lib := &itunes.ITLLibrary{Tracks: []itunes.ITLTrack{
		fixtureTrack(t, "00000000000000a1", "Alpha", `W:\Books\a.m4b`),
		fixtureTrack(t, "00000000000000a2", "Bravo Old", `W:\Books\b.m4b`),
		fixtureTrack(t, "00000000000000a3", "Charlie", `W:\Old\c.m4b`),
		fixtureTrack(t, "00000000000000a5", "Echo Old", `W:\Books\e.m4b`),
		fixtureTrack(t, "00000000000000a9", "Unclaimed Song", `W:\Music\z.mp3`),
	}}
	books := []database.Book{
		{ID: "bk-a", Title: "Alpha"},
		{ID: "bk-b", Title: "Bravo"},
		{ID: "bk-c", Title: "Charlie"},
		{ID: "bk-d", Title: "Delta"},
		{ID: "bk-e", Title: "Echo", IsPrimaryVersion: boolp(false)},
	}
	files := map[string][]database.BookFile{
		"bk-a": {{BookID: "bk-a", Title: "Alpha", ITunesPersistentID: "00000000000000A1", ITunesPath: `W:\Books\a.m4b`}},
		"bk-b": {{BookID: "bk-b", Title: "Bravo", ITunesPersistentID: "00000000000000A2", ITunesPath: `W:\Books\b.m4b`}},
		"bk-c": {{BookID: "bk-c", Title: "Charlie", ITunesPersistentID: "00000000000000A3", ITunesPath: `W:\New\c.m4b`}},
		"bk-d": {{BookID: "bk-d", Title: "Delta", ITunesPersistentID: "00000000000000A4", ITunesPath: `W:\Books\d.m4b`}},
		"bk-e": {{BookID: "bk-e", Title: "Echo", ITunesPersistentID: "00000000000000A5", ITunesPath: `W:\Books\e.m4b`}},
	}
	byID := map[string]*database.Book{}
	for i := range books {
		byID[books[i].ID] = &books[i]
	}
	store := &database.MockStore{
		GetAllBooksFullFromFunc: func(afterID string, limit int) ([]database.Book, error) {
			var out []database.Book
			for _, b := range books {
				if b.ID > afterID {
					out = append(out, b)
				}
			}
			if len(out) > limit {
				out = out[:limit]
			}
			return out, nil
		},
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			if b, ok := byID[id]; ok {
				cp := *b
				return &cp, nil
			}
			return nil, nil
		},
		GetBookFilesFunc: func(id string) ([]database.BookFile, error) { return files[id], nil },
	}
	return store, lib
}

func TestPlanRequeue_SelectsOnlyUpdatesOfTracksInTheLibrary(t *testing.T) {
	store, lib := requeueFixture(t)
	plan, err := PlanRequeue(context.Background(), store, lib, RequeueOptions{Metadata: true, Location: true})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"bk-b", "bk-c"}; !reflect.DeepEqual(plan.SelectedBookIDs, want) {
		t.Fatalf("selected %v, want %v", plan.SelectedBookIDs, want)
	}
	if plan.BooksScanned != 4 {
		t.Errorf("books_scanned %d, want 4 (non-primary excluded)", plan.BooksScanned)
	}
	if plan.IgnoredAddTracks != 1 {
		t.Errorf("ignored_add_tracks %d, want 1 (bk-d)", plan.IgnoredAddTracks)
	}
	// a5 (non-primary book) and a9 (no book) are unclaimed.
	if plan.IgnoredRemoveTracks == nil || *plan.IgnoredRemoveTracks != 2 {
		t.Errorf("ignored_remove_tracks %v, want 2", plan.IgnoredRemoveTracks)
	}
	if plan.TrackMetadataChanges != 1 || plan.TrackLocationChanges != 1 {
		t.Errorf("track changes meta=%d loc=%d, want 1/1", plan.TrackMetadataChanges, plan.TrackLocationChanges)
	}
	if len(plan.Sample) != 2 || plan.Sample[0].BookID != "bk-b" {
		t.Fatalf("sample %+v", plan.Sample)
	}
	md := plan.Sample[0].Tracks[0].Metadata
	if md == nil || md.From.Name != "Bravo Old" || md.To.Name != "Bravo" {
		t.Errorf("bk-b metadata diff %+v", md)
	}
	loc := plan.Sample[1].Tracks[0].Location
	if loc == nil || loc.From != `W:\Old\c.m4b` || loc.To != `W:\New\c.m4b` {
		t.Errorf("bk-c location diff %+v", loc)
	}
}

func TestPlanRequeue_KindsFilterSelection(t *testing.T) {
	store, lib := requeueFixture(t)
	plan, err := PlanRequeue(context.Background(), store, lib, RequeueOptions{Location: true})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"bk-c"}; !reflect.DeepEqual(plan.SelectedBookIDs, want) {
		t.Fatalf("location-only selected %v, want %v", plan.SelectedBookIDs, want)
	}
	// Book-level counts ignore kinds; track counts cover selected books only.
	if plan.BooksWithMetadataChanges != 1 || plan.TrackMetadataChanges != 0 {
		t.Errorf("meta books=%d tracks=%d, want 1/0", plan.BooksWithMetadataChanges, plan.TrackMetadataChanges)
	}
	if _, err := PlanRequeue(context.Background(), store, lib, RequeueOptions{}); err == nil {
		t.Error("no kinds: want error")
	}
}

func TestPlanRequeue_Subset(t *testing.T) {
	store, lib := requeueFixture(t)
	plan, err := PlanRequeue(context.Background(), store, lib, RequeueOptions{
		BookIDs:  []string{"bk-c", "bk-a", "bk-e", "missing", "bk-c"},
		Metadata: true, Location: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"bk-c"}; !reflect.DeepEqual(plan.SelectedBookIDs, want) {
		t.Fatalf("subset selected %v, want %v", plan.SelectedBookIDs, want)
	}
	if plan.BooksScanned != 2 {
		t.Errorf("books_scanned %d, want 2", plan.BooksScanned)
	}
	if !reflect.DeepEqual(plan.NotFound, []string{"missing"}) || !reflect.DeepEqual(plan.SkippedNonPrimary, []string{"bk-e"}) {
		t.Errorf("not_found=%v skipped_non_primary=%v", plan.NotFound, plan.SkippedNonPrimary)
	}
	if plan.IgnoredRemoveTracks != nil {
		t.Errorf("subset plan must not report ignored removes, got %d", *plan.IgnoredRemoveTracks)
	}
}

// The requeue selection and the flush must agree: a book PlanRequeue does not
// select produces no update in planBookWrite for any track in the library.
func TestPlanRequeue_AgreesWithFlushPlanner(t *testing.T) {
	store, lib := requeueFixture(t)
	tracks := TracksByPID(lib)
	plan, err := PlanRequeue(context.Background(), store, lib, RequeueOptions{Metadata: true, Location: true})
	if err != nil {
		t.Fatal(err)
	}
	selected := map[string]bool{}
	for _, id := range plan.SelectedBookIDs {
		selected[id] = true
	}
	for _, id := range []string{"bk-a", "bk-b", "bk-c", "bk-d"} {
		b, _ := store.GetBookByID(id)
		p := planBookWrite(store, b, tracks)
		inLib := 0
		for _, ch := range p.Changes {
			if ch.Current != nil {
				inLib++
			}
		}
		if (inLib > 0) != selected[id] {
			t.Errorf("%s: flush would write %d library tracks, selected=%v", id, inLib, selected[id])
		}
	}
}

func TestEnqueueBooks_ReportsWhatItDid(t *testing.T) {
	b := NewWriteBackBatcher(time.Hour, disabledFlushCfg(), nil)
	n, err := b.EnqueueBooks([]string{"x", "y", "x"})
	if err != nil || n != 2 {
		t.Fatalf("EnqueueBooks = %d, %v; want 2, nil", n, err)
	}
	n, err = b.EnqueueBooks([]string{"y", "z"})
	if err != nil || n != 1 {
		t.Fatalf("second EnqueueBooks = %d, %v; want 1, nil", n, err)
	}
	if got := b.Status().PendingUpdates; got != 3 {
		t.Errorf("pending updates %d, want 3", got)
	}
	_ = b.Stop(context.Background())
	if _, err := b.EnqueueBooks([]string{"w"}); !errors.Is(err, ErrBatcherStopped) {
		t.Errorf("after stop: %v, want ErrBatcherStopped", err)
	}

	off := NewWriteBackBatcher(time.Hour, autoOffCfg(), nil)
	t.Cleanup(func() { _ = off.Stop(context.Background()) })
	if _, err := off.EnqueueBooks([]string{"x"}); !errors.Is(err, ErrAutoWriteBackDisabled) {
		t.Errorf("auto off: %v, want ErrAutoWriteBackDisabled", err)
	}
	if err := off.EnqueueRemoveChecked("ab"); !errors.Is(err, ErrAutoWriteBackDisabled) {
		t.Errorf("remove, auto off: %v, want ErrAutoWriteBackDisabled", err)
	}
}

func TestEnqueueRemoveChecked_Held(t *testing.T) {
	b := NewWriteBackBatcher(time.Hour, disabledFlushCfg(), nil)
	t.Cleanup(func() { _ = b.Stop(context.Background()) })
	b.mu.Lock()
	b.held["00000000000000a1"] = time.Now()
	b.mu.Unlock()
	if err := b.EnqueueRemoveChecked("00000000000000A1"); !errors.Is(err, ErrRemoveHeld) {
		t.Fatalf("held: %v, want ErrRemoveHeld", err)
	}
	if err := b.EnqueueRemoveChecked("00000000000000A2"); err != nil {
		t.Fatal(err)
	}
	if !b.IsRemovePending("00000000000000a2") || b.IsRemovePending("00000000000000a1") {
		t.Error("pending removes wrong")
	}
}

// removeFixture: loser bk-l (soft-deleted) holds pids b1 (eligible), b2 (not
// tombstoned), b3 (on a live book's file), b4 (not in library); live bk-w holds
// b5 at book level and the loser's file also lists it. b3 is duplicated: the
// loser's book_file row and live bk-o's row both carry it.
func removeFixture(t *testing.T) (*database.MockStore, *itunes.ITLLibrary) {
	t.Helper()
	lib := &itunes.ITLLibrary{Tracks: []itunes.ITLTrack{
		fixtureTrack(t, "00000000000000b1", "Loser One", `W:\L\1.m4b`),
		fixtureTrack(t, "00000000000000b2", "Loser Two", `W:\L\2.m4b`),
		fixtureTrack(t, "00000000000000b3", "Loser Three", `W:\L\3.m4b`),
		fixtureTrack(t, "00000000000000b5", "Winner", `W:\W\5.m4b`),
	}}
	pid5 := "00000000000000B5"
	books := map[string]*database.Book{
		"bk-l":    {ID: "bk-l", Title: "Loser", MarkedForDeletion: boolp(true)},
		"bk-w":    {ID: "bk-w", Title: "Winner", ITunesPersistentID: &pid5},
		"bk-live": {ID: "bk-live", Title: "Live primary"},
		"bk-o":    {ID: "bk-o", Title: "Other live"},
	}
	tomb := map[string]bool{"00000000000000B1": true, "00000000000000B3": true, "00000000000000B4": true, "00000000000000B5": true}
	store := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) { return books[id], nil },
		GetBookFilesFunc: func(id string) ([]database.BookFile, error) {
			if id != "bk-l" {
				return nil, nil
			}
			var out []database.BookFile
			for _, p := range []string{"00000000000000B1", "00000000000000B2", "00000000000000B3", "00000000000000B4", pid5} {
				out = append(out, database.BookFile{BookID: "bk-l", ITunesPersistentID: p})
			}
			return out, nil
		},
		IsExternalIDTombstonedFunc: func(source, id string) (bool, error) { return source == "itunes" && tomb[id], nil },
		// The loser's own row comes first, as the book_file_pid index could
		// return it; bk-o's duplicate row must still be found.
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) {
			var out []database.BookFileCore
			for _, p := range []string{"00000000000000B1", "00000000000000B2", "00000000000000B3", "00000000000000B4", pid5} {
				out = append(out, database.BookFileCore{BookID: "bk-l", ITunesPersistentID: p})
			}
			return append(out, database.BookFileCore{BookID: "bk-o", ITunesPersistentID: "00000000000000b3"}), nil
		},
		ListBooksByITunesPIDFunc: func(limit, offset int) ([]database.Book, error) {
			return []database.Book{*books["bk-w"]}, nil
		},
	}
	return store, lib
}

func TestPlanRemoveRequeue_OnlyExplicitEligibleLoserPIDs(t *testing.T) {
	store, lib := removeFixture(t)
	b := NewWriteBackBatcher(time.Hour, disabledFlushCfg(), nil)
	t.Cleanup(func() { _ = b.Stop(context.Background()) })

	plan, err := b.PlanRemoveRequeue(store, lib, []string{"bk-l", "bk-live", "nope"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"00000000000000b1"}; !reflect.DeepEqual(plan.EligiblePIDs, want) {
		t.Fatalf("eligible %v, want %v", plan.EligiblePIDs, want)
	}
	reasons := map[string]string{}
	for _, p := range plan.Books[0].PIDs {
		reasons[p.PID] = p.Reason
	}
	for pid, want := range map[string]string{
		"00000000000000b2": "not tombstoned",
		"00000000000000b3": "live book bk-o",
		"00000000000000b4": "not in the library",
		"00000000000000b5": "live book bk-w",
	} {
		if !strings.Contains(reasons[pid], want) {
			t.Errorf("%s reason %q, want it to mention %q", pid, reasons[pid], want)
		}
	}
	if plan.Books[1].Refused == "" || plan.Books[2].Refused == "" {
		t.Errorf("live primary and missing book must be refused: %+v", plan.Books[1:])
	}

	if _, err := b.PlanRemoveRequeue(store, lib, nil); !errors.Is(err, ErrRemoveRequeueRequest) {
		t.Errorf("no ids: %v", err)
	}
	if _, err := b.PlanRemoveRequeue(store, lib, []string{"1", "2", "3", "4", "5", "6"}); !errors.Is(err, ErrRemoveRequeueRequest) {
		t.Errorf("six ids: %v", err)
	}
}
