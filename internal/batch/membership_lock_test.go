// file: internal/batch/membership_lock_test.go
// version: 1.1.0
// guid: 3e8b1f6a-7c2d-4a95-9e04-b6d1c8f2a573
// last-edited: 2026-10-02

package batch

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// A batch edit that moves a book out of version group g waits for g's
// hand-off lock: a reader holding it (itunes.regroup's apply-time recheck
// through its moves) sees no member leave until it releases. The same holds
// for the group the book joins.
func TestBatchUpdate_GroupChangeWaitsForGroupLocks(t *testing.T) {
	for _, held := range []string{"g", "h"} {
		t.Run("holding "+held, func(t *testing.T) {
			f := vptest.New(t)
			prev := config.AppConfig.RootDir
			config.AppConfig.RootDir = f.Root
			t.Cleanup(func() { config.AppConfig.RootDir = prev })
			f.Book(t, vptest.Spec{ID: "inc", Group: "g", Primary: "true"})
			mover := f.Book(t, vptest.Spec{ID: "mover", Group: "g", Primary: "false"})

			unlock := versionprimary.LockGroup(held)
			done := make(chan *BatchResponse, 1)
			go func() {
				done <- NewBatchService(f.S).UpdateAudiobooks(&BatchUpdateRequest{
					IDs: []string{mover}, Updates: map[string]any{"version_group_id": "h"},
				})
			}()
			select {
			case <-done:
				unlock()
				t.Fatalf("batch group change wrote while group %q's lock was held", held)
			case <-time.After(300 * time.Millisecond):
			}
			b, err := f.S.GetBookByID(mover)
			require.NoError(t, err)
			require.Equal(t, "g", *b.VersionGroupID, "book left the group while its lock was held")
			unlock()

			select {
			case resp := <-done:
				require.Equal(t, 1, resp.Success, "errors: %+v", resp.Results)
			case <-time.After(10 * time.Second):
				t.Fatal("batch group change still blocked after the lock was released")
			}
			b, err = f.S.GetBookByID(mover)
			require.NoError(t, err)
			require.Equal(t, "h", *b.VersionGroupID)
		})
	}
}

// movingStore moves the book into a fresh group just before each of the
// first `moves` batch writes, as a concurrent writer would between the
// batch's read and its write.
type movingStore struct {
	*database.PebbleStore
	id    string
	moves int
	calls int
}

func (s *movingStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if id == s.id && s.calls < s.moves {
		s.calls++
		g := fmt.Sprintf("elsewhere-%d", s.calls)
		if _, err := s.PebbleStore.ModifyBook(id, func(b *database.Book) error {
			b.VersionGroupID = &g
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return s.PebbleStore.ModifyBook(id, fn)
}

// A book that changes group between the batch's read and its write is
// re-read and retried; after three such changes the write is refused with
// ErrMembershipChanged and nothing is written.
func TestBatchUpdate_GroupChangeRetriesOnMembershipChange(t *testing.T) {
	cases := []struct {
		name    string
		moves   int
		wantOK  bool
		wantGrp string
	}{
		{"one change then success", 1, true, "h"},
		{"changes every attempt", 99, false, "elsewhere-3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := vptest.New(t)
			prev := config.AppConfig.RootDir
			config.AppConfig.RootDir = f.Root
			t.Cleanup(func() { config.AppConfig.RootDir = prev })
			f.Book(t, vptest.Spec{ID: "inc", Group: "g", Primary: "true"})
			mover := f.Book(t, vptest.Spec{ID: "mover", Group: "g", Primary: "false"})

			st := &movingStore{PebbleStore: f.S, id: mover, moves: tc.moves}
			resp := NewBatchService(st).UpdateAudiobooks(&BatchUpdateRequest{
				IDs: []string{mover}, Updates: map[string]any{"version_group_id": "h"},
			})
			if tc.wantOK {
				require.Equal(t, 1, resp.Success, "results: %+v", resp.Results)
				// The one move made the first attempt fail; landing in h
				// proves the retry wrote.
				require.Equal(t, 1, st.calls)
			} else {
				require.Equal(t, 0, resp.Success)
				require.Equal(t, 3, st.calls, "want exactly three attempts")
				require.NotEmpty(t, resp.Results)
				require.Contains(t, fmt.Sprintf("%+v", resp.Results), versionprimary.ErrMembershipChanged.Error())
			}
			b, err := f.S.GetBookByID(mover)
			require.NoError(t, err)
			require.Equal(t, tc.wantGrp, *b.VersionGroupID)
		})
	}
}
