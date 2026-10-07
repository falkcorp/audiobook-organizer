// file: internal/server/itunes_writeback_requeue_test.go
// version: 1.0.0
// guid: 8d4f2b6a-1c73-4e95-a0b8-5f9e3d7c2a61
// last-edited: 2026-10-07
//
// Handler tests for POST /itunes/writeback/requeue and /requeue-remove.
// Synthetic fixtures only: the library is built in Go and the books are
// invented.

package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/gin-gonic/gin"
)

func requeueTestPID(t *testing.T, s string) [8]byte {
	t.Helper()
	var out [8]byte
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 8 {
		t.Fatalf("bad pid %q", s)
	}
	copy(out[:], b)
	return out
}

// requeueTestServer builds a server whose write target parses to a synthetic
// library:
//
//	bk-b  pid ...c2  in library, title differs  -> update
//	bk-c  pid ...c3  in library, location moved -> update
//	bk-d  pid ...c4  not in library             -> add (never queued)
//	      pid ...c9  in library, no book        -> remove (never queued)
//	bk-l  soft-deleted loser, pid ...c7 in library, tombstoned
func requeueTestServer(t *testing.T, auto bool) (*Server, *itunesservice.WriteBackBatcher) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	track := func(pid, name, loc string) itunes.ITLTrack {
		return itunes.ITLTrack{PersistentID: requeueTestPID(t, pid), Name: name, Album: name, Genre: "Audiobook", Location: loc}
	}
	lib := &itunes.ITLLibrary{Tracks: []itunes.ITLTrack{
		track("00000000000000c2", "Bravo Old", `W:\Books\b.m4b`),
		track("00000000000000c3", "Charlie", `W:\Old\c.m4b`),
		track("00000000000000c7", "Loser", `W:\Books\l.m4b`),
		track("00000000000000c9", "Some Song", `W:\Music\z.mp3`),
	}}
	orig := requeueParseITL
	requeueParseITL = func(string) (*itunes.ITLLibrary, error) { return lib, nil }
	t.Cleanup(func() { requeueParseITL = orig })

	deleted := true
	books := []database.Book{
		{ID: "bk-b", Title: "Bravo"},
		{ID: "bk-c", Title: "Charlie"},
		{ID: "bk-d", Title: "Delta"},
	}
	loser := database.Book{ID: "bk-l", Title: "Loser", MarkedForDeletion: &deleted}
	files := map[string][]database.BookFile{
		"bk-b": {{BookID: "bk-b", Title: "Bravo", ITunesPersistentID: "00000000000000C2", ITunesPath: `W:\Books\b.m4b`}},
		"bk-c": {{BookID: "bk-c", Title: "Charlie", ITunesPersistentID: "00000000000000C3", ITunesPath: `W:\New\c.m4b`}},
		"bk-d": {{BookID: "bk-d", Title: "Delta", ITunesPersistentID: "00000000000000C4", ITunesPath: `W:\Books\d.m4b`}},
		"bk-l": {{BookID: "bk-l", Title: "Loser", ITunesPersistentID: "00000000000000C7", ITunesPath: `W:\Books\l.m4b`}},
	}
	store := &database.MockStore{
		GetAllBooksFullFromFunc: func(afterID string, limit int) ([]database.Book, error) {
			var out []database.Book
			for _, b := range books {
				if b.ID > afterID {
					out = append(out, b)
				}
			}
			return out, nil
		},
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			if id == loser.ID {
				cp := loser
				return &cp, nil
			}
			for i := range books {
				if books[i].ID == id {
					cp := books[i]
					return &cp, nil
				}
			}
			return nil, nil
		},
		GetBookFilesFunc:           func(id string) ([]database.BookFile, error) { return files[id], nil },
		IsExternalIDTombstonedFunc: func(_, id string) (bool, error) { return strings.EqualFold(id, "00000000000000C7"), nil },
	}

	b := itunesservice.NewWriteBackBatcher(time.Hour, itunesservice.WriteBackBatcherConfig{
		AutoWriteBack:       auto,
		ITLWriteBackEnabled: true,
		LibraryWritePath:    "/synthetic/library.itl",
		WriteBackDryRun:     true,
	}, nil)
	t.Cleanup(func() { _ = b.Stop(context.Background()) })
	return &Server{store: store, writeBackBatcher: b}, b
}

func callRequeue(t *testing.T, h gin.HandlerFunc, body string) (int, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/itunes/writeback/requeue", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h(c)
	var raw map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &raw)
	if d, ok := raw["data"].(map[string]any); ok {
		raw = d
	}
	return w.Code, raw
}

func TestWritebackRequeue_DryRunIsTheDefault(t *testing.T) {
	for _, body := range []string{"", "{}", `{"book_ids":[]}`, `{"dry_run":true}`} {
		s, b := requeueTestServer(t, true)
		code, resp := callRequeue(t, s.itunesWritebackRequeueHandler, body)
		if code != http.StatusOK {
			t.Fatalf("body %q: status %d %v", body, code, resp)
		}
		if resp["dry_run"] != true {
			t.Errorf("body %q: dry_run %v, want true", body, resp["dry_run"])
		}
		if resp["would_enqueue"] != float64(2) {
			t.Errorf("body %q: would_enqueue %v, want 2", body, resp["would_enqueue"])
		}
		if got := b.Status().PendingUpdates; got != 0 {
			t.Errorf("body %q: dry run queued %d books", body, got)
		}
	}
}

