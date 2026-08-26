// file: internal/server/handlers/abs/file_not_found_reason_test.go
// version: 1.0.0
// guid: 8b3f0d92-47a1-4e6c-95d8-2c710fa6e534
// last-edited: 2026-08-17

package abs_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// captureLogs redirects the default slog logger into a buffer for the duration of
// one test and restores it afterwards.
//
// 🔴 THE LOG IS THE ONLY OBSERVABLE. Every one of these failures answers the
// client with an identical 404 and an identical "file not found" body — that is
// the protocol contract and it is deliberately unchanged. So a test that asserted
// on the response could not tell the five cases apart either, which is precisely
// the defect under repair. The log is the surface being tested.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// seedFileServing builds one book with one book_file whose path DOES NOT EXIST,
// plus the sync file that addresses it, and returns the seed with the book id and
// the minted ino.
//
// The missing path is deliberately inside a real t.TempDir() rather than something
// like "/nope": the parent directory exists and only the file is absent, which is
// the exact live shape — a row naming a destination under a tree that is genuinely
// mounted, where the bytes were never written.
func seedFileServing(t *testing.T) (seed *oracleSeed, itemID, ino, missingPath string) {
	t.Helper()
	seed = seedOracleLibrary(t)

	intp := func(i int) *int { return &i }
	strp := func(s string) *string { return &s }
	boolp := func(b bool) *bool { return &b }

	const bookID = "fnf-book"
	missingPath = filepath.Join(t.TempDir(), "never-written.m4b")
	seed.lib.addBook(&database.Book{
		ID: bookID, Title: "File Not Found Book",
		Duration:     intp(3600),
		LibraryState: strp("organized"), IsPrimaryVersion: boolp(true),
	}, []database.BookFile{{ID: "fnf-file", BookID: bookID, FilePath: missingPath}}, nil)

	id, err := seed.lib.MintOrGetSyncFileID(bookID, "fnf-file")
	if err != nil {
		t.Fatalf("MintOrGetSyncFileID: %v", err)
	}
	// The route addresses an item by its SYNC id, not the internal book id — the
	// same indirection the client sees.
	return seed, mustSyncID(t, seed, bookID), id, missingPath
}

// logReason extracts the reason= value from the captured log, or "" if the log
// recorded no file-not-found at all.
func logReason(logs string) string {
	for line := range strings.SplitSeq(logs, "\n") {
		if !strings.Contains(line, "abs: file not found") {
			continue
		}
		for f := range strings.FieldsSeq(line) {
			if after, ok := strings.CutPrefix(f, "reason="); ok {
				return after
			}
		}
	}
	return ""
}

// harnessFor wires the seed into an authenticated harness. The file routes sit
// behind the ABS auth middleware, so an unauthenticated probe answers 401 and never
// reaches the code under test at all.
func harnessFor(t *testing.T, seed *oracleSeed) (*harness, string) {
	t.Helper()
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(fixtureUserData()))
	h.seedUser(t, "u1", "oracle", "", "pw-pw-pw-pw")
	body := h.login(t, "oracle", "pw-pw-pw-pw")
	return h, str(t, userObj(t, body), "accessToken")
}

// 🔴 THE CASE THAT ACTUALLY FIRES IN PRODUCTION. 41.8% of book_file rows point at
// bytes that are not on disk, and every one of those downloads used to 404 in
// silence. This is the assertion that a real "can't find the file" report is now
// self-diagnosing.
func TestFileNotFound_MissingBytesAreLoggedAsSuch(t *testing.T) {
	seed, itemID, ino, missingPath := seedFileServing(t)
	logs := captureLogs(t)
	h, tok := harnessFor(t, seed)

	res, _ := h.do(t, request{method: http.MethodGet, path: "/api/items/" + itemID + "/file/" + ino + "/download", headers: bearer(tok)})
	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.Code)
	}
	if got := logReason(logs.String()); got != "bytes_missing" {
		t.Errorf("logged reason = %q, want %q\nlogs:\n%s", got, "bytes_missing", logs.String())
	}
	// The served path is the single most useful fact for the next person, because it
	// names WHICH tree the row pointed into.
	if !strings.Contains(logs.String(), filepath.Base(missingPath)) {
		t.Errorf("log does not name the path that was missing\nlogs:\n%s", logs.String())
	}
}

// 🔴 THE DISCRIMINATION IS THE WHOLE POINT. A different failure must produce a
// DIFFERENT reason — otherwise the field is decoration and the next investigation
// is no better off than the four production probes this replaces.
func TestFileNotFound_UnknownInoIsADifferentReason(t *testing.T) {
	seed, itemID, _, _ := seedFileServing(t)
	logs := captureLogs(t)
	h, tok := harnessFor(t, seed)

	res, _ := h.do(t, request{method: http.MethodGet, path: "/api/items/" + itemID + "/file/sf-does-not-exist/download", headers: bearer(tok)})
	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.Code)
	}
	if got := logReason(logs.String()); got != "no_syncfile" {
		t.Errorf("logged reason = %q, want %q\nlogs:\n%s", got, "no_syncfile", logs.String())
	}
}

// The client-visible answer must NOT change. The finer distinction is for the
// server operator; a client can do nothing with it, and ABS clients key off the
// status and body.
func TestFileNotFound_ClientAnswerIsUnchanged(t *testing.T) {
	seed, itemID, ino, _ := seedFileServing(t)
	_ = captureLogs(t)
	h, tok := harnessFor(t, seed)

	missing, _ := h.do(t, request{method: http.MethodGet, path: "/api/items/" + itemID + "/file/" + ino + "/download", headers: bearer(tok)})
	unknown, _ := h.do(t, request{method: http.MethodGet, path: "/api/items/" + itemID + "/file/sf-does-not-exist/download", headers: bearer(tok)})

	if missing.Code != unknown.Code {
		t.Errorf("status differs between causes: %d vs %d — the client answer must stay identical",
			missing.Code, unknown.Code)
	}
	if missing.Body.String() != unknown.Body.String() {
		t.Errorf("body differs between causes:\n  %q\n  %q", missing.Body.String(), unknown.Body.String())
	}
	if !strings.Contains(missing.Body.String(), "file not found") {
		t.Errorf("body = %q, want it to still say \"file not found\"", missing.Body.String())
	}
}
