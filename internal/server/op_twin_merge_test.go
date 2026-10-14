// file: internal/server/op_twin_merge_test.go
// version: 1.10.0
// guid: 7e3c9a15-2b84-4d61-9f0a-c5d8e1b36a72
// last-edited: 2026-10-14

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
// schedule that still names it lands on the survivor's gate. Each merge PR
// appends its own rows.
//
// The survivor's gate is not always at least as strict as the loser's. The
// series pair and the author-scan pair had a loser with no Permissions, which
// fell back to settings.manage (admin only); their survivors require
// library.edit_metadata (editor), so the alias WIDENS from admin to editor. That
// is accepted because an editor could already run the same body under the
// survivor's own id; no row may add a permission the survivor does not already
// require.
var twinMergeRows = []struct {
	loser, survivor string
	perms           []auth.Permission
	minTimeout      time.Duration
}{
	{"maintenance.series-prune", "dedup.series-prune", []auth.Permission{auth.PermLibraryEditMetadata}, 2 * time.Hour},
	{"maintenance.series-normalize", "dedup.series-normalize", []auth.Permission{auth.PermLibraryEditMetadata}, 4 * time.Hour},
	// 04-P4a: the four scheduler.* twins merged into their maintenance.* survivors.
	{"scheduler.temp-file-cleanup", "maintenance.temp-file-cleanup", []auth.Permission{auth.PermSettingsManage}, 1 * time.Hour},
	{"scheduler.trash-cleanup", "maintenance.trash-cleanup", []auth.Permission{auth.PermSettingsManage}, 1 * time.Hour},
	{"scheduler.tombstone-cleanup", "maintenance.tombstone-cleanup", []auth.Permission{auth.PermSettingsManage}, 1 * time.Hour},
	{"scheduler.db-optimize", "maintenance.db-optimize", []auth.Permission{auth.PermSettingsManage}, 2 * time.Hour},
	// 04-P4b: scheduler.purge-deleted merged into maintenance.purge-deleted.
	{"scheduler.purge-deleted", "maintenance.purge-deleted", []auth.Permission{auth.PermSettingsManage}, 2 * time.Hour},
	// 04-P4c: scheduler.metadata-refresh merged into maintenance.metadata-refresh.
	{"scheduler.metadata-refresh", "maintenance.metadata-refresh", []auth.Permission{auth.PermSettingsManage}, 4 * time.Hour},
	// 04-P4d: scheduler.resolve-production-authors merged into maintenance.resolve-production-authors.
	{"scheduler.resolve-production-authors", "maintenance.resolve-production-authors", []auth.Permission{auth.PermSettingsManage}, 2 * time.Hour},
	// 04-P4e: scheduler.author-split-scan merged into maintenance.author-split-scan.
	{"scheduler.author-split-scan", "maintenance.author-split-scan", []auth.Permission{auth.PermSettingsManage}, 2 * time.Hour},
	// 04-P4f: scheduler.cleanup-old-backups merged into maintenance.cleanup-old-backups.
	{"scheduler.cleanup-old-backups", "maintenance.cleanup-old-backups", []auth.Permission{auth.PermSettingsManage}, 1 * time.Hour},
	// 04-P14a: maintenance.author-dedup-scan merged into dedup.author-scan. The alias moves from
	// admin-only (settings.manage fallback) to library.edit_metadata (editor); same body editors
	// could already run as dedup.author-scan.
	{"maintenance.author-dedup-scan", "dedup.author-scan", []auth.Permission{auth.PermLibraryEditMetadata}, 1 * time.Hour},
	// 04-P14b: scheduler.dedup-llm-review (settings.manage) merged into dedup.llm-review (D54).
	// The survivor declared no Permissions and fell back to settings.manage, so the
	// alias neither widens nor narrows the gate; the explicit declaration only
	// pins it. Timeout stays 2h (120m equals the loser's 2h).
	{"scheduler.dedup-llm-review", "dedup.llm-review", []auth.Permission{auth.PermSettingsManage}, 2 * time.Hour},
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
				// The body must name the survivor's permission. Without this the
				// alias leg also passes against the pre-merge loser def, whose empty
				// Permissions fall back to settings.manage and refuse scan.trigger too.
				assert.Contains(t, w.Body.String(), string(row.perms[0]),
					"403 on %q must come from the survivor's gate", id)
			}
		})
	}
}

// retiredMerges lists ops that were retired with NO alias because the survivor
// is a different deleter: routing an old enqueue to it would run something the
// caller did not ask for, so the old ID resolves to nothing and fails loudly.
// The survivor must still carry its own gate.
var retiredMerges = []struct {
	old, survivor string
	perms         []auth.Permission
}{
	// 04-P3b: the manual cleanup-backups job deleted *.backup / *.bak files with
	// no age test; the survivor deletes only fileops-written .bak-<unix>[-N]
	// names past retention.
	{"maintenance.cleanup-backups", "maintenance.cleanup-old-backups", []auth.Permission{auth.PermSettingsManage}},
}

func TestTwinMerge_RetiredIDsAndSurvivorPermissions(t *testing.T) {
	srv, _, _ := bootRegisteredOpIDs(t)

	for _, row := range retiredMerges {
		t.Run(row.old, func(t *testing.T) {
			// The old ID resolves to nothing, and canonicalisation leaves it as
			// itself so a timeline filter on it still finds its old rows.
			_, ok := srv.opRegistry.Def(row.old)
			assert.False(t, ok, "Def(%q) must not resolve: it is retired, not aliased", row.old)
			assert.Equal(t, row.old, srv.opRegistry.CanonicalDefID(row.old))

			surv, ok := srv.opRegistry.Def(row.survivor)
			require.True(t, ok, "survivor %q must be registered", row.survivor)
			assert.Equal(t, row.perms, surv.Permissions)

			// A caller holding only scan.trigger is refused on the survivor and
			// EnqueueOp is never reached (the mock sets no EnqueueOp expectation).
			reg := handlersmocks.NewMockOperationsRegistry(t)
			reg.EXPECT().Def(row.survivor).RunAndReturn(srv.opRegistry.Def)
			h := handlers.NewOperationsV2Handler(nil, reg, nil, true)

			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/operations/v2",
				strings.NewReader(`{"def_id":"`+row.survivor+`"}`))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Request = c.Request.WithContext(auth.WithPermissions(c.Request.Context(),
				[]auth.Permission{auth.PermScanTrigger}))
			h.TriggerOperationV2(c)

			assert.Equal(t, http.StatusForbidden, w.Code)
			assert.Contains(t, w.Body.String(), string(row.perms[0]))
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
