// file: internal/server/handlers/scheduler_admin_test.go
// version: 1.0.0
// guid: 5b86925c-8469-4216-83f5-c7733c9ff488
// last-edited: 2026-08-22

// Unit tests for the task-scheduler and maintenance-window HTTP handlers,
// moved verbatim (renamed to the SchedulerHandler receiver and its own
// newTestSchedulerHandler/run helpers) from
// internal/server/handlers/operations/handler_test.go when TODO.md's
// scheduler-config item split these six routes off operations.Handler. The
// store is exercised through databasemocks.MockStore (a superset of
// database.SettingsStore, the handler's actual dependency); the scheduler dep
// uses the generated handlersmocks.MockScheduler.

package handlers_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	databasemocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/scheduler"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// --- helpers ---

func newTestSchedulerHandler(t *testing.T) (*handlers.SchedulerHandler, *databasemocks.MockStore, *handlersmocks.MockScheduler) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	store := databasemocks.NewMockStore(t)
	sched := handlersmocks.NewMockScheduler(t)

	h := handlers.NewSchedulerHandler(store, func() handlers.Scheduler { return sched })
	return h, store, sched
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

// --- ListTasks ---

func TestListTasks_Success(t *testing.T) {
	h, _, sched := newTestSchedulerHandler(t)
	sched.EXPECT().ListTasks().Return([]scheduler.TaskInfo{{Name: "dedup_refresh"}})
	w := run(http.MethodGet, "/tasks", "/tasks", nil, func(r *gin.Engine) {
		r.GET("/tasks", h.ListTasks)
	})
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestListTasks_NilScheduler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := handlers.NewSchedulerHandler(databasemocks.NewMockStore(t), nil)
	w := run(http.MethodGet, "/tasks", "/tasks", nil, func(r *gin.Engine) {
		r.GET("/tasks", h.ListTasks)
	})
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// --- RunTask ---

func TestRunTask_UnknownName(t *testing.T) {
	h, _, sched := newTestSchedulerHandler(t)
	sched.EXPECT().RunTaskManual("bogus").Return(nil, errors.New("unknown task"))
	w := run(http.MethodPost, "/tasks/:name/run", "/tasks/bogus/run", nil, func(r *gin.Engine) {
		r.POST("/tasks/:name/run", h.RunTask)
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestRunTask_Success(t *testing.T) {
	h, _, sched := newTestSchedulerHandler(t)
	sched.EXPECT().RunTaskManual("dedup_refresh").Return(&database.Operation{ID: "op-1"}, nil)
	w := run(http.MethodPost, "/tasks/:name/run", "/tasks/dedup_refresh/run", nil, func(r *gin.Engine) {
		r.POST("/tasks/:name/run", h.RunTask)
	})
	assert.Equal(t, http.StatusAccepted, w.Code)
}

// --- UpdateTaskConfig ---

func TestUpdateTaskConfig_KnownTask(t *testing.T) {
	h, store, _ := newTestSchedulerHandler(t)
	// config.SaveConfigToDatabase persists settings; it reads/writes via the
	// SettingsStore subset of our store. Allow any setting writes.
	store.EXPECT().SetSetting(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	store.EXPECT().GetSetting(mock.Anything).Return(nil, nil).Maybe()
	enabled := true
	body, _ := json.Marshal(map[string]any{"enabled": enabled})
	w := run(http.MethodPut, "/tasks/:name", "/tasks/dedup_refresh", body, func(r *gin.Engine) {
		r.PUT("/tasks/:name", h.UpdateTaskConfig)
	})
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestUpdateTaskConfig_UnknownTask(t *testing.T) {
	h, _, _ := newTestSchedulerHandler(t)
	body, _ := json.Marshal(map[string]any{"enabled": true})
	w := run(http.MethodPut, "/tasks/:name", "/tasks/not_a_task", body, func(r *gin.Engine) {
		r.PUT("/tasks/:name", h.UpdateTaskConfig)
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// --- RunMaintenanceWindowNow ---

func TestRunMaintenanceWindowNow_Triggers(t *testing.T) {
	h, _, sched := newTestSchedulerHandler(t)
	sched.EXPECT().RunMaintenanceWindow(mock.Anything).Return(nil)
	w := run(http.MethodPost, "/maintenance-window/run", "/maintenance-window/run", nil, func(r *gin.Engine) {
		r.POST("/maintenance-window/run", h.RunMaintenanceWindowNow)
	})
	assert.Equal(t, http.StatusAccepted, w.Code)
}

// --- GetMaintenanceWindowStatus ---

func TestGetMaintenanceWindowStatus_RunningState(t *testing.T) {
	h, _, sched := newTestSchedulerHandler(t)
	sched.EXPECT().GetLastMaintenanceRunDate().Return("2026-06-01")
	sched.EXPECT().IsMaintenanceRunning().Return(true)
	w := run(http.MethodGet, "/maintenance-window/status", "/maintenance-window/status", nil, func(r *gin.Engine) {
		r.GET("/maintenance-window/status", h.GetMaintenanceWindowStatus)
	})
	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	assert.Equal(t, true, data["currently_running"])
	assert.NotEmpty(t, data["next_run_estimate"])
}

// --- UpdateMaintenanceWindowConfig ---

func TestUpdateMaintenanceWindowConfig_Valid(t *testing.T) {
	h, store, _ := newTestSchedulerHandler(t)
	store.EXPECT().SetSetting(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	store.EXPECT().GetSetting(mock.Anything).Return(nil, nil).Maybe()
	body, _ := json.Marshal(map[string]any{"enabled": true, "window_start": 3, "window_end": 5})
	w := run(http.MethodPut, "/maintenance-window/config", "/maintenance-window/config", body, func(r *gin.Engine) {
		r.PUT("/maintenance-window/config", h.UpdateMaintenanceWindowConfig)
	})
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestUpdateMaintenanceWindowConfig_InvalidHour(t *testing.T) {
	h, _, _ := newTestSchedulerHandler(t)
	body, _ := json.Marshal(map[string]any{"enabled": true, "window_start": 24, "window_end": 5})
	w := run(http.MethodPut, "/maintenance-window/config", "/maintenance-window/config", body, func(r *gin.Engine) {
		r.PUT("/maintenance-window/config", h.UpdateMaintenanceWindowConfig)
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// --- UpdateTaskConfig: field bindings ---
//
// The tables below are deliberately a SECOND, independently written statement
// of what bindingForTask encodes. That redundancy is the point: a binding table
// with one mis-pointed field writes the wrong setting and still answers 200, so
// only a separately authored claim about where each field must land can catch
// it. Asserting the response code alone would pass for every mis-pointing.

// taskBoolField is one (task, body field) pair whose write must show up in the
// config value read by get.
type taskBoolField struct {
	task  string
	field string
	get   func() bool
}

// taskIntField is the same for the integer-valued body field.
type taskIntField struct {
	task  string
	field string
	get   func() int
}

// acceptedBoolFields lists every boolean knob a task really has, paired with
// the config value the scheduler's TaskDefinition reads for that trigger
// (internal/scheduler/tasks.go).
func acceptedBoolFields() []taskBoolField {
	return []taskBoolField{
		{"dedup_refresh", "enabled", func() bool { return config.AppConfig.Scheduled.DedupRefresh.Enabled }},
		{"dedup_refresh", "run_on_startup", func() bool { return config.AppConfig.Scheduled.DedupRefresh.OnStartup }},
		{"dedup_refresh", "run_in_maintenance_window", func() bool { return config.AppConfig.Maintenance.DedupRefresh }},
		{"author_split_scan", "enabled", func() bool { return config.AppConfig.Scheduled.AuthorSplit.Enabled }},
		{"author_split_scan", "run_on_startup", func() bool { return config.AppConfig.Scheduled.AuthorSplit.OnStartup }},
		{"author_split_scan", "run_in_maintenance_window", func() bool { return config.AppConfig.Maintenance.AuthorSplit }},
		{"db_optimize", "enabled", func() bool { return config.AppConfig.Scheduled.DbOptimize.Enabled }},
		{"db_optimize", "run_on_startup", func() bool { return config.AppConfig.Scheduled.DbOptimize.OnStartup }},
		{"db_optimize", "run_in_maintenance_window", func() bool { return config.AppConfig.Maintenance.DbOptimize }},
		{"metadata_refresh", "enabled", func() bool { return config.AppConfig.Scheduled.MetadataRefresh.Enabled }},
		{"metadata_refresh", "run_on_startup", func() bool { return config.AppConfig.Scheduled.MetadataRefresh.OnStartup }},
		{"metadata_refresh", "run_in_maintenance_window", func() bool { return config.AppConfig.Maintenance.MetadataRefresh }},
		{"series_prune", "enabled", func() bool { return config.AppConfig.Scheduled.SeriesPrune.Enabled }},
		{"series_prune", "run_on_startup", func() bool { return config.AppConfig.Scheduled.SeriesPrune.OnStartup }},
		{"series_prune", "run_in_maintenance_window", func() bool { return config.AppConfig.Maintenance.SeriesPrune }},
		// library_scan's first three bound only after 2026-08-16; before that the
		// case wired the maintenance-window flag alone and dropped the rest.
		{"library_scan", "enabled", func() bool { return config.AppConfig.Scheduled.LibraryScan.Enabled }},
		{"library_scan", "run_on_startup", func() bool { return config.AppConfig.Scheduled.LibraryScan.OnStartup }},
		{"library_scan", "run_in_maintenance_window", func() bool { return config.AppConfig.Maintenance.LibraryScan }},
		{"reconcile_scan", "enabled", func() bool { return config.AppConfig.Scheduled.Reconcile.Enabled }},
		{"reconcile_scan", "run_on_startup", func() bool { return config.AppConfig.Scheduled.Reconcile.OnStartup }},
		{"reconcile_scan", "run_in_maintenance_window", func() bool { return config.AppConfig.Maintenance.Reconcile }},
		{"itunes_sync", "enabled", func() bool { return config.AppConfig.ITunes.SyncEnabled }},
		{"purge_deleted", "run_in_maintenance_window", func() bool { return config.AppConfig.Maintenance.PurgeDeleted }},
		{"purge_old_logs", "run_in_maintenance_window", func() bool { return config.AppConfig.Maintenance.PurgeOldLogs }},
		{"tombstone_cleanup", "run_in_maintenance_window", func() bool { return config.AppConfig.Maintenance.TombstoneCleanup }},
		{"library_organize", "run_in_maintenance_window", func() bool { return config.AppConfig.Maintenance.LibraryOrganize }},
	}
}

func acceptedIntFields() []taskIntField {
	return []taskIntField{
		{"dedup_refresh", "interval_minutes", func() int { return config.AppConfig.Scheduled.DedupRefresh.Interval }},
		{"author_split_scan", "interval_minutes", func() int { return config.AppConfig.Scheduled.AuthorSplit.Interval }},
		{"db_optimize", "interval_minutes", func() int { return config.AppConfig.Scheduled.DbOptimize.Interval }},
		{"metadata_refresh", "interval_minutes", func() int { return config.AppConfig.Scheduled.MetadataRefresh.Interval }},
		{"series_prune", "interval_minutes", func() int { return config.AppConfig.Scheduled.SeriesPrune.Interval }},
		{"library_scan", "interval_minutes", func() int { return config.AppConfig.Scheduled.LibraryScan.Interval }},
		{"reconcile_scan", "interval_minutes", func() int { return config.AppConfig.Scheduled.Reconcile.Interval }},
		{"itunes_sync", "interval_minutes", func() int { return config.AppConfig.ITunes.SyncInterval }},
	}
}

// rejectedFields lists (task, field) pairs the task genuinely cannot apply,
// either because the scheduler hardcodes that trigger or because the real knob
// is a different config key. Each of these used to answer 200 and drop the
// write.
func rejectedFields() []struct{ task, field string } {
	return []struct{ task, field string }{
		{"purge_deleted", "enabled"},
		{"purge_deleted", "interval_minutes"},
		{"purge_deleted", "run_on_startup"},
		{"purge_old_logs", "enabled"},
		{"purge_old_logs", "interval_minutes"},
		{"purge_old_logs", "run_on_startup"},
		{"tombstone_cleanup", "enabled"},
		{"tombstone_cleanup", "interval_minutes"},
		{"tombstone_cleanup", "run_on_startup"},
		{"library_organize", "enabled"},
		{"library_organize", "interval_minutes"},
		{"library_organize", "run_on_startup"},
		{"itunes_sync", "run_on_startup"},
		{"itunes_sync", "run_in_maintenance_window"},
	}
}

// putTaskConfig issues PUT /tasks/<task> with a single-field body, matching how
// MaintenanceTab.tsx sends these — one field per request, never a merged body.
func putTaskConfig(t *testing.T, task, field string, value any) *httptest.ResponseRecorder {
	t.Helper()
	h, store, _ := newTestSchedulerHandler(t)
	store.EXPECT().SetSetting(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	store.EXPECT().GetSetting(mock.Anything).Return(nil, nil).Maybe()
	body, err := json.Marshal(map[string]any{field: value})
	require.NoError(t, err)
	return run(http.MethodPut, "/tasks/:name", "/tasks/"+task, body, func(r *gin.Engine) {
		r.PUT("/tasks/:name", h.UpdateTaskConfig)
	})
}

// restoreAppConfig snapshots the package-level config so a subtest's write does
// not leak into its siblings.
func restoreAppConfig(t *testing.T) {
	t.Helper()
	saved := config.AppConfig
	t.Cleanup(func() { config.AppConfig = saved })
}

func TestUpdateTaskConfig_AcceptedFieldsActuallyApply(t *testing.T) {
	for _, tc := range acceptedBoolFields() {
		t.Run(tc.task+"/"+tc.field, func(t *testing.T) {
			restoreAppConfig(t)
			// Send the opposite of whatever is configured now, so the assertion
			// cannot pass by coincidence on a value that was already correct.
			want := !tc.get()
			w := putTaskConfig(t, tc.task, tc.field, want)
			require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
			require.Equal(t, want, tc.get(),
				"PUT /tasks/%s {%q} answered 200 but the config value it names did not change",
				tc.task, tc.field)
		})
	}
	for _, tc := range acceptedIntFields() {
		t.Run(tc.task+"/"+tc.field, func(t *testing.T) {
			restoreAppConfig(t)
			want := tc.get() + 7
			w := putTaskConfig(t, tc.task, tc.field, want)
			require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
			require.Equal(t, want, tc.get(),
				"PUT /tasks/%s {%q} answered 200 but the config value it names did not change",
				tc.task, tc.field)
		})
	}
}

func TestUpdateTaskConfig_UnsettableFieldsAreRejected(t *testing.T) {
	for _, tc := range rejectedFields() {
		t.Run(tc.task+"/"+tc.field, func(t *testing.T) {
			restoreAppConfig(t)
			var value any = true
			if tc.field == "interval_minutes" {
				value = 42
			}
			w := putTaskConfig(t, tc.task, tc.field, value)
			require.Equal(t, http.StatusBadRequest, w.Code,
				"PUT /tasks/%s {%q} cannot be applied, so it must not report success; body: %s",
				tc.task, tc.field, w.Body.String())
			// The error has to name the field the caller sent, otherwise it is
			// not actionable.
			require.Contains(t, w.Body.String(), tc.field)
		})
	}
}

// TestUpdateTaskConfig_PurgeDeletedEnabledNamesTheRealKey pins the specific
// production incident: the purge could not be turned off through this endpoint,
// and the 200 gave no clue that purge_soft_deleted_after_days was the real
// switch.
func TestUpdateTaskConfig_PurgeDeletedEnabledNamesTheRealKey(t *testing.T) {
	restoreAppConfig(t)
	w := putTaskConfig(t, "purge_deleted", "enabled", false)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "purge_soft_deleted_after_days")
	require.Contains(t, w.Body.String(), "run_in_maintenance_window",
		"the error should list what the task does accept")
}

// schedulerView reports what the real scheduler says about a task, computed
// through the actual TaskDefinition getters in internal/scheduler/tasks.go.
//
// The sibling tests above assert that a PUT changed the config FIELD the
// binding points at. That is necessary but not sufficient: a getter may OR its
// field with something else, in which case the field changes and the value the
// scheduler acts on does not. library_scan's IsEnabled does exactly that with
// the legacy scan_on_startup key. Asserting through ListTasks closes the gap by
// checking the value the caller can actually observe rather than the write.
func schedulerView(t *testing.T, name string) (scheduler.TaskInfo, bool) {
	t.Helper()
	ts := scheduler.NewTaskScheduler(scheduler.SchedulerDeps{
		Store:               func() scheduler.SchedulerStore { return nil },
		HasDedupEngine:      func() bool { return false },
		HasMetadataFetchSvc: func() bool { return false },
		HasActivitySvc:      func() bool { return false },
		HasBatchPoller:      func() bool { return false },
	})
	for _, info := range ts.ListTasks() {
		if info.Name == name {
			return info, true
		}
	}
	return scheduler.TaskInfo{}, false
}

func TestUpdateTaskConfig_SchedulerReportsTheAppliedValue(t *testing.T) {
	for _, tc := range acceptedBoolFields() {
		t.Run(tc.task+"/"+tc.field, func(t *testing.T) {
			restoreAppConfig(t)
			before, ok := schedulerView(t, tc.task)
			if !ok {
				t.Skipf("%s is not a registered scheduler task", tc.task)
			}
			read := func(info scheduler.TaskInfo) bool {
				switch tc.field {
				case "enabled":
					return info.Enabled
				case "run_on_startup":
					return info.RunOnStartup
				case "run_in_maintenance_window":
					return info.RunInMaintenanceWindow
				}
				t.Fatalf("unhandled field %q", tc.field)
				return false
			}
			want := !read(before)
			w := putTaskConfig(t, tc.task, tc.field, want)
			require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
			after, ok := schedulerView(t, tc.task)
			require.True(t, ok)
			require.Equal(t, want, read(after),
				"PUT /tasks/%s {%q: %v} answered 200 but the scheduler still reports %v",
				tc.task, tc.field, want, read(after))
		})
	}
}

// TestUpdateTaskConfig_LibraryScanEnabledBeatsLegacyScanOnStartup pins the
// specific masking this endpoint used to have. Before the fold, this PUT wrote
// Scheduled.LibraryScan.Enabled=false, answered 200, and IsEnabled() stayed
// true because it ORs in the legacy scan_on_startup key.
func TestUpdateTaskConfig_LibraryScanEnabledBeatsLegacyScanOnStartup(t *testing.T) {
	restoreAppConfig(t)
	config.AppConfig.ScanOnStartup = true
	config.AppConfig.Scheduled.LibraryScan.Enabled = true

	before, ok := schedulerView(t, "library_scan")
	require.True(t, ok)
	require.True(t, before.Enabled, "precondition: the task should start enabled")

	w := putTaskConfig(t, "library_scan", "enabled", false)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	after, ok := schedulerView(t, "library_scan")
	require.True(t, ok)
	require.False(t, after.Enabled,
		"disabling library_scan must actually disable it, not be masked by scan_on_startup")
}

// The fold must not over-reach: turning OFF the startup run is not a request to
// disable the task, so IsEnabled has to survive it.
func TestUpdateTaskConfig_LibraryScanRunOnStartupOffKeepsTaskEnabled(t *testing.T) {
	restoreAppConfig(t)
	config.AppConfig.ScanOnStartup = true
	config.AppConfig.Scheduled.LibraryScan.Enabled = false
	config.AppConfig.Scheduled.LibraryScan.OnStartup = false

	w := putTaskConfig(t, "library_scan", "run_on_startup", false)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	after, ok := schedulerView(t, "library_scan")
	require.True(t, ok)
	require.False(t, after.RunOnStartup, "run_on_startup=false must take effect")
	require.True(t, after.Enabled,
		"clearing the startup run must not silently disable the task; scan_on_startup had it enabled")
}
