// file: internal/httputil/rangeserve_test.go
// version: 1.0.0
// guid: 133e0add-c0e6-4450-93fb-5f36864a36f7
// last-edited: 2026-07-29

package httputil

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// deterministicContent builds n bytes where byte i == byte(i % 251). 251 is
// prime and close to 256, so the pattern doesn't line up with common range
// sizes/powers of two, which makes off-by-one bugs in range math visible in
// assertions instead of silently passing.
func deterministicContent(n int) []byte {
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = byte(i % 251)
	}
	return buf
}

// newTempFile writes content to a temp file and returns its absolute path.
// The file's mtime is truncated to a whole second (matching what HTTP dates
// and Last-Modified/If-Modified-Since round-trip to) so ETag/If-Range
// comparisons in tests are deterministic.
func newTempFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	mtime := time.Now().Truncate(time.Second)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	return abs
}

func serveRequest(t *testing.T, path string, opts Options, mutate func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/file", nil)
	if mutate != nil {
		mutate(r)
	}
	w := httptest.NewRecorder()
	if err := ServeFileWithRange(w, r, path, opts); err != nil {
		t.Fatalf("ServeFileWithRange: %v", err)
	}
	return w
}

func TestServeFileWithRange_NoRangeHeader_Returns200Full(t *testing.T) {
	content := deterministicContent(10000)
	path := newTempFile(t, "full.bin", content)

	w := serveRequest(t, path, Options{}, nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body head=%v", w.Code, w.Body.Bytes()[:min(32, w.Body.Len())])
	}
	if got := w.Header().Get("Content-Length"); got != "10000" {
		t.Errorf("Content-Length = %q, want 10000", got)
	}
	if !bytes.Equal(w.Body.Bytes(), content) {
		t.Errorf("body mismatch: got %d bytes, want %d bytes", w.Body.Len(), len(content))
	}
	if got := w.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges = %q, want bytes", got)
	}
}

func TestServeFileWithRange_AcceptRangesAlwaysPresent(t *testing.T) {
	content := deterministicContent(1000)
	path := newTempFile(t, "ar.bin", content)

	cases := []func(*http.Request){
		nil,
		func(r *http.Request) { r.Header.Set("Range", "bytes=0-99") },
		func(r *http.Request) { r.Header.Set("Range", "garbage") },
	}
	for i, mutate := range cases {
		w := serveRequest(t, path, Options{}, mutate)
		if got := w.Header().Get("Accept-Ranges"); got != "bytes" {
			t.Errorf("case %d: Accept-Ranges = %q, want bytes (status %d)", i, got, w.Code)
		}
	}
}

func TestServeFileWithRange_PartialRange_206(t *testing.T) {
	content := deterministicContent(10000)
	path := newTempFile(t, "partial.bin", content)

	w := serveRequest(t, path, Options{}, func(r *http.Request) {
		r.Header.Set("Range", "bytes=0-1023")
	})

	if w.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", w.Code)
	}
	wantCR := fmt.Sprintf("bytes 0-1023/%d", len(content))
	if got := w.Header().Get("Content-Range"); got != wantCR {
		t.Errorf("Content-Range = %q, want %q", got, wantCR)
	}
	if got := w.Header().Get("Content-Length"); got != "1024" {
		t.Errorf("Content-Length = %q, want 1024", got)
	}
	if !bytes.Equal(w.Body.Bytes(), content[0:1024]) {
		t.Errorf("body mismatch for bytes=0-1023")
	}
}

func TestServeFileWithRange_OpenEndedRange(t *testing.T) {
	content := deterministicContent(10000)
	path := newTempFile(t, "openend.bin", content)

	w := serveRequest(t, path, Options{}, func(r *http.Request) {
		r.Header.Set("Range", "bytes=500-")
	})

	if w.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", w.Code)
	}
	wantCR := fmt.Sprintf("bytes 500-9999/%d", len(content))
	if got := w.Header().Get("Content-Range"); got != wantCR {
		t.Errorf("Content-Range = %q, want %q", got, wantCR)
	}
	if !bytes.Equal(w.Body.Bytes(), content[500:]) {
		t.Errorf("body mismatch for bytes=500-")
	}
}

