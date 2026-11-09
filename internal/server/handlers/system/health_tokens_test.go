// file: internal/server/handlers/system/health_tokens_test.go
// version: 1.0.2
// guid: 06a9de9e-cc8c-4b3d-ae81-fafb45892f6f
// last-edited: 2026-11-08

package system

// Which degraded tokens an unauthenticated caller may see (05-health 5H.3.1,
// 5H.9). /health is unauthenticated, so its `degraded` list carries only a
// fixed set; every posture token is reported on the authenticated
// GET /system/status `degraded` field and on no public endpoint.
//
// TestHealth_UnauthenticatedTokenSetGolden is a table of release cells. R0-06
// created it with the R0 cell; R1-24, F2-10 and F8-04 add their cells (and
// their tokens to the golden set where 5H.3.1 allows it) without rewriting
// the test.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	systemmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/system/mocks"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/falkcorp/audiobook-organizer/internal/startup"
	"github.com/falkcorp/audiobook-organizer/internal/sysinfo"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// unauthenticatedTokenGolden is the fixed set of 05-health 5H.3.1: the only
// tokens /health's `degraded` list may ever carry. basic_gate_no_password is
// R0-05's token, listed now so its merge needs no edit here.
var unauthenticatedTokenGolden = []string{
	"memdb_warming",
	"search_index",
	"basic_gate_no_password",
}

// tokenRaise configures one cell's dependencies so that every token the code
// at that release can raise is raised at once.
type tokenRaise struct {
	store       SystemStore
	getDegraded func() DegradedSource
	posture     func() []PostureToken // R1: the storage_checkpoint_* tokens
}

// tokenCell is one release's row of the golden test.
type tokenCell struct {
	name string
	// raise raises every token the release's handler can raise. It may set
	// config.AppConfig; the test restores it after each cell.
	raise func(t *testing.T) tokenRaise
	// wantPublic are tokens the cell raises that must appear on /health
	// (each is in the golden set), proving the raise took effect.
	wantPublic []string
	// wantAuthenticatedOnly are posture tokens that must appear on
	// GET /system/status's `degraded` and never on /health.
	wantAuthenticatedOnly []string
	// everyTokenAuthenticated: from R1 the authenticated field carries
	// every token, so wantPublic must appear there too (05-health 5H.10:
	// R1 splits HEAD's fixed-token source in two).
	everyTokenAuthenticated bool
}

// fixedTokens is a DegradedSource yielding the tokens it holds.
type fixedTokens []string

func (f fixedTokens) Tokens() []string { return slices.Clone(f) }

// warmingTokenStore is a SystemStore whose memdb is still warming.
type warmingTokenStore struct {
	*systemmocks.MockSystemStore
}

func (warmingTokenStore) WarmupStatus() (bool, bool, int64) { return false, false, 0 }

var tokenCells = []tokenCell{
	{
		name: "R0 (HEAD's handler, the updater hotfix)",
		raise: func(t *testing.T) tokenRaise {
			m := systemmocks.NewMockSystemStore(t)
			m.EXPECT().CountAuthors().Return(1, nil).Maybe()
			steps := startup.New()
			_ = steps.Run("searchindex", func() error { return errors.New("index missing") })
			config.AppConfig.AutoUpdate.Enabled = true
			return tokenRaise{
				store:       warmingTokenStore{m},
				getDegraded: func() DegradedSource { return steps },
			}
		},
		wantPublic:            []string{"memdb_warming", "search_index"},
		wantAuthenticatedOnly: []string{DegradedAutoUpdatePaused},
	},
	{
		name: "R1 (the split source and the storage_checkpoint_* tokens)",
		raise: func(t *testing.T) tokenRaise {
			m := systemmocks.NewMockSystemStore(t)
			m.EXPECT().CountAuthors().Return(1, nil).Maybe()
			config.AppConfig.AutoUpdate.Enabled = true
			due := time.Date(2026, time.March, 21, 0, 0, 0, 0, time.UTC)
			// A posture token fed through the fixed source as well, so a
			// /health that stopped filtering would show it.
			src := fixedTokens{"search_index", servermiddleware.BasicGateNoPasswordToken, DegradedStorageCheckpointMode}
			return tokenRaise{
				store:       warmingTokenStore{m},
				getDegraded: func() DegradedSource { return src },
				posture: func() []PostureToken {
					return []PostureToken{
						{Token: DegradedStorageCheckpointRetained, Subject: "20260314T150926Z-ba7ca5e0", DeleteAfter: &due},
						{Token: DegradedStorageCheckpointOpen, Subject: "scratch/rehearsal-robin", AgeSeconds: 3600},
						{Token: DegradedStorageCheckpointMode, Subject: "/batcave/.mediacore-checkpoints/x"},
					}
				},
			}
		},
		wantPublic: []string{"memdb_warming", "search_index", "basic_gate_no_password"},
		wantAuthenticatedOnly: []string{
			DegradedAutoUpdatePaused, DegradedStorageCheckpointRetained, DegradedStorageCheckpointOpen, DegradedStorageCheckpointMode,
		},
		everyTokenAuthenticated: true,
	},
}

