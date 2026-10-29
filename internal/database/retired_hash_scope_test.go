// file: internal/database/retired_hash_scope_test.go
// version: 1.1.0
// guid: 4eaca36f-4f66-43fd-8c64-10b5f3a30043
// last-edited: 2026-10-29

package database

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// scopedRetiredFixture is a fixture with one retired row. The tests call
// RetiredBookFilesWithHash directly, which reads Pebble whatever memdb is doing
// (RetiredHoldersOfHash is what chooses it when memdb is not serving), and the
// retire and un-retire writes they make need memdb serving, so memdb stays on.
func scopedRetiredFixture(t *testing.T) (*retiredFixture, *BookFile) {
	t.Helper()
	fx := newRetiredFixture(t)
	kept := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/Gotham/scope/shell/01.mp3", FileHash: "h-kept", OriginalFileHash: "h-kept-orig", TrackNumber: 1})
	require.NoError(t, fx.retire(t, kept))
	return fx, kept
}

// Inside a scope the retired rows are read once: lookups of different hashes,
// by FileHash and by OriginalFileHash, share one build.
func TestRetiredBookFilesWithHash_ScopeBuildsOnce(t *testing.T) {
	fx, kept := scopedRetiredFixture(t)
	end := fx.ps.BeginRetiredHashScope()
	defer end()

	for _, hash := range []string{"h-kept", "h-kept-orig", "h-kept", "h-nobody"} {
		retired, liveHolds, err := fx.ps.RetiredBookFilesWithHash(hash)
		require.NoError(t, err)
		require.False(t, liveHolds)
		if hash == "h-nobody" {
			require.Empty(t, retired)
			continue
		}
		require.Len(t, retired, 1, hash)
		require.Equal(t, kept.ID, retired[0].ID)
		require.Equal(t, fx.holder.ID, retired[0].BookID)
		require.Equal(t, fx.version.ID, retired[0].VersionID)
	}
	require.EqualValues(t, 1, fx.ps.retiredHash.builds.Load(), "four lookups, one read of the retired rows")
}

// Outside a scope nothing is cached: each lookup reads the rows itself.
func TestRetiredBookFilesWithHash_NoScopeCachesNothing(t *testing.T) {
	fx, _ := scopedRetiredFixture(t)
	for range 3 {
		retired, _, err := fx.ps.RetiredBookFilesWithHash("h-kept")
		require.NoError(t, err)
		require.Len(t, retired, 1)
	}
	require.Zero(t, fx.ps.retiredHash.builds.Load())
	require.False(t, fx.ps.retiredHash.built)
}

// A retire that lands while a scope is open is seen by the next lookup: the
// generation moved, so the cached map is rebuilt.
func TestRetiredBookFilesWithHash_RetireDuringScopeIsSeen(t *testing.T) {
	fx, _ := scopedRetiredFixture(t)
	late := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/Gotham/scope/shell/02.mp3", FileHash: "h-late", TrackNumber: 2})
	end := fx.ps.BeginRetiredHashScope()
	defer end()

	retired, _, err := fx.ps.RetiredBookFilesWithHash("h-late")
	require.NoError(t, err)
	require.Empty(t, retired, "the row is still live on the shell")
	require.EqualValues(t, 1, fx.ps.retiredHash.builds.Load())

	// The retire runs on another goroutine, as the retire apply does beside a
	// scan; its error is checked here, not from inside it (require off the test
	// goroutine would FailNow the wrong one).
	retireErr := make(chan error, 1)
	go func() { retireErr <- fx.retire(t, late) }()
	require.NoError(t, <-retireErr)

	retired, _, err = fx.ps.RetiredBookFilesWithHash("h-late")
	require.NoError(t, err)
	require.Len(t, retired, 1, "a cache that ignored the retire would still answer none and let the file import as new")
	require.Equal(t, late.ID, retired[0].ID)
	require.EqualValues(t, 2, fx.ps.retiredHash.builds.Load())

	// And the un-retire.
	require.NoError(t, fx.ps.UnretireBookFiles(
		[]BookFileMove{{FileIDs: []string{late.ID}, SourceBookID: fx.holder.ID}}, fx.shell.ID, fx.version.ID, nil))
	retired, _, err = fx.ps.RetiredBookFilesWithHash("h-late")
	require.NoError(t, err)
	require.Empty(t, retired)
}

