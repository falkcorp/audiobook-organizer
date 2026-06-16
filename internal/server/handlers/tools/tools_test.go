// file: internal/server/handlers/tools/tools_test.go
// version: 1.0.0
// guid: c5d6e7f8-a9b0-1234-cdef-234567890123
// last-edited: 2026-06-15

package toolshandler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	toolshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/tools"
	"github.com/falkcorp/audiobook-organizer/internal/tools"
)

func TestHandlerList(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &tools.ToolsConfig{Fpcalc: tools.ToolConfig{Mode: tools.ToolModeDisabled}}
	reg := tools.NewToolRegistry(cfg)
	rel, ok := tools.LatestRelease("fpcalc")
	require.True(t, ok)
	reg.Register(tools.ToolDef{Name: "fpcalc", Release: rel})

	h := toolshandler.New(reg, cfg, nil)
	r := gin.New()
	r.GET("/tools", h.List)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/tools", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp []tools.ToolStatus
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Len(t, resp, 1)
	assert.Equal(t, "fpcalc", resp[0].Name)
	assert.False(t, resp[0].Available) // disabled
}

func TestHandlerStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &tools.ToolsConfig{Fpcalc: tools.ToolConfig{Mode: tools.ToolModeDisabled}}
	reg := tools.NewToolRegistry(cfg)
	rel, ok := tools.LatestRelease("fpcalc")
	require.True(t, ok)
	reg.Register(tools.ToolDef{Name: "fpcalc", Release: rel})

	h := toolshandler.New(reg, cfg, nil)
	r := gin.New()
	r.GET("/tools/:name/status", h.Status)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/tools/fpcalc/status", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var s tools.ToolStatus
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &s))
	assert.Equal(t, "fpcalc", s.Name)
	assert.False(t, s.Available)
}

func TestHandlerInstall_UnknownTool(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &tools.ToolsConfig{}
	reg := tools.NewToolRegistry(cfg)

	h := toolshandler.New(reg, cfg, nil)
	r := gin.New()
	r.POST("/tools/:name/install", h.Install)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/tools/unknown/install", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
}
