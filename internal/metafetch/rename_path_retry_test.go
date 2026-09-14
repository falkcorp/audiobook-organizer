// file: internal/metafetch/rename_path_retry_test.go
// version: 1.0.0
// guid: 2b6f9d14-8a37-4c05-9e21-5d0c7b3a6e98
// last-edited: 2026-09-14

package metafetch

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
)

func shortRetry(t *testing.T, attempts int, base time.Duration) {
	t.Helper()
	oa, ob := renamePathWriteAttempts, renamePathWriteBaseDelay
	t.Cleanup(func() { renamePathWriteAttempts, renamePathWriteBaseDelay = oa, ob })
	renamePathWriteAttempts, renamePathWriteBaseDelay = attempts, base
}

// recordingPrefs attaches a `_system` preference recorder to the fixture's
// MockStore.
func recordingPrefs(svc *Service) (func() map[string]string, *sync.Mutex) {
	var mu sync.Mutex
	prefs := map[string]string{}
	m := svc.db.(*database.MockStore)
	m.SetUserPreferenceForUserFunc = func(_, key, value string) error {
		mu.Lock()
		defer mu.Unlock()
		prefs[key] = value
		return nil
	}
	return func() map[string]string {
		mu.Lock()
		defer mu.Unlock()
		out := map[string]string{}
		for k, v := range prefs {
			if strings.HasPrefix(k, organizer.RenamePathWriteFailurePrefix) && v != "" {
				out[k] = v
			}
		}
		return out
	}, &mu
}

func TestRenameRetry_TransientBookFileWriteFailureRecovers(t *testing.T) {
	shortRetry(t, 4, time.Millisecond)
	fx, svc := newRenameFixture(t)
	records, _ := recordingPrefs(svc)
	// Each of the two files fails its first two writes, then succeeds.
	fails := map[int]bool{1: true, 2: true, 4: true, 5: true}
	fx.onFileWrite = func(n int) error {
		if fails[n] {
			return errInjectedFileWrite
		}
		return nil
	}
	stale := fx.stored
	require.NoError(t, svc.RunApplyPipelineRenameOnly(context.Background(), "b1", &stale))
	require.Empty(t, records(), "a write that succeeded on retry must leave no repair record")
	for _, f := range fx.files {
		require.NotContains(t, f.FilePath, "incoming", "book_file %s not repointed", f.ID)
	}
}

func TestRenameRetry_PersistentFailureRecordsEachPairAndFails(t *testing.T) {
	shortRetry(t, 3, time.Millisecond)
	for _, tc := range []struct {
		name string
		run  func(*Service, *database.Book) error
	}{
		{"RunApplyPipelineRenameOnly", func(s *Service, b *database.Book) error {
			return s.RunApplyPipelineRenameOnly(context.Background(), "b1", b)
		}},
		{"runApplyPipeline", func(s *Service, b *database.Book) error {
			_, err := s.runApplyPipeline(context.Background(), "b1", b, "b1", nil)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx, svc := newRenameFixture(t)
			records, _ := recordingPrefs(svc)
			oldPaths := map[string]string{}
			for _, f := range fx.files {
				oldPaths[f.ID] = f.FilePath
			}
			fx.onFileWrite = func(int) error { return errInjectedFileWrite }
			stale := fx.stored
			err := tc.run(svc, &stale)
			require.ErrorIs(t, err, errInjectedFileWrite, "the rename must still fail when the pair was recorded")
			require.Equal(t, 6, fx.fileWrites, "each of the 2 files must be attempted 3 times")

			got := records()
			for id, oldPath := range oldPaths {
				raw, ok := got[organizer.RenamePathWriteFailureKey("b1", id)]
				require.True(t, ok, "no record for book_file %s; have %v", id, got)
				var rec organizer.RenamePathWriteFailure
				require.NoError(t, json.Unmarshal([]byte(raw), &rec))
				require.Equal(t, "b1", rec.BookID)
				require.Equal(t, id, rec.BookFileID)
				require.Equal(t, oldPath, rec.OldPath)
				require.NotEmpty(t, rec.NewPath)
				require.NotEqual(t, oldPath, rec.NewPath)
				require.Contains(t, rec.Error, errInjectedFileWrite.Error())
				require.NotEmpty(t, rec.RecordedAt)
			}
		})
	}
}

func TestRetryPathWrite_CanceledContextStopsBackoff(t *testing.T) {
	shortRetry(t, 5, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		done <- retryPathWrite(ctx, func() error { calls++; return errInjectedFileWrite })
	}()
	time.AfterFunc(20*time.Millisecond, cancel)
	select {
	case err := <-done:
		require.ErrorIs(t, err, errInjectedFileWrite)
		require.ErrorIs(t, err, context.Canceled)
		require.Less(t, time.Since(start), 5*time.Second)
		require.Equal(t, 1, calls, "no attempt may run after cancellation")
	case <-time.After(5 * time.Second):
		t.Fatal("retryPathWrite ignored ctx cancellation during its hour-long backoff")
	}
}

func TestRetryPathWrite_SucceedsAfterFailures(t *testing.T) {
	shortRetry(t, 4, time.Millisecond)
	calls := 0
	err := retryPathWrite(context.Background(), func() error {
		calls++
		if calls < 3 {
			return errInjectedFileWrite
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 3, calls)
}
