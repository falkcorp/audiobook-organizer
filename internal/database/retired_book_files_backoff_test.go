// file: internal/database/retired_book_files_backoff_test.go
// version: 1.0.0
// guid: c1eafd0a-58c3-4033-89f4-dc8c02517083
// last-edited: 2026-10-30

package database

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// versionStripeProbe stands in for the backoff sleep between whole-batch
// re-stage rounds. Each "sleep" starts a second writer that takes the version
// stripe (what a batch upsert or UpdateBookVersion for the same version does)
// and waits up to probeWait for it. If the retired-row writer still held the
// stripe across its backoff, the second writer could not get in until the whole
// attempt ended, and the probe records a miss.
type versionStripeProbe struct {
	mu       sync.Mutex
	sleeps   int
	acquired int
	missed   int
	wg       sync.WaitGroup
}

const probeWait = 2 * time.Second

// probeVersionStripeDuringBackoff installs the probe as the restage sleep. during,
// when non-nil, runs inside each sleep window after the probe got in (the window
// in which the stripe is free for any other writer to change the version).
func probeVersionStripeDuringBackoff(t *testing.T, ps *PebbleStore, versionID string, during func()) *versionStripeProbe {
	t.Helper()
	p := &versionStripeProbe{}
	bookFileRestageSleep = func(time.Duration) {
		p.mu.Lock()
		p.sleeps++
		p.mu.Unlock()
		got := make(chan struct{})
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			unlock := ps.lockBookVersion(versionID)
			close(got)
			unlock()
		}()
		select {
		case <-got:
			p.mu.Lock()
			p.acquired++
			p.mu.Unlock()
			if during != nil {
				during()
			}
		case <-time.After(probeWait):
			p.mu.Lock()
			p.missed++
			p.mu.Unlock()
		}
	}
	t.Cleanup(func() {
		bookFileRestageSleep = time.Sleep
		p.wg.Wait()
	})
	return p
}

func (p *versionStripeProbe) require(t *testing.T, writer string) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	require.Equal(t, 1, p.sleeps, "%s: want exactly one backoff (one refused round)", writer)
	require.Zero(t, p.missed, "%s held the version stripe across its backoff sleep: a second writer for the version waited %v and did not get in", writer, probeWait)
	require.Equal(t, 1, p.acquired, "%s: a second writer takes the version stripe during the backoff", writer)
}