func TestWritebackRequeue_ExplicitFalseEnqueuesUpdatesOnly(t *testing.T) {
	s, b := requeueTestServer(t, true)
	code, resp := callRequeue(t, s.itunesWritebackRequeueHandler, `{"dry_run":false}`)
	if code != http.StatusOK {
		t.Fatalf("status %d %v", code, resp)
	}
	if resp["enqueued"] != float64(2) {
		t.Fatalf("enqueued %v, want 2", resp["enqueued"])
	}
	for _, id := range []string{"bk-b", "bk-c"} {
		if !b.HasPendingBook(id) {
			t.Errorf("%s not queued", id)
		}
	}
	// The add (bk-d) is never queued; neither is any remove.
	st := b.Status()
	if b.HasPendingBook("bk-d") || st.PendingAdds != 0 || st.PendingRemoves != 0 {
		t.Errorf("add/remove queued: bk-d=%v status=%+v", b.HasPendingBook("bk-d"), st)
	}
	plan := resp["plan"].(map[string]any)
	if plan["ignored_add_tracks"] != float64(1) || plan["ignored_remove_tracks"] != float64(2) {
		t.Errorf("ignored counts add=%v remove=%v, want 1 and 2 (c7 loser + c9)", plan["ignored_add_tracks"], plan["ignored_remove_tracks"])
	}
}

func TestWritebackRequeue_SubsetAndKinds(t *testing.T) {
	s, b := requeueTestServer(t, true)
	code, resp := callRequeue(t, s.itunesWritebackRequeueHandler, `{"dry_run":false,"book_ids":["bk-b","bk-c"],"kinds":["location"]}`)
	if code != http.StatusOK {
		t.Fatalf("status %d %v", code, resp)
	}
	if resp["enqueued"] != float64(1) || !b.HasPendingBook("bk-c") || b.HasPendingBook("bk-b") {
		t.Errorf("location-only subset: enqueued=%v bk-c=%v bk-b=%v", resp["enqueued"], b.HasPendingBook("bk-c"), b.HasPendingBook("bk-b"))
	}

	code, _ = callRequeue(t, s.itunesWritebackRequeueHandler, `{"kinds":["adds"]}`)
	if code != http.StatusBadRequest {
		t.Errorf("unknown kind: status %d, want 400", code)
	}
	code, _ = callRequeue(t, s.itunesWritebackRequeueHandler, `{"dryrun":false}`)
	if code != http.StatusBadRequest {
		t.Errorf("misspelled field: status %d, want 400", code)
	}
}

func TestWritebackRequeue_AutoWriteBackOffIsAConflict(t *testing.T) {
	s, _ := requeueTestServer(t, false)
	code, resp := callRequeue(t, s.itunesWritebackRequeueHandler, `{"dry_run":false}`)
	if code != http.StatusConflict {
		t.Fatalf("status %d %v, want 409", code, resp)
	}
	if code, _ := callRequeue(t, (&Server{}).itunesWritebackRequeueHandler, ""); code != http.StatusServiceUnavailable {
		t.Errorf("no batcher: status %d, want 503", code)
	}
}

func TestWritebackRequeueRemove_ExplicitIDOnly(t *testing.T) {
	s, b := requeueTestServer(t, true)

	// No ids: refused, never "all tombstoned PIDs".
	if code, _ := callRequeue(t, s.itunesWritebackRequeueRemoveHandler, ""); code != http.StatusBadRequest {
		t.Fatalf("no ids: status %d, want 400", code)
	}
	// Dry run by default.
	code, resp := callRequeue(t, s.itunesWritebackRequeueRemoveHandler, `{"book_ids":["bk-l"]}`)
	if code != http.StatusOK || resp["dry_run"] != true || resp["would_enqueue"] != float64(1) {
		t.Fatalf("dry run: %d %v", code, resp)
	}
	if b.Status().PendingRemoves != 0 {
		t.Fatal("dry run queued a remove")
	}
	// A live primary book is refused even when named explicitly.
	code, resp = callRequeue(t, s.itunesWritebackRequeueRemoveHandler, `{"dry_run":false,"book_ids":["bk-b"]}`)
	if code != http.StatusOK || len(resp["enqueued"].([]any)) != 0 {
		t.Fatalf("live book: %d %v", code, resp)
	}
	// Explicit false queues the loser's tombstoned PID, and only it.
	code, resp = callRequeue(t, s.itunesWritebackRequeueRemoveHandler, `{"dry_run":false,"book_ids":["bk-l"]}`)
	if code != http.StatusOK {
		t.Fatalf("status %d %v", code, resp)
	}
	if got := resp["enqueued"].([]any); len(got) != 1 || got[0] != "00000000000000c7" {
		t.Fatalf("enqueued %v", got)
	}
	if !b.IsRemovePending("00000000000000C7") || b.Status().PendingRemoves != 1 {
		t.Errorf("remove not queued: %+v", b.Status())
	}
}
