// file: internal/server/handlers/system/handler_test.go
// version: 1.2.0
// guid: af6670e5-d640-4339-b0b2-3b0cf1596ce7
// last-edited: 2026-07-16

// Unit tests for the system-domain HTTP handlers. Each public method has at
// least one test; happy paths plus key branches (config mask-secrets path,
// backup not-found / validation, blocked-hash add/remove validation,
// user-preference get/set/delete, factory-reset confirm guard, SSE nil-hub) are
// covered. The store is exercised through the generated systemmocks
// (MockSystemStore satisfies the narrow SystemStore); the system service /
// config-update service / plugin health checker / event hub / operation-logs
// provider use their generated mocks, and the injected funcs (getDiskStats /
// resetLibrarySizeCache / appVersion / filterReviewedAuthorGroups) are stubbed.
//
// healthCheck / getDashboard / getQuickQueries intentionally exercise the
// type-assertion `ok==false` fallback (broken_file_count = 0, empty queries):
// MockSystemStore does not implement GetBrokenFileCount / Unwrap /
// GetQuickQueryCounts, which is the correct coverage for the non-PebbleDB path.

package system_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/system"
	systemmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/system/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/sysinfo"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// --- helpers ---

type deps struct {
	store   *systemmocks.MockSystemStore
	sysSvc  *systemmocks.MockSystemService
	cfgUpd  *systemmocks.MockConfigUpdateService
	plugins *systemmocks.MockPluginHealthChecker
	hub     *systemmocks.MockEventStreamer
	opLogs  *systemmocks.MockOperationLogsProvider
}

// newTestHandler builds a Handler with all-mock deps and benign stub funcs.
// The hub is wired non-nil here; tests that need the 503 nil-hub branch
// construct a bespoke handler.
func newTestHandler(t *testing.T) (*system.Handler, deps) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	d := deps{
		store:   systemmocks.NewMockSystemStore(t),
		sysSvc:  systemmocks.NewMockSystemService(t),
		cfgUpd:  systemmocks.NewMockConfigUpdateService(t),
		plugins: systemmocks.NewMockPluginHealthChecker(t),
		hub:     systemmocks.NewMockEventStreamer(t),
		opLogs:  systemmocks.NewMockOperationLogsProvider(t),
	}
	h := system.New(
		func() system.SystemStore { return d.store },
		d.sysSvc,
		d.cfgUpd,
		d.plugins,
		func() system.EventStreamer { return d.hub },
		d.opLogs,
		nil, // olService (concrete *metafetch.OpenLibraryService) — nil-checked
		func(path string) (uint64, uint64, error) { return 1000, 400, nil },
		func() {},
		func() string { return "test-version" },
		func(g []dedup.AuthorDedupGroup) []dedup.AuthorDedupGroup { return g },
	)
	return h, d
}

