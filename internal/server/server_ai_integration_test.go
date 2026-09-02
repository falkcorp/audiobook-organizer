// file: internal/server/server_ai_integration_test.go
// version: 1.3.1
// guid: 6d5c4b3a-2918-1706-f5e4-d3c2b1a09f8e
// last-edited: 2026-09-02

package server

import (
	"bytes"
	json "encoding/json/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAIEndpoints_WithStubbedOpenAI(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Stub OpenAI Chat Completions.
	openAI := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Accept any path that ends with /chat/completions.
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		resp := map[string]any{
			"id":      "chatcmpl-test",
			"object":  "chat.completion",
			"created": 1,
			"model":   "gpt-4o-mini",
			"choices": []map[string]any{
				{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": `{"title":"The Hobbit","author":"J.R.R. Tolkien","series":"Middle Earth","series_number":1,"narrator":"Rob Inglis","publisher":"Random House","year":1937,"confidence":"high"}`,
					},
					"finish_reason": "stop",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.MarshalWrite(w, resp)
	}))
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	openAI.Listener = listener
	openAI.Start()
	t.Cleanup(openAI.Close)

	// Configure AI parsing to be enabled and point at stub server.
	// (AppConfig is fully restored by defer cleanup() from setupTestServer.)
	config.AppConfig.EnableAIParsing = true
	config.AppConfig.OpenAIAPIKey = "test-key"
	config.AppConfig.OpenAIBaseURL = openAI.URL + "/v1"

	// 1) POST /ai/parse-filename should succeed.
	parseBody := bytes.NewBufferString(`{"filename":"The Hobbit - J.R.R. Tolkien"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ai/parse-filename", parseBody)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	// Response is wrapped in {"data": ...}
	var parseResp struct {
		Data struct {
			Metadata map[string]any `json:"metadata"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &parseResp))

	// 2) POST /ai/test-connection should succeed using provided api_key.
	connBody := bytes.NewBufferString(`{"api_key":"test-key"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/ai/test-connection", connBody)
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	// Response is wrapped in {"data": ...}
	var connResp struct {
		Data struct {
			Success bool   `json:"success"`
			Message string `json:"message"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &connResp))
	require.True(t, connResp.Data.Success)

	// 3) POST /audiobooks/:id/parse-with-ai should update a book.
	filePath := filepath.Join(t.TempDir(), "The Hobbit - J.R.R. Tolkien.m4b")
	require.NoError(t, os.WriteFile(filePath, []byte("audio"), 0o644))
	book, err := database.GetGlobalStore().CreateBook(&database.Book{Title: "Old", FilePath: filePath, Format: "m4b"})
	require.NoError(t, err)

	req = httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/"+book.ID+"/parse-with-ai", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	// Response is wrapped in {"data": ...}
	var parseABResp struct {
		Data struct {
			Message    string         `json:"message"`
			Book       map[string]any `json:"book"`
			Confidence string         `json:"confidence"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &parseABResp))

	updated, err := database.GetGlobalStore().GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	assert.Equal(t, "The Hobbit", updated.Title)
	require.NotNil(t, updated.AuthorID)
}