// A rehome moves the version and its rows to another holder; the cached rows
// name the old one until the generation moves.
func TestRetiredBookFilesWithHash_RehomeDuringScopeIsSeen(t *testing.T) {
	fx, kept := scopedRetiredFixture(t)
	other, err := fx.ps.CreateBook(&Book{Title: "Year Two", FilePath: "/lib/Gotham/scope/other"})
	require.NoError(t, err)
	end := fx.ps.BeginRetiredHashScope()
	defer end()

	retired, _, err := fx.ps.RetiredBookFilesWithHash("h-kept")
	require.NoError(t, err)
	require.Len(t, retired, 1)
	require.Equal(t, fx.holder.ID, retired[0].BookID)

	require.NoError(t, fx.ps.RehomeRetiredVersion(fx.version.ID, other.ID))
	retired, _, err = fx.ps.RetiredBookFilesWithHash("h-kept")
	require.NoError(t, err)
	require.Len(t, retired, 1)
	require.Equal(t, kept.ID, retired[0].ID)
	require.Equal(t, other.ID, retired[0].BookID, "the rehome invalidated the cache")
}

// Rewriting the hash of a retired row changes what it answers to.
func TestRetiredBookFilesWithHash_HashRewriteOfARetiredRowIsSeen(t *testing.T) {
	fx, kept := scopedRetiredFixture(t)
	end := fx.ps.BeginRetiredHashScope()
	defer end()

	retired, _, err := fx.ps.RetiredBookFilesWithHash("h-rewritten")
	require.NoError(t, err)
	require.Empty(t, retired)

	require.NoError(t, fx.ps.UpdateBookFileHashes(kept.ID, "", "", "h-rewritten"))
	retired, _, err = fx.ps.RetiredBookFilesWithHash("h-rewritten")
	require.NoError(t, err)
	require.Len(t, retired, 1, "the row's new hash is found")
	retired, _, err = fx.ps.RetiredBookFilesWithHash("h-kept")
	require.NoError(t, err)
	require.Empty(t, retired, "and its old one is not")
}

// Only the retired side is cached: a live row that claims the hash after the
// cache was built is still reported, because a scan makes such rows as it runs.
func TestRetiredBookFilesWithHash_LiveSideIsNeverCached(t *testing.T) {
	fx, _ := scopedRetiredFixture(t)
	end := fx.ps.BeginRetiredHashScope()
	defer end()

	_, liveHolds, err := fx.ps.RetiredBookFilesWithHash("h-kept")
	require.NoError(t, err)
	require.False(t, liveHolds)

	fx.create(t, fx.holder.ID, BookFile{FilePath: "/lib/Gotham/scope/holder/01.mp3", FileHash: "h-kept", TrackNumber: 1})
	retired, liveHolds, err := fx.ps.RetiredBookFilesWithHash("h-kept")
	require.NoError(t, err)
	require.True(t, liveHolds, "the live row is seen inside the same scope")
	require.Len(t, retired, 1)
	require.EqualValues(t, 1, fx.ps.retiredHash.builds.Load(), "and the retired side still came from the cache")
}

// A caller that changes a returned row does not change the cache.
func TestRetiredBookFilesWithHash_ReturnedRowsAreCopies(t *testing.T) {
	fx, _ := scopedRetiredFixture(t)
	end := fx.ps.BeginRetiredHashScope()
	defer end()

	retired, _, err := fx.ps.RetiredBookFilesWithHash("h-kept")
	require.NoError(t, err)
	require.Len(t, retired, 1)
	retired[0].BookID = "mutated"
	retired, _, err = fx.ps.RetiredBookFilesWithHash("h-kept")
	require.NoError(t, err)
	require.Equal(t, fx.holder.ID, retired[0].BookID)
}

// Scopes count: closing an inner one leaves the cache to the outer, the last
// close drops it, and closing twice is harmless.
func TestBeginRetiredHashScope_Overlapping(t *testing.T) {
	fx, _ := scopedRetiredFixture(t)
	outer := fx.ps.BeginRetiredHashScope()
	inner := fx.ps.BeginRetiredHashScope()
	_, _, err := fx.ps.RetiredBookFilesWithHash("h-kept")
	require.NoError(t, err)

	inner()
	inner()
	_, _, err = fx.ps.RetiredBookFilesWithHash("h-kept")
	require.NoError(t, err)
	require.EqualValues(t, 1, fx.ps.retiredHash.builds.Load(), "the outer scope still has its cache")

	outer()
	require.False(t, fx.ps.retiredHash.built, "the last close drops the cache")
	_, _, err = fx.ps.RetiredBookFilesWithHash("h-kept")
	require.NoError(t, err)
	require.EqualValues(t, 1, fx.ps.retiredHash.builds.Load(), "and an unscoped lookup builds nothing")
}

