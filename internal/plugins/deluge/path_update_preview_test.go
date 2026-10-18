// file: internal/plugins/deluge/path_update_preview_test.go
// version: 1.0.0
// guid: f7ea2f7c-4454-4eb8-881b-09016a583879
// last-edited: 2026-10-18

package deluge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	delugeclient "github.com/falkcorp/audiobook-organizer/internal/deluge"
)

// moveStorageDeluge is a Deluge Web UI that accepts a login and counts the
// core.move_storage calls it receives.
type moveStorageDeluge struct {
	mu    sync.Mutex
	moves int
}

func (d *moveStorageDeluge) moveCalls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.moves
}

func (d *moveStorageDeluge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string `json:"method"`
		ID     int64  `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var result any = true
	if req.Method == "core.move_storage" {
		d.mu.Lock()
		d.moves++
		d.mu.Unlock()
		result = nil
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"id": req.ID, "result": result, "error": nil})
}

// progressRecorder keeps the last progress message (the op's summary).
type progressRecorder struct {
	centralizationTestReporter
	mu   sync.Mutex
	last string
}

func (r *progressRecorder) UpdateProgress(_, _ int, msg string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last = msg
	return nil
}

func (r *progressRecorder) lastMessage() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

// TestPreviewBehaviour_PathUpdate runs the registered op with the params the
// /api/v3/ops start path would queue for each mode and counts the
// core.move_storage calls that reach Deluge: none for a preview, at least one
// for a live run.
func TestPreviewBehaviour_PathUpdate(t *testing.T) {
	orig := config.AppConfig.DelugeMoveEnabled
	config.AppConfig.DelugeMoveEnabled = true
	t.Cleanup(func() { config.AppConfig.DelugeMoveEnabled = orig })

	store := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, FilePath: "/library/Example Author/Example Book/part1.mp3"}, nil
		},
		GetBookVersionsByBookIDFunc: func(bookID string) ([]database.BookVersion, error) {
			return []database.BookVersion{{
				ID: "v1", BookID: bookID, Status: database.BookVersionStatusActive,
				TorrentHash: "0123456789abcdef0123456789abcdef01234567",
			}}, nil
		},
	}

	for _, tc := range []struct {
		mode      string
		wantMoves func(int) bool
		wantMsg   string
	}{
		{"preview", func(n int) bool { return n == 0 }, "DRY RUN: 1 move_storage planned, 0 updated"},
		{"live", func(n int) bool { return n >= 1 }, "APPLY: 0 move_storage planned, 1 updated"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			fd := &moveStorageDeluge{}
			srv := httptest.NewServer(fd)
			t.Cleanup(srv.Close)
			client, err := delugeclient.New(srv.URL, "test-password")
			if err != nil {
				t.Fatal(err)
			}
			p := &Plugin{client: client, store: store}
			def := p.pathUpdateDef()

			params, err := def.Preview.Resolve(json.RawMessage(`{"book_id":"b1"}`), tc.mode)
			if err != nil {
				t.Fatalf("Resolve(%s): %v", tc.mode, err)
			}
			rep := &progressRecorder{}
			if err := def.Run(context.Background(), params, rep); err != nil {
				t.Fatalf("Run(%s) with %s: %v", tc.mode, params, err)
			}
			if got := fd.moveCalls(); !tc.wantMoves(got) {
				t.Fatalf("%s run made %d core.move_storage calls (params %s)", tc.mode, got, params)
			}
			if got := rep.lastMessage(); got == "" || !strings.Contains(got, tc.wantMsg) {
				t.Fatalf("%s final progress = %q, want it to contain %q", tc.mode, got, tc.wantMsg)
			}
		})
	}
}

// An omitted mode resolves to a preview, and dry_run=false decodes as live.
func TestPathUpdate_ModeDefaults(t *testing.T) {
	def := (&Plugin{}).pathUpdateDef()
	raw, err := def.Preview.Resolve(json.RawMessage(`{"book_id":"b1"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["dry_run"] != true || m["dryRun"] != true {
		t.Fatalf("an empty mode must resolve to a preview, got %s", raw)
	}
	var args pathUpdateParams
	if err := json.Unmarshal([]byte(`{"book_id":"b1","dry_run":false}`), &args); err != nil || args.DryRun == nil || *args.DryRun {
		t.Fatalf("dry_run=false must decode as live: %+v, %v", args, err)
	}
}
