// file: internal/server/itunes_test.go
// version: 3.1.0
// guid: 57e871fa-41b4-4fe6-9ed6-457ae78f0a07
// last-edited: 2026-10-08

package server

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// NOTE: TestCalculatePercent moved to the handlers package alongside the
// calculatePercent helper (now unexported in internal/server/handlers/itunes.go).
// Its behavior is covered there via ITunesHandler.ImportStatus progress assertions.

// TestValidateITunesLibrary tests library validation endpoint.
func TestValidateITunesLibrary(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	libPath := filepath.Join("../../testdata/itunes", "iTunes Music Library.xml")
	if _, err := os.Stat(libPath); os.IsNotExist(err) {
		t.Skipf("iTunes test library not found at %s", libPath)
	}

	payload := map[string]any{
		"library_path": libPath,
	}
	body := marshal(t, payload)

	req := httptest.NewRequest("POST", "/api/v1/itunes/validate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	if w.Code != 200 && w.Code != 400 {
		t.Errorf("unexpected status code: %d, body: %s", w.Code, w.Body.String())
	}
}

func marshal(t *testing.T, v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	return b
}
