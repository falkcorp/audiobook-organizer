// file: internal/metafetch/rename_path_record_clear_test.go
// version: 1.0.0
// guid: 7f3c1a58-e926-4b04-8d7e-2a5b9c0e1f64
// last-edited: 2026-09-14

package metafetch

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
)

// prefStore gives the fixture's MockStore a readable and writable `_system`
// keyspace, seeded with prefs.
func prefStore(svc *Service, prefs map[string]string) *sync.Mutex {
	var mu sync.Mutex
	m := svc.db.(*database.MockStore)
	m.GetUserPreferenceForUserFunc = func(userID, key string) (*database.UserPreferenceKV, error) {
		mu.Lock()
		defer mu.Unlock()
		v, ok := prefs[key]
		if !ok {
			return nil, nil
		}
		return &database.UserPreferenceKV{UserID: userID, Key: key, Value: v}, nil
	}
	m.SetUserPreferenceForUserFunc = func(_, key, value string) error {
		mu.Lock()
		defer mu.Unlock()
		prefs[key] = value
		return nil
	}
	return &mu
}

// A record left by an earlier failed write is cleared when a later rename of
// the same row writes its path successfully. Left in place, it would describe a
// move that is no longer the row's state (an ABA hazard for the repair op).
func TestRename_SuccessfulPathWriteClearsEarlierRecord(t *testing.T) {
	fx, svc := newRenameFixture(t)
	stale := `{"book_id":"b1","old_path":"/x","new_path":"/y","error":"old"}`
	prefs := map[string]string{
		organizer.RenamePathWriteFailureKey("b1", "f1"): stale,
		organizer.RenamePathWriteFailureKey("b1", "f2"): stale,
		organizer.RenamePathWriteFailureKey("b1", ""):   stale,
	}
	mu := prefStore(svc, prefs)

	st := fx.stored
	require.NoError(t, svc.RunApplyPipelineRenameOnly(context.Background(), "b1", &st))

	mu.Lock()
	defer mu.Unlock()
	for k, v := range prefs {
		require.Empty(t, v, "record %s not cleared by the successful write", k)
	}
}
