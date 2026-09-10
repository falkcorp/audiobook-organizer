// file: internal/covers/covers_ssrf_test.go
// version: 1.0.1
// guid: 2da127b7-65dc-4e3a-9bcd-a25e71c11fd0
// last-edited: 2026-09-10

package covers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// These two tests are the behavioural statement of CodeQL alert #645
// (go/request-forgery on internal/covers/covers.go). Before the fix they both
// FAIL: FetchAndCacheCover used http.Get (= http.DefaultClient), so a URL that
// resolves to loopback was fetched and cached, and a 302 from an allowlisted
// host to a loopback address was followed with no address check at all.
//
// httptest servers always listen on 127.0.0.1, which is exactly the class of
// address the guard must refuse — so "the test server is unreachable" is the
// pass condition here, not a broken fixture.

func TestFetchAndCacheCover_RefusesLoopbackAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("not-really-a-jpeg"))
	}))
	defer srv.Close()

	cacheDir := t.TempDir()
	cachePath, errMsg := FetchAndCacheCover(srv.URL+"/cover.jpg", cacheDir)
	if errMsg == "" {
		t.Fatalf("FetchAndCacheCover(%q) succeeded and cached %q; a loopback address must be refused", srv.URL, cachePath)
	}
	if cachePath != "" {
		t.Fatalf("cachePath = %q, want empty on refusal", cachePath)
	}
	if _, err := os.Stat(GetCachePath(srv.URL+"/cover.jpg", cacheDir)); err == nil {
		t.Fatal("a cache file was written for a refused fetch")
	}
	// The caller only ever sees this string, so it is the one place a blocked
	// address is distinguishable from an unreachable upstream. Asserting it
	// also proves the guard's error survives net/http's wrapping intact.
	if errMsg != "cover URL not allowed" {
		t.Fatalf("errMsg = %q, want the blocked-address message; a refusal reported as a generic fetch failure is a silent failure", errMsg)
	}
}

func TestFetchAndCacheCover_RefusesRedirectToLoopback(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("internal-service-response"))
	}))
	defer target.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/latest/meta-data/", http.StatusFound)
	}))
	defer origin.Close()

	cacheDir := t.TempDir()
	cachePath, errMsg := FetchAndCacheCover(origin.URL+"/cover.jpg", cacheDir)
	if errMsg == "" {
		t.Fatalf("FetchAndCacheCover followed a 302 to %q and cached %q; the hop must be refused", target.URL, cachePath)
	}
	if cachePath != "" {
		t.Fatalf("cachePath = %q, want empty on refusal", cachePath)
	}
}

func TestFetchAndCacheCover_RejectsNonHTTPScheme(t *testing.T) {
	for _, raw := range []string{"file:///etc/passwd", "gopher://192.0.2.10:70/_x", "ftp://192.0.2.10/cover.jpg"} {
		cachePath, errMsg := FetchAndCacheCover(raw, t.TempDir())
		if errMsg == "" {
			t.Errorf("FetchAndCacheCover(%q) succeeded and cached %q; only http/https are allowed", raw, cachePath)
		}
	}
}

func TestFetchAndCacheCover_AllowedSourceStillFetchesAndCaches(t *testing.T) {
	// Anti-over-suppression. The guard refuses loopback, which is the only
	// address a test server can listen on, so a permitted fetch is proved
	// through the client seam instead: everything except the address decision
	// is the production path.
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("cover-bytes"))
	}))
	defer srv.Close()

	cacheDir := t.TempDir()
	coverURL := srv.URL + "/b/id/1234-L.jpg"

	cachePath, errMsg := fetchAndCacheCoverWithClient(srv.Client(), coverURL, cacheDir)
	if errMsg != "" {
		t.Fatalf("permitted fetch failed: %s", errMsg)
	}
	body, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("reading cached cover: %v", err)
	}
	if string(body) != "cover-bytes" {
		t.Fatalf("cached body = %q, want the fetched bytes", body)
	}

	// Second call must be served from the cache, not refetched.
	again, errMsg := fetchAndCacheCoverWithClient(srv.Client(), coverURL, cacheDir)
	if errMsg != "" || again != cachePath {
		t.Fatalf("second call = (%q, %q), want the cached path and no error", again, errMsg)
	}
	if hits != 1 {
		t.Fatalf("server saw %d requests, want 1 (the second call must hit the cache)", hits)
	}
}
