<!-- file: changelog.d/20260729_020000_range_file_serving.md -->
<!-- version: 1.0.0 -->
<!-- guid: f4e2c9a1-7b3d-4a6e-9c8f-1d5e7a3b2c9f -->
<!-- last-edited: 2026-07-29 -->

### Added

#### HTTP Range (byte-range) file-serving helper (`internal/httputil`)

Added `ServeFileWithRange` (protocol-agnostic, `http.ResponseWriter`/`*http.Request`)
and a thin `ServeFileWithRangeGin` adapter for handlers holding a `*gin.Context`. This
is new plumbing only — no route registers it yet — but it closes the biggest playback
gap in the project: until now the server had no Range-capable audio serving at all,
only a transcoded ffmpeg *sample* endpoint (`internal/server/audio_sample.go`) that
isn't reusable for real playback. Seeking in a large `.m4b`/`.m4a`/`.mp3`/`.flac`/`.opus`
file and resumable downloads both require correct `206 Partial Content` handling.

Implementation delegates to the standard library's `http.ServeContent` for the actual
Range/If-Range/If-None-Match/206/416/multipart-byteranges logic (battle-tested,
avoids reimplementing range parsing), adding: extension-based `Content-Type` sniffing
tuned for audiobook formats (`.m4b`/`.m4a` → `audio/mp4`, `.mp3` → `audio/mpeg`,
`.flac` → `audio/flac`, `.opus` → `audio/ogg`), a cheap `"<size>-<mtime-unixnano>"`
`ETag`, and `Accept-Ranges: bytes` on every response. One deliberate correction over
the stdlib default: a syntactically malformed `Range` header is now ignored (served as
a full `200`) per RFC 9110 §14.2, rather than stdlib's default of returning `416` for
both malformed and well-formed-but-unsatisfiable ranges — a pre-validation pass strips
the header before handoff only when it fails to parse as a byte-ranges-specifier, so a
genuinely out-of-bounds (but well-formed) range still gets the correct `416`.

The helper takes an already-resolved absolute path and validates it resolves (after
following symlinks) to a regular file; it performs no authorization or path-containment
checks beyond that, and the doc comment is explicit that callers must confine the path
to an allowed root themselves — this repo has a history of path-injection findings.

Covered by 25 tests including exact-byte assertions against a deterministic fixture and
one integration test against the real 115 MB committed `odyssey_complete.m4b` fixture
that proves a mid-file range read matches a direct `os.ReadAt` at the same offset.
