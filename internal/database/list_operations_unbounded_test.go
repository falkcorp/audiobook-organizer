// file: internal/database/list_operations_unbounded_test.go
// version: 1.0.2
// guid: 7d40b6c1-9e25-4a83-bb17-2c5f8e04d961
// last-edited: 2026-09-02

package database

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// ListOperations treats limit <= 0 as "no limit". expiredOperationIDs in
// internal/maintenance/jobs relies on that to collect the whole listing in one
// call instead of paging, so the sentinel is load-bearing for a production
// retention run rather than a convenience.
//
// This test has to exist HERE, against the real PebbleStore. The jobs package
// exercises expiredOperationIDs entirely through a fake Store, so reverting the
// sentinel in the real implementation leaves every test in that package green —
// verified by mutation. Were the real store to return an empty page for
// limit == 0, retention would collect nothing, delete nothing, report success,
// and no test would notice.
func TestListOperations_LimitZeroReturnsEverything(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	// More rows than any page size the callers use, so a implementation that
	// silently clamps to a default page cannot pass.
	const total = 1200
	for i := range total {
		_, err := store.CreateOperation(fmt.Sprintf("op-%04d", i), "scan", nil)
		require.NoError(t, err)
	}

	t.Run("limit 0 returns all rows", func(t *testing.T) {
		ops, reported, err := store.ListOperations(0, 0)
		require.NoError(t, err)
		require.Equal(t, total, reported, "reported total must count every operation")
		require.Len(t, ops, total,
			"limit <= 0 means no limit; a short slice here makes retention collect nothing")
	})

	t.Run("negative limit behaves the same", func(t *testing.T) {
		ops, _, err := store.ListOperations(-1, 0)
		require.NoError(t, err)
		require.Len(t, ops, total)
	})

	t.Run("offset still applies when unbounded", func(t *testing.T) {
		const offset = 500
		ops, reported, err := store.ListOperations(0, offset)
		require.NoError(t, err)
		require.Equal(t, total, reported, "total is the full count, not the page length")
		require.Len(t, ops, total-offset,
			"an unbounded listing must still start at offset")
	})

	t.Run("a positive limit is still a limit", func(t *testing.T) {
		// Guards the premise: if limit were ignored outright rather than
		// treated as a sentinel, the assertions above would pass for the
		// wrong reason.
		ops, _, err := store.ListOperations(10, 0)
		require.NoError(t, err)
		require.Len(t, ops, 10)
	})

	t.Run("unbounded listing agrees with walking pages", func(t *testing.T) {
		all, _, err := store.ListOperations(0, 0)
		require.NoError(t, err)

		var paged []Operation
		const pageSize = 500
		for offset := 0; offset < total; offset += pageSize {
			page, _, pErr := store.ListOperations(pageSize, offset)
			require.NoError(t, pErr)
			paged = append(paged, page...)
		}

		require.Len(t, paged, len(all))
		for i := range all {
			require.Equal(t, all[i].ID, paged[i].ID,
				"unbounded and paged listings must be the same sequence (index %d)", i)
		}
	})
}