// The package-level opener resolves the store through AsCapability and is a
// no-op for a store without the cache.
func TestBeginRetiredHashScope_ThroughCapability(t *testing.T) {
	fx, _ := scopedRetiredFixture(t)
	end := BeginRetiredHashScope(wrappedPebble{Store: fx.ps})
	_, _, err := fx.ps.RetiredBookFilesWithHash("h-kept")
	require.NoError(t, err)
	require.EqualValues(t, 1, fx.ps.retiredHash.builds.Load())
	end()
	require.False(t, fx.ps.retiredHash.built)

	require.NotPanics(t, func() { BeginRetiredHashScope(struct{}{})() })
	require.NotPanics(t, func() { BeginRetiredHashScope(nil)() })
}

type wrappedPebble struct{ Store }

func (w wrappedPebble) Unwrap() Store { return w.Store }

// A build that reads the retired rows while a retire commits is tagged with the
// generation it started from, so the retire it missed is not hidden by the map
// it stored. The hook lands the retire between the build's read of the rows and
// the store of the map; a build tagged with a generation loaded after the read
// would carry the post-retire tag over a map without the row.
func TestRetiredBookFilesWithHash_BuildStraddlingARetireIsRebuilt(t *testing.T) {
	fx, _ := scopedRetiredFixture(t)
	late := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/Gotham/scope/shell/02.mp3", FileHash: "h-late", TrackNumber: 2})
	end := fx.ps.BeginRetiredHashScope()
	defer end()

	var fired atomic.Bool
	var hookErr error
	hook := func() {
		if fired.CompareAndSwap(false, true) {
			hookErr = fx.retire(t, late)
		}
	}
	retiredHashAfterBuildHook.Store(&hook)
	t.Cleanup(func() { retiredHashAfterBuildHook.Store(nil) })

	// This lookup may answer either way: its build raced the retire.
	_, _, err := fx.ps.RetiredBookFilesWithHash("h-late")
	require.NoError(t, err)
	require.True(t, fired.Load())
	require.NoError(t, hookErr)

	retired, _, err := fx.ps.RetiredBookFilesWithHash("h-late")
	require.NoError(t, err)
	require.Len(t, retired, 1, "the map built across the retire must not be served after it")
	require.Equal(t, late.ID, retired[0].ID)
	require.EqualValues(t, 2, fx.ps.retiredHash.builds.Load(), "the stale map was rebuilt")
}

// A batch upsert that rewrites a retired row's hash is a write the cache must
// see (BatchUpsertBookFiles is how the scanner and the backfills write rows),
// and one that changes nothing the index holds must not cost a rebuild.
func TestRetiredBookFilesWithHash_BatchUpsertOfARetiredRow(t *testing.T) {
	fx, kept := scopedRetiredFixture(t)
	end := fx.ps.BeginRetiredHashScope()
	defer end()

	retired, _, err := fx.ps.RetiredBookFilesWithHash("h-batch")
	require.NoError(t, err)
	require.Empty(t, retired)
	require.EqualValues(t, 1, fx.ps.retiredHash.builds.Load())

	// Same row, same hashes: nothing the index holds changes.
	genBefore := fx.ps.retiredHash.gen.Load()
	require.NoError(t, fx.ps.BatchUpsertBookFiles([]*BookFile{
		{ID: kept.ID, BookID: fx.holder.ID, FilePath: kept.FilePath, Duration: 321}}))
	require.Equal(t, genBefore, fx.ps.retiredHash.gen.Load(), "a duration-only rewrite of a retired row does not invalidate the index")
	retired, _, err = fx.ps.RetiredBookFilesWithHash("h-kept")
	require.NoError(t, err)
	require.Len(t, retired, 1, "and the row is still retired and indexed")
	require.EqualValues(t, 1, fx.ps.retiredHash.builds.Load(), "no rebuild")

	// A new hash on the same row.
	require.NoError(t, fx.ps.BatchUpsertBookFiles([]*BookFile{
		{ID: kept.ID, BookID: fx.holder.ID, FilePath: kept.FilePath, FileHash: "h-batch"}}))
	require.Greater(t, fx.ps.retiredHash.gen.Load(), genBefore)
	retired, _, err = fx.ps.RetiredBookFilesWithHash("h-batch")
	require.NoError(t, err)
	require.Len(t, retired, 1, "the batch-written hash is found; a cache that missed the write would say none")
	require.Equal(t, kept.ID, retired[0].ID)
	retired, _, err = fx.ps.RetiredBookFilesWithHash("h-kept")
	require.NoError(t, err)
	require.Empty(t, retired)
}

