// file: internal/database/retired_hash_scope_test.go
// version: 1.0.0
// guid: 4eaca36f-4f66-43fd-8c64-10b5f3a30043
// last-edited: 2026-10-29

package database

import (
	"sync"
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
		require.True(t, retired[0].Retired)
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

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		require.NoError(t, fx.retire(t, late))
	}()
	wg.Wait()

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

// Lookups racing retires and un-retires: run under -race. Every lookup of a
// hash whose row is settled in one state must answer that state.
func TestRetiredBookFilesWithHash_ScopeUnderConcurrentRetires(t *testing.T) {
	fx, _ := scopedRetiredFixture(t)
	rows := make([]*BookFile, 6)
	for i := range rows {
		rows[i] = fx.create(t, fx.shell.ID, BookFile{
			FilePath: "/lib/Gotham/scope/shell/" + string(rune('a'+i)) + ".mp3", FileHash: "h-race-" + string(rune('a'+i)), TrackNumber: i + 10})
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
					if _, _, err := fx.ps.RetiredBookFilesWithHash(r.FileHash); err != nil {
						t.Error(err)
						return
					}
				}
			}
		}()
	}
	for _, r := range rows {
		require.NoError(t, fx.retire(t, r))
		retired, _, err := fx.ps.RetiredBookFilesWithHash(r.FileHash)
		require.NoError(t, err)
		require.Len(t, retired, 1, "a retire that returned is visible to the next lookup")
	}
	for _, r := range rows {
		require.NoError(t, fx.ps.UnretireBookFiles(
			[]BookFileMove{{FileIDs: []string{r.ID}, SourceBookID: fx.holder.ID}}, fx.shell.ID, fx.version.ID, nil))
		retired, _, err := fx.ps.RetiredBookFilesWithHash(r.FileHash)
		require.NoError(t, err)
		require.Empty(t, retired, "an un-retire that returned is visible to the next lookup")
	}
	close(stop)
	wg.Wait()
}
