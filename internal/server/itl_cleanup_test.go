// file: internal/server/itl_cleanup_test.go
// version: 1.0.0
// guid: 2f6a8c14-9b3d-4e71-8a05-6c1d3f9b7e42
// last-edited: 2026-09-10

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/gin-gonic/gin"
)

// fixtureITLSource is the checked-in static ITL fixture also used by
// internal/itunes's own parser tests. Track 100 ("The Hobbit") has persistent
// ID bytes {0xAB,0xCD,0x12,0x34,0xEF,0x56,0x78,0x01}, which ParseITL exposes
// as the hex PID "ABCD1234EF567801" (internal/itunes/itl_test.go fixtureTracks).
const fixtureITLSource = "../itunes/testdata/test_library.itl"

const fixtureHobbitPID = "ABCD1234EF567801"

// setupCleanupMergedTestServer wires an in-memory Pebble-backed *Server, points
// ITunes.LibraryWritePath at a private copy of the static ITL fixture, and
// restores the prior config value on cleanup. Returns the server and the path
// to the writable fixture copy so tests can assert on-disk bytes.
func setupCleanupMergedTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	pebblePath := filepath.Join(t.TempDir(), "pebble")
	store, err := database.NewPebbleStoreInMemory(pebblePath)
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	srv := NewServer(store)

	fixtureBytes, err := os.ReadFile(fixtureITLSource)
	if err != nil {
		t.Fatalf("read fixture ITL: %v", err)
	}
	itlPath := filepath.Join(t.TempDir(), "iTunes Library.itl")
	if err := os.WriteFile(itlPath, fixtureBytes, 0o644); err != nil {
		t.Fatalf("write fixture ITL copy: %v", err)
	}

	orig := config.AppConfig.ITunes.LibraryWritePath
	config.AppConfig.ITunes.LibraryWritePath = itlPath
	t.Cleanup(func() { config.AppConfig.ITunes.LibraryWritePath = orig })

	return srv, itlPath
}

func doCleanupMergedRequest(srv *Server, query string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/itunes/cleanup-merged"+query, nil)
	srv.cleanupMergedHandler(c)
	return w
}

// TestCleanupMergedHandler_DryRun_StillReturnsPreview is the anti-over-suppression
// check — proves the retirement did not also kill the harmless measurement path.
// A known-good dry_run=true request still returns 200 with the preview payload,
// with no book_files seeded (so the computed ops set is empty — one of the
// acceptance criteria's required cases).
func TestCleanupMergedHandler_DryRun_StillReturnsPreview(t *testing.T) {
	srv, itlPath := setupCleanupMergedTestServer(t)
	before, err := os.ReadFile(itlPath)
	if err != nil {
		t.Fatalf("read fixture before request: %v", err)
	}

	w := doCleanupMergedRequest(srv, "?dry_run=true")

	if w.Code != http.StatusOK {
		t.Fatalf("dry_run=true status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			DryRun  bool `json:"dry_run"`
			Preview struct {
				TracksInITL int `json:"tracks_in_itl"`
			} `json:"preview"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, w.Body.String())
	}
	if !resp.Data.DryRun {
		t.Errorf("dry_run in response = false, want true")
	}
	if resp.Data.Preview.TracksInITL == 0 {
		t.Errorf("preview.tracks_in_itl = 0, want > 0 (fixture has 9 tracks)")
	}

	after, err := os.ReadFile(itlPath)
	if err != nil {
		t.Fatalf("read fixture after request: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("dry_run must never write the ITL file; bytes changed")
	}
}

// TestCleanupMergedHandler_Apply_RefusesAndNeverWrites proves the apply path
// (dry_run=false, or the param omitted) is a structural refusal, not a
// conditional one: it never reaches itunesservice.SafeWriteITL (that import is
// gone from itl_cleanup.go entirely — grep -n 'SafeWriteITL' returns zero
// hits) and it never mutates the on-disk .itl file. Covers both the non-empty
// ops case (a seeded merged/non-primary book_file whose PID is NOT also owned
// by a primary — the case the old handler would have actually removed) and the
// empty ops case (no seeded book_files — the deleted `ops.IsEmpty()` branch's
// case, which the pre-fix handler answered with 200 "applied": true).
func TestCleanupMergedHandler_Apply_RefusesAndNeverWrites(t *testing.T) {
	cases := []struct {
		name     string
		query    string
		nonEmpty bool // seed a book_file that makes the computed ops set non-empty
	}{
		{"omitted dry_run, empty ops", "", false},
		{"dry_run=false, empty ops", "?dry_run=false", false},
		{"omitted dry_run, non-empty ops", "", true},
		{"dry_run=false, non-empty ops", "?dry_run=false", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, itlPath := setupCleanupMergedTestServer(t)

			if tc.nonEmpty {
				store := srv.storeForWiring()
				nonPrimary := false
				if _, err := store.CreateBook(&database.Book{
					ID:               "merged-loser",
					Title:            "The Hobbit (merged duplicate)",
					FilePath:         "/x/merged-loser.m4b",
					IsPrimaryVersion: &nonPrimary,
				}); err != nil {
					t.Fatalf("seed book: %v", err)
				}
				if err := store.CreateBookFile(&database.BookFile{
					ID:                 "merged-loser-file",
					BookID:             "merged-loser",
					FilePath:           "/x/merged-loser.m4b",
					ITunesPersistentID: fixtureHobbitPID,
				}); err != nil {
					t.Fatalf("seed book_file: %v", err)
				}
			}

			before, err := os.ReadFile(itlPath)
			if err != nil {
				t.Fatalf("read fixture before request: %v", err)
			}

			w := doCleanupMergedRequest(srv, tc.query)

			// Behavioral assertion that fails on the pre-fix handler: the old
			// code returned 200 "applied":true (empty ops) or attempted a real
			// SafeWriteITL call (non-empty ops, 200 on success / 500 on
			// failure) — never 410.
			if w.Code != http.StatusGone {
				t.Fatalf("%s: status = %d, want %d (Gone); body=%s", tc.name, w.Code, http.StatusGone, w.Body.String())
			}
			var resp struct {
				Data struct {
					Applied bool   `json:"applied"`
					Error   string `json:"error"`
				} `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v; body=%s", err, w.Body.String())
			}
			if resp.Data.Applied {
				t.Errorf("%s: applied = true, want false", tc.name)
			}
			if resp.Data.Error != cleanupMergedApplyRetiredMessage {
				t.Errorf("%s: error = %q, want %q", tc.name, resp.Data.Error, cleanupMergedApplyRetiredMessage)
			}

			after, err := os.ReadFile(itlPath)
			if err != nil {
				t.Fatalf("read fixture after request: %v", err)
			}
			if string(before) != string(after) {
				t.Errorf("%s: apply path must never write the ITL file; bytes changed", tc.name)
			}
		})
	}
}