// retiredHashRelevantChange is the filter behind every non-retire bump.
func TestRetiredHashRelevantChange(t *testing.T) {
	retired := BookFile{ID: "f", BookID: "holder", VersionID: "v", Retired: true, FileHash: "h", OriginalFileHash: "o"}
	same := func(mut func(*BookFile)) *BookFile { c := retired; mut(&c); return &c }
	live := BookFile{ID: "f", BookID: "shell", FileHash: "h"}

	for name, tc := range map[string]struct {
		old, updated *BookFile
		want         bool
	}{
		"live row, live rewrite":       {&live, same(func(b *BookFile) { b.Retired = false; b.BookID = "shell"; b.VersionID = ""; b.FileHash = "h2" }), false},
		"retired, duration only":       {&retired, same(func(b *BookFile) { b.Duration = 9 }), false},
		"retired, file hash":           {&retired, same(func(b *BookFile) { b.FileHash = "h2" }), true},
		"retired, original hash":       {&retired, same(func(b *BookFile) { b.OriginalFileHash = "o2" }), true},
		"retired, version":             {&retired, same(func(b *BookFile) { b.VersionID = "v2" }), true},
		"retired, holder":              {&retired, same(func(b *BookFile) { b.BookID = "other" }), true},
		"retired to live":              {&retired, same(func(b *BookFile) { b.Retired = false }), true},
		"live to retired":              {&live, same(func(b *BookFile) {}), true},
		"retired, path and track only": {&retired, same(func(b *BookFile) { b.FilePath = "/x"; b.TrackNumber = 4 }), false},
	} {
		require.Equal(t, tc.want, retiredHashRelevantChange(tc.old, tc.updated), name)
	}
}

// Lookups racing retires and un-retires: run under -race. A lookup that starts
// and ends while its row sits in one settled state must answer that state; one
// that overlaps a transition may answer either.
func TestRetiredBookFilesWithHash_ScopeUnderConcurrentRetires(t *testing.T) {
	fx, _ := scopedRetiredFixture(t)
	const (
		phaseLive = iota // never retired
		phaseRetiring
		phaseRetired
		phaseUnretiring
		phaseLiveAgain
	)
	type tracked struct {
		row   *BookFile
		phase atomic.Int32
	}
	rows := make([]*tracked, 6)
	for i := range rows {
		rows[i] = &tracked{row: fx.create(t, fx.shell.ID, BookFile{
			FilePath: "/lib/Gotham/scope/shell/" + string(rune('a'+i)) + ".mp3", FileHash: "h-race-" + string(rune('a'+i)), TrackNumber: i + 10})}
	}
	end := fx.ps.BeginRetiredHashScope()
	defer end()

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, r := range rows {
					before := r.phase.Load()
					retired, _, err := fx.ps.RetiredBookFilesWithHash(r.row.FileHash)
					after := r.phase.Load()
					if err != nil {
						t.Error(err)
						return
					}
					if before != after {
						continue // a transition overlapped the lookup; either answer is true
					}
					switch before {
					case phaseLive, phaseLiveAgain:
						if len(retired) != 0 {
							t.Errorf("%s answered %v while live", r.row.FileHash, retired)
							return
						}
					case phaseRetired:
						if len(retired) != 1 || retired[0].ID != r.row.ID {
							t.Errorf("%s answered %v while retired as %s", r.row.FileHash, retired, r.row.ID)
							return
						}
					}
				}
			}
		}()
	}
	for _, r := range rows {
		r.phase.Store(phaseRetiring)
		require.NoError(t, fx.retire(t, r.row))
		r.phase.Store(phaseRetired)
		retired, _, err := fx.ps.RetiredBookFilesWithHash(r.row.FileHash)
		require.NoError(t, err)
		require.Len(t, retired, 1, "a retire that returned is visible to the next lookup")
	}
	for _, r := range rows {
		r.phase.Store(phaseUnretiring)
		require.NoError(t, fx.ps.UnretireBookFiles(
			[]BookFileMove{{FileIDs: []string{r.row.ID}, SourceBookID: fx.holder.ID}}, fx.shell.ID, fx.version.ID, nil))
		r.phase.Store(phaseLiveAgain)
		retired, _, err := fx.ps.RetiredBookFilesWithHash(r.row.FileHash)
		require.NoError(t, err)
		require.Empty(t, retired, "an un-retire that returned is visible to the next lookup")
	}
	close(stop)
	wg.Wait()
}
