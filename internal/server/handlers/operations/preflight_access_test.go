// file: internal/server/handlers/operations/preflight_access_test.go
// version: 1.0.0
// guid: 9aeb6546-3b73-4a06-860d-1971f3a0c029
// last-edited: 2026-10-25

package operations_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/opaccess"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/operations"
	operationsmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/operations/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// noFixers is a fixer registry with nothing in it.
type noFixers struct{}

func (noFixers) Get(string) (repairs.Fixer, bool) { return nil, false }

// opRowsByID answers GetOperationV2 from a map; an id in fail errors.
type opRowsByID struct {
	rows map[string]*database.OperationV2Row
	fail map[string]bool
}

func (o opRowsByID) GetOperationV2(id string) (*database.OperationV2Row, error) {
	if o.fail[id] {
		return nil, errors.New("invented op store failure")
	}
	return o.rows[id], nil
}

// The undo report refuses on any row the caller may not use. When the refusal
// comes from an op row that could not be read (a row with no Source is scoped
// by its op row), the store failed, so the answer is 500 and not the 403 that
// says the caller lacks a fixer's permission. Either way no report is built.
func TestUndoPreflightHandler_OpStoreErrorIs500(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ops := opRowsByID{
		rows: map[string]*database.OperationV2Row{
			"op-repairs": {ID: "op-repairs", DefID: repairs.ApplyOpID, Params: `{"fixer_id":"fixer.invented-gone"}`},
		},
		fail: map[string]bool{"op-unreadable": true},
	}
	// A caller holding library.view only.
	holds := func(_ *gin.Context, p auth.Permission) bool { return p == auth.PermLibraryView }
	for name, tc := range map[string]struct {
		rowOp string
		want  int
	}{
		"op row unreadable":           {"op-unreadable", http.StatusInternalServerError},
		"repairs op the caller lacks": {"op-repairs", http.StatusForbidden},
	} {
		store := operationsmocks.NewMockOperationsStore(t)
		store.EXPECT().GetOperationV2("op-1").Return(nil, nil)
		store.EXPECT().GetOperationChanges("op-1").Return([]*database.OperationChange{
			{ID: "c1", OperationID: tc.rowOp, ChangeType: undo.ChangeTypeBookFileMove},
		}, nil)
		built := false
		h := operations.New(store, nil, nil, nil, nil, nil, nil,
			func([]*database.OperationChange) (*undo.UndoConflictReport, error) {
				built = true
				return &undo.UndoConflictReport{}, nil
			},
			func(string) (*audiobooks.RevertResult, error) { return nil, nil },
		).WithRepairsOpAccess(opaccess.NewRepairs(noFixers{}, ops, holds))
		w := run(http.MethodGet, "/operations/:id/undo/preflight", "/operations/op-1/undo/preflight", nil, func(r *gin.Engine) {
			r.GET("/operations/:id/undo/preflight", h.UndoPreflightHandler)
		})
		assert.Equal(t, tc.want, w.Code, "%s: %s", name, w.Body.String())
		assert.False(t, built, "%s: no report is built", name)
		assert.NotContains(t, w.Body.String(), "invented op store failure", "%s: the store error is not sent", name)
	}
}
