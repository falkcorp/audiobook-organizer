// file: internal/server/handlers/audiobooks/search_limit_term_error_internal_test.go
// version: 1.0.0
// guid: 8c1491fa-2613-4958-9d68-b036739e6106
// last-edited: 2026-10-26

package audiobookshandler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/search"
)

// A search term the service refuses for every caller (the list's own pre-check is
// only the first place it is caught: a saved search run by a background op, or a
// path that reaches GetAudiobooks without the handler, gets the service's error)
// must come back as a 400 that says what to write, not as the 500 of an error nobody
// recognised. The tag: refusal is the newest of these and rides the same mapping.
func TestRespondSearchLimit_AQueryTermTheServiceRefusedIsA400WithItsReason(t *testing.T) {
	cases := map[string]string{
		"tag under OR": "title:Hush OR tag:noir",
		"tag negated":  "-tag:noir",
		"per-user OR":  "read_status:finished OR author:Kyle",
	}
	for name, q := range cases {
		t.Run(name, func(t *testing.T) {
			n, err := search.ParseQuery(q)
			require.NoError(t, err)
			termErr := search.CheckQueryTerms(n, time.Now())
			require.Error(t, termErr)
			require.True(t, search.IsQueryTermError(termErr))

			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodGet, "/audiobooks", nil)

			// The service wraps what it refuses; the mapping must see through it.
			handled := respondSearchLimit(c, fmt.Errorf("search: %w", termErr))

			assert.True(t, handled)
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			var body struct {
				Error string `json:"error"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.Contains(t, body.Error, search.QueryTermReason(termErr))
		})
	}
}
