// file: internal/server/handlers/abs/own_folder_duration_test.go
// version: 2.0.0
// guid: 351283f9-d5b7-4792-be3b-be7929a7d86a
// last-edited: 2026-09-25

package abs_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// A book whose rows span its own folder and other folders neither lists nor
// sums an out-of-folder row that is a copy of a present own-folder row (the
// iTunes copy, here hash-equal to own1). An out-of-folder row that is not a
// copy (a merge's moved row, "moved") is real content and counts.
const ownFolderBookID = "01OWNFOLDER000000000000000"

func addOwnFolderBook(v *visFixture) {
	now := time.UnixMilli(1785370000000)
	primary, state := true, "organized"
	dur := 9999
	row := func(id, path, hash string, track, sec int) database.BookFile {
		return database.BookFile{ID: id, BookID: ownFolderBookID, FilePath: path, TrackNumber: track,
			Duration: sec, FileSize: 1000, FileHash: hash, Format: "m4b"}
	}
	v.lib.addBook(&database.Book{ID: ownFolderBookID, Title: "Own Folder", FilePath: "/srv/lib/Author/Own Folder",
		IsPrimaryVersion: &primary, LibraryState: &state, CreatedAt: &now, UpdatedAt: &now, Duration: &dur},
		[]database.BookFile{
			row("own1", "/srv/lib/Author/Own Folder/01.m4b", "h-own1", 1, 600),
			row("own2", "/srv/lib/Author/Own Folder/CD2/02.m4b", "h-own2", 2, 400),
			row("itunes", "/srv/books/itunes/Author/Own Folder/Track 01.m4b", "h-own1", 1, 600),
			row("moved", "/srv/lib/Author/Loser/03.m4b", "h-moved", 3, 2000),
		}, nil)
}

func TestItem_DurationSkipsOnlyOutOfFolderCopies(t *testing.T) {
	v := newVisFixture(t)
	addOwnFolderBook(v)
	sync, err := v.lib.MintOrGetSyncID(ownFolderBookID)
	if err != nil {
		t.Fatal(err)
	}
	item := v.get(t, "/api/items/"+sync+"?expanded=1")
	media := obj(t, item["media"])
	if d, _ := media["duration"].(float64); d != 3000 {
		t.Fatalf("media.duration = %v, want 3000 (600+400 own + 2000 moved; the iTunes copy excluded)", media["duration"])
	}
	if files, _ := media["audioFiles"].([]any); len(files) != 3 {
		t.Fatalf("audioFiles = %d, want 2: the track list must be the same rows the duration sums", len(files))
	}
}

// The progress path (durationBoundsForBook) must use the same rows as the
// item: a finish at the counted total must mark the book finished. Summing
// the iTunes copy too would put the end at 3600 s and leave it unfinished.
func TestSessionLocalAll_FinishAtCountedTotalFinishesBook(t *testing.T) {
	v := newVisFixture(t)
	addOwnFolderBook(v)
	sync, err := v.lib.MintOrGetSyncID(ownFolderBookID)
	if err != nil {
		t.Fatal(err)
	}
	code, body := v.h.doAny(t, request{method: http.MethodPost, path: "/api/session/local-all", headers: bearer(v.tok),
		body: map[string]any{"sessions": []map[string]any{{"id": "own-end", "userId": "u1", "libraryItemId": sync, "currentTime": 3000}}}})
	if code != http.StatusOK {
		t.Fatalf("local-all = %d %v", code, body)
	}
	st, _ := v.lib.GetUserBookState("u1", ownFolderBookID)
	if st == nil || st.Status != database.UserBookStatusFinished {
		t.Fatalf("finishing at the counted total did not finish the book: %+v", st)
	}
}
