// file: internal/covers/covers.go
// version: 1.2.1
// guid: c3d4e5f6-7890-abcd-ef12-34567890abcd
// last-edited: 2026-09-10
//
// Cover service logic for proxy caching and validation.
// Business logic extracted from internal/server/covers.go.

package covers

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/security/safehttp"
	"github.com/falkcorp/audiobook-organizer/internal/security/safepath"
)

// coverFetchTimeout bounds one proxied cover fetch. http.DefaultClient, which
// this path used until 2026-09-10, has no timeout at all.
const coverFetchTimeout = 30 * time.Second

// ProxyCoverRequest holds parameters for proxying a cover image.
type ProxyCoverRequest struct {
	URL      string
	CacheDir string
	RootDir  string
}

// ProxyCoverResult holds the result of a proxy operation.
type ProxyCoverResult struct {
	CachePath string
	Error     string
}

// IsAllowedCoverSource validates that a URL is from an approved cover source.
func IsAllowedCoverSource(url string) bool {
	allowed := []string{
		"https://covers.openlibrary.org/",
		"http://covers.openlibrary.org/",
		"https://books.google.com/",
		"http://books.google.com/",
		"https://images-na.ssl-images-amazon.com/",
		"http://images-na.ssl-images-amazon.com/",
		"https://images.amazon.com/",
		"http://images.amazon.com/",
	}
	for _, prefix := range allowed {
		if strings.HasPrefix(url, prefix) {
			return true
		}
	}
	return false
}

// GetCachePath computes the cache path for a given cover URL.
func GetCachePath(coverURL, cacheDir string) string {
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(coverURL)))
	ext := ".jpg"
	if strings.Contains(coverURL, ".png") {
		ext = ".png"
	}
	return filepath.Join(cacheDir, hash+ext)
}

// FetchAndCacheCover fetches a cover from a URL and caches it.
// Returns the cache path on success or an error string.
//
// IsAllowedCoverSource, which the HTTP handler applies before calling this, is
// a string-prefix check on the FIRST URL only. It cannot see what an allowed
// hostname resolves to, and it never sees a redirect target at all — so until
// 2026-09-10 a 302 from covers.openlibrary.org to 169.254.169.254 was followed
// and the response cached (CodeQL alert #645, go/request-forgery). The client
// below applies the scheme allowlist, blocks private/reserved resolved
// addresses on every hop, and caps the redirect chain.
//
// The client is built once. safehttp.NewClient constructs an http.Transport,
// and a Transport per request means no connection reuse plus an idle-connection
// pool that nothing ever closes — this path runs once per proxied image.
var coverFetchClient = safehttp.NewClient(coverFetchTimeout)

func FetchAndCacheCover(coverURL, cacheDir string) (string, string) {
	return fetchAndCacheCoverWithClient(coverFetchClient, coverURL, cacheDir)
}

// fetchAndCacheCoverWithClient is the implementation, split out so tests can
// substitute a plain client pointing at a loopback httptest server — the same
// seam metadata.downloadCoverArtWithClient uses, and the only way to assert
// that a permitted fetch still succeeds once the guard refuses loopback.
func fetchAndCacheCoverWithClient(client *http.Client, coverURL, cacheDir string) (string, string) {
	// Checked before the directory is created so a rejected URL leaves nothing
	// behind on disk.
	if err := safehttp.ValidateURL(coverURL); err != nil {
		return "", "cover URL not allowed"
	}

	// Create cache directory
	if err := os.MkdirAll(cacheDir, 0775); err != nil {
		return "", "failed to create cache directory"
	}

	cachePath := GetCachePath(coverURL, cacheDir)

	// Check if already cached
	if _, err := os.Stat(cachePath); err == nil {
		return cachePath, ""
	}

	// Fetch from source. The guard is in the client's transport and redirect
	// hook, not on this line; the nolint is only about the missing context,
	// which FetchAndCacheCover has no parameter for.
	resp, err := client.Get(coverURL) //nolint:noctx // no caller-supplied context; client.Timeout bounds this
	if err != nil {
		// A refused address is reported distinctly from an unreachable one.
		// The caller only ever sees this string, so collapsing the two would
		// make a blocked SSRF attempt indistinguishable from a dead upstream.
		if errors.Is(err, safehttp.ErrBlockedAddress) || errors.Is(err, safehttp.ErrBlockedScheme) || errors.Is(err, safehttp.ErrTooManyRedirects) {
			return "", "cover URL not allowed"
		}
		return "", "failed to fetch cover"
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "cover source returned error"
	}

	// Write to cache
	f, err := os.Create(cachePath)
	if err != nil {
		return "", "failed to cache cover"
	}

	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(cachePath)
		return "", "failed to write cover"
	}
	f.Close()

	return cachePath, ""
}

// FindCoverFile searches for a cover file in standard directories.
func FindCoverFile(filename string, rootDir string) (string, error) {
	roots := []string{".covers", "covers"}
	for _, sub := range roots {
		sp, err := safepath.Join(rootDir, sub, filename)
		if err != nil {
			continue
		}
		if _, err := os.Stat(sp.String()); err == nil {
			return sp.String(), nil
		}
	}
	return "", os.ErrNotExist
}
