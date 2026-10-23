// file: internal/server/middleware/request_size_test.go
// version: 1.1.0
// guid: 8f5ed221-2f04-49aa-86f7-f63fa1732b2d
// last-edited: 2026-10-23

package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestMethodHasBody(t *testing.T) {
	t.Parallel()

	assert.True(t, methodHasBody(http.MethodPost))
	assert.True(t, methodHasBody(http.MethodPut))
	assert.True(t, methodHasBody(http.MethodPatch))
	assert.False(t, methodHasBody(http.MethodGet))
	assert.False(t, methodHasBody(http.MethodDelete))
}

func TestSelectBodyLimit(t *testing.T) {
	t.Parallel()

	assert.Equal(t, int64(10), selectBodyLimit("/api/v1/backup/create", 1, 10))
	assert.Equal(t, int64(10), selectBodyLimit("/api/v1/import/file", 1, 10))
	assert.Equal(t, int64(1), selectBodyLimit("/api/v1/config", 1, 10))
	assert.Equal(t, int64(10), selectBodyLimit("/api/v1/master-lists", 1, 10))
	assert.Equal(t, int64(1), selectBodyLimit("/api/v1/master-lists/other", 1, 10))
}

func TestMaxRequestBodySize_Middleware(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(MaxRequestBodySize(8, 16))
	router.POST("/api/v1/config", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.POST("/api/v1/import/file", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.GET("/api/v1/config", func(c *gin.Context) { c.Status(http.StatusOK) })

	// JSON endpoint over limit should be rejected.
	jsonPayload := bytes.Repeat([]byte("a"), 9)
	jsonReq := httptest.NewRequest(http.MethodPost, "/api/v1/config", bytes.NewReader(jsonPayload))
	jsonResp := httptest.NewRecorder()
	router.ServeHTTP(jsonResp, jsonReq)
	assert.Equal(t, http.StatusRequestEntityTooLarge, jsonResp.Code)

	// Upload endpoint can accept larger payload due upload limit.
	uploadPayload := bytes.Repeat([]byte("b"), 12)
	uploadReq := httptest.NewRequest(http.MethodPost, "/api/v1/import/file", bytes.NewReader(uploadPayload))
	uploadResp := httptest.NewRecorder()
	router.ServeHTTP(uploadResp, uploadReq)
	assert.Equal(t, http.StatusOK, uploadResp.Code)

	// Methods without request bodies should pass untouched.
	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
	getResp := httptest.NewRecorder()
	router.ServeHTTP(getResp, getReq)
	assert.Equal(t, http.StatusOK, getResp.Code)
}
