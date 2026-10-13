// file: internal/metadata/cover_test.go
// version: 1.1.1
// guid: 5fa1b8c9-d3e4-48f5-95a8-4ac57cde0b12

package metadata

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func testClient() *http.Client { return &http.Client{} }

func TestReplaceCoverArt_Success(t *testing.T) {
	// Serve a fake JPEG
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte{0xFF, 0xD8, 0xFF}) // JPEG magic bytes
	}))
	defer srv.Close()

	dir := t.TempDir()
	path, err := replaceCoverArtWithClient(testClient(), srv.URL+"/cover.jpg", dir, "book123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filepath.Base(path) != "book123.jpg" {
		t.Errorf("expected book123.jpg, got %s", filepath.Base(path))
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file not created: %v", err)
	}
}

func TestReplaceCoverArt_RejectsNonImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html></html>"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	_, err := replaceCoverArtWithClient(testClient(), srv.URL+"/notimage", dir, "book456")
	if err == nil {
		t.Fatal("expected error for non-image content type")
	}
}

func TestReplaceCoverArt_EmptyInputs(t *testing.T) {
	if _, err := ReplaceCoverArt("", "/tmp", "book"); err == nil {
		t.Error("expected error for empty URL")
	}
	if _, err := ReplaceCoverArt("http://example.com/img.jpg", "/tmp", ""); err == nil {
		t.Error("expected error for empty bookID")
	}
}

func TestReplaceCoverArt_BlocksPrivateIPs(t *testing.T) {
	urls := []string{
		"http://127.0.0.1/cover.jpg",
		"http://169.254.169.254/latest/meta-data/",
		"http://192.168.1.1/cover.jpg",
		"http://10.0.0.1/cover.jpg",
	}
	for _, u := range urls {
		_, err := ReplaceCoverArt(u, t.TempDir(), "book-ssrf")
		if err == nil {
			t.Errorf("expected SSRF block for %s, got nil", u)
		}
	}
}

func TestReplaceCoverArt_BlocksNonHTTP(t *testing.T) {
	urls := []string{
		"file:///etc/passwd",
		"ftp://example.com/cover.jpg",
	}
	for _, u := range urls {
		_, err := ReplaceCoverArt(u, t.TempDir(), "book-scheme")
		if err == nil {
			t.Errorf("expected scheme block for %s, got nil", u)
		}
	}
}

func TestReplaceCoverArt_PNG(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte{0x89, 0x50, 0x4E, 0x47})
	}))
	defer srv.Close()

	dir := t.TempDir()
	path, err := replaceCoverArtWithClient(testClient(), srv.URL+"/cover.png", dir, "bookpng")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filepath.Ext(path) != ".png" {
		t.Errorf("expected .png extension, got %s", filepath.Ext(path))
	}
}

func TestCoverPathForBook(t *testing.T) {
	dir := t.TempDir()
	coversDir := filepath.Join(dir, "covers")
	os.MkdirAll(coversDir, 0755)

	// No cover exists
	if got := CoverPathForBook(dir, "missing"); got != "" {
		t.Errorf("expected empty, got %s", got)
	}

	// Create one
	os.WriteFile(filepath.Join(coversDir, "found.jpg"), []byte("img"), 0644)
	if got := CoverPathForBook(dir, "found"); got == "" {
		t.Error("expected to find cover")
	}
}

func TestExtensionFromContentType(t *testing.T) {
	tests := []struct {
		ct   string
		want string
	}{
		{"image/jpeg", ".jpg"},
		{"image/png", ".png"},
		{"image/gif", ".gif"},
		{"image/webp", ".webp"},
		{"image/something", ".jpg"},
	}
	for _, tt := range tests {
		if got := extensionFromContentType(tt.ct); got != tt.want {
			t.Errorf("extensionFromContentType(%q) = %q, want %q", tt.ct, got, tt.want)
		}
	}
}
