// file: internal/deluge/swap_notify_dir_test.go
// version: 1.0.0
// guid: 77a49ce3-e2ba-4ba9-8abf-c142385b4ace
// last-edited: 2026-10-30
//
// A version swap must send Deluge the folder each torrent's files live in:
// the book folder for the entering version, its .versions/{id} slot for the
// leaving one.

package deluge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

type moveCall struct {
	IDs  []string
	Dest string
}

// newMoveRecorder installs a fake Deluge as the global client and records the
// arguments of every core.move_storage it receives.
func newMoveRecorder(t *testing.T) func() []moveCall {
	t.Helper()
	var mu sync.Mutex
	var calls []moveCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "core.move_storage" && len(req.Params) == 2 {
			var mc moveCall
			if ids, ok := req.Params[0].([]any); ok {
				for _, id := range ids {
					mc.IDs = append(mc.IDs, id.(string))
				}
			}
			mc.Dest, _ = req.Params[1].(string)
			mu.Lock()
			calls = append(calls, mc)
			mu.Unlock()
		}
		_ = json.NewEncoder(w).Encode(rpcResponse{ID: req.ID, Result: json.RawMessage(`true`)})
	}))
	t.Cleanup(srv.Close)
	client, err := New(srv.URL, "deluge")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(SetGlobalClientForTest(client))
	return func() []moveCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]moveCall(nil), calls...)
	}
}

func TestNotifyDelugeAfterVersionSwap_SendsEachVersionsFolder(t *testing.T) {
	calls := newMoveRecorder(t)
	setMoveEnabled(t, true)

	bookDir := "/library/Batman Year One"
	st := &guardBookStore{book: &database.Book{ID: "b1", FilePath: filepath.Join(bookDir, "book.m4b")}}
	leaving := &database.BookVersion{ID: "v-robin", BookID: "b1", TorrentHash: "aaaa"}
	entering := &database.BookVersion{ID: "v-batgirl", BookID: "b1", TorrentHash: "bbbb"}

	NotifyDelugeAfterVersionSwap(st, leaving, entering, filepath.Join(bookDir, "book.m4b"))

	got := calls()
	if len(got) != 2 {
		t.Fatalf("want 2 move_storage calls, got %d: %+v", len(got), got)
	}
	want := map[string]string{
		"bbbb": bookDir,
		"aaaa": filepath.Join(bookDir, ".versions", "v-robin"),
	}
	for _, c := range got {
		if len(c.IDs) != 1 || want[c.IDs[0]] != c.Dest {
			t.Errorf("torrent %v sent to %q, want %q", c.IDs, c.Dest, want[c.IDs[0]])
		}
		delete(want, c.IDs[0])
	}
	if len(want) != 0 {
		t.Errorf("no move_storage seen for %v", want)
	}
}

func TestNotifyDelugeMoveStorage_FileSendsParentAndToDirSendsFolderAsGiven(t *testing.T) {
	calls := newMoveRecorder(t)
	setMoveEnabled(t, true)

	NotifyDelugeMoveStorage("aaaa", "/library/Gotham/book.m4b")
	NotifyDelugeMoveStorageToDir("bbbb", "/library/Gotham/.versions/v1")

	got := calls()
	if len(got) != 2 {
		t.Fatalf("want 2 calls, got %+v", got)
	}
	if got[0].Dest != "/library/Gotham" {
		t.Errorf("file variant sent %q, want /library/Gotham", got[0].Dest)
	}
	if got[1].Dest != "/library/Gotham/.versions/v1" {
		t.Errorf("dir variant sent %q, want the folder unchanged", got[1].Dest)
	}
}
