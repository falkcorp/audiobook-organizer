// file: internal/metadata/cover.go
// version: 1.11.0
// guid: 4efaa7b8-e29a-47f3-84f7-39b46bfc9a01
// last-edited: 2026-09-12

package metadata

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metadata/providerhttp"
	"github.com/falkcorp/audiobook-organizer/internal/security/safehttp"
)

// ErrSSRFBlocked is returned when a cover URL resolves to a private/reserved
// address. It aliases safehttp.ErrBlockedAddress so that existing callers that
// compare against this name keep working; the guard itself lives in
// internal/security/safehttp and is shared with internal/covers.
//
// The scheme allowlist, the resolved-address block and the redirect re-check
// used to be three private helpers in this file. They were duplicated nowhere
// and absent from internal/covers, which is why CodeQL alert #645 was a real
// finding and #662 was not. One implementation now serves both.
var ErrSSRFBlocked = safehttp.ErrBlockedAddress

// DownloadCoverArt downloads a cover image from coverURL and saves it to
// {destDir}/covers/{bookID}.{ext}. Returns the local file path on success.
// Skips download if the file already exists. Only accepts image/* content types.
// Rejects non-http(s) URLs and URLs that resolve to private/reserved IPs.
func DownloadCoverArt(coverURL string, destDir string, bookID string) (string, error) {
	// Rate-limited, but the SSRF guard is preserved: safeCoverDialContext refuses
	// private/reserved IPs and MUST stay on the transport. providerhttp wraps the
	// caller's transport rather than replacing it, so throttling is added without
	// dropping the security control.
	return downloadCoverArtWithClient(coverClient(), coverURL, destDir, bookID)
}

// ReplaceCoverArt is DownloadCoverArt for an explicitly applied cover. It
// always fetches coverURL -- DownloadCoverArt returns an existing cover file
// without fetching, so applying a new cover to a book that already had one
// kept the old image forever. The new image is written to a temp file in the
// covers directory and renamed over the old cover only once it is complete; on
// any failure the existing cover is left exactly as it was.
func ReplaceCoverArt(coverURL string, destDir string, bookID string) (string, error) {
	return replaceCoverArtWithClient(coverClient(), coverURL, destDir, bookID)
}

// coverClient is the rate-limited, SSRF-guarded client both entry points use.
func coverClient() *http.Client {
	// Rate-limited, but the SSRF guard is preserved: safeCoverDialContext refuses
	// private/reserved IPs and MUST stay on the transport. providerhttp wraps the
	// caller's transport rather than replacing it, so throttling is added without
	// dropping the security control.
	client := providerhttp.ClientWithTransport("cover", safehttp.NewTransport())
	// providerhttp wraps the transport rather than replacing it, so the address
	// guard survives the throttling wrapper. It does not set CheckRedirect,
	// though, so the per-hop scheme check has to be attached here — without it
	// the scheme allowlist would only ever have run on the first URL.
	client.CheckRedirect = safehttp.CheckRedirect
	return client
}

// downloadCoverArtWithClient is the internal implementation — accepts a custom
// client so tests can substitute a plain http.Client pointing to localhost.
func downloadCoverArtWithClient(client *http.Client, coverURL string, destDir string, bookID string) (string, error) {
	return fetchCoverArt(client, coverURL, destDir, bookID, false)
}

// replaceCoverArtWithClient is ReplaceCoverArt with a caller-supplied client.
func replaceCoverArtWithClient(client *http.Client, coverURL string, destDir string, bookID string) (string, error) {
	return fetchCoverArt(client, coverURL, destDir, bookID, true)
}

