// file: internal/plugins/deluge/protected_paths_sync_test.go
// version: 1.0.0
// guid: 0de680bf-c4bf-446d-a8cd-8156be0a2c8c
// last-edited: 2026-10-30

package deluge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	delugeclient "github.com/falkcorp/audiobook-organizer/internal/deluge"
)

// fakeTorrentServer is a Deluge endpoint that lists one torrent and counts the
// torrent-list requests it serves; it answers 503 while down is set.
type fakeTorrentServer struct {
	fetches  atomic.Int64
	down     atomic.Bool
	savePath string
}

func (f *fakeTorrentServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if f.down.Load() {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Method string `json:"method"`
		ID     int64  `json:"id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	var result any
	switch req.Method {
	case "auth.login":
		result = true
	case "core.get_torrents_status":
		f.fetches.Add(1)
		result = map[string]any{"0123456789abcdef0123456789abcdef01234567": map[string]any{
			"hash": "0123456789abcdef0123456789abcdef01234567", "save_path": f.savePath,
		}}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"id": req.ID, "result": result, "error": nil})
}

// The scheduled sync has to fetch from Deluge even though the cache's TTL has
// not run out. It used to call IsProtected(""), which is answered before the
// cache looks at the TTL, so it fetched nothing.
func TestRunProtectedPathsSync_FetchesFromDeluge(t *testing.T) {
	fake := &fakeTorrentServer{savePath: "/seeding/Gotham"}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	client, err := delugeclient.New(srv.URL, "test-password")
	if err != nil {
		t.Fatal(err)
	}
	cache := delugeclient.NewProtectedPathCache(client, nil)
	if !cache.IsProtected("/seeding/Gotham/a.m4b") { // the lazy first load
		t.Fatal("fixture: the first load did not list the torrent")
	}
	if got := fake.fetches.Load(); got != 1 {
		t.Fatalf("fixture: %d fetches after the first load, want 1", got)
	}

	p := New(client, cache, nil)
	if err := p.runProtectedPathsSync(context.Background(), nil, &centralizationTestReporter{}); err != nil {
		t.Fatalf("runProtectedPathsSync: %v", err)
	}
	if got := fake.fetches.Load(); got != 2 {
		t.Errorf("%d fetches after the sync, want 2: the sync did not fetch", got)
	}

	// A Deluge that is down is an error of the op, not a silent success.
	fake.down.Store(true)
	if err := p.runProtectedPathsSync(context.Background(), nil, &centralizationTestReporter{}); err == nil {
		t.Error("runProtectedPathsSync against a down Deluge returned nil")
	}
}
