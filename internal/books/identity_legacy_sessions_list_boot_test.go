// file: internal/books/identity_legacy_sessions_list_boot_test.go
// version: 1.0.0
// guid: 0dcae09a-08e8-4951-9dd5-1d16820b5093
// last-edited: 2026-11-28

package books

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// HEAD's GET /api/v1/auth/sessions under core identity (F4-08 fix-forward). The
// sessions are core's: HEAD's handler read the books session family M3 drained
// and answered 200 with an empty list while the caller's core session was live.
// The route now answers 410 and names core's GET /api/core/v1/me/sessions,
// which lists the session.
func TestIdentityBoot_LegacySessionsListIsGoneAndNeverAnEmptyList(t *testing.T) {
	seed := &fpSeed{}
	fx := newFlipFixture(t, func(s *database.PebbleStore) { seedFootprint(t, s, seed) }, "")
	r := bootOverHEADStore(t, fx)
	h := r.app.Handler()

	ck := loginAs(t, r, "alfred.pennyworth", fpPass)

	list := doJSON(t, h, http.MethodGet, "/api/v1/auth/sessions", nil, ck)
	require.Equal(t, http.StatusGone, list.Code, "the legacy sessions list is gone under core identity: %s", list.Body.String())
	want, err := json.Marshal(map[string]string{"error": "gone", "message": "this endpoint has been removed; use GET /api/core/v1/me/sessions instead"})
	require.NoError(t, err)
	require.JSONEq(t, string(want), list.Body.String(), "it names core's sessions list and lists nothing")

	coreList := doJSON(t, h, http.MethodGet, "/api/core/v1/me/sessions", nil, ck)
	require.Equal(t, http.StatusOK, coreList.Code, coreList.Body.String())
	var page struct {
		Items []map[string]any `json:"items"`
	}
	require.NoError(t, json.Unmarshal(coreList.Body.Bytes(), &page), coreList.Body.String())
	require.NotEmpty(t, page.Items, "core's list holds the caller's live session: %s", coreList.Body.String())
}