// degradedOf serves one GET through handler and returns data.degraded.
func degradedOf(t *testing.T, path string, handler gin.HandlerFunc) []string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET(path, handler)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp struct {
		Data struct {
			Degraded *[]string `json:"degraded"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotNil(t, resp.Data.Degraded, "%s has no degraded field: %s", path, w.Body.String())
	return *resp.Data.Degraded
}

func statusHandler(t *testing.T, raised tokenRaise) *Handler {
	t.Helper()
	svc := systemmocks.NewMockSystemService(t)
	svc.EXPECT().CollectSystemStatus(mock.Anything).Return(&sysinfo.SystemStatus{Status: "ok"}, nil).Maybe()
	return New(
		func() SystemStore { return raised.store },
		svc, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		raised.getDegraded,
	).WithPostureTokens(raised.posture)
}

func TestHealth_UnauthenticatedTokenSetGolden(t *testing.T) {
	for _, cell := range tokenCells {
		t.Run(cell.name, func(t *testing.T) {
			orig := config.AppConfig
			t.Cleanup(func() { config.AppConfig = orig })

			raised := cell.raise(t)
			h := statusHandler(t, raised)

			public := degradedOf(t, "/health", h.HealthCheck)
			for _, tok := range public {
				assert.Contains(t, unauthenticatedTokenGolden, tok,
					"/health carries %q, which is not in the unauthenticated set of 05-health 5H.3.1", tok)
			}
			for _, tok := range cell.wantPublic {
				assert.Contains(t, public, tok, "the cell raised %q but /health does not show it", tok)
			}
			for _, tok := range cell.wantAuthenticatedOnly {
				assert.NotContains(t, unauthenticatedTokenGolden, tok, "posture token %q is in the golden set", tok)
				assert.NotContains(t, public, tok, "posture token %q reached /health", tok)
			}

			authenticated := degradedOf(t, "/system/status", h.GetSystemStatus)
			for _, tok := range cell.wantAuthenticatedOnly {
				assert.Contains(t, authenticated, tok, "GET /system/status's degraded misses %q", tok)
			}
			if cell.everyTokenAuthenticated {
				for _, tok := range cell.wantPublic {
					assert.Contains(t, authenticated, tok, "GET /system/status's degraded misses %q, which /health carries", tok)
				}
			}
		})
	}
}

func TestSystemStatus_AutoUpdatePausedAuthenticatedOnly(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "auto_update enabled", false: "auto_update disabled"}[enabled], func(t *testing.T) {
			orig := config.AppConfig
			t.Cleanup(func() { config.AppConfig = orig })
			config.AppConfig.AutoUpdate.Enabled = enabled

			m := systemmocks.NewMockSystemStore(t)
			m.EXPECT().CountAuthors().Return(1, nil).Maybe()
			h := statusHandler(t, tokenRaise{store: m})

			authenticated := degradedOf(t, "/system/status", h.GetSystemStatus)
			public := degradedOf(t, "/health", h.HealthCheck)

			if enabled {
				assert.Equal(t, []string{DegradedAutoUpdatePaused}, authenticated)
			} else {
				assert.Empty(t, authenticated)
			}
			assert.False(t, slices.Contains(public, DegradedAutoUpdatePaused), "/health must never carry %s", DegradedAutoUpdatePaused)
			assert.Empty(t, public, "nothing else is raised in this test")
		})
	}
}

// TestSystemStatus_PostureTokenDetails (05-storage §5S.3.12): each posture
// token's subject travels GET /system/status's degraded_details, the
// retained checkpoint with its delete_after and the open entry with its age,
// and degraded_details is [] while none is raised.
func TestSystemStatus_PostureTokenDetails(t *testing.T) {
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	config.AppConfig.AutoUpdate.Enabled = false
	m := systemmocks.NewMockSystemStore(t)
	m.EXPECT().CountAuthors().Return(1, nil).Maybe()
	due := time.Date(2026, time.March, 21, 0, 0, 0, 0, time.UTC)
	raised := []PostureToken{
		{Token: DegradedStorageCheckpointRetained, Subject: "20260314T150926Z-ba7ca5e0", DeleteAfter: &due},
		{Token: DegradedStorageCheckpointOpen, Subject: "scratch/rehearsal-robin", AgeSeconds: 3600},
	}
	details := func(posture func() []PostureToken) []PostureToken {
		h := statusHandler(t, tokenRaise{store: m, posture: posture})
		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.GET("/system/status", h.GetSystemStatus)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/system/status", nil))
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var resp struct {
			Data struct {
				DegradedDetails *[]PostureToken `json:"degraded_details"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.NotNil(t, resp.Data.DegradedDetails, "no degraded_details field: %s", w.Body.String())
		return *resp.Data.DegradedDetails
	}
	got := details(func() []PostureToken { return raised })
	require.Len(t, got, 2)
	assert.Equal(t, raised[0].Subject, got[0].Subject)
	require.NotNil(t, got[0].DeleteAfter)
	assert.True(t, got[0].DeleteAfter.Equal(due))
	assert.Equal(t, int64(3600), got[1].AgeSeconds)
	assert.Empty(t, details(nil))
}
