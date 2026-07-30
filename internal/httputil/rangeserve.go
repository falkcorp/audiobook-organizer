// file: internal/httputil/rangeserve.go
// version: 1.0.0
// guid: ac93e82b-5098-4681-93db-4a457dcb6f28
// last-edited: 2026-07-29

package httputil

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Options configures ServeFileWithRange. The zero value is a valid,
// sensible default (content type sniffed from the file extension).
type Options struct {
	// ContentType overrides the sniffed Content-Type, when non-empty. Use
	// this when the caller already knows the correct MIME type (e.g. from
	// stored metadata) and wants to skip extension-based sniffing.
	ContentType string
}

// contentTypeByExt maps audiobook-relevant file extensions to their MIME
// type. mime.TypeByExtension is deliberately not used here: on some
// platforms it consults OS-level registries that can be absent, inconsistent
// across machines, or map .m4b/.m4a to generic/incorrect types — audiobook
// clients care about getting exactly "audio/mp4" for MPEG-4 containers, so
// we hardcode the mapping the spec requires.
func contentTypeByExt(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".m4b", ".m4a":
		return "audio/mp4"
	case ".mp3":
		return "audio/mpeg"
	case ".flac":
		return "audio/flac"
	case ".opus":
		return "audio/ogg"
	default:
		return "application/octet-stream"
	}
}

// etagFor builds a cheap strong-ish validator from file size and mtime:
// `"<size>-<mtime-unixnano>"`. It is not a content hash, so two different
// byte streams that happen to share size and mtime would collide — that is
// an accepted tradeoff for an audiobook file that is written once and never
// mutated in place afterward. It is quoted, satisfying the ETag grammar
// (RFC 9110 §8.8.3) so it round-trips through If-None-Match/If-Range as-is.
func etagFor(size int64, modUnixNano int64) string {
	return fmt.Sprintf("%q", fmt.Sprintf("%d-%d", size, modUnixNano))
}

// isSyntacticallyValidRange reports whether s (the raw value of a Range
// request header) is a syntactically well-formed RFC 9110 §14.1.2
// byte-ranges-specifier — WITHOUT checking whether the named range(s)
// actually fall inside the resource.
//
// This exists to work around a deliberate simplification in the standard
// library: net/http's internal parseRange conflates "syntactically invalid
// Range header" and "syntactically valid Range header whose bounds don't
// overlap the resource" into the same 416 response. RFC 9110 requires only
// the latter to produce 416; the former must be treated as if the Range
// header were absent (RFC 9110 §14.2 — an invalid Range MUST be ignored, not
// rejected). ServeFileWithRange calls this first so it can strip a malformed
// header before handing off to http.ServeContent, preserving http.
// ServeContent's correct, battle-tested 416-on-out-of-bounds behavior for
// well-formed-but-unsatisfiable ranges.
func isSyntacticallyValidRange(s string) bool {
	const prefix = "bytes="
	if !strings.HasPrefix(s, prefix) {
		return false
	}
	specs := strings.Split(s[len(prefix):], ",")
	sawSpec := false
	for _, spec := range specs {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			continue
		}
		start, end, ok := strings.Cut(spec, "-")
		if !ok {
			return false
		}
		start, end = strings.TrimSpace(start), strings.TrimSpace(end)
		switch {
		case start == "":
			// Suffix range: "-<suffix-length>". The suffix length must be a
			// non-negative integer.
			if !isNonNegativeInt64(end) {
				return false
			}
		default:
			if !isNonNegativeInt64(start) {
				return false
			}
			if end != "" && !isNonNegativeInt64(end) {
				return false
			}
		}
		sawSpec = true
	}
	// "bytes=" with no actual range-spec (e.g. just "bytes=" or "bytes=,,")
	// is not a usable Range header either.
	return sawSpec
}