// fetchCoverArt is the shared implementation. replace=false keeps the
// long-standing short-circuit (an existing cover is returned unfetched);
// replace=true always fetches and swaps the new image in.
func fetchCoverArt(client *http.Client, coverURL string, destDir string, bookID string, replace bool) (string, error) {
	if coverURL == "" {
		return "", fmt.Errorf("empty cover URL")
	}
	if bookID == "" {
		return "", fmt.Errorf("empty book ID")
	}
	// Sanitize once, here, so the existence check and the os.Create below cannot
	// disagree about which file they mean. This site previously used bookID raw
	// while CoverPathForBook applied filepath.Base; CodeQL flagged the read as
	// "uncontrolled data used in path expression" and the write one line down had
	// the identical exposure, so both now go through the sanitized id.
	safeID := safeCoverID(bookID)
	if safeID == "" {
		return "", fmt.Errorf("invalid book ID %q", bookID)
	}

	if err := safehttp.ValidateURL(coverURL); err != nil {
		return "", fmt.Errorf("cover URL rejected: %w", err)
	}

	coversDir := filepath.Join(destDir, "covers")

	// Check if cover already exists (any known extension). This is the same
	// lookup CoverPathForBook does; see findExistingCover for why it is a stat
	// probe rather than a glob.
	// Skipped under replace: an explicitly applied new cover must be fetched.
	if !replace {
		if existing := findExistingCover(coversDir, safeID); existing != "" {
			return existing, nil
		}
	}

	// Create covers directory
	if err := os.MkdirAll(coversDir, 0775); err != nil {
		return "", fmt.Errorf("failed to create covers directory: %w", err)
	}

	// The SSRF control is the transport, not this line: safehttp's DialContext
	// checks the resolved address on every hop. The nolint is only about the
	// missing context — DownloadCoverArt takes none, and the providerhttp
	// client's own timeout bounds the request.
	resp, err := client.Get(coverURL) //nolint:noctx // no caller-supplied context; client.Timeout bounds this
	if err != nil {
		return "", fmt.Errorf("failed to download cover: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("cover download returned status %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "image/") {
		return "", fmt.Errorf("unexpected content type: %s", contentType)
	}

	// The filename is built only from the validated id and an extension from
	// the coverExtensions allow-list (never from the URL), and coverPathIn
	// proves the result is a direct child of coversDir.
	name, err := coverFileName(safeID, extensionFromContentType(contentType))
	if err != nil {
		return "", fmt.Errorf("invalid cover destination for book %q: %w", bookID, err)
	}
	destPath, err := coverPathIn(coversDir, name)
	if err != nil {
		return "", fmt.Errorf("invalid cover destination for book %q: %w", bookID, err)
	}
	// The rename and the removals below go through an os.Root on coversDir,
	// which refuses any name that resolves outside it: "..", an absolute path,
	// or a symlink that leaves the directory.
	root, err := os.OpenRoot(coversDir)
	if err != nil {
		return "", fmt.Errorf("failed to open covers directory: %w", err)
	}
	defer root.Close()

	// Write to a temp file in the same directory and rename it into place only
	// once the image is complete, so a failed or truncated download can never
	// replace -- or half-overwrite -- the cover already on disk. Limit to 10 MB.
	tmp, err := os.CreateTemp(coversDir, "."+safeID+".*.tmp")
	if err != nil {
		return "", fmt.Errorf("failed to create cover file: %w", err)
	}
	tmpPath := tmp.Name()
	_, copyErr := io.Copy(tmp, io.LimitReader(resp.Body, 10*1024*1024))
	closeErr := tmp.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("failed to write cover file: %w", errors.Join(copyErr, closeErr))
	}
	if err := root.Rename(filepath.Base(tmpPath), name); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("failed to move cover file into place: %w", err)
	}
	if replace {
		removeOtherCovers(root, safeID, name)
	}
	return destPath, nil
}

// removeOtherCovers deletes the book's cover under every other extension, so
// the file just written is the one findExistingCover serves. Precedence there
// is alphabetical by extension: an old "<id>.gif" would otherwise keep
// shadowing a newly applied "<id>.jpg".
//
// root is the os.Root on the covers directory, so a removal cannot leave it.
func removeOtherCovers(root *os.Root, safeID, keep string) {
	for _, ext := range coverExtensions {
		name, err := coverFileName(safeID, ext)
		if err != nil || name == keep {
			continue
		}
		// A failed removal is the case that matters: the stale file keeps
		// shadowing the new cover, so say so rather than swallow it.
		if rmErr := root.Remove(name); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			slog.Warn("cover replace: could not remove the previous cover; it may still be served",
				"path", logger.SanitizeLogValue(filepath.Join(root.Name(), name)), "kept", logger.SanitizeLogValue(keep),
				"error", logger.SanitizeLogValue(rmErr.Error()))
		}
	}
}

