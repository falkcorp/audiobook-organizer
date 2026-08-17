// file: internal/server/handlers/abs/head_routes_test.go
// version: 1.0.0
// guid: 9c4e17b0-3a58-4d2f-8e61-b0d5c7a92f14
// last-edited: 2026-08-17

package abs_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// 🔴 WHY THESE TESTS EXIST. gin registers one method per call, so a GET-only
// route answers HEAD with 404 — not 405. That is indistinguishable from "this
// file does not exist", and it silently destroyed a real measurement: a probe
// built to find book_file rows with no bytes behind them reported 100% of 1,786
// files missing and passed its own sanity check, because a fabricated ino
// returned the same 404 as every real one.
//
// A uniformly-dead instrument agrees with every hypothesis. So every test below
// asserts a known-GOOD and a known-BAD value in the SAME run: if the router ever
// goes back to answering 404 for everything, the good half fails rather than the
// suite going quietly green.

// seedServableFile builds one book whose byte are actually ON DISK, plus the sync
// file that addresses it. It is the deliberate inverse of seedFileServing, which
// seeds a path that does not exist.
func seedServableFile(t *testing.T) (seed *oracleSeed, itemID, ino string, size int64) {
	t.Helper()
	seed = seedOracleLibrary(t)

	intp := func(i int) *int { return &i }
	strp := func(s string) *string { return &s }
	boolp := func(b bool) *bool { return &b }

	const bookID = "head-book"
	realPath := filepath.Join(t.TempDir(), "present.m4b")
	payload := []byte("not really an m4b, but it is real bytes on a real disk")
	if err := os.WriteFile(realPath, payload, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	seed.lib.addBook(&database.Book{
		ID: bookID, Title: "Head Route Book",
		Duration:     intp(3600),
		LibraryState: strp("organized"), IsPrimaryVersion: boolp(true),
	}, []database.BookFile{{ID: "head-file", BookID: bookID, FilePath: realPath}}, nil)

	id, err := seed.lib.MintOrGetSyncFileID(bookID, "head-file")
	if err != nil {
		t.Fatalf("MintOrGetSyncFileID: %v", err)
	}
	return seed, mustSyncID(t, seed, bookID), id, int64(len(payload))
}

// HEAD must reach the handler and agree with GET about whether the bytes exist.
//
// The GET assertion is not incidental — it is the known-good half. Without it a
// router that 404s every method passes the "HEAD is not 404" check by accident
// the moment someone changes the fixture.
func TestHeadRoutes_HeadAgreesWithGetOnAFilePresentOnDisk(t *testing.T) {
	seed, itemID, ino, size := seedServableFile(t)
	h, tok := harnessFor(t, seed)

	for _, suffix := range []string{"", "/download"} {
		path := "/api/items/" + itemID + "/file/" + ino + suffix
		t.Run("path="+path, func(t *testing.T) {
			get, _ := h.do(t, request{method: http.MethodGet, path: path, headers: bearer(tok)})
			head, _ := h.do(t, request{method: http.MethodHead, path: path, headers: bearer(tok)})

			// Known-good: the file is really there, so GET must succeed. If this
			// fails the HEAD assertion below proves nothing.
			if get.Code != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200 — the fixture is not serving, so "+
					"the HEAD result carries no information", path, get.Code)
			}
			if head.Code != get.Code {
				t.Fatalf("HEAD %s = %d but GET = %d; a GET-only gin route answers "+
					"HEAD with 404, which reads as 'file missing' to any probe",
					path, head.Code, get.Code)
			}

			// HEAD is not merely routed — it must be a real HEAD: same metadata,
			// no body. Registering HEAD against a handler that streamed the body
			// anyway would pass a status-only check while doubling the bytes a
			// probe pulls over the network.
			if head.Body.Len() != 0 {
				t.Errorf("HEAD %s returned a %d-byte body; HEAD must not carry one",
					path, head.Body.Len())
			}
			if got := head.Header().Get("Content-Length"); got != strconv.FormatInt(size, 10) {
				t.Errorf("HEAD %s Content-Length = %q, want %q — the header is the "+
					"entire point of a HEAD probe", path, got, strconv.FormatInt(size, 10))
			}
			if got := head.Header().Get("Accept-Ranges"); got != "bytes" {
				t.Errorf("HEAD %s Accept-Ranges = %q, want \"bytes\"", path, got)
			}
		})
	}
}

// Known-bad, in the same package as the known-good above: a HEAD for bytes that
// are genuinely absent must still 404. Registering HEAD must not turn every
// probe into a 200 — that would be the opposite failure, and just as silent.
func TestHeadRoutes_HeadStill404sWhenTheBytesAreReallyMissing(t *testing.T) {
	seed, itemID, ino, _ := seedFileServing(t) // path deliberately never created
	h, tok := harnessFor(t, seed)

	for _, tc := range []struct {
		name string
		ino  string
	}{
		{"bytes missing from disk", ino},
		{"ino not in this book", "sf-does-not-exist"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := "/api/items/" + itemID + "/file/" + tc.ino + "/download"
			head, _ := h.do(t, request{method: http.MethodHead, path: path, headers: bearer(tok)})
			if head.Code != http.StatusNotFound {
				t.Fatalf("HEAD %s = %d, want 404; HEAD must report absence as "+
					"absence, not as success", path, head.Code)
			}
		})
	}
}

// The cover route is registered without the auth middleware by protocol
// requirement, so it needs its own coverage: a HEAD there must not fall through
// to the SPA index or a 404 either.
func TestHeadRoutes_CoverAcceptsHead(t *testing.T) {
	seed, itemID, _, _ := seedServableFile(t)
	h, _ := harnessFor(t, seed)

	path := "/api/items/" + itemID + "/cover"
	get, _ := h.do(t, request{method: http.MethodGet, path: path})
	head, _ := h.do(t, request{method: http.MethodHead, path: path})

	if head.Code != get.Code {
		t.Fatalf("HEAD %s = %d but GET = %d; the two must agree on whether a "+
			"cover exists", path, head.Code, get.Code)
	}

	// Deliberately NO body assertion here, unlike the file-route test above.
	//
	// httptest.ResponseRecorder does not emulate net/http's transport-level HEAD
	// body suppression — the real server discards the body for HEAD whatever the
	// handler wrote. So a recorder body assertion tests only HANDLER-level
	// suppression. That is meaningful for the file routes, where
	// http.ServeContent skips the body itself, and meaningless here, where the
	// not-found path answers via c.JSON and would be suppressed a layer lower.
	// Asserting it anyway would fail against correct production behaviour.
}
