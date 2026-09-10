// file: internal/server/handlers/openai_validate.go
// version: 1.0.0
// guid: f02610a7-f033-47cc-b457-d097778bd858
// last-edited: 2026-09-10

package handlers

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// openAIModelsURL is the ONLY endpoint this handler will ever contact. It is a
// compile-time constant on purpose: nothing from the request may influence the
// outbound URL, so a caller cannot turn this endpoint into a request-forgery
// primitive by supplying a base URL of their own.
//
// The app config does carry an OpenAIBaseURL override, but the setup wizard has
// never used it (it hardcoded api.openai.com in the browser), so honoring it
// here would be a behaviour change rather than a move. Left alone deliberately.
const openAIModelsURL = "https://api.openai.com/v1/models"

// validateOpenAIKeyTimeout bounds the whole upstream round trip. The wizard
// blocks a button on this call, so it must fail fast rather than hang.
const validateOpenAIKeyTimeout = 10 * time.Second

// validateOpenAIKeyRequest is the wizard's payload. The key is used once, for
// one outbound Authorization header, and is never logged, stored, or echoed.
type validateOpenAIKeyRequest struct {
	APIKey string `json:"api_key"`
}

// validateOpenAIKeyResponse is what the wizard sees. Error is a fixed,
// non-secret reason code — never an upstream body and never the key, because
// OpenAI's own 401 body quotes the rejected key back at you.
type validateOpenAIKeyResponse struct {
	Valid bool   `json:"valid"`
	Error string `json:"error,omitempty"`
}

// ValidateOpenAIKey proxies the setup wizard's "Test Connection" check.
//
// SEC-9: the wizard used to call api.openai.com straight from the browser with
// the user's typed key, which put the raw credential in the browser network
// log, in reach of any extension with request-access, and in front of a
// corporate TLS-inspecting proxy. Doing the call here keeps the key on the
// server side of that boundary.
func ValidateOpenAIKey(c *gin.Context) {
	validateOpenAIKey(c, openAIModelsURL, validateOpenAIKeyTimeout)
}

// validateOpenAIKey is the testable body. baseURL and timeout are parameters
// only so a test can stand an httptest server in for OpenAI; the exported entry
// point above always passes the constants.
func validateOpenAIKey(c *gin.Context, baseURL string, timeout time.Duration) {
	var req validateOpenAIKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// Note the error is NOT interpolated: a malformed body may itself be a
		// half-typed key, and binding errors quote the offending input.
		c.JSON(http.StatusBadRequest, validateOpenAIKeyResponse{Valid: false, Error: "invalid request body"})
		return
	}

	key := strings.TrimSpace(req.APIKey)
	if key == "" {
		c.JSON(http.StatusBadRequest, validateOpenAIKeyResponse{Valid: false, Error: "api key is required"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL, nil)
	if err != nil {
		slog.Error("openai key validation: could not build upstream request")
		c.JSON(http.StatusBadGateway, validateOpenAIKeyResponse{Valid: false, Error: "could not verify the key"})
		return
	}
	httpReq.Header.Set("Authorization", "Bearer "+key)

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		// Deliberately no err in the message or the log: a transport error can
		// carry the request URL, and future refactors could put the key there.
		slog.Warn("openai key validation: upstream request failed")
		c.JSON(http.StatusBadGateway, validateOpenAIKeyResponse{Valid: false, Error: "could not reach the OpenAI API"})
		return
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		c.JSON(http.StatusOK, validateOpenAIKeyResponse{Valid: true})
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		// The one case where "not valid" is a real answer rather than a failure
		// to find out. Everything else falls through to 502 on purpose: a 429 or
		// a 500 from OpenAI means we could not verify the key, and reporting
		// that as valid:false would tell the user to throw away a good key.
		c.JSON(http.StatusOK, validateOpenAIKeyResponse{Valid: false, Error: "the OpenAI API rejected this key"})
	default:
		slog.Warn("openai key validation: unexpected upstream status", "status", resp.StatusCode)
		c.JSON(http.StatusBadGateway, validateOpenAIKeyResponse{Valid: false, Error: "could not verify the key"})
	}
}
