// file: internal/plugins/maintenance/scan_standdown_apply_test.go
// version: 1.0.0
// guid: 5c8e1a94-2d76-4f03-9b6e-1a4c7e0d258f
// last-edited: 2026-09-06

package maintenance

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// recordingScanController is a fake ScanController that counts every call and
// lets a test steer acquire failure and lease loss. renewOK drives both
// RenewScanStandDown and ScanStandDownValid.
type recordingScanController struct {
	mu         sync.Mutex
	acquires   int
	releases   int
	renews     int
	valids     int
	renewOK    bool // what Renew/Valid return (false = lease lost)
	acquireErr error
	lastHolder string
	lastReason string
}

func (c *recordingScanController) AcquireScanStandDown(_ context.Context, holderOpID, reason string) (func(), error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.acquires++
	c.lastHolder = holderOpID
	c.lastReason = reason
	if c.acquireErr != nil {
		return func() {}, c.acquireErr
	}
	return func() {
		c.mu.Lock()
		c.releases++
		c.mu.Unlock()
	}, nil
}

func (c *recordingScanController) RenewScanStandDown(_ string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.renews++
	return c.renewOK
}

func (c *recordingScanController) ScanStandDownValid(_ string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.valids++
	return c.renewOK
}

func (c *recordingScanController) counts() (acq, rel, ren int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.acquires, c.releases, c.renews
}

// opIDReporter is a fakeReporter that carries an op id, so ReporterOpID (which
// type-asserts for OpID() string) returns non-empty and the stand-down is
// actually acquired. A bare fakeReporter yields "" and the gate is skipped —
// which is itself a case we assert below.
type opIDReporter struct {
	fakeReporter
	id string
}

func (r *opIDReporter) OpID() string { return r.id }

// ---- helper-level tests: the shared acquire/renew contract ----

func TestAcquireScanStandDownForApply_NilControllerRunsWithNoInterlock(t *testing.T) {
	holder, held, release, err := acquireScanStandDownForApply(context.Background(), nil, &opIDReporter{id: "op-1"}, "x")
	require.NoError(t, err)
	require.False(t, held, "a nil controller means no gate is held")
	require.Empty(t, holder)
	require.NotNil(t, release)
	release() // must be safe to call
}

func TestAcquireScanStandDownForApply_EmptyOpIDSkipsAcquire(t *testing.T) {
	c := &recordingScanController{renewOK: true}
	// bare fakeReporter has no OpID() -> ReporterOpID returns "".
	holder, held, release, err := acquireScanStandDownForApply(context.Background(), c, &fakeReporter{}, "x")
	require.NoError(t, err)
	require.False(t, held)
	require.Empty(t, holder)
	release()
	acq, _, _ := c.counts()
	require.Equal(t, 0, acq, "no op id -> the registry (which rejects empty holders) is never called")
}

func TestAcquireScanStandDownForApply_AcquiresWithOpID(t *testing.T) {
	c := &recordingScanController{renewOK: true}
	holder, held, release, err := acquireScanStandDownForApply(context.Background(), c, &opIDReporter{id: "op-7"}, "reason-x")
	require.NoError(t, err)
	require.True(t, held)
	require.Equal(t, "op-7", holder)
	require.Equal(t, "op-7", c.lastHolder)
	require.Equal(t, "reason-x", c.lastReason)
	release()
	acq, rel, _ := c.counts()
	require.Equal(t, 1, acq)
	require.Equal(t, 1, rel)
}

func TestAcquireScanStandDownForApply_PropagatesAcquireError(t *testing.T) {
	sentinel := errors.New("scan would not park")
	c := &recordingScanController{renewOK: true, acquireErr: sentinel}
	_, held, _, err := acquireScanStandDownForApply(context.Background(), c, &opIDReporter{id: "op-1"}, "x")
	require.ErrorIs(t, err, sentinel)
	require.False(t, held, "a failed acquire is not held")
}

func TestScanStandDownLostForApply(t *testing.T) {
	require.False(t, scanStandDownLostForApply(nil, "", false),
		"not held -> never reports lost (op runs with no interlock)")
	require.False(t, scanStandDownLostForApply(&recordingScanController{renewOK: true}, "op", true),
		"held and renew ok -> not lost")
	require.True(t, scanStandDownLostForApply(&recordingScanController{renewOK: false}, "op", true),
		"held and renew fails -> lost, caller must abort")
}

// ---- integration: the retrofitted apply path holds and honors the gate ----

// seedMarkGone builds a mark store with n rows whose files are absent (all will
// flip to Missing=true on apply).
func seedMarkGone(dir string, n int) *markFakeStore {
	s := &markFakeStore{full: map[string][]database.BookFile{}}
	for i := 0; i < n; i++ {
		id := "f" + string(rune('a'+i))
		bookID := "b" + string(rune('a'+i))
		path := filepath.Join(dir, id+".mp3") // never written -> gone
		s.cores = append(s.cores, database.BookFileCore{ID: id, BookID: bookID, FilePath: path, Missing: false})
		s.full[bookID] = []database.BookFile{{ID: id, BookID: bookID, FilePath: path, Missing: false}}
	}
	return s
}

func TestMarkMissing_Apply_HoldsScanStandDownAndRenews(t *testing.T) {
	dir := t.TempDir()
	const n = 5
	store := seedMarkGone(dir, n)
	c := &recordingScanController{renewOK: true}

	plan, err := planMarkMissingFiles(context.Background(), store, c,
		markMissingParams{Apply: true}, &opIDReporter{id: "op-42"})
	require.NoError(t, err)
	require.Equal(t, n, plan.MarkedMissing)
	require.Len(t, store.updates, n, "every gone row is written when the gate holds")

	acq, rel, ren := c.counts()
	require.Equal(t, 1, acq, "acquired exactly once for the apply")
	require.Equal(t, 1, rel, "released exactly once (deferred)")
	require.Equal(t, n, ren, "renewed once per write item (the per-item heartbeat)")
	require.Equal(t, "mark-missing-files apply", c.lastReason)
}

func TestMarkMissing_Apply_AbortsWhenLeaseLost(t *testing.T) {
	dir := t.TempDir()
	const n = 6
	store := seedMarkGone(dir, n)
	c := &recordingScanController{renewOK: false} // lease is gone from the first heartbeat

	plan, err := planMarkMissingFiles(context.Background(), store, c,
		markMissingParams{Apply: true}, &opIDReporter{id: "op-99"})
	require.Error(t, err, "a lapsed lease mid-apply must surface as an error")
	require.Contains(t, err.Error(), "lease lapsed")
	require.Equal(t, 0, plan.MarkedMissing)
	require.Empty(t, store.updates, "no row is written once the lease is lost")

	acq, rel, _ := c.counts()
	require.Equal(t, 1, acq)
	require.Equal(t, 1, rel, "the gate is still released on the abort path")
}

func TestMarkMissing_DryRun_DoesNotAcquire(t *testing.T) {
	dir := t.TempDir()
	store := seedMarkGone(dir, 3)
	c := &recordingScanController{renewOK: true}

	_, err := planMarkMissingFiles(context.Background(), store, c,
		markMissingParams{}, &opIDReporter{id: "op-1"}) // Apply defaults false
	require.NoError(t, err)

	acq, _, ren := c.counts()
	require.Equal(t, 0, acq, "dry run must never stand the scanner down")
	require.Equal(t, 0, ren)
	require.Empty(t, store.updates)
}
