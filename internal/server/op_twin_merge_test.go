// file: internal/server/op_twin_merge_test.go
// version: 1.0.0
// guid: 7e3c9a15-2b84-4d61-9f0a-c5d8e1b36a72
// last-edited: 2026-10-10

package server

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
)

// twinMergeRows lists every pair of ops merged into one survivor. The loser's ID
// stays resolvable as a FormerIDs alias, so a stored row, a script or an old
// schedule that still names it must land on a def that is at least as strict as
// the one it replaced. Each merge PR appends its own rows.
//
// The series pair keeps library.edit_metadata (the survivor's own, stricter
// gate) rather than the settings.manage the loser's absent Permissions implied;
// the survivors are not downgraded.
var twinMergeRows = []struct {
	loser, survivor string
	perms           []auth.Permission
	minTimeout      time.Duration
}{
	{"maintenance.series-prune", "dedup.series-prune", []auth.Permission{auth.PermLibraryEditMetadata}, 2 * time.Hour},
	{"maintenance.series-normalize", "dedup.series-normalize", []auth.Permission{auth.PermLibraryEditMetadata}, 4 * time.Hour},
}

func TestTwinMerge_AliasPermissionAndTimeout(t *testing.T) {
	srv, _, _ := bootRegisteredOpIDs(t)

	for _, row := range twinMergeRows {
		t.Run(row.survivor, func(t *testing.T) {
			// (a) the alias resolves to the survivor's def.
			def, ok := srv.opRegistry.Def(row.loser)
			require.True(t, ok, "Def(%q) must resolve through the alias", row.loser)
			assert.Equal(t, row.survivor, def.ID)

			// (b) and (c): the survivor's gate and timeout.
			surv, ok := srv.opRegistry.Def(row.survivor)
			require.True(t, ok)
			assert.Equal(t, row.perms, surv.Permissions)
			assert.GreaterOrEqual(t, surv.Timeout, row.minTimeout)

			// (d) a caller holding only scan.trigger is refused for both IDs, and
			// EnqueueOp is never reached: the mock registers no EnqueueOp
			// expectation, so mockery fails the test if the handler enqueues.
			for _, id := range []string{row.survivor, row.loser} {
				reg := handlersmocks.NewMockOperationsRegistry(t)
				reg.EXPECT().Def(id).RunAndReturn(srv.opRegistry.Def)
				h := handlers.NewOperationsV2Handler(nil, reg, nil, true)

				gin.SetMode(gin.TestMode)
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				c.Request = httptest.NewRequest(http.MethodPost, "/operations/v2",
					strings.NewReader(`{"def_id":"`+id+`"}`))
				c.Request.Header.Set("Content-Type", "application/json")
				c.Request = c.Request.WithContext(auth.WithPermissions(c.Request.Context(),
					[]auth.Permission{auth.PermScanTrigger}))
				h.TriggerOperationV2(c)

				assert.Equal(t, http.StatusForbidden, w.Code, "caller with only scan.trigger on %q", id)
			}
		})
	}
}

// dedup.series-normalize renames series, so it declares the series write-set;
// the dispatcher's write-set gate then never runs it alongside
// entities.series-rename or dedup.series-merge. (This invariant was asserted on
// maintenance.series-normalize until that def was merged into this one.)
func TestSeriesNormalizeDef_DeclaresSeriesWriteSet(t *testing.T) {
	srv, _, _ := bootRegisteredOpIDs(t)
	def, ok := srv.opRegistry.Def("dedup.series-normalize")
	require.True(t, ok)
	if !slices.Contains(def.Writes, opsregistry.ResSeries) {
		t.Fatalf("dedup.series-normalize Writes = %v, want it to include %q", def.Writes, opsregistry.ResSeries)
	}
}
