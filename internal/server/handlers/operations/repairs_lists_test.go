// file: internal/server/handlers/operations/repairs_lists_test.go
// version: 1.0.0
// guid: c7d1a4e8-39f2-4b65-8e0a-5f6b2d9c1a73
// last-edited: 2026-10-30

package operations_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/opaccess"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/operations"
	operationsmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/operations/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// scopedFixer is a repairs.Fixer asking for the default permission; only
// RequiredPermissions is ever called on it.
type scopedFixer struct{ repairs.Fixer }

type fixerSet map[string]repairs.Fixer

func (m fixerSet) Get(id string) (repairs.Fixer, bool) { f, ok := m[id]; return f, ok }

type noOpRows struct{}

func (noOpRows) GetOperationV2(string) (*database.OperationV2Row, error) { return nil, nil }

// repairsAccessHolding is the access check for a caller who holds exactly held.
func repairsAccessHolding(held ...auth.Permission) *opaccess.Repairs {
	return opaccess.NewRepairs(fixerSet{"plain": scopedFixer{}}, noOpRows{}, func(_ *gin.Context, p auth.Permission) bool {
		for _, h := range held {
			if h == p {
				return true
			}
		}
		return false
	})
}

func handlerWithStale(t *testing.T, stale []database.Operation, access *opaccess.Repairs) (*operations.Handler, *operationsmocks.MockOperationsStore) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	store := operationsmocks.NewMockOperationsStore(t)
	h := operations.New(
		store,
		operationsmocks.NewMockOperationsRegistry(t),
		func() operations.Scheduler { return operationsmocks.NewMockScheduler(t) },
		operationsmocks.NewMockScanCanceler(t),
		operationsmocks.NewMockAIScanLister(t),
		func(time.Duration) ([]database.Operation, error) { return stale, nil },
		func() (int, error) { return 0, nil },
		func([]*database.OperationChange) (*undo.UndoConflictReport, error) {
			return &undo.UndoConflictReport{}, nil
		},
		func(id string, _ audiobooks.RevertActor) (*audiobooks.RevertResult, error) {
			return &audiobooks.RevertResult{OperationID: id}, nil
		},
	)
	return h.WithRepairsOpAccess(access), store
}

// GET /operations/stale lists v1 rows; a Repairs row the caller may not read is
// neither listed nor counted, and an unwired check refuses every Repairs row.
func TestListStaleOperations_LeavesOutRepairsRowsTheCallerMayNotRead(t *testing.T) {
	plain := `{"fixer_id":"plain"}`
	stale := []database.Operation{
		{ID: "op-robin-patrol", Type: "library.scan", Status: "running"},
		{ID: "op-batgirl-apply", Type: repairs.ApplyOpID, Status: "running", ResultData: &plain},
		{ID: "op-nightwing-plan", Type: repairs.PlanOpID, Status: "running"},
	}
	for name, tc := range map[string]struct {
		access *opaccess.Repairs
		want   []string
	}{
		"viewer":   {repairsAccessHolding(auth.PermLibraryView), []string{"op-robin-patrol"}},
		"editor":   {repairsAccessHolding(auth.PermLibraryView, auth.PermLibraryEditMetadata), []string{"op-robin-patrol", "op-batgirl-apply"}},
		"unwired":  {nil, []string{"op-robin-patrol"}},
		"everyone": {repairsAccessHolding(auth.All()...), []string{"op-robin-patrol", "op-batgirl-apply"}},
	} {
		h, _ := handlerWithStale(t, stale, tc.access)
		w := run(http.MethodGet, "/operations/stale", "/operations/stale", nil, func(r *gin.Engine) {
			r.GET("/operations/stale", h.ListStaleOperations)
		})
		require.Equal(t, http.StatusOK, w.Code, name)
		var body struct {
			Data struct {
				Count      int                  `json:"count"`
				Operations []database.Operation `json:"operations"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), name)
		got := []string{}
		for _, o := range body.Data.Operations {
			got = append(got, o.ID)
		}
		require.Equal(t, tc.want, got, name)
		require.Equal(t, len(tc.want), body.Data.Count, "%s: the count says how many the caller may see", name)
	}
}

// POST /operations/resume answers with the same lists as GET: settings.manage
// does not imply every fixer's permissions, so the lists are filtered there too.
func TestResumeOperations_PauseListsLeaveOutRepairsRuns(t *testing.T) {
	rows := []database.OperationV2Row{
		{ID: "op-robin-patrol", DefID: "library.scan", Status: "running"},
		{ID: "op-batgirl-apply", DefID: repairs.ApplyOpID, Status: "running", Params: `{"fixer_id":"plain"}`},
	}
	for name, tc := range map[string]struct {
		access *opaccess.Repairs
		want   []string
	}{
		"manager without the fixer's permission": {repairsAccessHolding(auth.PermSettingsManage), []string{"library.scan"}},
		"manager who may use the fixer":          {repairsAccessHolding(auth.PermSettingsManage, auth.PermLibraryEditMetadata), []string{"library.scan", repairs.ApplyOpID}},
	} {
		h, store := handlerWithStale(t, nil, tc.access)
		store.EXPECT().ListOperationsV2Since(mock.Anything, mock.Anything).Return(rows, nil).Maybe()
		w := run(http.MethodPost, "/operations/resume", "/operations/resume", nil, func(r *gin.Engine) {
			r.POST("/operations/resume", h.ResumeOperations)
		})
		require.Equal(t, http.StatusOK, w.Code, name)
		var body pauseStateBody
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), name)
		require.Equal(t, tc.want, body.Data.RunningNotPausable, name)
	}
}