func TestServeFileWithRange_SuffixRange(t *testing.T) {
	content := deterministicContent(10000)
	path := newTempFile(t, "suffix.bin", content)

	w := serveRequest(t, path, Options{}, func(r *http.Request) {
		r.Header.Set("Range", "bytes=-500")
	})

	if w.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", w.Code)
	}
	wantCR := fmt.Sprintf("bytes 9500-9999/%d", len(content))
	if got := w.Header().Get("Content-Range"); got != wantCR {
		t.Errorf("Content-Range = %q, want %q", got, wantCR)
	}
	if !bytes.Equal(w.Body.Bytes(), content[9500:]) {
		t.Errorf("body mismatch for bytes=-500 (last 500 bytes)")
	}
}

func TestServeFileWithRange_Unsatisfiable_416(t *testing.T) {
	content := deterministicContent(10000)
	path := newTempFile(t, "unsat.bin", content)

	w := serveRequest(t, path, Options{}, func(r *http.Request) {
		r.Header.Set("Range", "bytes=20000-30000")
	})

	if w.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("status = %d, want 416", w.Code)
	}
	wantCR := fmt.Sprintf("bytes */%d", len(content))
	if got := w.Header().Get("Content-Range"); got != wantCR {
		t.Errorf("Content-Range = %q, want %q", got, wantCR)
	}
	if got := w.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges = %q, want bytes (must be present on 416 too)", got)
	}
}

func TestServeFileWithRange_MalformedRangeHeader_Ignored(t *testing.T) {
	content := deterministicContent(10000)
	path := newTempFile(t, "malformed.bin", content)

	w := serveRequest(t, path, Options{}, func(r *http.Request) {
		r.Header.Set("Range", "not-a-valid-range-header")
	})

	// RFC 9110 §14.2: a syntactically invalid Range header must be ignored,
	// not rejected — the server proceeds as if the header were absent.
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (malformed Range must be ignored)", w.Code)
	}
	if !bytes.Equal(w.Body.Bytes(), content) {
		t.Errorf("body mismatch: malformed Range should serve full body")
	}
}

func TestServeFileWithRange_ContentType(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"book.m4b", "audio/mp4"},
		{"book.m4a", "audio/mp4"},
		{"book.mp3", "audio/mpeg"},
		{"book.flac", "audio/flac"},
		{"book.opus", "audio/ogg"},
		{"book.weird", "application/octet-stream"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := newTempFile(t, tc.name, deterministicContent(100))
			w := serveRequest(t, path, Options{}, nil)
			if got := w.Header().Get("Content-Type"); got != tc.want {
				t.Errorf("Content-Type = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestServeFileWithRange_LastModifiedAndETag(t *testing.T) {
	content := deterministicContent(500)
	path := newTempFile(t, "meta.bin", content)

	w := serveRequest(t, path, Options{}, nil)

	if got := w.Header().Get("Last-Modified"); got == "" {
		t.Error("Last-Modified header missing")
	}
	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatal("ETag header missing")
	}
	if etag[0] != '"' || etag[len(etag)-1] != '"' {
		t.Errorf("ETag = %q, want quoted strong-ish validator", etag)
	}
}

func TestServeFileWithRange_IfNoneMatch_304(t *testing.T) {
	content := deterministicContent(500)
	path := newTempFile(t, "inm.bin", content)

	// First request to learn the ETag.
	w1 := serveRequest(t, path, Options{}, nil)
	etag := w1.Header().Get("ETag")
	if etag == "" {
		t.Fatal("ETag header missing on first request")
	}

	w2 := serveRequest(t, path, Options{}, func(r *http.Request) {
		r.Header.Set("If-None-Match", etag)
	})
	if w2.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", w2.Code)
	}
	if w2.Body.Len() != 0 {
		t.Errorf("304 response should have empty body, got %d bytes", w2.Body.Len())
	}
	if got := w2.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges = %q, want bytes (must be present on 304 too)", got)
	}
}

