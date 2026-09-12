// file: internal/metadata/cover_replace_test.go
// version: 1.0.0
// guid: 6f1d2c84-93a7-4e0b-b5c2-8d47e19a3f60
// last-edited: 2026-09-12

package metadata

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func seedCover(t *testing.T, dir, name, body string) string {
	t.Helper()
	covers := filepath.Join(dir, "covers")
	if err := os.MkdirAll(covers, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(covers, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func noTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(filepath.Join(dir, "covers"))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// An explicitly applied cover replaces the one on disk. DownloadCoverArt's
// short-circuit (return the existing file unfetched) is kept for its callers.
func TestReplaceCoverArt_ReplacesTheExistingCover(t *testing.T) {
	dir := t.TempDir()
	old := seedCover(t, dir, "book1.jpg", "old")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("new-image"))
	}))
	defer srv.Close()

	got, err := downloadCoverArtWithClient(testClient(), srv.URL+"/c.png", dir, "book1")
	if err != nil || got != old {
		t.Fatalf("download path: got %q, %v; want the existing %q unfetched", got, err, old)
	}

	got, err = replaceCoverArtWithClient(testClient(), srv.URL+"/c.png", dir, "book1")
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	want := filepath.Join(dir, "covers", "book1.png")
	if got != want {
		t.Fatalf("replace path = %q, want %q", got, want)
	}
	if b, _ := os.ReadFile(got); string(b) != "new-image" {
		t.Errorf("new cover content = %q", b)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("old %s still present; it would shadow the new cover (err=%v)", filepath.Base(old), err)
	}
	if served := findExistingCover(filepath.Join(dir, "covers"), "book1"); served != want {
		t.Errorf("served cover = %q, want %q", served, want)
	}
	noTempFiles(t, dir)
}

// Any failure leaves the old cover exactly as it was.
func TestReplaceCoverArt_FailureKeepsTheOldCover(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"server error": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/jpeg")
			w.WriteHeader(http.StatusInternalServerError)
		},
		"not an image": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>"))
		},
		"truncated body": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set("Content-Length", "1000")
			_, _ = w.Write([]byte("partial"))
		},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			old := seedCover(t, dir, "book1.jpg", "old")
			srv := httptest.NewServer(h)
			defer srv.Close()

			if _, err := replaceCoverArtWithClient(testClient(), srv.URL+"/c.jpg", dir, "book1"); err == nil {
				t.Fatal("expected an error")
			}
			if b, err := os.ReadFile(old); err != nil || string(b) != "old" {
				t.Errorf("old cover = %q, %v; want it untouched", b, err)
			}
			noTempFiles(t, dir)
		})
	}
}
