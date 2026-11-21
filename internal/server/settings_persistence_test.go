// file: internal/server/settings_persistence_test.go
// version: 1.3.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-11-19

package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAPIKeyPersistenceRoundtrip is the secret's write-only round trip through
// PUT and GET /config: the plaintext goes in, takes effect, and never comes
// back. The old test's storage legs (the encrypted openai_api_key row, the
// config_blob, the reload through LoadConfigFromDatabase) belonged to the update
// service; under F3-07 the secrets store owns them (core/secrets, and the
// appconfig Engine's secret tests), and no route test boots that store.
func TestAPIKeyPersistenceRoundtrip(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	const plaintext = "sk-test-1234567890abcdef"
	body, _ := json.Marshal(map[string]any{"set": map[string]any{
		"openai_api_key":    plaintext,
		"enable_ai_parsing": true,
	}})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), plaintext, "PUT must not echo a secret")

	assert.Equal(t, plaintext, config.AppConfig.OpenAIAPIKey, "API key should be in effect after PUT")
	assert.True(t, config.AppConfig.EnableAIParsing)

	// GET: the key is a secret entry with metadata and no value, anywhere in the body.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	assert.NotContains(t, w.Body.String(), plaintext, "GET must never carry a secret's value")
	assert.NotContains(t, w.Body.String(), "****", "no masked placeholder either: there is nothing to echo back")

	var got struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	entry, ok := configKey(got.Data, "openai_api_key")
	require.True(t, ok, "the secret has an entry so the page can show its status")
	assert.Equal(t, "s", entry["class"])
	assert.NotContains(t, entry, "value")
	meta, _ := entry["secret"].(map[string]any)
	require.NotNil(t, meta)
	assert.Equal(t, true, meta["set"])

	enableAI, ok := configKey(got.Data, "enable_ai_parsing")
	require.True(t, ok)
	assert.Equal(t, true, enableAI["value"])

	// Clearing is explicit: an unset, never an empty echo.
	body, _ = json.Marshal(map[string]any{"unset": []string{"openai_api_key"}})
	req = httptest.NewRequest(http.MethodPut, "/api/v1/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Empty(t, config.AppConfig.OpenAIAPIKey)
}

// TestOLDumpUpload tests that the OpenLibrary dump upload endpoint works.
func TestOLDumpUpload(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Set up a dump dir
	config.AppConfig.OpenLibraryDumpDir = t.TempDir()

	// Create a multipart form with a small .gz file
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	err := writer.WriteField("type", "editions")
	require.NoError(t, err)

	part, err := writer.CreateFormFile("file", "ol_dump_editions_latest.txt.gz")
	require.NoError(t, err)

	// Write some fake gzipped content (doesn't need to be valid gzip for upload test)
	fakeContent := strings.Repeat("fake gzip data", 100)
	_, err = io.WriteString(part, fakeContent)
	require.NoError(t, err)

	err = writer.Close()
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/openlibrary/upload", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	t.Logf("Upload response: status=%d body=%s", w.Code, w.Body.String())
	assert.Equal(t, http.StatusOK, w.Code, "Upload should succeed")

	var envelope struct {
		Data map[string]any `json:"data"`
	}
	err = json.Unmarshal(w.Body.Bytes(), &envelope)
	require.NoError(t, err)
	assert.Equal(t, "dump file uploaded", envelope.Data["message"])
	assert.Equal(t, "editions", envelope.Data["type"])
}

// TestOLDumpUploadBadType tests upload with invalid dump type.
func TestOLDumpUploadBadType(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	_ = writer.WriteField("type", "invalid")
	part, _ := writer.CreateFormFile("file", "test.txt.gz")
	_, _ = io.WriteString(part, "data")
	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/openlibrary/upload", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestOLDumpUploadNoFile tests upload without a file.
func TestOLDumpUploadNoFile(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	_ = writer.WriteField("type", "editions")
	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/openlibrary/upload", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestOLDumpUploadBadExtension tests upload with wrong file extension.
func TestOLDumpUploadBadExtension(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	config.AppConfig.OpenLibraryDumpDir = t.TempDir()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	_ = writer.WriteField("type", "editions")
	part, _ := writer.CreateFormFile("file", "test.txt")
	_, _ = io.WriteString(part, "data")
	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/openlibrary/upload", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestOLDumpDirAutoDerive tests that the dump dir is auto-derived from RootDir.
func TestOLDumpDirAutoDerive(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	_ = server

	// Clear the dump dir, set root dir
	config.AppConfig.OpenLibraryDumpDir = ""
	config.AppConfig.RootDir = "/tmp/test-audiobooks"

	dir := metafetch.GetOLDumpDir()
	assert.Equal(t, "/tmp/test-audiobooks/openlibrary-dumps", dir)
}

// GET /config must not serve the eleven settings 01-P80 removed, and what GET
// serves must be acceptable back: the page PUTs the keys it changed, so the
// round trip is "every writable key's served value, sent back, is accepted".
// (HEAD seeded a pre-01-P80 config_blob through LoadConfigFromDatabase; F3-07
// deleted that loader. Decoding such a blob is internal/config's
// TestConfigBlob_RemovedUnreadSettingsDecode.)
func TestConfigRoundTrip_ServedValuesAcceptedWithoutRemovedKeys(t *testing.T) {
	f := setupCredGuardServer(t)

	w := f.do(t, f.sessionToken, http.MethodGet, "/api/v1/config", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var got struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got), w.Body.String())
	require.NotEmpty(t, got.Data["keys"], w.Body.String())

	for _, k := range []string{"metadata_review_default_view", "default_user_quota_gb", "memory_limit_type",
		"cache_size", "memory_limit_percent", "memory_limit_mb", "log_format", "enable_json_logging",
		"auth_rate_limit_per_minute", "language", "api_keys"} {
		_, ok := configKey(got.Data, k)
		assert.False(t, ok, "GET /config still serves removed key %s", k)
	}

	// Send every plain key's served value back: all of it must be accepted.
	set := map[string]any{}
	for _, raw := range got.Data["keys"].([]any) {
		k := raw.(map[string]any)
		if k["class"] != "k" || k["locked_by"] != nil {
			continue
		}
		set[k["name"].(string)] = k["value"]
	}
	require.NotEmpty(t, set)
	w = f.do(t, f.sessionToken, http.MethodPut, "/api/v1/config", map[string]any{"set": set})
	assert.Equal(t, http.StatusOK, w.Code, "PUT of the served values: %s", w.Body.String())
}
