// file: internal/server/handlers/audiobooks/handler_batch_hard_delete_permission_test.go
// version: 1.1.0
// guid: e8602b33-8ac7-4a49-ab67-bc5184d0e660
// last-edited: 2026-10-25

package audiobookshandler_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/batch"
)

// batchCase is one request shape and the permissions it must ask for, in order.
type batchCase struct {
	name string
	// ops is the body of POST /audiobooks/batch-operations, updates the body of
	// POST /audiobooks/batch (nil when the shape has no equivalent there).
	ops     map[string]any
	updates map[string]any
	asks    []auth.Permission
}

func opsBody(ops ...map[string]any) map[string]any {
	return map[string]any{"operations": ops}
}

func batchCases() []batchCase {
	del := auth.PermLibraryDelete
	org := auth.PermLibraryOrganize
	return []batchCase{
		{
			name: "operations: hard delete asks library.delete",
			ops:  opsBody(map[string]any{"id": "b1", "action": "delete", "hard_delete": true}),
			asks: []auth.Permission{del},
		},
		{
			name: "operations: soft delete asks library.delete",
			ops:  opsBody(map[string]any{"id": "b1", "action": "delete"}),
			asks: []auth.Permission{del},
		},
		{
			name: "operations: delete with hard_delete false is a soft delete",
			ops:  opsBody(map[string]any{"id": "b1", "action": "delete", "hard_delete": false}),
			asks: []auth.Permission{del},
		},
		{
			name: "operations: restore action asks library.organize",
			ops:  opsBody(map[string]any{"id": "b1", "action": "restore"}),
			asks: []auth.Permission{org},
		},
		{
			name: "operations: update marked_for_deletion true asks library.delete",
			ops:  opsBody(map[string]any{"id": "b1", "action": "update", "updates": map[string]any{"marked_for_deletion": true}}),
			asks: []auth.Permission{del},
		},
		{
			name: "operations: update library_state deleted asks library.delete",
			ops:  opsBody(map[string]any{"id": "b1", "action": "update", "updates": map[string]any{"library_state": " Deleted "}}),
			asks: []auth.Permission{del},
		},
		{
			name: "operations: update marked_for_deletion false asks library.organize",
			ops:  opsBody(map[string]any{"id": "b1", "action": "update", "updates": map[string]any{"marked_for_deletion": false}}),
			asks: []auth.Permission{org},
		},
		{
			name: "operations: a trash and a restore in one list ask for both",
			ops: opsBody(
				map[string]any{"id": "b1", "action": "delete"},
				map[string]any{"id": "b2", "action": "restore"},
			),
			asks: []auth.Permission{del, org},
		},
		{
			name: "operations: a metadata update asks for nothing",
			ops:  opsBody(map[string]any{"id": "b1", "action": "update", "updates": map[string]any{"title": "A Title"}}),
			asks: nil,
		},
		{
			name:    "batch: marked_for_deletion true asks library.delete",
			updates: map[string]any{"marked_for_deletion": true},
			asks:    []auth.Permission{del},
		},
		{
			name:    "batch: library_state deleted asks library.delete",
			updates: map[string]any{"library_state": "deleted"},
			asks:    []auth.Permission{del},
		},
		{
			name:    "batch: marked_for_deletion false asks library.organize",
			updates: map[string]any{"marked_for_deletion": false},
			asks:    []auth.Permission{org},
		},
		{
			name:    "batch: a metadata edit asks for nothing",
			updates: map[string]any{"title": "A Title", "genre": "Fantasy"},
			asks:    nil,
		},
	}
}

// call runs the case's body on the route it belongs to and reports which route
// that was.
func (tc batchCase) call(t *testing.T, h interface {
	BatchOperations(*gin.Context)
	BatchUpdateAudiobooks(*gin.Context)
}) *httptest.ResponseRecorder {
	t.Helper()
	if tc.ops != nil {
		c, w := newCtx("POST", "/audiobooks/batch-operations", tc.ops, nil)
		h.BatchOperations(c)
		return w
	}
	c, w := newCtx("POST", "/audiobooks/batch", map[string]any{"ids": []string{"b1"}, "updates": tc.updates}, nil)
	h.BatchUpdateAudiobooks(c)
	return w
}

