// file: internal/server/handlers/abs/cover_local_test.go
// version: 1.0.0
// guid: 3f6c2a91-8d4e-4b7a-9e15-c0a7d42b8e63
// last-edited: 2026-09-25

package abs_test

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// ABS-COVER-LOCAL (2026-09-25): a book whose organizer cover is a LOCAL cover
// (cover_url=/api/v1/covers/local/<name>) showed a cover in the web UI and none
// in ABS: media.coverPath was null and GET /api/items/:id/cover answered 404,
// because the ABS resolver only looked at {root}/covers/<bookID>.<ext>.

// setCoverURL sets the fake book's CoverURL under the fake's lock.
func setCoverURL(t *testing.T, seed *oracleSeed, bookID, url string) {
	t.Helper()
	seed.lib.mu.Lock()
	defer seed.lib.mu.Unlock()
	b := seed.lib.books[bookID]
	if b == nil {
		t.Fatalf("no book %s in the fake", bookID)
	}
	b.CoverURL = &url
}

// writeCoverAt writes the minimal PNG writeCover uses at root/sub/name.
func writeCoverAt(t *testing.T, seed *oracleSeed, sub, name string) {
	t.Helper()
	dir := filepath.Join(seed.root, sub)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	png := []byte{
		0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
		0, 0, 0, 13, 'I', 'H', 'D', 'R', 0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0,
		0x1f, 0x15, 0xc4, 0x89,
	}
	if err := os.WriteFile(filepath.Join(dir, name), png, 0o644); err != nil {
		t.Fatalf("write cover: %v", err)
	}
}

// itemCoverPath reads media.coverPath from GET /api/items/:id (nil when null).
func itemCoverPath(t *testing.T, h *harness, tok, syncID string) any {
	t.Helper()
	w, body := h.do(t, request{method: http.MethodGet, path: "/api/items/" + syncID, headers: bearer(tok)})
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/items/%s = %d", syncID, w.Code)
	}
	media, ok := body["media"].(map[string]any)
	if !ok {
		t.Fatalf("item has no media object: %#v", body["media"])
	}
	return media["coverPath"]
}

func TestCover_LocalCoverURLResolves(t *testing.T) {
	cases := []struct {
		name string
		sub  string // directory under the root the file lives in
		file string
	}{
		// Embedded art extracted at import: {root}/.covers/<sha256>.jpg.
		{"extracted hash cover under .covers", ".covers", "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08.jpg"},
		// A merged/relinked book whose cover_url names ANOTHER book's id-named file.
		{"another book's id-named cover under covers", "covers", "01OTHERBOOKCOVER0000000000.jpg"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, seed, tok := newBrowseHarness(t)
			writeCoverAt(t, seed, tc.sub, tc.file)
			setCoverURL(t, seed, seed.multiID, "/api/v1/covers/local/"+tc.file)
			syncID := mustSyncID(t, seed, seed.multiID)

			w, _ := h.do(t, request{method: http.MethodGet, path: "/api/items/" + syncID + "/cover"})
			if w.Code != http.StatusOK {
				t.Fatalf("cover for a local cover_url: got %d want 200", w.Code)
			}
			want := filepath.Join(seed.root, tc.sub, tc.file)
			if got := itemCoverPath(t, h, tok, syncID); got != want {
				t.Fatalf("media.coverPath = %v, want %s", got, want)
			}
		})
	}
}

// TestCover_BookIDCoverWinsOverCoverURL: the id-named cover stays first, so a
// book with both serves the one the cover writers keep current.
func TestCover_BookIDCoverWinsOverCoverURL(t *testing.T) {
	h, seed, tok := newBrowseHarness(t)
	writeCover(t, seed, seed.multiID)
	writeCoverAt(t, seed, ".covers", "abc.jpg")
	setCoverURL(t, seed, seed.multiID, "/api/v1/covers/local/abc.jpg")
	syncID := mustSyncID(t, seed, seed.multiID)

	want := filepath.Join(seed.root, "covers", seed.multiID+".png")
	if got := itemCoverPath(t, h, tok, syncID); got != want {
		t.Fatalf("media.coverPath = %v, want the id-named cover %s", got, want)
	}
}

// TestCover_UnusableCoverURLIs404: a traversal-shaped, remote or missing local
// cover_url never reaches a file.
func TestCover_UnusableCoverURLIs404(t *testing.T) {
	for _, url := range []string{
		"/api/v1/covers/local/../secret.jpg",
		"/api/v1/covers/local/..",
		"/api/v1/covers/local/sub/abc.jpg",
		"/api/v1/covers/local/sub\\abc.jpg",
		"/api/v1/covers/local/",
		"/api/v1/covers/local/missing.jpg",
		"https://example.com/cover.jpg",
		"/api/v1/covers/proxy?url=x",
	} {
		t.Run(url, func(t *testing.T) {
			h, seed, tok := newBrowseHarness(t)
			// A real file one level up and in a subdirectory, so a resolver that
			// followed the name would find something.
			writeCoverAt(t, seed, "", "secret.jpg")
			writeCoverAt(t, seed, filepath.Join("covers", "sub"), "abc.jpg")
			setCoverURL(t, seed, seed.multiID, url)
			syncID := mustSyncID(t, seed, seed.multiID)

			w, _ := h.do(t, request{method: http.MethodGet, path: "/api/items/" + syncID + "/cover"})
			if w.Code != http.StatusNotFound {
				t.Fatalf("cover for cover_url %q: got %d want 404", url, w.Code)
			}
			if got := itemCoverPath(t, h, tok, syncID); got != nil {
				t.Fatalf("media.coverPath = %v, want null", got)
			}
		})
	}
}
