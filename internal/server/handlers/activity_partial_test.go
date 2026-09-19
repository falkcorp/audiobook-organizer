// file: internal/server/handlers/activity_partial_test.go
// version: 1.0.0
// guid: 768e4939-c876-45fc-9357-b2d6303dfa42
// last-edited: 2026-09-19

package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
)

// partialService adds QueryWithPartial to the generated ActivityService mock,
// the way *activity.Service implements it in production.
type partialService struct {
	*handlersmocks.MockActivityService
	res database.ActivityQueryResult
}

func (p *partialService) QueryWithPartial(context.Context, database.ActivityFilter) (database.ActivityQueryResult, error) {
	return p.res, nil
}

// TestListActivity_ReportsPartial: a budget-truncated store answer reaches the
// client as "partial": true, so the UI can say older matches were not searched
// instead of presenting a short page as the whole answer.
func TestListActivity_ReportsPartial(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, partial := range []bool{true, false} {
		svc := &partialService{
			MockActivityService: handlersmocks.NewMockActivityService(t),
			res: database.ActivityQueryResult{
				Entries: []database.ActivityEntry{{ID: 1, Source: "scanner"}},
				Total:   1, Partial: partial,
			},
		}
		h := handlers.NewActivityHandler(svc, nil)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/activity?source=scanner", nil)
		h.ListActivity(c)

		require.Equal(t, http.StatusOK, w.Code)
		var body struct {
			Data struct {
				Total   int  `json:"total"`
				Partial bool `json:"partial"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		assert.Equal(t, partial, body.Data.Partial)
		assert.Equal(t, 1, body.Data.Total)
	}
}
