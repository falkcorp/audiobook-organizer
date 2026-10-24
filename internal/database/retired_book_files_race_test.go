// file: internal/database/retired_book_files_race_test.go
// version: 1.0.0
// guid: 91e6c4d2-7a3b-4f05-b8d9-2c1a5e60f7b4
// last-edited: 2026-10-23

package database

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"
)

// installBeforeCommitHook sets bookFileBeforeCommitHook for one test. The hook
// runs on the committing goroutine with the writer's FILE stripes (and, for the
// retired writers, the version stripe) held and its owner stripes not yet
// taken, so it must not call a stripe-taking writer: a stripe-hash collision
// with a held stripe would deadlock the test. Raw p.db writes are safe; a
// competing writer runs in a goroutine the hook waits on with a timeout.
func installBeforeCommitHook(t *testing.T, fn func()) {
	t.Helper()
	prev := bookFileBeforeCommitHook
	bookFileBeforeCommitHook = fn
	t.Cleanup(func() { bookFileBeforeCommitHook = prev })
}

// A retire decides, from the index value it read, to repoint a key at the
// retired row. If another row takes that key between the read and the commit,
// the batch used to write the retired row's reference over it, and the
// single-identity getters (filtered to live rows) then answered nil for a live
// identity. The commit now re-checks the key under the owner stripes and the
// attempt re-stages from fresh reads.
func TestRetireBookFiles_DoesNotOverwriteAKeyTakenBeforeCommit(t *testing.T) {
	fx := newRetiredFixture(t)
	const pid = "PID-RACE"
	x := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/race/x.mp3", ITunesPersistentID: pid})
	pidKey := "book_file_pid:" + pid
	require.Equal(t, fx.shell.ID+":"+x.ID, indexOwner(t, fx.ps, pidKey), "premise: the retiring row owns the key")

	thief := "someone-else:their-row"
	var fired atomic.Int32
	installBeforeCommitHook(t, func() {
		if fired.Add(1) == 1 {
			// A live row takes the PID key after the retire read it.
			require.NoError(t, fx.ps.db.Set([]byte(pidKey), []byte(thief), pebble.NoSync))
		}
	})
	require.NoError(t, fx.retire(t, x))

	require.GreaterOrEqual(t, fired.Load(), int32(2), "the first commit was refused and the attempt re-staged")
	require.Equal(t, thief, indexOwner(t, fx.ps, pidKey), "the key another row took is not overwritten by the retired row's reference")
	got, err := fx.ps.GetBookFileByID(fx.holder.ID, x.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.True(t, got.Retired, "the retire itself still completed")
}

// Un-retire has the same shape: a key it decided to claim may be taken by a live
// row before the commit.
func TestUnretireBookFiles_DoesNotOverwriteAKeyTakenBeforeCommit(t *testing.T) {
	fx := newRetiredFixture(t)
	const pid = "PID-RACE-UN"
	x := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/race/un.mp3", ITunesPersistentID: pid})
	require.NoError(t, fx.retire(t, x))
	pidKey := "book_file_pid:" + pid
	require.Equal(t, fx.holder.ID+":"+x.ID, indexOwner(t, fx.ps, pidKey), "premise: the retired row owns the key")

	// The key is free of live rows now; a live row takes it just before the commit.
	thief := fx.holder.ID + ":live-thief"
	liveThief := &BookFile{ID: "live-thief", BookID: fx.holder.ID, FilePath: "/lib/ret/race/thief.mp3", ITunesPersistentID: pid}
	data, err := json.Marshal(liveThief)
	require.NoError(t, err)
	var fired atomic.Int32
	installBeforeCommitHook(t, func() {
		if fired.Add(1) == 1 {
			require.NoError(t, fx.ps.db.Set(bookFileKey(fx.holder.ID, "live-thief"), data, pebble.NoSync))
			require.NoError(t, fx.ps.db.Set([]byte(pidKey), []byte(thief), pebble.NoSync))
		}
	})
	require.NoError(t, fx.ps.UnretireBookFiles(
		[]BookFileMove{{FileIDs: []string{x.ID}, SourceBookID: fx.holder.ID}}, fx.shell.ID, fx.version.ID, nil))

	require.GreaterOrEqual(t, fired.Load(), int32(2))
	require.Equal(t, thief, indexOwner(t, fx.ps, pidKey), "the live row's claim on the key survives the un-retire")
}

// The verify hook itself: a changed key and a no-longer-live alternative owner
// are both errBookFileRowVanished; an unchanged state passes.
func TestIndexCAS_Verify(t *testing.T) {
	fx := newRetiredFixture(t)
	live := fx.create(t, fx.holder.ID, BookFile{FilePath: "/lib/ret/cas/live.mp3", FileHash: "cas-hash"})
	hashKey := []byte("book_file_hash:cas-hash")

	cas := newIndexCAS()
	require.NoError(t, cas.note(fx.ps, hashKey))
	cas.wrote(hashKey)
	cas.alt(singleOwnerKey{kind: ownerFileHash, key: hashKey, value: "cas-hash"}, live.BookID, live.ID)
	require.NoError(t, cas.verify(fx.ps)(), "nothing changed")

	// The alternative stops being live.
	stored, err := fx.ps.getBookFileByID(live.BookID, live.ID)
	require.NoError(t, err)
	stored.Retired = true
	data, err := json.Marshal(stored)
	require.NoError(t, err)
	require.NoError(t, fx.ps.db.Set(bookFileKey(live.BookID, live.ID), data, pebble.NoSync))
	require.ErrorIs(t, cas.verify(fx.ps)(), errBookFileRowVanished)

	// The key moves.
	cas = newIndexCAS()
	require.NoError(t, cas.note(fx.ps, hashKey))
	cas.wrote(hashKey)
	require.NoError(t, fx.ps.db.Set(hashKey, []byte("b:moved"), pebble.NoSync))
	require.ErrorIs(t, cas.verify(fx.ps)(), errBookFileRowVanished)

	// A key absent when read and present at commit.
	cas = newIndexCAS()
	absent := []byte("book_file_hash:cas-absent")
	require.NoError(t, cas.note(fx.ps, absent))
	cas.wrote(absent)
	require.NoError(t, cas.verify(fx.ps)())
	require.NoError(t, fx.ps.db.Set(absent, []byte("b:new"), pebble.NoSync))
	require.ErrorIs(t, cas.verify(fx.ps)(), errBookFileRowVanished)
}

// BatchUpsertBookFiles takes the file stripe of every row it rewrites, so a scan
// cannot commit between a retire's read and its commit and have the retire
// write the row back as it read it. With the retire holding the stripe, the
// upsert waits; it then sees the retired row (never a match for an upsert) and
// writes the caller's row live instead of being silently lost.
func TestBatchUpsertBookFiles_WaitsForARetireHoldingTheRowsStripe(t *testing.T) {
	fx := newRetiredFixture(t)
	const path = "/lib/ret/race/up.mp3"
	x := fx.create(t, fx.shell.ID, BookFile{FilePath: path, Duration: 100, Format: "mp3"})

	upsertDone := make(chan error, 1)
	var finishedInsideRetire atomic.Bool
	// An atomic first-fire guard, not sync.Once: the upsert's own commit runs this
	// hook too, and a Once would make that call wait for this one to return, so
	// the upsert would be blocked by the hook rather than by the stripe.
	var fired atomic.Int32
	installBeforeCommitHook(t, func() {
		if fired.Add(1) != 1 {
			return
		}
		go func() {
			upsertDone <- fx.ps.BatchUpsertBookFiles([]*BookFile{
				{BookID: fx.shell.ID, FilePath: path, Duration: 999, Format: "mp3"},
			})
		}()
		select {
		case err := <-upsertDone:
			finishedInsideRetire.Store(true)
			upsertDone <- err
		case <-time.After(400 * time.Millisecond):
		}
	})
	require.NoError(t, fx.retire(t, x))
	require.False(t, finishedInsideRetire.Load(), "the upsert committed while the retire held the row's stripe")

	select {
	case err := <-upsertDone:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("the upsert never finished after the retire released its stripes")
	}

	retired, err := fx.ps.GetBookFileByID(fx.holder.ID, x.ID)
	require.NoError(t, err)
	require.NotNil(t, retired)
	require.True(t, retired.Retired)
	require.Equal(t, 100, retired.Duration, "the retired row is as the retire read it")
	liveRow, err := fx.ps.GetBookFileByPath(path)
	require.NoError(t, err)
	require.NotNil(t, liveRow, "the upsert's row was written, not lost")
	require.False(t, liveRow.Retired)
	require.Equal(t, 999, liveRow.Duration)
}

// The attempt's candidate set is a lock-free guess. A row that appears between
// the guess and the lock is matched under the stripes, found missing from the
// set, and the pass starts over with it added: the caller's row updates that row
// and exactly one row sits at the path.
func TestBatchUpsertBookFiles_StaleCandidateSetIsRetriedUnderTheStripes(t *testing.T) {
	fx := newRetiredFixture(t)
	const path = "/lib/ret/race/late.mp3"
	var created *BookFile
	prev := batchUpsertAfterCandidatesHook
	t.Cleanup(func() { batchUpsertAfterCandidatesHook = prev })
	var calls atomic.Int32
	batchUpsertAfterCandidatesHook = func() {
		if calls.Add(1) == 1 {
			created = &BookFile{BookID: fx.holder.ID, FilePath: path, Duration: 5, Format: "mp3"}
			require.NoError(t, fx.ps.CreateBookFile(created))
		}
	}
	row := &BookFile{BookID: fx.holder.ID, FilePath: path, Duration: 321, Format: "mp3"}
	require.NoError(t, fx.ps.BatchUpsertBookFiles([]*BookFile{row}))

	require.NotNil(t, created)
	require.Equal(t, created.ID, row.ID, "the caller's struct carries the identity of the row it updated")
	rows, err := fx.ps.BookFilesAtPath(path)
	require.NoError(t, err)
	require.Len(t, rows, 1, "no second row at the path")
	require.Equal(t, 321, rows[0].Duration)
}

// The staging pass alone: a matched row outside the locked set is reported as
// needing its stripe and nothing is committed; with the row in the set it
// commits.
func TestBatchUpsertBookFilesStaged_ReportsAnUnlockedMatch(t *testing.T) {
	fx := newRetiredFixture(t)
	const path = "/lib/ret/race/staged.mp3"
	existing := fx.create(t, fx.holder.ID, BookFile{FilePath: path, Duration: 10, Format: "mp3"})
	released := 0
	release := func() { released++ }

	row := &BookFile{BookID: fx.holder.ID, FilePath: path, Duration: 77, Format: "mp3"}
	_, need, err := fx.ps.batchUpsertBookFilesStaged([]*BookFile{row}, nil, bookFileUpsertOpts{}, map[string]struct{}{}, release)
	require.NoError(t, err)
	require.Equal(t, []string{existing.ID}, need)
	require.Zero(t, released, "nothing was committed, so nothing was released")
	got, err := fx.ps.GetBookFileByID(fx.holder.ID, existing.ID)
	require.NoError(t, err)
	require.Equal(t, 10, got.Duration, "an unlocked match writes nothing")

	row = &BookFile{BookID: fx.holder.ID, FilePath: path, Duration: 77, Format: "mp3"}
	_, need, err = fx.ps.batchUpsertBookFilesStaged([]*BookFile{row}, nil, bookFileUpsertOpts{}, map[string]struct{}{existing.ID: {}}, release)
	require.NoError(t, err)
	require.Empty(t, need)
	require.Equal(t, 1, released, "the stripes are released right after the commit")
	got, err = fx.ps.GetBookFileByID(fx.holder.ID, existing.ID)
	require.NoError(t, err)
	require.Equal(t, 77, got.Duration)
}

// Lock-order sanity: upserts that take many file stripes in opposite row order,
// racing retire / un-retire cycles and single-row writers, all finish. A cycle
// among the stripes (or a stripe taken twice by one goroutine) would hang here.
func TestBatchUpsertBookFiles_StripeOrderDoesNotDeadlock(t *testing.T) {
	fx := newRetiredFixture(t)
	const n = 24
	var upRows []*BookFile
	for i := range n {
		upRows = append(upRows, fx.create(t, fx.holder.ID, BookFile{FilePath: "/lib/ret/lock/u" + string(rune('a'+i)) + ".mp3", Duration: 1, Format: "mp3"}))
	}
	cycler := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/lock/cycle.mp3", Duration: 1, Format: "mp3"})

	var wg sync.WaitGroup
	errs := make(chan error, 64)
	run := func(fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(); err != nil {
				errs <- err
			}
		}()
	}
	batch := func(reverse bool, dur int) []*BookFile {
		out := make([]*BookFile, 0, n)
		for i := range n {
			src := upRows[i]
			if reverse {
				src = upRows[n-1-i]
			}
			out = append(out, &BookFile{BookID: fx.holder.ID, FilePath: src.FilePath, Duration: dur, Format: "mp3"})
		}
		return out
	}
	for g := range 4 {
		run(func() error {
			for it := range 15 {
				if err := fx.ps.BatchUpsertBookFiles(batch(g%2 == 1, 10+it)); err != nil {
					return err
				}
			}
			return nil
		})
	}
	run(func() error {
		for range 15 {
			if err := fx.retire(t, cycler); err != nil {
				return err
			}
			if err := fx.ps.UnretireBookFiles(
				[]BookFileMove{{FileIDs: []string{cycler.ID}, SourceBookID: fx.holder.ID}}, fx.shell.ID, fx.version.ID, nil); err != nil {
				return err
			}
		}
		return nil
	})
	run(func() error {
		for it := range 30 {
			if _, err := fx.ps.ModifyBookFile(fx.holder.ID, upRows[it%n].ID, func(f *BookFile) error { f.TrackNumber = it; return nil }); err != nil {
				return err
			}
		}
		return nil
	})

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("writers deadlocked on the file stripes")
	}
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}