// coverExtensions lists the image extensions a stored cover may carry, in the
// order that decides precedence when a book somehow has more than one on disk.
//
// This order is load-bearing and is NOT arbitrary. It reproduces what the
// previous filepath.Glob implementation did: Go's glob() sorts the directory
// names before matching, so Glob returned "<id>.gif" ahead of "<id>.jpg", and
// the caller took the first match whose extension was allowed. Precedence was
// therefore alphabetical by extension, not the order the old if-statement
// happened to list them in. Reordering this slice silently changes which file
// gets served for any book with two covers.
//
// Lowercase-only is safe by construction, not by observation: every write goes
// through extensionFromContentType, which returns a hardcoded lowercase
// extension. (Confirmed against production on 2026-09-08 — 0 of 9,288 files in
// the covers directory had a non-lowercase extension, on a case-sensitive
// filesystem.)
var coverExtensions = [...]string{".gif", ".jpeg", ".jpg", ".png", ".webp"}

// findExistingCover returns the path of the stored cover for id, or "" if there
// is none. Both callers pass an id already checked by safeCoverID; the io/fs
// confinement below is a second, independent guard rather than the only one.
//
// This intentionally does not use filepath.Glob. The pattern "<id>.*" contains a
// meta character, so Glob cannot do a point lookup — it falls into glob(), which
// calls Readdirnames(-1) on the entire covers directory, slices.Sort's every
// name, and runs filepath.Match against each one. That makes resolving a single
// cover O(size of the covers directory) even though the filename is already
// known apart from its extension.
//
// It mattered: on 2026-09-08 this sat on the ABS search path once per result
// with 9,288 files in that directory, and a 35s production CPU profile
// attributed 27.96s (6.4% of process CPU) to it — 18.00s Readdirnames, 5.57s
// sort, 4.17s Match, and only 0.08s of actual os.Stat. Probing the five
// candidate extensions is constant in the directory size; os.Stat measured
// 0.01ms on that host.
//
// Two deliberate refinements over the Glob version, both narrowing what can be
// returned: a directory named "<id>.jpg" is no longer treated as a cover, and
// neither is a dangling symlink (Glob listed both; serving either fails).
// The probe goes through an fs.FS rooted at coversDir rather than
// os.Stat(filepath.Join(...)). id reaches here from callers that pass
// request-supplied values (CoverPathForBook is called with c.Param("id")), and
// io/fs confines a lookup to its root by construction: fs.ValidPath rejects any
// name containing a separator or "..", so no argument can walk out of coversDir.
// filepath.Glob had the same underlying exposure — CodeQL just does not model
// Glob as a path sink — so replacing it is what surfaced the pre-existing taint.
//
// Going through an fs.FS costs nothing here: os.DirFS implements fs.StatFS, so
// fs.Stat dispatches straight to os.Stat rather than falling back to Open+Stat.
// Measured, worst case (a hit on the last extension, i.e. five probes): 8.25us
// via fs.Stat vs 9.36us via os.Stat+filepath.Join.
func findExistingCover(coversDir, id string) string {
	fsys := os.DirFS(coversDir)
	for _, ext := range coverExtensions {
		name := id + ext
		if !fs.ValidPath(name) || strings.ContainsRune(name, '/') {
			// Redundant with os.dirFS.Open, which rejects the same names with
			// ErrInvalid — verified by mutation: deleting this block changes no
			// test outcome. Kept because it states the precondition at the point
			// it matters, and because it is what still holds if this ever stops
			// going through an fs.FS. ValidPath alone would accept "a/b", which
			// is a subdirectory rather than a cover we own.
			return ""
		}
		if fi, err := fs.Stat(fsys, name); err == nil && !fi.IsDir() {
			return filepath.Join(coversDir, name)
		}
	}
	return ""
}