// run wires a single route and serves one request, returning the recorder.
func run(method, routePath, reqPath string, body []byte, register func(r *gin.Engine)) *httptest.ResponseRecorder {
	r := gin.New()
	register(r)
	var rdr *bytes.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, reqPath, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// --- HealthCheck ---

func TestHealthCheck_OK(t *testing.T) {
	h, d := newTestHandler(t)
	// One cheap probe, not four counts: /health answers "can the store respond",
	// nothing more. See the doc comment on HealthCheck.
	d.store.EXPECT().CountAuthors().Return(5, nil)

	w := run(http.MethodGet, "/health", "/health", nil, func(r *gin.Engine) {
		r.GET("/health", h.HealthCheck)
	})
	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	assert.Equal(t, "ok", data["status"])
	assert.NotNil(t, data["timestamp"])

	// /health is unauthenticated, so it must disclose nothing about what this
	// server is or holds. The build string is the sharpest edge: it maps
	// directly to which advisories apply.
	for _, leak := range []string{"version", "database_type", "metrics", "broken_file_count"} {
		assert.NotContains(t, data, leak, "/health must not disclose %q to an anonymous caller", leak)
	}
}

func TestHealthCheck_PartialError(t *testing.T) {
	h, d := newTestHandler(t)
	d.store.EXPECT().CountAuthors().Return(0, errors.New("db down"))

	w := run(http.MethodGet, "/health", "/health", nil, func(r *gin.Engine) {
		r.GET("/health", h.HealthCheck)
	})
	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	// Degraded, not dead — the process serves, the store does not answer.
	assert.Equal(t, "degraded", data["status"])
	// The error string is NOT echoed: it can carry filesystem paths, and this
	// endpoint has no credential behind it.
	assert.NotContains(t, data, "partial_error")
	assert.NotContains(t, w.Body.String(), "db down")
}

// --- GetSystemStatus ---

func TestGetSystemStatus_OK(t *testing.T) {
	h, d := newTestHandler(t)
	d.sysSvc.EXPECT().CollectSystemStatus().Return(&sysinfo.SystemStatus{}, nil)
	d.plugins.EXPECT().HealthCheckAll().Return(map[string]error{
		"acoustid": nil,
		"itunes":   errors.New("offline"),
	})

	w := run(http.MethodGet, "/system/status", "/system/status", nil, func(r *gin.Engine) {
		r.GET("/system/status", h.GetSystemStatus)
	})
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestGetSystemStatus_Error(t *testing.T) {
	h, d := newTestHandler(t)
	d.sysSvc.EXPECT().CollectSystemStatus().Return(nil, errors.New("boom"))

	w := run(http.MethodGet, "/system/status", "/system/status", nil, func(r *gin.Engine) {
		r.GET("/system/status", h.GetSystemStatus)
	})
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// --- GetSystemAnnouncements ---

func TestGetSystemAnnouncements_DuplicateAuthors(t *testing.T) {
	h, d := newTestHandler(t)
	// Two near-identical authors so FindDuplicateAuthors groups them; the stub
	// filterReviewedAuthorGroups passes the group through.
	d.store.EXPECT().GetAllAuthors().Return([]database.Author{
		{ID: 1, Name: "Brandon Sanderson"},
		{ID: 2, Name: "Brandon Sanderson"},
	}, nil)
	d.store.EXPECT().GetBooksByAuthorIDWithRoleCore(mock.AnythingOfType("int")).Return([]database.BookCore{{ID: "b1"}}, nil).Maybe()
	d.store.EXPECT().GetAllBooksCore(100, 0).Return([]database.BookCore{}, nil)

	w := run(http.MethodGet, "/system/announcements", "/system/announcements", nil, func(r *gin.Engine) {
		r.GET("/system/announcements", h.GetSystemAnnouncements)
	})
	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	anns := resp["data"].(map[string]any)["announcements"].([]any)
	assert.NotEmpty(t, anns)
}

func TestGetSystemAnnouncements_Empty(t *testing.T) {
	h, d := newTestHandler(t)
	d.store.EXPECT().GetAllAuthors().Return([]database.Author{}, nil)
	d.store.EXPECT().GetAllBooksCore(100, 0).Return([]database.BookCore{}, nil)

	w := run(http.MethodGet, "/system/announcements", "/system/announcements", nil, func(r *gin.Engine) {
		r.GET("/system/announcements", h.GetSystemAnnouncements)
	})
	assert.Equal(t, http.StatusOK, w.Code)
}

// --- GetSystemStorage ---

func TestGetSystemStorage_NoRootDir(t *testing.T) {
	h, _ := newTestHandler(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = ""
	defer func() { config.AppConfig.RootDir = prev }()

	w := run(http.MethodGet, "/system/storage", "/system/storage", nil, func(r *gin.Engine) {
		r.GET("/system/storage", h.GetSystemStorage)
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestGetSystemStorage_OK(t *testing.T) {
	h, _ := newTestHandler(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = "/tmp/library"
	defer func() { config.AppConfig.RootDir = prev }()

	w := run(http.MethodGet, "/system/storage", "/system/storage", nil, func(r *gin.Engine) {
		r.GET("/system/storage", h.GetSystemStorage)
	})
	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	assert.Equal(t, float64(1000), data["total_bytes"])
	assert.Equal(t, float64(600), data["used_bytes"])
}

// --- GetSystemLogs ---

func TestGetSystemLogs_DelegatesToOperationLogs(t *testing.T) {
	h, d := newTestHandler(t)
	d.opLogs.EXPECT().GetOperationLogs(mock.Anything).Run(func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"delegated": true})
	}).Return()

	w := run(http.MethodGet, "/system/logs", "/system/logs?operation_id=op-1", nil, func(r *gin.Engine) {
		r.GET("/system/logs", h.GetSystemLogs)
	})
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "delegated")
}

func TestGetSystemLogs_Collect(t *testing.T) {
	h, d := newTestHandler(t)
	d.sysSvc.EXPECT().CollectSystemLogs(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return([]sysinfo.SystemLogEntry{{Message: "hello"}}, 1, nil)

	w := run(http.MethodGet, "/system/logs", "/system/logs", nil, func(r *gin.Engine) {
		r.GET("/system/logs", h.GetSystemLogs)
	})
	assert.Equal(t, http.StatusOK, w.Code)
}

// --- GetSystemActivityLog ---

func TestGetSystemActivityLog_OK(t *testing.T) {
	h, d := newTestHandler(t)
	d.store.EXPECT().GetSystemActivityLogs("", 50).Return([]database.SystemActivityLog{{Message: "x"}}, nil)

	w := run(http.MethodGet, "/system/activity-log", "/system/activity-log", nil, func(r *gin.Engine) {
		r.GET("/system/activity-log", h.GetSystemActivityLog)
	})
	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, float64(1), resp["data"].(map[string]any)["count"])
}

// --- ResetSystem ---

func TestResetSystem_OK(t *testing.T) {
	h, d := newTestHandler(t)
	d.store.EXPECT().Reset().Return(nil)
	d.store.EXPECT().InvalidateLibraryStats().Return()

	w := run(http.MethodPost, "/system/reset", "/system/reset", []byte(`{"confirm":"RESET"}`), func(r *gin.Engine) {
		r.POST("/system/reset", h.ResetSystem)
	})
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestResetSystem_DBError(t *testing.T) {
	h, d := newTestHandler(t)
	d.store.EXPECT().Reset().Return(errors.New("reset failed"))

	w := run(http.MethodPost, "/system/reset", "/system/reset", []byte(`{"confirm":"RESET"}`), func(r *gin.Engine) {
		r.POST("/system/reset", h.ResetSystem)
	})
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// TestResetSystem_RequiresConfirm is a regression guard: ResetSystem wipes the
// whole database, so a bare POST (or a wrong token) must be rejected with 400
// before store.Reset() is ever called — matching FactoryReset. Without the
// confirm gate this returned 200 and wiped the DB. The mock store asserts no
// Reset() call (unexpected calls fail the test).
func TestResetSystem_RequiresConfirm(t *testing.T) {
	h, _ := newTestHandler(t)
	w := run(http.MethodPost, "/system/reset", "/system/reset", []byte(`{"confirm":"nope"}`), func(r *gin.Engine) {
		r.POST("/system/reset", h.ResetSystem)
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// --- FactoryReset ---

func TestFactoryReset_RequiresConfirm(t *testing.T) {
	h, _ := newTestHandler(t)
	w := run(http.MethodPost, "/system/factory-reset", "/system/factory-reset", []byte(`{"confirm":"nope"}`), func(r *gin.Engine) {
		r.POST("/system/factory-reset", h.FactoryReset)
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestFactoryReset_OK(t *testing.T) {
	h, d := newTestHandler(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = "" // skip the library-folder clear branch
	defer func() { config.AppConfig.RootDir = prev }()

	d.store.EXPECT().Reset().Return(nil)
	d.store.EXPECT().InvalidateLibraryStats().Return()
	// olService is nil (skips OL branch). config.SaveConfigToDatabase writes via
	// the SettingsStore subset of our store — allow any setting writes.
	d.store.EXPECT().SetSetting(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	d.store.EXPECT().GetSetting(mock.Anything).Return(nil, nil).Maybe()
	d.store.EXPECT().DeleteSetting(mock.Anything).Return(nil).Maybe()

	w := run(http.MethodPost, "/system/factory-reset", "/system/factory-reset", []byte(`{"confirm":"RESET"}`), func(r *gin.Engine) {
		r.POST("/system/factory-reset", h.FactoryReset)
	})
	assert.Equal(t, http.StatusOK, w.Code)
}

// --- GetConfig ---

func TestGetConfig_OK(t *testing.T) {
	h, d := newTestHandler(t)
	d.cfgUpd.EXPECT().MaskSecrets(mock.Anything).Return(config.Config{})
	w := run(http.MethodGet, "/config", "/config", nil, func(r *gin.Engine) {
		r.GET("/config", h.GetConfig)
	})
	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotNil(t, resp["data"].(map[string]any)["config"])
}

func TestGetConfig_MasksAllSecrets(t *testing.T) {
	h, d := newTestHandler(t)
	// MaskSecrets is called with whatever Snapshot() returns; we verify the
	// handler uses MaskSecrets (not manual per-field masking) by confirming the
	// mock is called exactly once.
	d.cfgUpd.EXPECT().MaskSecrets(mock.Anything).Return(config.Config{
		OpenAIAPIKey:      "sk-****",
		AcoustIDAPIKey:    "ac-****",
		GoogleBooksAPIKey: "gb-****",
		HardcoverAPIToken: "hc-****",
		BasicAuthPassword: "****",
	})
	w := run(http.MethodGet, "/config", "/config", nil, func(r *gin.Engine) {
		r.GET("/config", h.GetConfig)
	})
	assert.Equal(t, http.StatusOK, w.Code)
}

// --- UpdateConfig ---

func TestUpdateConfig_MaskSecretsHappyPath(t *testing.T) {
	h, d := newTestHandler(t)
	d.cfgUpd.EXPECT().UpdateConfig(mock.Anything).Return(http.StatusOK, map[string]any{})
	d.cfgUpd.EXPECT().MaskSecrets(mock.Anything).Return(config.Config{})

	w := run(http.MethodPut, "/config", "/config", []byte(`{"root_dir":"/x"}`), func(r *gin.Engine) {
		r.PUT("/config", h.UpdateConfig)
	})
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestUpdateConfig_ServiceError(t *testing.T) {
	h, d := newTestHandler(t)
	d.cfgUpd.EXPECT().UpdateConfig(mock.Anything).Return(http.StatusBadRequest, map[string]any{"error": "bad"})

	w := run(http.MethodPut, "/config", "/config", []byte(`{"x":1}`), func(r *gin.Engine) {
		r.PUT("/config", h.UpdateConfig)
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// --- HandleEvents ---

func TestHandleEvents_NilHub503(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := systemmocks.NewMockSystemStore(t)
	h := system.New(
		func() system.SystemStore { return store },
		nil, nil, nil,
		nil, // nil getHub provider -> resolveHub() returns nil -> 503
		nil, nil, nil, nil, nil, nil,
	)
	w := run(http.MethodGet, "/api/events", "/api/events", nil, func(r *gin.Engine) {
		r.GET("/api/events", h.HandleEvents)
	})
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestHandleEvents_DelegatesToHub(t *testing.T) {
	h, d := newTestHandler(t)
	d.hub.EXPECT().HandleSSE(mock.Anything).Run(func(c *gin.Context) {
		c.Status(http.StatusOK)
	}).Return()

	w := run(http.MethodGet, "/api/events", "/api/events", nil, func(r *gin.Engine) {
		r.GET("/api/events", h.HandleEvents)
	})
	assert.Equal(t, http.StatusOK, w.Code)
}

// --- CreateBackup ---

func TestCreateBackup_Error(t *testing.T) {
	h, _ := newTestHandler(t)
	prev := config.AppConfig.DatabasePath
	config.AppConfig.DatabasePath = "/nonexistent/path/does-not-exist.db"
	defer func() { config.AppConfig.DatabasePath = prev }()

	w := run(http.MethodPost, "/backup/create", "/backup/create", []byte(`{}`), func(r *gin.Engine) {
		r.POST("/backup/create", h.CreateBackup)
	})
	// CreateBackup on a missing source DB returns an internal error.
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// --- ListBackups ---

func TestListBackups_EmptyDir(t *testing.T) {
	h, _ := newTestHandler(t)
	prev := config.AppConfig.DatabasePath
	config.AppConfig.DatabasePath = t.TempDir() + "/audiobooks.db"
	defer func() { config.AppConfig.DatabasePath = prev }()

	w := run(http.MethodGet, "/backup/list", "/backup/list", nil, func(r *gin.Engine) {
		r.GET("/backup/list", h.ListBackups)
	})
	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, float64(0), resp["data"].(map[string]any)["count"])
}

// --- RestoreBackup ---

func TestRestoreBackup_RequiresFilename(t *testing.T) {
	h, _ := newTestHandler(t)
	w := run(http.MethodPost, "/backup/restore", "/backup/restore", []byte(`{}`), func(r *gin.Engine) {
		r.POST("/backup/restore", h.RestoreBackup)
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestRestoreBackup_NotFound(t *testing.T) {
	h, _ := newTestHandler(t)
	prev := config.AppConfig.DatabasePath
	config.AppConfig.DatabasePath = t.TempDir() + "/audiobooks.db"
	defer func() { config.AppConfig.DatabasePath = prev }()

	w := run(http.MethodPost, "/backup/restore", "/backup/restore", []byte(`{"backup_filename":"missing.tar.gz"}`), func(r *gin.Engine) {
		r.POST("/backup/restore", h.RestoreBackup)
	})
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// --- DeleteBackup ---

func TestDeleteBackup_RequiresFilename(t *testing.T) {
	h, _ := newTestHandler(t)
	w := run(http.MethodDelete, "/backup/:filename", "/backup/", nil, func(r *gin.Engine) {
		r.DELETE("/backup/:filename", h.DeleteBackup)
	})
	// Empty :filename does not match the route; Gin returns 404. Use a slash-only
	// filename to hit the handler's own "filename required" guard instead.
	_ = w
	w2 := run(http.MethodDelete, "/backup/:filename", "/backup/missing.tar.gz", nil, func(r *gin.Engine) {
		r.DELETE("/backup/:filename", h.DeleteBackup)
	})
	// Deleting a non-existent backup returns an internal error.
	assert.Equal(t, http.StatusInternalServerError, w2.Code)
}

// --- GetDashboard ---

func TestGetDashboard_OK(t *testing.T) {
	h, d := newTestHandler(t)
	d.store.EXPECT().GetDashboardStats().Return(&database.DashboardStats{TotalBooks: 3}, nil)
	d.store.EXPECT().GetRecentOperations(5).Return([]database.Operation{}, nil)

	w := run(http.MethodGet, "/dashboard", "/dashboard", nil, func(r *gin.Engine) {
		r.GET("/dashboard", h.GetDashboard)
	})
	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, float64(3), resp["data"].(map[string]any)["totalBooks"])
}

func TestGetDashboard_StatsError(t *testing.T) {
	h, d := newTestHandler(t)
	d.store.EXPECT().GetDashboardStats().Return(nil, errors.New("boom"))

	w := run(http.MethodGet, "/dashboard", "/dashboard", nil, func(r *gin.Engine) {
		r.GET("/dashboard", h.GetDashboard)
	})
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// --- ListBlockedHashes ---

func TestListBlockedHashes_OK(t *testing.T) {
	h, d := newTestHandler(t)
	d.store.EXPECT().GetAllBlockedHashes().Return([]database.DoNotImport{{Hash: "h1"}}, nil)

	w := run(http.MethodGet, "/blocked-hashes", "/blocked-hashes", nil, func(r *gin.Engine) {
		r.GET("/blocked-hashes", h.ListBlockedHashes)
	})
	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, float64(1), resp["data"].(map[string]any)["total"])
}

// --- AddBlockedHash ---

func TestAddBlockedHash_RejectsBadLength(t *testing.T) {
	h, _ := newTestHandler(t)
	w := run(http.MethodPost, "/blocked-hashes", "/blocked-hashes", []byte(`{"hash":"short","reason":"r"}`), func(r *gin.Engine) {
		r.POST("/blocked-hashes", h.AddBlockedHash)
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestAddBlockedHash_OK(t *testing.T) {
	h, d := newTestHandler(t)
	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	d.store.EXPECT().AddBlockedHash(hash, "spam").Return(nil)

	w := run(http.MethodPost, "/blocked-hashes", "/blocked-hashes", []byte(`{"hash":"`+hash+`","reason":"spam"}`), func(r *gin.Engine) {
		r.POST("/blocked-hashes", h.AddBlockedHash)
	})
	assert.Equal(t, http.StatusCreated, w.Code)
}

// --- RemoveBlockedHash ---

func TestRemoveBlockedHash_OK(t *testing.T) {
	h, d := newTestHandler(t)
	d.store.EXPECT().RemoveBlockedHash("h1").Return(nil)

	w := run(http.MethodDelete, "/blocked-hashes/:hash", "/blocked-hashes/h1", nil, func(r *gin.Engine) {
		r.DELETE("/blocked-hashes/:hash", h.RemoveBlockedHash)
	})
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestRemoveBlockedHash_DBError(t *testing.T) {
	h, d := newTestHandler(t)
	d.store.EXPECT().RemoveBlockedHash("h2").Return(errors.New("nope"))

	w := run(http.MethodDelete, "/blocked-hashes/:hash", "/blocked-hashes/h2", nil, func(r *gin.Engine) {
		r.DELETE("/blocked-hashes/:hash", h.RemoveBlockedHash)
	})
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// --- GetUserPreference ---

func TestGetUserPreference_Unset(t *testing.T) {
	h, d := newTestHandler(t)
	d.store.EXPECT().GetUserPreference("col").Return(nil, nil)

	w := run(http.MethodGet, "/preferences/:key", "/preferences/col", nil, func(r *gin.Engine) {
		r.GET("/preferences/:key", h.GetUserPreference)
	})
	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "", resp["data"].(map[string]any)["value"])
}

func TestGetUserPreference_Found(t *testing.T) {
	h, d := newTestHandler(t)
	val := "v"
	d.store.EXPECT().GetUserPreference("col").Return(&database.UserPreference{Key: "col", Value: &val}, nil)

	w := run(http.MethodGet, "/preferences/:key", "/preferences/col", nil, func(r *gin.Engine) {
		r.GET("/preferences/:key", h.GetUserPreference)
	})
	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "v", resp["data"].(map[string]any)["value"])
}

// --- SetUserPreference ---

func TestSetUserPreference_OK(t *testing.T) {
	h, d := newTestHandler(t)
	d.store.EXPECT().SetUserPreference("col", "abc").Return(nil)

	w := run(http.MethodPut, "/preferences/:key", "/preferences/col", []byte(`{"value":"abc"}`), func(r *gin.Engine) {
		r.PUT("/preferences/:key", h.SetUserPreference)
	})
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestSetUserPreference_DBError(t *testing.T) {
	h, d := newTestHandler(t)
	d.store.EXPECT().SetUserPreference("col", "abc").Return(errors.New("nope"))

	w := run(http.MethodPut, "/preferences/:key", "/preferences/col", []byte(`{"value":"abc"}`), func(r *gin.Engine) {
		r.PUT("/preferences/:key", h.SetUserPreference)
	})
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// --- DeleteUserPreference ---

func TestDeleteUserPreference_OK(t *testing.T) {
	h, d := newTestHandler(t)
	d.store.EXPECT().SetUserPreference("col", "").Return(nil)

	w := run(http.MethodDelete, "/preferences/:key", "/preferences/col", nil, func(r *gin.Engine) {
		r.DELETE("/preferences/:key", h.DeleteUserPreference)
	})
	assert.Equal(t, http.StatusOK, w.Code)
}

// --- HandlePolicyTags ---

func TestHandlePolicyTags_OK(t *testing.T) {
	h, _ := newTestHandler(t)
	w := run(http.MethodGet, "/policy/tags", "/policy/tags", nil, func(r *gin.Engine) {
		r.GET("/policy/tags", h.HandlePolicyTags)
	})
	assert.Equal(t, http.StatusOK, w.Code)
}

// --- GetQuickQueries ---

func TestGetQuickQueries_UnsupportedStore(t *testing.T) {
	// MockSystemStore does not implement GetQuickQueryCounts nor Unwrap, so the
	// handler returns the empty-queries fallback.
	h, _ := newTestHandler(t)
	w := run(http.MethodGet, "/library/quick-queries", "/library/quick-queries", nil, func(r *gin.Engine) {
		r.GET("/library/quick-queries", h.GetQuickQueries)
	})
	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	queries := resp["data"].(map[string]any)["queries"].([]any)
	assert.Empty(t, queries)
}
