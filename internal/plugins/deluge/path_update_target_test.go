// file: internal/plugins/deluge/path_update_target_test.go
// version: 0.1.0
// guid: 0b4d6f2e-8a31-4c75-9e10-d3a5c7b92f48
// last-edited: 2026-11-22

package deluge

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// destMover records every MoveStorage call's destination.
type destMover struct {
	mu    sync.Mutex
	moves map[string]string // torrent hash -> destination
}

func (m *destMover) MoveStorage(ids []string, dest string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.moves == nil {
		m.moves = map[string]string{}
	}
	m.moves[ids[0]] = dest
	return nil
}

func (m *destMover) snapshot() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]string{}
	for k, v := range m.moves {
		out[k] = v
	}
	return out
}

// runTarget runs pathUpdateCore live for the fixture's one-version original
// with the given target, over a store that serves the original and a target
// book looked up by id at run time (targets maps id -> stored path; an id not
// in the map is a book that does not exist).
func runTarget(t *testing.T, targetID string, targets map[string]string, m *destMover) (pathUpdateResult, error) {
	t.Helper()
	f := newPUFixture()
	f.versions = f.versions[:1]
	st := f.store()
	orig := st.GetBookByIDFunc
	st.GetBookByIDFunc = func(id string) (*database.Book, error) {
		if p, ok := targets[id]; ok {
			return &database.Book{ID: id, FilePath: p}, nil
		}
		if id == f.book.ID {
			return orig(id)
		}
		return nil, nil
	}
	env := pathUpdateEnv{
		kindEnv: headEnv(&parityReporter{}, true),
		Cfg:     &config.Config{DelugeMoveEnabled: true},
		Store:   st,
		Mover:   m,
	}
	return pathUpdateCore(context.Background(), pathUpdateCoreParams{BookID: f.book.ID, TargetBookID: targetID}, env)
}

// The torrents of the original follow the TARGET's path as stored when the run
// runs: a copy that was moved again after the message was staged is followed,
// and the original's own (unchanged) path is never the destination.
func TestPathUpdateTarget_MovesToTheTargetsCurrentStoredPath(t *testing.T) {
	h1, _, _ := puHashes()
	targets := map[string]string{"copy-1": "/gotham/library/batman/dark-knight/part1.m4b"}

	m := &destMover{}
	res, err := runTarget(t, "copy-1", targets, m)
	require.NoError(t, err)
	require.EqualValues(t, 1, res.Updated)
	require.Equal(t, map[string]string{h1: "/gotham/library/batman/dark-knight"}, m.snapshot())

	// The copy is moved again before a replay of the same message runs.
	targets["copy-1"] = "/gotham/library/miller/dark-knight/part1.m4b"
	m2 := &destMover{}
	_, err = runTarget(t, "copy-1", targets, m2)
	require.NoError(t, err)
	require.Equal(t, map[string]string{h1: "/gotham/library/miller/dark-knight"}, m2.snapshot(),
		"a replay follows the copy where it is now, never a path taken from the message")
}

// A target that is gone (the copy was rolled back or deleted) or has no path is
// permanent: the run ends successfully, having moved nothing.
func TestPathUpdateTarget_MissingTargetIsANoOp(t *testing.T) {
	for name, targets := range map[string]map[string]string{
		"the target book does not exist": {},
		"the target book has no path":    {"copy-1": ""},
	} {
		t.Run(name, func(t *testing.T) {
			m := &destMover{}
			res, err := runTarget(t, "copy-1", targets, m)
			require.NoError(t, err, "not retryable and not a failure: nothing is left to follow")
			require.Empty(t, m.snapshot(), "no torrent moved")
			require.EqualValues(t, 0, res.Updated)
		})
	}
}

// No target keeps the kind's own meaning: the book's own stored path.
func TestPathUpdateTarget_NoTargetFollowsTheBooksOwnPath(t *testing.T) {
	h1, _, _ := puHashes()
	m := &destMover{}
	_, err := runTarget(t, "", map[string]string{}, m)
	require.NoError(t, err)
	require.Equal(t, map[string]string{h1: "/library/Frank Miller/Dark Knight Returns"}, m.snapshot())
}
