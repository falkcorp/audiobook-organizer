// file: internal/server/server_lifecycle_abssession_test.go
// version: 1.0.0
// guid: 753d5dbe-11ab-44b4-b8a7-65a78b8a9f27
// last-edited: 2026-08-22

package server

import (
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// fakeSessionPruner records each call so a test can assert both sweeps ran.
type fakeSessionPruner struct {
	authCalls, absCalls int
	authNow, absNow     time.Time
	authN, absN         int
	authErr, absErr     error
}

func (f *fakeSessionPruner) DeleteExpiredSessions(now time.Time) (int, error) {
	f.authCalls++
	f.authNow = now
	return f.authN, f.authErr
}

func (f *fakeSessionPruner) DeleteExpiredABSSessions(now time.Time) (int, error) {
	f.absCalls++
	f.absNow = now
	return f.absN, f.absErr
}

func testSessionLogger() *logger.StandardLogger {
	return logger.NewWithActivityLog("session-cleanup-test", nil)
}

// The whole point of TASK-139: the periodic sweep must prune ABS sessions too,
// not just auth sessions. Before this change abs_sess: records were never
// reclaimed.
func TestPruneExpiredSessions_SweepsBothStores(t *testing.T) {
	f := &fakeSessionPruner{authN: 3, absN: 5}
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

	pruneExpiredSessions(f, testSessionLogger(), now)

	if f.authCalls != 1 {
		t.Errorf("DeleteExpiredSessions called %d times, want 1", f.authCalls)
	}
	if f.absCalls != 1 {
		t.Errorf("DeleteExpiredABSSessions called %d times, want 1", f.absCalls)
	}
	if !f.authNow.Equal(now) || !f.absNow.Equal(now) {
		t.Errorf("both sweeps must use the same tick time: auth=%v abs=%v want %v", f.authNow, f.absNow, now)
	}
}

// A failing auth sweep must not skip the ABS sweep — the two stores are
// independent, and short-circuiting would let abs_sess: grow without bound
// whenever the auth delete is unhealthy.
func TestPruneExpiredSessions_ABSSweepRunsAfterAuthError(t *testing.T) {
	f := &fakeSessionPruner{authErr: errors.New("pebble: auth sweep failed"), absN: 2}

	pruneExpiredSessions(f, testSessionLogger(), time.Now())

	if f.absCalls != 1 {
		t.Errorf("ABS sweep ran %d times after an auth-sweep error, want 1", f.absCalls)
	}
}

// Zero deletions and an ABS error are both non-fatal: the function logs and
// returns rather than panicking on the nil-ish/empty path.
func TestPruneExpiredSessions_ZeroDeletionsAndABSErrorAreNonFatal(t *testing.T) {
	f := &fakeSessionPruner{authN: 0, absErr: errors.New("pebble: abs sweep failed")}

	pruneExpiredSessions(f, testSessionLogger(), time.Now())

	if f.authCalls != 1 || f.absCalls != 1 {
		t.Errorf("both sweeps must be attempted: auth=%d abs=%d", f.authCalls, f.absCalls)
	}
}