func isNonNegativeInt64(s string) bool {
	if s == "" {
		return false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return err == nil && n >= 0
}

// stripRangeHeader returns a shallow copy of r with the Range request
// header removed, leaving the original request (and its Header map)
// untouched. Used to make a malformed Range header invisible to
// http.ServeContent so it falls back to serving the full 200 response,
// without mutating headers the caller may still read after this call
// returns.
func stripRangeHeader(r *http.Request) *http.Request {
	r2 := new(http.Request)
	*r2 = *r
	r2.Header = r.Header.Clone()
	r2.Header.Del("Range")
	return r2
}

// ServeFileWithRange serves the file at path over w/r with full HTTP
// byte-range support (RFC 9110 §14, RFC 9110 §13 conditional requests).
//
// # Contract
//
// path MUST already be a fully-resolved, absolute filesystem path that the
// caller has confined to an allowed root and has already authorized the
// requester to read. ServeFileWithRange performs no authorization and no
// path-containment checks beyond rejecting non-absolute paths and rejecting
// anything that does not resolve (after following symlinks) to a regular
// file — it is not a defense against path traversal by itself. This
// repository has a history of path-injection findings; callers MUST build
// path by joining a trusted root with a validated, non-traversing relative
// segment (e.g. via a store lookup keyed by book/file ID, never by echoing
// user-supplied path segments) before calling this function.
//
// # Behavior
//
// Delegates the actual range/conditional-request logic to the standard
// library's http.ServeContent, which already implements Range parsing,
// If-Range, If-None-Match, If-Modified-Since, 200/206/304/416, and
// multipart/byteranges for multi-range requests correctly and is
// battle-tested — reimplementing that parsing is a well-known source of
// subtle bugs, so this function's job is limited to: validating the path,
// opening the file, computing Content-Type/ETag/Last-Modified, and handing
// off.
//
// Multi-range requests (e.g. "bytes=0-99,200-299") are therefore served as
// http.ServeContent implements them: a 206 response with a
// multipart/byteranges body containing each requested range. We deliberately
// do not special-case or restrict this to "first range only" — the stdlib
// behavior is spec-correct and audiobook clients that ever issue multi-range
// requests (uncommon, but real for some resumable-download implementations)
// get correct output for free.
//
// A syntactically malformed Range header is ignored (full 200 response), per
// RFC 9110 §14.2 ("MUST ignore" on invalid ranges). This does NOT match
// http.ServeContent's built-in behavior — the stdlib returns 416 for both a
// malformed header and a well-formed-but-out-of-bounds one, so
// ServeFileWithRange pre-validates the header's syntax (see
// isSyntacticallyValidRange) and strips it before delegating when it isn't
// well-formed, letting a well-formed-but-unsatisfiable range still reach
// http.ServeContent's correct 416 path.
//
// Always sets Accept-Ranges: bytes, regardless of request method or Range
// presence, so clients discover range support up front.
//
// Returns a non-nil error, with no headers/body written, if path is not
// absolute, cannot be opened, or does not resolve to a regular file (a
// directory, or a symlink to one, is rejected; a symlink to a regular file
// is allowed since os.Stat follows symlinks). Callers are responsible for
// translating that error into an appropriate HTTP status (404 for
// not-found, 400/403 for a rejected path shape, 500 otherwise).
func ServeFileWithRange(w http.ResponseWriter, r *http.Request, path string, opts Options) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("httputil: ServeFileWithRange: path must be absolute, got %q", path)
	}

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("httputil: ServeFileWithRange: open %q: %w", path, err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("httputil: ServeFileWithRange: stat %q: %w", path, err)
	}
	// fi reflects the target of a symlink (os.Stat, unlike os.Lstat, follows
	// symlinks), so this rejects directories and symlinks-to-directories
	// alike while still allowing a symlink that resolves to a regular file.
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("httputil: ServeFileWithRange: %q is not a regular file", path)
	}

	ct := opts.ContentType
	if ct == "" {
		ct = contentTypeByExt(path)
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("ETag", etagFor(fi.Size(), fi.ModTime().UnixNano()))
	w.Header().Set("Accept-Ranges", "bytes")

	if rng := r.Header.Get("Range"); rng != "" && !isSyntacticallyValidRange(rng) {
		r = stripRangeHeader(r)
	}

	http.ServeContent(w, r, filepath.Base(path), fi.ModTime(), f)
	return nil
}
