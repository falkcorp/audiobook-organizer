// file: internal/server/handlers/aibackends/aibackends_test.go
// version: 1.1.0
// guid: 8d4e0f32-5b6c-4a7d-8e9f-2b3c4d5e6f7a
// last-edited: 2026-07-03

package aibackendshandler_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	aibackendshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/aibackends"
)

func TestHandlerStatusDisabledModesSkipProbe(t *testing.T) {
	gin.SetMode(gin.TestMode)

	orig := config.AppConfig
	defer func() { config.AppConfig = orig }()
	config.AppConfig = config.Config{
		AIBackend: config.AIBackendConfig{
			EmbeddingMode: config.AIBackendModeDisabled,
			LLMMode:       config.AIBackendModeDisabled,
		},
	}

	h := aibackendshandler.New(nil, nil)
	r := gin.New()
	r.GET("/ai/backends/status", h.Status)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/ai/backends/status", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var body struct {
		Data aibackendshandler.StatusResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, config.AIBackendModeDisabled, body.Data.EmbeddingMode)
	require.Equal(t, config.AIBackendModeDisabled, body.Data.LLMMode)
	require.False(t, body.Data.LocalReachable)
	require.Nil(t, body.Data.EmbeddingModel)
	require.Nil(t, body.Data.LLMModel)
}

func TestHandlerStatusLocalModeUnreachableEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)

	orig := config.AppConfig
	defer func() { config.AppConfig = orig }()
	config.AppConfig = config.Config{
		AIBackend: config.AIBackendConfig{
			EmbeddingMode:       config.AIBackendModeLocal,
			LLMMode:             config.AIBackendModeDisabled,
			LocalBaseURL:        "http://127.0.0.1:1/v1", // nothing listens here
			LocalEmbeddingModel: "bge-m3",
		},
	}

	h := aibackendshandler.New(nil, nil)
	r := gin.New()
	r.GET("/ai/backends/status", h.Status)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/ai/backends/status", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var body struct {
		Data aibackendshandler.StatusResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, config.AIBackendModeLocal, body.Data.EmbeddingMode)
	require.False(t, body.Data.LocalReachable)
	require.NotEmpty(t, body.Data.FallbackReason)
	require.NotNil(t, body.Data.EmbeddingModel)
	require.Equal(t, "bge-m3", body.Data.EmbeddingModel.Name)
	require.False(t, body.Data.EmbeddingModel.Pulled)
}

func TestHandlerPullModelRequiresModel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := aibackendshandler.New(nil, nil)
	r := gin.New()
	r.POST("/ai/backends/pull-model", h.PullModel)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/ai/backends/pull-model", nil)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandlerPullModelNoRegistryReturnsUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := aibackendshandler.New(nil, nil)
	r := gin.New()
	r.POST("/ai/backends/pull-model", h.PullModel)

	w := httptest.NewRecorder()
	body := []byte(`{"model":"bge-m3"}`)
	req, _ := http.NewRequest(http.MethodPost, "/ai/backends/pull-model", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestHandlerPullModelRejectsArgvInjection(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := aibackendshandler.New(nil, nil)
	r := gin.New()
	r.POST("/ai/backends/pull-model", h.PullModel)

	for _, bad := range []string{"--insecure", "-q", "a b", "a;b", "a$(x)", ":tag", "/leading", "a//b"} {
		w := httptest.NewRecorder()
		body := strings.NewReader(`{"model":"` + bad + `"}`)
		req, _ := http.NewRequest(http.MethodPost, "/ai/backends/pull-model", body)
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		require.Equalf(t, http.StatusBadRequest, w.Code, "model %q must be rejected", bad)
	}
}
