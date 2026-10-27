// file: internal/server/handlers/operations_v2_current_item_user_data_test.go
// version: 1.0.0
// guid: b94e1d7a-2c63-4f80-9a15-d3e8c7f20b46
// last-edited: 2026-10-26

package handlers_test

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	databasemocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/opaccess"
)

const (
	currentItemUserLabel  = "moving state for user 01INVENTEDCURRENTITEM00A"
	currentItemUserMarker = "01INVENTEDCURRENTITEM00A"
)

// A running op's in-memory current item can name a user. The timeline and the
// single-op read withhold that label from a caller without users.manage (and
// from one whose access is not wired, which fails closed), and serve it whole
// to one who holds it. An ordinary label is served as it is.
func TestOperationsV2Handler_CurrentItemNamingAUserNeedsUsersManage(t *testing.T) {
	const opID = "op-current-item"
	row := database.OperationV2Row{ID: opID, DefID: "library.scan", Status: "running"}

	cases := []struct {
		name    string
		access  *opaccess.Repairs // nil: not wired
		label   string
		want    string
		notWant string
	}{
		{"not wired", nil, currentItemUserLabel, activity.WithheldUserText, currentItemUserMarker},
		{"without users.manage", opaccess.NewRepairs(nil, nil, func(_ *gin.Context, p auth.Permission) bool {
			return p != auth.PermUsersManage
		}), currentItemUserLabel, activity.WithheldUserText, currentItemUserMarker},
		{"with users.manage", opaccess.NewRepairs(nil, nil, func(*gin.Context, auth.Permission) bool { return true }),
			currentItemUserLabel, currentItemUserMarker, activity.WithheldUserText},
		{"ordinary label, not wired", nil, "Year One / chapter 3", "Year One / chapter 3", activity.WithheldUserText},
	}
	for _, tc := range cases {
		build := func(t *testing.T) *handlers.OperationsV2Handler {
			store := databasemocks.NewMockOpsV2Store(t)
			store.EXPECT().ListOperationsV2Since(mock.Anything, 5000).Return([]database.OperationV2Row{row}, nil).Maybe()
			store.EXPECT().GetOperationV2(opID).Return(&row, nil).Maybe()
			store.EXPECT().GetOpLogsV2(opID, 50).Return(nil, nil).Maybe()
			registry := handlersmocks.NewMockOperationsRegistry(t)
			expectRegisteredDefs(registry, []opsregistry.OperationDef{})
			registry.EXPECT().GetCurrentItem(opID).Return(tc.label).Maybe()
			var opts []handlers.OperationsV2Option
			if tc.access != nil {
				opts = append(opts, handlers.WithRepairsOpAccess(tc.access))
			}
			return handlers.NewOperationsV2Handler(store, registry, nil, false, opts...)
		}

		t.Run(tc.name+"/timeline", func(t *testing.T) {
			h := build(t)
			c, w := newOpsV2Ctx(http.MethodGet, "/operations/timeline?since=168h", "", nil)
			h.GetOperationTimeline(c)
			require.Equal(t, http.StatusOK, w.Code)
			require.Contains(t, w.Body.String(), tc.want)
			require.NotContains(t, w.Body.String(), tc.notWant)
		})
		t.Run(tc.name+"/single read", func(t *testing.T) {
			h := build(t)
			c, w := newOpsV2Ctx(http.MethodGet, "/operations/v2/"+opID, "", gin.Params{{Key: "id", Value: opID}})
			h.GetOperationV2(c)
			require.Equal(t, http.StatusOK, w.Code)
			require.Contains(t, w.Body.String(), tc.want)
			require.NotContains(t, w.Body.String(), tc.notWant)
		})
	}
}
