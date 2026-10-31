// file: internal/server/handlers/operations/revert_user_data_test.go
// version: 1.0.2
// guid: 3b9f7d20-c4a1-4e58-9d62-8a1e5f0b7c36
// last-edited: 2026-10-30

package operations_test

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/opaccess"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/operations"
	operationsmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/operations/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// The revert report's merge warnings name a user and what they were listening
// to ("user <id> progress on ..."). POST /operations/:id/revert is a
// library.organize route, so the report is stripped of those notes for a caller
// without users.manage, in the body and in the failure message alike.
func TestRevertOperation_MergeWarningsThatNameAUserNeedUsersManage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const namesUser = "change c1: user 01INVENTEDUSER0000000000A progress on The-Long-Halloween kept at 91%"
	const plain = "change c2: a credit added since was left as it is"
	result := func() *audiobooks.RevertResult {
		return &audiobooks.RevertResult{
			OperationID: "op-1", Total: 2, Restored: 2, PartiallyRestored: 1,
			Kept: []audiobooks.RevertNote{{Text: namesUser, UserIDs: []string{"01INVENTEDUSER0000000000A"}}, {Text: plain}},
		}
	}
	for name, tc := range map[string]struct {
		perms   []string
		canRead bool
	}{
		"editor": {[]string{auth.PermLibraryView, auth.PermLibraryOrganize}, false},
		"admin":  {[]string{auth.PermLibraryView, auth.PermLibraryOrganize, auth.PermUsersManage}, true},
	} {
		holds := func(_ *gin.Context, p auth.Permission) bool {
			for _, have := range tc.perms {
				if have == p {
					return true
				}
			}
			return false
		}
		store := operationsmocks.NewMockOperationsStore(t)
		allowRevertReads(store)
		h := operations.New(store, nil, nil, nil, nil, nil, nil,
			func([]*database.OperationChange) (*undo.UndoConflictReport, error) { return nil, nil },
			func(string, audiobooks.RevertActor) (*audiobooks.RevertResult, error) { return result(), nil },
		).WithRepairsOpAccess(opaccess.NewRepairs(noFixers{}, opRowsByID{}, holds))
		w := postRevert(h)
		require.Equal(t, http.StatusOK, w.Code, "%s: %s", name, w.Body.String())
		body := w.Body.String()
		assert.Contains(t, body, "a credit added since was left as it is", "%s: the ordinary note stays", name)
		if tc.canRead {
			assert.Contains(t, body, "01INVENTEDUSER0000000000A", name)
			continue
		}
		assert.NotContains(t, body, "01INVENTEDUSER0000000000A", "%s is served a note that names a user", name)
		assert.NotContains(t, body, "The-Long-Halloween", name)
		assert.Contains(t, body, "1 note(s) that name a user withheld", "%s: the list says it is shorter", name)
	}
}
