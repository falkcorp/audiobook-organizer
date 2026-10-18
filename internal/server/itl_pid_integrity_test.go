// file: internal/server/itl_pid_integrity_test.go
// version: 1.0.0
// guid: 5a3c8e17-d204-4b69-8f15-c7e92b0a6d34
// last-edited: 2026-10-18

package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// TestPIDIntegrity_UnsetITLPath_SingleBadRequest: with no ITL path configured
// GET /itunes/pid-integrity answers exactly one 400 response. The handler used
// to ignore resolveITLPath's refusal and carry on, so a second JSON document
// (the census, or a 500) was appended to the 400 body.
func TestPIDIntegrity_UnsetITLPath_SingleBadRequest(t *testing.T) {
	f := setupCredGuardServer(t)
	config.AppConfig.ITunes.LibraryITLPath = ""

	w := f.do(t, f.sessionToken, http.MethodGet, "/api/v1/itunes/pid-integrity", nil)
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())

	dec := json.NewDecoder(bytes.NewReader(w.Body.Bytes()))
	var first map[string]any
	require.NoError(t, dec.Decode(&first), w.Body.String())
	assert.Contains(t, first["error"], "ITL path", "the 400 must be the not-configured error")
	assert.NotContains(t, first, "report")
	var extra json.RawMessage
	assert.ErrorIs(t, dec.Decode(&extra), io.EOF, "a second response was written after the 400: %s", w.Body.String())
}