func TestServeFileWithRange_IfRangeMismatch_ServesFull200(t *testing.T) {
	content := deterministicContent(10000)
	path := newTempFile(t, "ifrange.bin", content)

	w := serveRequest(t, path, Options{}, func(r *http.Request) {
		r.Header.Set("Range", "bytes=0-99")
		r.Header.Set("If-Range", `"stale-etag-that-does-not-match"`)
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (If-Range mismatch => full body)", w.Code)
	}
	if !bytes.Equal(w.Body.Bytes(), content) {
		t.Errorf("body mismatch: If-Range mismatch should serve full body")
	}
}

func TestServeFileWithRange_IfRangeMatch_ServesPartial206(t *testing.T) {
	content := deterministicContent(10000)
	path := newTempFile(t, "ifrangeok.bin", content)

	w1 := serveRequest(t, path, Options{}, nil)
	etag := w1.Header().Get("ETag")

	w2 := serveRequest(t, path, Options{}, func(r *http.Request) {
		r.Header.Set("Range", "bytes=0-99")
		r.Header.Set("If-Range", etag)
	})
	if w2.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206 (If-Range matches current ETag)", w2.Code)
	}
}

func TestServeFileWithRange_HEAD(t *testing.T) {
	content := deterministicContent(10000)
	path := newTempFile(t, "head.bin", content)

	r := httptest.NewRequest(http.MethodHead, "/file", nil)
	w := httptest.NewRecorder()
	if err := ServeFileWithRange(w, r, path, Options{}); err != nil {
		t.Fatalf("ServeFileWithRange: %v", err)
	}

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Content-Length"); got != "10000" {
		t.Errorf("Content-Length = %q, want 10000", got)
	}
	if w.Body.Len() != 0 {
		t.Errorf("HEAD response should have empty body, got %d bytes", w.Body.Len())
	}
	if got := w.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges = %q, want bytes", got)
	}
}

func TestServeFileWithRange_HEAD_WithRange(t *testing.T) {
	content := deterministicContent(10000)
	path := newTempFile(t, "headrange.bin", content)

	r := httptest.NewRequest(http.MethodHead, "/file", nil)
	r.Header.Set("Range", "bytes=0-1023")
	w := httptest.NewRecorder()
	if err := ServeFileWithRange(w, r, path, Options{}); err != nil {
		t.Fatalf("ServeFileWithRange: %v", err)
	}

	if w.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", w.Code)
	}
	if got := w.Header().Get("Content-Length"); got != "1024" {
		t.Errorf("Content-Length = %q, want 1024", got)
	}
	if w.Body.Len() != 0 {
		t.Errorf("HEAD response should have empty body, got %d bytes", w.Body.Len())
	}
}

// TestServeFileWithRange_MultiRange documents and verifies the deliberate
// multi-range decision: we delegate to http.ServeContent, which natively
// implements multipart/byteranges for a request that names more than one
// range. We do not special-case or restrict this — see the doc comment on
// ServeFileWithRange for the reasoning. This test proves the composed
// behavior: a multi-range request gets a 206 with a multipart/byteranges
// Content-Type and both requested ranges present in the body.
func TestServeFileWithRange_MultiRange(t *testing.T) {
	content := deterministicContent(10000)
	path := newTempFile(t, "multi.bin", content)

	w := serveRequest(t, path, Options{}, func(r *http.Request) {
		r.Header.Set("Range", "bytes=0-99,200-299")
	})

	if w.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !bytes.HasPrefix([]byte(ct), []byte("multipart/byteranges")) {
		t.Fatalf("Content-Type = %q, want multipart/byteranges prefix", ct)
	}
	body := w.Body.String()
	// Both range payloads must appear somewhere in the multipart body.
	if !bytes.Contains([]byte(body), content[0:100]) {
		t.Errorf("multipart body missing first range payload (bytes 0-99)")
	}
	if !bytes.Contains([]byte(body), content[200:300]) {
		t.Errorf("multipart body missing second range payload (bytes 200-299)")
	}
}

