// file: internal/server/itunes_test.go
// version: 3.0.0
// guid: 57e871fa-41b4-4fe6-9ed6-457ae78f0a07

package server

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
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

// copyLibraryWithCleanModTime copies an iTunes library XML to a temp dir
// and sets its modTime to a whole-second value so it survives RFC3339 round-trip.
func copyLibraryWithCleanModTime(t *testing.T, srcPath string) string {
	t.Helper()
	data, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("failed to read library: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "iTunes Music Library.xml")
	if err := os.WriteFile(dst, data, 0644); err != nil {
		t.Fatalf("failed to write library copy: %v", err)
	}
	cleanTime := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(dst, cleanTime, cleanTime); err != nil {
		t.Fatalf("failed to set modtime: %v", err)
	}
	return dst
}

// TestITunesSyncForceFlag_NoChanges tests that force=false returns "no changes detected"
// when the library fingerprint matches the stored fingerprint.
func TestITunesSyncForceFlag_NoChanges(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	srcPath := filepath.Join("../../testdata/itunes", "iTunes Music Library.xml")
	if _, err := os.Stat(srcPath); os.IsNotExist(err) {
		t.Skipf("iTunes test library not found at %s", srcPath)
	}

	libPath := copyLibraryWithCleanModTime(t, srcPath)

	info, err := os.Stat(libPath)
	if err != nil {
		t.Fatalf("failed to stat library file: %v", err)
	}
	err = database.GetGlobalStore().SaveLibraryFingerprint(libPath, info.Size(), info.ModTime(), 0)
	if err != nil {
		t.Fatalf("failed to save fingerprint: %v", err)
	}

	payload := map[string]any{
		"library_path": libPath,
		"force":        false,
	}
	body := marshal(t, payload)

	req := httptest.NewRequest("POST", "/api/v1/itunes/sync", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var wrapper struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &wrapper); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	resp := wrapper.Data
	msg, _ := resp["message"].(string)
	if !strings.Contains(msg, "no changes detected") {
		t.Errorf("expected 'no changes detected' in message, got %q", msg)
	}
	opID, _ := resp["operation_id"].(string)
	if opID != "" {
		t.Errorf("expected empty operation_id, got %q", opID)
	}
}

// TestITunesSyncForceFlag_Bypass tests that force=true bypasses the fingerprint check
// even when the stored fingerprint matches.
func TestITunesSyncForceFlag_Bypass(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	srcPath := filepath.Join("../../testdata/itunes", "iTunes Music Library.xml")
	if _, err := os.Stat(srcPath); os.IsNotExist(err) {
		t.Skipf("iTunes test library not found at %s", srcPath)
	}

	libPath := copyLibraryWithCleanModTime(t, srcPath)

	info, err := os.Stat(libPath)
	if err != nil {
		t.Fatalf("failed to stat library file: %v", err)
	}
	err = database.GetGlobalStore().SaveLibraryFingerprint(libPath, info.Size(), info.ModTime(), 0)
	if err != nil {
		t.Fatalf("failed to save fingerprint: %v", err)
	}

	payload := map[string]any{
		"library_path": libPath,
		"force":        true,
	}
	body := marshal(t, payload)

	req := httptest.NewRequest("POST", "/api/v1/itunes/sync", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	if w.Code != 202 {
		t.Fatalf("expected 202 (Accepted), got %d: %s", w.Code, w.Body.String())
	}

	var wrapper struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &wrapper); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	resp := wrapper.Data
	opID, _ := resp["operation_id"].(string)
	if opID == "" {
		t.Errorf("expected non-empty operation_id when force=true")
	}
	msg, _ := resp["message"].(string)
	if !strings.Contains(msg, "queued") {
		t.Errorf("expected 'queued' in message, got %q", msg)
	}
}

func marshal(t *testing.T, v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	return b
}
