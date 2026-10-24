// file: internal/server/middleware/check_permission_test.go
// version: 1.1.0
// guid: fce91a68-942b-4396-a09d-db4029d93a8b
// last-edited: 2026-10-24

package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// countUsers is a UserCounter answering n (or err).
type countUsers struct {
	n   int
	err error
}

func (c countUsers) CountUsers() (int, error) { return c.n, c.err }

// checkAs runs CheckPermission for p on a request carrying user (nil: none)
// and perms, and returns its answer and the response it wrote.
func checkAs(store UserCounter, user *database.User, perms []auth.Permission, p auth.Permission) (bool, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	ctx := auth.WithPermissions(auth.WithUser(req.Context(), user), perms)
	c.Request = req.WithContext(ctx)
	ok := CheckPermission(store, c, p)
	return ok, w
}

// TestCheckPermission_UnauthenticatedIs401ForbiddenIs403 pins the status
// codes handlers inherit from CheckPermission: no user on the request is a
// 401, a user without the permission is a 403 naming it, a user holding it
// passes with nothing written, and the first-run bypass (no users yet)
// passes even with no user. A store that cannot count users does not
// bypass.
func TestCheckPermission_UnauthenticatedIs401ForbiddenIs403(t *testing.T) {
	editor := &database.User{ID: "u-editor", Username: "editor"}
	some := countUsers{n: 2}

	ok, w := checkAs(some, nil, nil, auth.PermSettingsManage)
	if ok || w.Code != http.StatusUnauthorized {
		t.Fatalf("no user: ok=%v code=%d, want false 401", ok, w.Code)
	}

	ok, w = checkAs(some, editor, []auth.Permission{auth.PermLibraryEditMetadata}, auth.PermSettingsManage)
	if ok || w.Code != http.StatusForbidden {
		t.Fatalf("user without the permission: ok=%v code=%d, want false 403", ok, w.Code)
	}
	if !strings.Contains(w.Body.String(), "permission denied: settings.manage") {
		t.Fatalf("403 body %q does not name the permission", w.Body.String())
	}

	ok, w = checkAs(some, editor, []auth.Permission{auth.PermSettingsManage}, auth.PermSettingsManage)
	if !ok || w.Body.Len() != 0 {
		t.Fatalf("user holding it: ok=%v body=%q, want true and nothing written", ok, w.Body.String())
	}

	ok, w = checkAs(countUsers{n: 0}, nil, nil, auth.PermSettingsManage)
	if !ok || w.Body.Len() != 0 {
		t.Fatalf("first run: ok=%v body=%q, want the bypass", ok, w.Body.String())
	}

	ok, w = checkAs(countUsers{err: errors.New("store down")}, nil, nil, auth.PermSettingsManage)
	if ok || w.Code != http.StatusUnauthorized {
		t.Fatalf("count error: ok=%v code=%d, want no bypass (401)", ok, w.Code)
	}

	ok, w = checkAs(nil, nil, nil, auth.PermSettingsManage)
	if ok || w.Code != http.StatusUnauthorized {
		t.Fatalf("no store: ok=%v code=%d, want no bypass (401)", ok, w.Code)
	}
}

// TestHoldsPermission_AnswersWithoutWriting: the question CheckPermission
// asks, with nothing written to the response whatever the answer.
func TestHoldsPermission_AnswersWithoutWriting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	editor := &database.User{ID: "u-editor", Username: "editor"}
	held := []auth.Permission{auth.PermLibraryEditMetadata}
	for name, tc := range map[string]struct {
		store UserCounter
		user  *database.User
		perms []auth.Permission
		want  bool
	}{
		"held":                  {countUsers{n: 2}, editor, held, true},
		"not held":              {countUsers{n: 2}, editor, nil, false},
		"no user":               {countUsers{n: 2}, nil, nil, false},
		"first run bypass":      {countUsers{n: 0}, nil, nil, true},
		"count error no bypass": {countUsers{err: errors.New("down")}, nil, nil, false},
		"nil store":             {nil, editor, nil, false},
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		c.Request = req.WithContext(auth.WithPermissions(auth.WithUser(req.Context(), tc.user), tc.perms))
		if got := HoldsPermission(tc.store, c, auth.PermLibraryEditMetadata); got != tc.want {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
		if w.Body.Len() != 0 || c.Writer.Written() {
			t.Errorf("%s: wrote a response: %q", name, w.Body.String())
		}
	}
}