func TestServeFileWithRange_RejectsRelativePath(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/file", nil)
	w := httptest.NewRecorder()
	err := ServeFileWithRange(w, r, "relative/path.bin", Options{})
	if err == nil {
		t.Fatal("expected error for relative path, got nil")
	}
}

func TestServeFileWithRange_RejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	r := httptest.NewRequest(http.MethodGet, "/file", nil)
	w := httptest.NewRecorder()
	err := ServeFileWithRange(w, r, dir, Options{})
	if err == nil {
		t.Fatal("expected error when path is a directory, got nil")
	}
}

func TestServeFileWithRange_RejectsSymlinkToDirectory(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "realdir")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	link := filepath.Join(dir, "linktodir")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/file", nil)
	w := httptest.NewRecorder()
	err := ServeFileWithRange(w, r, link, Options{})
	if err == nil {
		t.Fatal("expected error when path is a symlink to a directory, got nil")
	}
}

func TestServeFileWithRange_RejectsNonexistentFile(t *testing.T) {
	dir := t.TempDir()
	r := httptest.NewRequest(http.MethodGet, "/file", nil)
	w := httptest.NewRecorder()
	err := ServeFileWithRange(w, r, filepath.Join(dir, "does-not-exist.bin"), Options{})
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}
}

// --- Fixture-backed integration test -------------------------------------

// findRepoRootHTTPUtil walks up from the current package directory to find
// the module root (identified by go.mod). Mirrors the pattern used in
// internal/metadata/real_audio_test.go.
func findRepoRootHTTPUtil(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (go.mod not found)")
		}
		dir = parent
	}
}

// TestServeFileWithRange_FixtureMidFileRange is the test that actually
// proves seeking works: it requests a 1024-byte range in the middle of the
// real 115MB committed audiobook fixture and verifies the server returns
// exactly the bytes that live at that offset on disk.
func TestServeFileWithRange_FixtureMidFileRange(t *testing.T) {
	root := findRepoRootHTTPUtil(t)
	fixture := filepath.Join(root, "testdata", "audio", "librivox", "odyssey_butler_librivox", "odyssey_complete.m4b")

	info, err := os.Stat(fixture)
	if os.IsNotExist(err) {
		t.Skip("odyssey_complete.m4b fixture not available")
	}
	if err != nil {
		t.Fatalf("stat fixture: %v", err)
	}
	// Guard against a Git LFS pointer file masquerading as the real fixture.
	if info.Size() < 1<<20 {
		probe := make([]byte, 64)
		f, ferr := os.Open(fixture)
		if ferr == nil {
			n, _ := f.Read(probe)
			f.Close()
			if bytes.HasPrefix(probe[:n], []byte("version https://git-lfs.github.com/")) {
				t.Skip("odyssey_complete.m4b is an LFS pointer (LFS not fetched)")
			}
		}
		t.Skip("odyssey_complete.m4b fixture too small, not the real file")
	}

	const (
		start  int64 = 50_000_000
		length int64 = 1024
	)
	if info.Size() < start+length {
		t.Skipf("fixture too small (%d bytes) for requested offset", info.Size())
	}

	want := make([]byte, length)
	f, err := os.Open(fixture)
	if err != nil {
		t.Fatalf("open fixture directly: %v", err)
	}
	defer f.Close()
	if _, err := f.ReadAt(want, start); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/file", nil)
	r.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, start+length-1))
	w := httptest.NewRecorder()
	if err := ServeFileWithRange(w, r, fixture, Options{}); err != nil {
		t.Fatalf("ServeFileWithRange: %v", err)
	}

	if w.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", w.Code)
	}
	wantCR := fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, info.Size())
	if got := w.Header().Get("Content-Range"); got != wantCR {
		t.Errorf("Content-Range = %q, want %q", got, wantCR)
	}
	if int64(w.Body.Len()) != length {
		t.Fatalf("body length = %d, want %d", w.Body.Len(), length)
	}
	if !bytes.Equal(w.Body.Bytes(), want) {
		t.Errorf("body bytes do not match direct os.ReadAt at the same offset")
	}
}
