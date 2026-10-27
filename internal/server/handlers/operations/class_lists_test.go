// file: internal/server/handlers/operations/class_lists_test.go
// version: 1.0.2
// guid: 4f1c7a92-0b3e-4d68-8a15-d27e6c9b30f1
// last-edited: 2026-10-27

package operations_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/dbtest"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/opaccess"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/operations"
	operationsmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/operations/mocks"
)

// storeWithRows is the operations store plus the row-store capability the real
// store has.
type storeWithRows struct {
	*operationsmocks.MockOperationsStore
	database.OpRowStore
}

// classListFixture stores two classes under op-1 the way the credits backfill
// does and returns the handler, the rows and the ids it stored.
func classListFixture(t *testing.T, withRows bool) (*operations.Handler, *operationsmocks.MockOperationsStore, *dbtest.MemOpRows, []string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	mock := operationsmocks.NewMockOperationsStore(t)
	rows := dbtest.NewMemOpRows()
	ids := make([]string, 2500)
	for i := range ids {
		ids[i] = fmt.Sprintf("book-%05d", i)
	}
	sum, err := database.SaveClassLists(rows, "op-1", map[string][]string{
		"narrator_fill_from_column": ids,
		"held_itunes":               nil,
	})
	require.NoError(t, err)
	result, err := json.Marshal(map[string]any{"class_lists": sum, "counts": sum.Counts})
	require.NoError(t, err)
	rd := string(result)
	mock.EXPECT().GetOperationV2("op-1").Return(&database.OperationV2Row{ID: "op-1", DefID: "maintenance.credits-backfill", ResultData: &rd}, nil).Maybe()
	var st operations.OperationsStore = mock
	if withRows {
		st = storeWithRows{mock, rows}
	}
	h := operations.New(st, nil, nil, nil, nil, nil, nil, nil, nil)
	return h, mock, rows, ids
}

func getClassList(t *testing.T, h *operations.Handler, id, class, query string) *httptest.ResponseRecorder {
	t.Helper()
	path := "/operations/" + id + "/class-lists/" + class
	if query != "" {
		path += "?" + query
	}
	return run(http.MethodGet, "/operations/:id/class-lists/:class", path, nil, func(r *gin.Engine) {
		r.GET("/operations/:id/class-lists/:class", h.GetOperationClassList)
	})
}

func decodeClassListPage(t *testing.T, w *httptest.ResponseRecorder) operations.ClassListPage {
	t.Helper()
	var resp struct {
		Data operations.ClassListPage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), w.Body.String())
	return resp.Data
}

// A class's ids are served a page at a time from the rows stored beside the
// operation, so every count in the (small) stored result still clicks through.
func TestGetOperationClassList_PagesAClassFromTheStoredRows(t *testing.T) {
	h, _, _, ids := classListFixture(t, true)

	w := getClassList(t, h, "op-1", "narrator_fill_from_column", "offset=995&limit=10")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	page := decodeClassListPage(t, w)
	assert.Equal(t, "op-1", page.OperationID)
	assert.Equal(t, "narrator_fill_from_column", page.Class)
	assert.Equal(t, len(ids), page.Total)
	assert.Equal(t, 995, page.Offset)
	assert.Equal(t, 10, page.Limit)
	assert.Equal(t, ids[995:1005], page.IDs, "a page that crosses a stored row boundary")

	// The defaults and the cap.
	page = decodeClassListPage(t, getClassList(t, h, "op-1", "narrator_fill_from_column", ""))
	assert.Equal(t, operations.ClassListDefaultLimit, page.Limit)
	assert.Equal(t, ids[:operations.ClassListDefaultLimit], page.IDs)
	page = decodeClassListPage(t, getClassList(t, h, "op-1", "narrator_fill_from_column", "limit=999999"))
	assert.Equal(t, operations.ClassListMaxLimit, page.Limit)
	assert.True(t, slices.Equal(ids[:operations.ClassListMaxLimit], page.IDs))

	// A class with no ids is an empty page with a total of 0, not a 404.
	w = getClassList(t, h, "op-1", "held_itunes", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	page = decodeClassListPage(t, w)
	assert.Equal(t, 0, page.Total)
	assert.Empty(t, page.IDs)
	assert.Contains(t, w.Body.String(), `"ids":[]`)
}

func TestGetOperationClassList_RefusesWhatItCannotServe(t *testing.T) {
	h, mock, rows, _ := classListFixture(t, true)

	for name, tc := range map[string]struct {
		id, class, query string
		want             int
	}{
		"unknown class":  {"op-1", "no_such_class", "", http.StatusNotFound},
		"negative offs":  {"op-1", "held_itunes", "offset=-1", http.StatusBadRequest},
		"offset text":    {"op-1", "held_itunes", "offset=abc", http.StatusBadRequest},
		"zero limit":     {"op-1", "held_itunes", "limit=0", http.StatusBadRequest},
		"limit text":     {"op-1", "held_itunes", "limit=many", http.StatusBadRequest},
		"unknown op row": {"nope", "held_itunes", "", http.StatusNotFound},
	} {
		if tc.id == "nope" {
			mock.EXPECT().GetOperationV2("nope").Return(nil, nil).Once()
		}
		w := getClassList(t, h, tc.id, tc.class, tc.query)
		assert.Equal(t, tc.want, w.Code, "%s: %s", name, w.Body.String())
	}

	// The rows were deleted (or only part of them written): 409 with the code
	// that says to run the operation again, never a short page.
	require.NoError(t, database.ClearClassLists(rows, "op-1"))
	w := getClassList(t, h, "op-1", "narrator_fill_from_column", "")
	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), operations.ClassListsNeedRerunCode)
}

