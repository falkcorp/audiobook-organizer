// file: internal/plugins/deluge/path_update_retry_test.go
// version: 0.1.0
// guid: c7e2a9d4-3f61-4b08-8d95-6a1b0e4f7c23
// last-edited: 2026-11-22

package deluge

import (
	"context"
	"errors"
	"net"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// scriptedMover answers each MoveStorage call for a hash from that hash's
// script (an error, or nil for success); a hash with no script left succeeds.
type scriptedMover struct {
	mu     sync.Mutex
	script map[string][]error
	calls  map[string]int
}

func (m *scriptedMover) MoveStorage(ids []string, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.calls == nil {
		m.calls = map[string]int{}
	}
	h := ids[0]
	m.calls[h]++
	if s := m.script[h]; len(s) > 0 {
		m.script[h] = s[1:]
		return s[0]
	}
	return nil
}

func (m *scriptedMover) callsFor(h string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls[h]
}

var errTransient = &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}

// runRetry runs pathUpdateCore live for the one-version fixture with the scripted
// mover, recording the waits between attempts.
func runRetry(t *testing.T, ctx context.Context, m *scriptedMover, sleep func(context.Context, time.Duration) error) (pathUpdateResult, error, []time.Duration) {
	t.Helper()
	f := newPUFixture()
	f.versions = f.versions[:1]
	var waits []time.Duration
	if sleep == nil {
		sleep = func(context.Context, time.Duration) error { return nil }
	}
	env := pathUpdateEnv{
		kindEnv: headEnv(&parityReporter{}, true),
		Cfg:     &config.Config{DelugeMoveEnabled: true},
		Store:   f.store(),
		Mover:   m,
		Sleep: func(ctx context.Context, d time.Duration) error {
			waits = append(waits, d)
			return sleep(ctx, d)
		},
	}
	res, err := pathUpdateCore(ctx, pathUpdateCoreParams{BookID: "book-dark-knight-returns"}, env)
	return res, err, waits
}

// TestPathUpdateRetry pins the in-run retry of a torrent's move (F6-03 m1). The
// retry is in the run because an outbox delivery is acked when the run is
// staged, so nothing redelivers a run that ends failed.
func TestPathUpdateRetry(t *testing.T) {
	h1, _, _ := puHashes()

	t.Run("transient twice then success is one move and no error", func(t *testing.T) {
		m := &scriptedMover{script: map[string][]error{h1: {errTransient, errTransient}}}
		res, err, waits := runRetry(t, context.Background(), m, nil)
		require.NoError(t, err)
		require.Equal(t, 3, m.callsFor(h1))
		require.EqualValues(t, 1, res.Updated)
		require.Equal(t, []time.Duration{time.Second, 2 * time.Second}, waits, "backoff doubles from moveBackoff")
	})

	t.Run("a permanent refusal is not retried and the run names the torrent", func(t *testing.T) {
		m := &scriptedMover{script: map[string][]error{h1: {errors.New("deluge error 4: InvalidTorrentError: not in session")}}}
		res, err, waits := runRetry(t, context.Background(), m, nil)
		require.EqualError(t, err, "move_storage failed for 1 torrent(s): "+h1)
		require.Equal(t, 1, m.callsFor(h1))
		require.Empty(t, waits)
		require.EqualValues(t, 0, res.Updated)
	})

	t.Run("transient three times fails the run, and a fresh run moves it", func(t *testing.T) {
		m := &scriptedMover{script: map[string][]error{h1: {errTransient, errTransient, errTransient}}}
		res, err, waits := runRetry(t, context.Background(), m, nil)
		require.EqualError(t, err, "move_storage failed for 1 torrent(s): "+h1)
		require.NotContains(t, err.Error(), "/library", "hashes only, never paths")
		require.Equal(t, moveAttempts, m.callsFor(h1), "the attempt bound is exactly moveAttempts")
		require.Len(t, waits, moveAttempts-1, "no wait after the last attempt")
		require.EqualValues(t, 0, res.Updated)

		// ResumeFromZero: the second run starts over and the script is spent.
		res, err, _ = runRetry(t, context.Background(), m, nil)
		require.NoError(t, err)
		require.Equal(t, moveAttempts+1, m.callsFor(h1))
		require.EqualValues(t, 1, res.Updated)
	})

	t.Run("a canceled run stops the wait and reports the context, not the torrent", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		m := &scriptedMover{script: map[string][]error{h1: {errTransient, errTransient, errTransient}}}
		_, err, _ := runRetry(t, ctx, m, func(ctx context.Context, _ time.Duration) error {
			cancel()
			return ctxSleep(ctx, time.Hour) // returns at once: ctx is done
		})
		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, 1, m.callsFor(h1), "no attempt after the cancel")
	})

	t.Run("ctxSleep honours the context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		start := time.Now()
		require.ErrorIs(t, ctxSleep(ctx, time.Hour), context.Canceled)
		require.Less(t, time.Since(start), time.Second)
		require.NoError(t, ctxSleep(context.Background(), time.Millisecond))
	})
}