// safeCoverID returns bookID when it is usable as a cover filename stem, and ""
// otherwise.
//
// It rejects rather than truncates. filepath.Base would reduce "a/b/c" to "c",
// which is worse than useless on the write path: two different book IDs sharing
// a last segment would resolve to the same cover filename and one book's art
// would overwrite the other's. Book IDs are DB-minted ULIDs — both cover-writing
// callers reach DownloadCoverArt only after GetBookByID has returned a real book
// — so a separator here means the caller is wrong, and guessing which book was
// meant is not this function's job.
//
// The separator test is written out rather than expressed as
// `bookID != filepath.Base(bookID)`, because Base("/") returns "/" — that form
// let a bare separator through, which the tests caught.
func safeCoverID(bookID string) string {
	if bookID == "" || bookID == "." || bookID == ".." {
		return ""
	}
	// '/' is checked alongside filepath.Separator so that a Unix-style path
	// is rejected on Windows too, where Separator is '\\'.
	if strings.ContainsRune(bookID, '/') || strings.ContainsRune(bookID, filepath.Separator) {
		return ""
	}
	return bookID
}

// coverFileName builds a stored cover's filename, "<id><ext>", from validated
// parts only. id must pass safeCoverID. ext must be one of coverExtensions: it
// is matched against that list and the list's own string is used, so nothing
// from the download (URL or response) reaches the name. The name must also be
// a single local path element with no "..": that guard is what CodeQL's
// go/path-injection query credits, and it holds on its own if either check
// above ever changes.
func coverFileName(id, ext string) (string, error) {
	safeID := safeCoverID(id)
	if safeID == "" {
		return "", fmt.Errorf("invalid book ID %q", id)
	}
	allowed := ""
	for _, e := range coverExtensions {
		if e == ext {
			allowed = e
			break
		}
	}
	if allowed == "" {
		return "", fmt.Errorf("cover extension %q is not allowed", ext)
	}
	name := safeID + allowed
	if strings.Contains(name, "..") || !filepath.IsLocal(name) || filepath.Base(name) != name {
		return "", fmt.Errorf("cover filename %q is not a plain file name", name)
	}
	return name, nil
}

// coverPathIn joins coversDir and a coverFileName result, and proves the
// result is a direct child of coversDir: after Clean, filepath.Rel must give
// back exactly name (not "..", not absolute, no separator).
func coverPathIn(coversDir, name string) (string, error) {
	if name == "" || name == "." || filepath.Base(name) != name {
		return "", fmt.Errorf("cover filename %q is not a plain file name", name)
	}
	dir := filepath.Clean(coversDir)
	p := filepath.Clean(filepath.Join(dir, name))
	rel, err := filepath.Rel(dir, p)
	if err != nil || rel != name || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("cover path for %q escapes the covers directory", name)
	}
	return p, nil
}

// CoverPathForBook returns the local cover file path if it exists, empty string otherwise.
func CoverPathForBook(destDir string, bookID string) string {
	// filepath.Base strips any directory traversal from the bookID segment.
	safeID := safeCoverID(bookID)
	if safeID == "" {
		return ""
	}
	return findExistingCover(filepath.Join(destDir, "covers"), safeID)
}

// HasExistingCoverArt checks if an audio file already has cover art, either
// embedded in the file or as a common image file in the same directory
// (e.g., cover.jpg, folder.jpg, etc.).
func HasExistingCoverArt(audioPath string) bool {
	// Check for embedded cover art
	if audioPath != "" {
		if coverPath, err := ExtractCoverArt(audioPath); err == nil && coverPath != "" {
			return true
		}
	}

	// Check for common cover image files in the same directory
	dir := filepath.Dir(audioPath)
	coverNames := []string{
		"cover", "folder", "front", "album", "artwork",
	}
	imageExts := []string{".jpg", ".jpeg", ".png", ".webp", ".gif"}
	for _, name := range coverNames {
		for _, ext := range imageExts {
			candidate := filepath.Join(dir, name+ext)
			if _, err := os.Stat(candidate); err == nil {
				return true
			}
			// Also check uppercase
			candidate = filepath.Join(dir, strings.ToUpper(name)+ext)
			if _, err := os.Stat(candidate); err == nil {
				return true
			}
		}
	}
	return false
}

func extensionFromContentType(ct string) string {
	ct = strings.ToLower(ct)
	switch {
	case strings.Contains(ct, "png"):
		return ".png"
	case strings.Contains(ct, "gif"):
		return ".gif"
	case strings.Contains(ct, "webp"):
		return ".webp"
	default:
		return ".jpg"
	}
}