// The retired-row writers re-stage a whole batch when a commit is refused, and
// wait between rounds. The version stripe must be free during that wait, and the
// retry must still land a correct result: the write that refused the first
// round is carried into the retried one.
func TestRetiredWriters_BackoffRunsWithoutTheVersionStripe(t *testing.T) {
	t.Run("RetireBookFiles", func(t *testing.T) {
		fx := newRetiredFixture(t)
		row := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/Gotham/Backoff/Retire/01.m4b", FileHash: "gotham-hash-old", Duration: 100})
		injectBeforeCommit(t, 1, func(int) {
			strayWriteByPath(t, fx.ps, row.FilePath, func(r *BookFile) { r.Duration = 250 })
		})
		probe := probeVersionStripeDuringBackoff(t, fx.ps, fx.version.ID, nil)

		require.NoError(t, fx.retire(t, row))

		probe.require(t, "RetireBookFiles")
		got, err := fx.ps.GetBookFileByID(fx.holder.ID, row.ID)
		require.NoError(t, err)
		require.NotNil(t, got)
		require.True(t, got.Retired)
		require.Equal(t, fx.version.ID, got.VersionID)
		require.Equal(t, 250, got.Duration, "the write that refused the first round is kept")
		require.Equal(t, fx.holder.ID+":"+row.ID, indexOwner(t, fx.ps, "book_file_hash:gotham-hash-old"))
	})

	t.Run("UnretireBookFiles", func(t *testing.T) {
		fx := newRetiredFixture(t)
		row := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/Gotham/Backoff/Unretire/01.m4b", FileHash: "gotham-hash-old"})
		require.NoError(t, fx.retire(t, row))
		injectBeforeCommit(t, 1, func(int) {
			rewriteRaw(t, fx.ps, fx.holder.ID, row.ID, func(r *BookFile) { r.Title = "Batman: Year Two" })
		})
		probe := probeVersionStripeDuringBackoff(t, fx.ps, fx.version.ID, nil)

		mv := []BookFileMove{{FileIDs: []string{row.ID}, SourceBookID: fx.holder.ID}}
		require.NoError(t, fx.ps.UnretireBookFiles(mv, fx.shell.ID, fx.version.ID, nil))

		probe.require(t, "UnretireBookFiles")
		got, err := fx.ps.GetBookFileByID(fx.shell.ID, row.ID)
		require.NoError(t, err)
		require.NotNil(t, got)
		require.False(t, got.Retired)
		require.Equal(t, "Batman: Year Two", got.Title, "the write that refused the first round is kept")
	})

	t.Run("RehomeRetiredVersion", func(t *testing.T) {
		fx := newRetiredFixture(t)
		row := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/Gotham/Backoff/Rehome/01.m4b", FileHash: "gotham-hash-old"})
		require.NoError(t, fx.retire(t, row))
		newHolder, err := fx.ps.CreateBook(&Book{Title: "Batman: Year Three", FilePath: "/lib/Gotham/Backoff/Year-Three"})
		require.NoError(t, err)
		injectBeforeCommit(t, 1, func(int) {
			rewriteRaw(t, fx.ps, fx.holder.ID, row.ID, func(r *BookFile) { r.Title = "Batman: Year Two" })
		})
		probe := probeVersionStripeDuringBackoff(t, fx.ps, fx.version.ID, nil)

		require.NoError(t, fx.ps.RehomeRetiredVersion(fx.version.ID, newHolder.ID))

		probe.require(t, "RehomeRetiredVersion")
		rows, err := fx.ps.GetRetiredBookFiles(newHolder.ID)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, "Batman: Year Two", rows[0].Title, "the write that refused the first round is kept")
		v, err := fx.ps.GetBookVersion(fx.version.ID)
		require.NoError(t, err)
		require.Equal(t, newHolder.ID, v.BookID)
	})
}

// Releasing the stripe over the backoff lets another writer change the version
// before the retry. The retry takes the stripe again and checks again, so a
// retire whose version was moved away in the gap is refused, not written under
// a holder that no longer owns the version.
func TestRetireBookFiles_RechecksTheVersionAfterTheBackoff(t *testing.T) {
	fx := newRetiredFixture(t)
	row := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/Gotham/Backoff/Recheck/01.m4b", FileHash: "gotham-hash-old", Duration: 100})
	otherHolder, err := fx.ps.CreateBook(&Book{Title: "Batman: Hush", FilePath: "/lib/Gotham/Backoff/Hush"})
	require.NoError(t, err)
	injectBeforeCommit(t, 1, func(int) {
		strayWriteByPath(t, fx.ps, row.FilePath, func(r *BookFile) { r.Duration = 250 })
	})
	var rehomeErr error
	probe := probeVersionStripeDuringBackoff(t, fx.ps, fx.version.ID, func() {
		rehomeErr = fx.ps.RehomeRetiredVersion(fx.version.ID, otherHolder.ID)
	})

	err = fx.retire(t, row)

	require.NoError(t, rehomeErr, "the rehome could take the version stripe because the retire released it")
	require.ErrorIs(t, err, ErrRetiredVersionMismatch, "the retry re-checks that the version still belongs to the holder")
	require.False(t, errors.Is(err, ErrBookFileConflict))
	probe.mu.Lock()
	require.Equal(t, 1, probe.sleeps)
	probe.mu.Unlock()
	still, err := fx.ps.GetBookFileByID(fx.shell.ID, row.ID)
	require.NoError(t, err)
	require.NotNil(t, still, "nothing was retired")
	require.False(t, still.Retired)
	retired, err := fx.ps.GetRetiredBookFiles(otherHolder.ID)
	require.NoError(t, err)
	require.Empty(t, retired)
}
