// file: internal/server/handlers/abs/own_folder_duration_test.go
// version: 1.0.0
// guid: 351283f9-d5b7-4792-be3b-be7929a7d86a
// last-edited: 2026-09-25

package abs_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// A book whose rows span its own folder and other folders (the iTunes copy,
// old chapter files) is summed from its own-folder rows only. Summing every
// copy is what read "Awaken Online: Flame" as 43.7 h instead of 17.6 h.
const ownFolderBookID = "01OWNFOLDER000000000000000"

func addOwnFolderBook(v *visFixture) {
	now := time.UnixMilli(1785370000000)
	primary, state := true, "organized"
	dur := 9999
	row := func(id, path string, track, sec int) database.BookFile {
		return database.BookFile{ID: id, BookID: ownFolderBookID, FilePath: path, TrackNumber: track,
			Duration: sec, FileSize: 1000, Format: "m4b"}
	}
	v.lib.addBook(&database.Book{ID: ownFolderBookID, Title: "Own Folder", FilePath: "/srv/lib/Author/Own Folder",
		IsPrimaryVersion: &primary, LibraryState: &state, CreatedAt: &now, UpdatedAt: &now, Duration: &dur},
		[]database.BookFile{
			row("own1", "/srv/lib/Author/Own Folder/01.m4b", 1, 600),
			row("own2", "/srv/lib/Author/Own Folder/CD2/02.m4b", 2, 400),
			row("itunes", "/srv/books/itunes/Author/Own Folder/01.m4b", 1, 1000),
			row("oldch", "/srv/old/Own Folder/ch01.mp3", 3, 2000),
		}, nil)
}

func TestItem_DurationCountsOnlyOwnFolderRows(t *testing.T) {
	v := newVisFixture(t)
	addOwnFolderBook(v)
	sync, err := v.lib.MintOrGetSyncID(ownFolderBookID)
	if err != nil {
		t.Fatal(err)
	}
	item := v.get(t, "/api/items/"+sync+"?expanded=1")
	media := obj(t, item["media"])
	if d, _ := media["duration"].(float64); d != 1000 {
		t.Fatalf("media.duration = %v, want 1000 (own-folder rows 600+400; strays excluded)", media["duration"])
	}
	if files, _ := media["audioFiles"].([]any); len(files) != 2 {
		t.Fatalf("audioFiles = %d, want 2: the track list must be the same rows the duration sums", len(files))
	}
}

// The progress path (durationBoundsForBook) must use the same rows as the
// item: a finish at the own-folder total must mark the book finished. Summing
// the strays would put the end at 4000 s and leave it unfinished.
func TestSessionLocalAll_FinishAtOwnFolderTotalFinishesBook(t *testing.T) {
	v := newVisFixture(t)
	addOwnFolderBook(v)
	sync, err := v.lib.MintOrGetSyncID(ownFolderBookID)
	if err != nil {
		t.Fatal(err)
	}
	code, body := v.h.doAny(t, request{method: http.MethodPost, path: "/api/session/local-all", headers: bearer(v.tok),
		body: map[string]any{"sessions": []map[string]any{{"id": "own-end", "userId": "u1", "libraryItemId": sync, "currentTime": 1000}}}})
	if code != http.StatusOK {
		t.Fatalf("local-all = %d %v", code, body)
	}
	st, _ := v.lib.GetUserBookState("u1", ownFolderBookID)
	if st == nil || st.Status != database.UserBookStatusFinished {
		t.Fatalf("finishing at the own-folder total did not finish the book: %+v", st)
	}
}