// expectService lets the case through to the batch service on whichever route
// it uses.
func (tc batchCase) expectService(d testDeps) {
	if tc.ops != nil {
		d.batchSvc.EXPECT().ExecuteOperations(mock.Anything).Return(&batch.BatchResponse{}).Maybe()
		return
	}
	d.batchSvc.EXPECT().UpdateAudiobooks(mock.Anything).Return(&batch.BatchResponse{}).Maybe()
}

// What a bulk request does to books' place in the library needs what the
// single-book routes need: hard delete and soft delete library.delete, restore
// library.organize. The check runs in the handler on the request it bound, so
// the permissions it asks for are exactly those the request's effects call for.
func TestBatchRoutes_TrashEffectsAskForTheSingleBookPermissions(t *testing.T) {
	for _, tc := range batchCases() {
		t.Run(tc.name, func(t *testing.T) {
			h, d := newHandler(t)
			var asked []auth.Permission
			h.SetPermissionCheck(func(_ *gin.Context, p auth.Permission) bool {
				asked = append(asked, p)
				return true
			})
			tc.expectService(d)
			w := tc.call(t, h)
			if w.Code != http.StatusOK {
				t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
			}
			if len(asked) != len(tc.asks) {
				t.Fatalf("asked %v, want %v", asked, tc.asks)
			}
			for i := range asked {
				if asked[i] != tc.asks[i] {
					t.Fatalf("asked %v, want %v", asked, tc.asks)
				}
			}
		})
	}
}

// A request with a trash or restore effect that the check refuses, or that
// reaches an unwired handler (strict: no user means 401), never reaches the
// batch service. mockery fails the test on any call it was not told to expect,
// and none is expected here.
func TestBatchRoutes_RefusedOrUnwiredNeverReachTheService(t *testing.T) {
	for _, tc := range batchCases() {
		if len(tc.asks) == 0 {
			continue
		}
		t.Run(tc.name+" / unwired", func(t *testing.T) {
			h, _ := newHandler(t)
			w := tc.call(t, h)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("want 401, got %d: %s", w.Code, w.Body.String())
			}
		})
		t.Run(tc.name+" / refused", func(t *testing.T) {
			h, _ := newHandler(t)
			var asked auth.Permission
			h.SetPermissionCheck(func(c *gin.Context, p auth.Permission) bool {
				asked = p
				c.AbortWithStatus(http.StatusForbidden)
				return false
			})
			w := tc.call(t, h)
			if w.Code != http.StatusForbidden {
				t.Fatalf("want 403, got %d: %s", w.Code, w.Body.String())
			}
			if asked != tc.asks[0] {
				t.Fatalf("refused on %q, want the first permission asked to be %q", asked, tc.asks[0])
			}
		})
	}
}

// A refusal on one permission stops the request even when another it needs is
// granted: a list that trashes and restores needs both.
func TestBatchOperations_TrashAndRestoreNeedBothPermissions(t *testing.T) {
	body := opsBody(
		map[string]any{"id": "b1", "action": "delete"},
		map[string]any{"id": "b2", "action": "restore"},
	)
	for _, withheld := range []auth.Permission{auth.PermLibraryDelete, auth.PermLibraryOrganize} {
		t.Run("without "+string(withheld), func(t *testing.T) {
			h, _ := newHandler(t)
			h.SetPermissionCheck(func(c *gin.Context, p auth.Permission) bool {
				if p == withheld {
					c.AbortWithStatus(http.StatusForbidden)
					return false
				}
				return true
			})
			c, w := newCtx("POST", "/audiobooks/batch-operations", body, nil)
			h.BatchOperations(c)
			if w.Code != http.StatusForbidden {
				t.Fatalf("want 403, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}