// A run that died after writing its rows and before storing its summary leaves
// the rows of a newer attempt under the stored summary of an older one; the
// counts can match. The route answers 409 (run it again), never ids that belong
// to another attempt.
func TestGetOperationClassList_RowsOfAnotherAttemptAreA409(t *testing.T) {
	h, _, rows, ids := classListFixture(t, true)
	_, err := database.SaveClassLists(rows, "op-1", map[string][]string{
		"narrator_fill_from_column": slices.Concat([]string{"dick-grayson"}, ids[1:]),
		"held_itunes":               nil,
	})
	require.NoError(t, err)

	w := getClassList(t, h, "op-1", "narrator_fill_from_column", "offset=0&limit=5")
	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), operations.ClassListsNeedRerunCode)
	assert.NotContains(t, w.Body.String(), "dick-grayson")
}

// A result that carries no class_lists summary (the census, which keeps its
// lists inline, or any other operation) has nothing to page.
func TestGetOperationClassList_ResultWithoutASummaryIsNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mock := operationsmocks.NewMockOperationsStore(t)
	rd := `{"classes":[{"key":"x","book_ids":["b1"]}]}`
	mock.EXPECT().GetOperationV2("op-census").Return(&database.OperationV2Row{ID: "op-census", ResultData: &rd}, nil)
	mock.EXPECT().GetOperationV2("op-running").Return(&database.OperationV2Row{ID: "op-running"}, nil)
	h := operations.New(storeWithRows{mock, dbtest.NewMemOpRows()}, nil, nil, nil, nil, nil, nil, nil, nil)

	assert.Equal(t, http.StatusNotFound, getClassList(t, h, "op-census", "x", "").Code)
	assert.Equal(t, http.StatusNotFound, getClassList(t, h, "op-running", "x", "").Code)
}

// A store that cannot keep op rows answers 503 rather than an empty page.
func TestGetOperationClassList_StoreWithoutRowsIs503(t *testing.T) {
	h, _, _, _ := classListFixture(t, false)
	w := getClassList(t, h, "op-1", "held_itunes", "")
	assert.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
}

// The same read gate as GET /operations/:id/result: a Repairs operation the
// caller may not read is refused before any row is read.
func TestGetOperationClassList_FollowsTheRepairsReadGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mock := operationsmocks.NewMockOperationsStore(t)
	rows := dbtest.NewMemOpRows()
	sum, err := database.SaveClassLists(rows, "op-repairs", map[string][]string{"x": {"b1"}})
	require.NoError(t, err)
	result, _ := json.Marshal(map[string]any{"class_lists": sum})
	rd := string(result)
	row := &database.OperationV2Row{ID: "op-repairs", DefID: repairs.ApplyOpID, Params: `{"fixer_id":"fixer.invented-gone"}`, ResultData: &rd}
	mock.EXPECT().GetOperationV2("op-repairs").Return(row, nil)
	holds := func(_ *gin.Context, p auth.Permission) bool { return p == auth.PermLibraryView }
	h := operations.New(storeWithRows{mock, rows}, nil, nil, nil, nil, nil, nil, nil, nil).
		WithRepairsOpAccess(opaccess.NewRepairs(noFixers{}, opRowsByID{rows: map[string]*database.OperationV2Row{"op-repairs": row}}, holds))

	w := getClassList(t, h, "op-repairs", "x", "")
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), "b1")
}
