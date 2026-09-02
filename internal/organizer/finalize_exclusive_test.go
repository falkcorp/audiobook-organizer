// file: internal/organizer/finalize_exclusive_test.go
// version: 1.2.1
// guid: 8b2e4f61-9d3a-4c07-b5e8-2f6a1c9d7e43
// last-edited: 2026-09-02

package organizer

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Mutation record for finalizeExclusive / copyFile / adoptExistingDestination
// (run at the commit that introduced them; re-run at final HEAD before merge):
//
//	KILLED  shared-temp        dst + tempFileSuffix (nonce dropped)   -> TempName test
//	KILLED  trunc-not-excl     CopyFileIngest (no O_EXCL)              -> PinnedNonce test
//	KILLED  remove-foreign     rm temp on EEXIST too                  -> PinnedNonce test
//	KILLED  rename-not-link    os.Rename instead of os.Link            -> 6 tests
//	KILLED  blind-adopt        adopt on any stat success               -> ConcurrentSameTarget, RaceBranch
//	KILLED  adopt-sameSize     adopt when sizes match                  -> 3 tests
//	KILLED  size-verify        dst size +1000 accepted                 -> 13 tests
//	KILLED  adopt-never        SameFile -> false                       -> DecisionTable
//
// Two survivors are equivalent mutants, not gaps: dropping the IsRegular check
// on dst after a successful link (a link of a regular file is always regular),
// and always falling back to rename on a non-EEXIST link error (rename fails
// on the same EACCES/ENOSPC the link did, so the observable result is identical).
func TestFinalizeExclusive_PublishesAndConsumesTemp(t *testing.T) {
	dir := t.TempDir()
	tmp := filepath.Join(dir, "book.m4b.abc.tmp")
	dst := filepath.Join(dir, "book.m4b")
	payload := []byte("the-audio")
	require.NoError(t, os.WriteFile(tmp, payload, 0644))

	require.NoError(t, finalizeExclusive(tmp, dst))

	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, payload, got)
	_, err = os.Lstat(tmp)
	assert.True(t, os.IsNotExist(err), "the temp name must be consumed on success, got %v", err)
}

func TestFinalizeExclusive_RefusesAnOccupiedDestination(t *testing.T) {
	dir := t.TempDir()
	tmp := filepath.Join(dir, "book.m4b.abc.tmp")
	dst := filepath.Join(dir, "book.m4b")
	require.NoError(t, os.WriteFile(tmp, []byte("mine"), 0644))
	require.NoError(t, os.WriteFile(dst, []byte("someone-else's-book"), 0644))

	err := finalizeExclusive(tmp, dst)
	require.Error(t, err)
	assert.True(t, os.IsExist(err), "collision must satisfy os.IsExist, got %v", err)
	assert.True(t, errors.Is(err, fs.ErrExist), "collision must satisfy errors.Is(fs.ErrExist), got %v", err)

	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, []byte("someone-else's-book"), got, "the occupant must be untouched")
	_, err = os.Lstat(tmp)
	assert.NoError(t, err, "finalizeExclusive does not own temp cleanup on refusal; copyFile does")
}

// A dangling symlink at dst is invisible to os.Stat (ENOENT) but link(2) still
// refuses it with EEXIST — the primitive sees the directory entry, not what it
// points at. rename(2) would have silently REPLACED the symlink. This is the
// deterministic stand-in for "dst appeared after the caller's stat".
func TestFinalizeExclusive_DoesNotReplaceADanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	tmp := filepath.Join(dir, "book.m4b.abc.tmp")
	dst := filepath.Join(dir, "book.m4b")
	require.NoError(t, os.WriteFile(tmp, []byte("mine"), 0644))
	require.NoError(t, os.Symlink(filepath.Join(dir, "gone"), dst))

	err := finalizeExclusive(tmp, dst)
	require.Error(t, err)
	assert.True(t, os.IsExist(err), "got %v", err)

	fi, err := os.Lstat(dst)
	require.NoError(t, err)
	assert.Equal(t, os.ModeSymlink, fi.Mode()&os.ModeSymlink, "the symlink must still be the symlink")
}

func TestFinalizeExclusive_RejectsANonRegularSource(t *testing.T) {
	dir := t.TempDir()
	tmp := filepath.Join(dir, "adir")
	require.NoError(t, os.Mkdir(tmp, 0755))
	err := finalizeExclusive(tmp, filepath.Join(dir, "book.m4b"))
	require.Error(t, err)
	assert.False(t, os.IsExist(err))
}

func TestLinkUnsupported_ClassifiesOnlyFilesystemRefusals(t *testing.T) {
	wrap := func(e syscall.Errno) error { return &os.LinkError{Op: "link", Old: "a", New: "b", Err: e} }
	assert.True(t, linkUnsupported(wrap(syscall.EPERM)), "EPERM: many SMB/exFAT mounts")
	assert.True(t, linkUnsupported(wrap(syscall.ENOTSUP)))
	assert.True(t, linkUnsupported(wrap(syscall.EXDEV)))
	assert.True(t, linkUnsupported(wrap(syscall.EMLINK)))
	assert.False(t, linkUnsupported(wrap(syscall.EACCES)), "a permission problem on THIS directory must surface")
	assert.False(t, linkUnsupported(wrap(syscall.ENOSPC)))
	assert.False(t, linkUnsupported(wrap(syscall.EIO)))
	assert.False(t, linkUnsupported(wrap(syscall.EEXIST)), "EEXIST is the collision, handled before this")
	assert.False(t, linkUnsupported(errors.New("not an errno")))
}

// The race-recovery branch in OrganizeBookDirectory. Two books with the same
// author and title — so the SAME planned destination — organized concurrently
// from different-content sources. Whoever loses the race used to get
// pathMap[src] = dst recorded UNVERIFIED, pointing that book's row at the
// other book's audio. Whether or not any given iteration actually interleaves,
// the invariant below must hold: every recorded destination holds THAT book's
// bytes.
func TestOrganizeBookDirectory_ConcurrentSameTarget_NeverRecordsTheOtherBooksBytes(t *testing.T) {
	const size = 256 << 10
	payloads := [][]byte{bytes.Repeat([]byte{'A'}, size), bytes.Repeat([]byte{'B'}, size)}

	for iter := range 30 {
		rootDir := t.TempDir()
		cfg := &config.Config{
			RootDir:              rootDir,
			OrganizationStrategy: "copy",
			FolderNamingPattern:  "{author}/{title}",
			FileNamingPattern:    "{title}",
		}
		type result struct {
			pathMap map[string]string
			err     error
		}
		results := make([]result, 2)
		srcs := make([]string, 2)
		for i := range 2 {
			srcs[i] = filepath.Join(t.TempDir(), "in.m4b")
			require.NoError(t, os.WriteFile(srcs[i], payloads[i], 0644))
		}

		var start, done sync.WaitGroup
		start.Add(1)
		for i := range 2 {
			done.Add(1)
			go func() {
				defer done.Done()
				org := NewOrganizer(cfg)
				book := &database.Book{ID: "book-" + string(rune('a'+i)), Title: "Same Title", Format: "m4b",
					Author: &database.Author{Name: "Same Author"}}
				start.Wait()
				_, pm, err := organizeDirTriple(org, book, segsFor(srcs[i]))
				results[i] = result{pm, err}
			}()
		}
		start.Done()
		done.Wait()

		recorded := 0
		for i := range 2 {
			for src, dst := range results[i].pathMap {
				recorded++
				got, err := os.ReadFile(dst)
				require.NoError(t, err, "iter %d: recorded destination unreadable", iter)
				assert.Truef(t, bytes.Equal(got, payloads[i]),
					"iter %d: book %d's row was pointed at %s (from %s) but the bytes there are not its own", iter, i, dst, src)
			}
		}
		assert.GreaterOrEqual(t, recorded, 1, "iter %d: at least one writer must land", iter)
		assert.LessOrEqual(t, recorded, 1, "iter %d: one destination cannot be two books' file", iter)
		if results[0].err != nil && results[1].err != nil {
			t.Fatalf("iter %d: both writers failed: %v / %v", iter, results[0].err, results[1].err)
		}
	}
}

// The directory-grain half of the same race: two multi-file books that
// format to the same target directory. A version row's FilePath is that
// directory, and ReOrganizeInPlace later renames it wholesale, so a
// directory holding files of two books is a corruption even when every file
// in it is intact. The invariant: after both workers return, the target
// directory holds EXACTLY the winner's files (or none, when each won a
// different file and both rolled back), never a mix; the loser's sources are
// untouched; and no landing is reported for a book whose files are not all
// there.
func TestOrganizeBookDirectory_ConcurrentSameTargetDirectory_NeverShared(t *testing.T) {
	const size = 256 << 10
	payloads := [][]byte{bytes.Repeat([]byte{'A'}, size), bytes.Repeat([]byte{'B'}, size)}

	for iter := range 30 {
		rootDir := t.TempDir()
		cfg := &config.Config{
			RootDir:              rootDir,
			OrganizationStrategy: "copy",
			FolderNamingPattern:  "{author}/{title}",
			FileNamingPattern:    "{title} - {track:02d}",
		}
		type result struct {
			dir     string
			pathMap map[string]string
			err     error
		}
		results := make([]result, 2)
		srcs := make([][]string, 2)
		for i := range 2 {
			in := t.TempDir()
			srcs[i] = []string{filepath.Join(in, "ch01.m4b"), filepath.Join(in, "ch02.m4b")}
			for _, p := range srcs[i] {
				require.NoError(t, os.WriteFile(p, payloads[i], 0644))
			}
		}

		var start, done sync.WaitGroup
		start.Add(1)
		for i := range 2 {
			done.Add(1)
			go func() {
				defer done.Done()
				org := NewOrganizer(cfg)
				book := &database.Book{ID: "book-" + string(rune('a'+i)), Title: "Same Title", Format: "m4b",
					Author: &database.Author{Name: "Same Author"}}
				start.Wait()
				dir, pm, err := organizeDirTriple(org, book, segsFor(srcs[i]...))
				results[i] = result{dir, pm, err}
			}()
		}
		start.Done()
		done.Wait()

		winners := 0
		for i := range 2 {
			if results[i].err != nil {
				assert.Empty(t, results[i].pathMap, "iter %d: a failed landing must report nothing", iter)
				continue
			}
			winners++
			require.Len(t, results[i].pathMap, 2, "iter %d: a successful directory landing is all of the book", iter)
			for _, dst := range results[i].pathMap {
				got, err := os.ReadFile(dst)
				require.NoError(t, err)
				assert.Truef(t, bytes.Equal(got, payloads[i]), "iter %d: book %d's row points at bytes that are not its own", iter, i)
			}
		}
		assert.LessOrEqual(t, winners, 1, "iter %d: two books cannot both own one directory", iter)

		// Whatever is in the target directory belongs to ONE book — or it is
		// empty because each worker won one file and both rolled back.
		targetDir := filepath.Join(rootDir, "Same Author", "Same Title")
		entries, err := os.ReadDir(targetDir)
		if errors.Is(err, fs.ErrNotExist) {
			entries = nil
			err = nil
		}
		require.NoError(t, err)
		owners := map[byte]int{}
		for _, e := range entries {
			require.False(t, isTempName(e.Name()), "iter %d: temp left behind: %s", iter, e.Name())
			got, err := os.ReadFile(filepath.Join(targetDir, e.Name()))
			require.NoError(t, err)
			require.NotEmpty(t, got)
			owners[got[0]]++
		}
		assert.LessOrEqual(t, len(owners), 1, "iter %d: the target directory holds files of %d books: %v", iter, len(owners), owners)
		if winners == 1 {
			assert.Len(t, entries, 2, "iter %d: the winner's directory holds exactly its two files", iter)
		} else {
			assert.Empty(t, entries, "iter %d: with no winner, every created file must have been rolled back", iter)
		}

		// The loser's (or both losers') sources are intact.
		for i := range 2 {
			for _, p := range srcs[i] {
				got, err := os.ReadFile(p)
				require.NoError(t, err, "iter %d: source must survive an organize", iter)
				assert.True(t, bytes.Equal(got, payloads[i]))
			}
		}
	}
}

// Deterministic version of the race branch's "not proven" side: a dangling
// symlink at the destination slips past the pre-copy os.Stat (ENOENT), the
// copy runs, and finalizeExclusive refuses with EEXIST. The old branch then
// recorded the destination anyway; the old finalize (os.Rename) would have
// replaced the symlink. Neither may happen — and since 2026-09-02 a
// directory landing is all-or-nothing, so the book fails, the segment that
// DID land is removed again, and the occupant is untouched.
func TestOrganizeBookDirectory_RaceBranch_DoesNotAdoptAnUnprovenOccupant(t *testing.T) {
	rootDir := t.TempDir()
	cfg := &config.Config{
		RootDir:              rootDir,
		OrganizationStrategy: "copy",
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title} - {track:02d}",
	}
	org := NewOrganizer(cfg)
	book := &database.Book{ID: "b1", Title: "Race Branch", Format: "m4b", Author: &database.Author{Name: "Author"}}

	importDir := t.TempDir()
	srcs := []string{filepath.Join(importDir, "ch01.m4b"), filepath.Join(importDir, "ch02.m4b")}
	require.NoError(t, os.WriteFile(srcs[0], []byte("one"), 0644))
	require.NoError(t, os.WriteFile(srcs[1], []byte("two"), 0644))

	planned, err := org.PlanFilePaths(book, segsFor(srcs...))
	require.NoError(t, err)
	dst1 := planned[srcs[1]]
	require.NoError(t, os.MkdirAll(filepath.Dir(dst1), 0755))
	require.NoError(t, os.Symlink(filepath.Join(rootDir, "does-not-exist"), dst1))

	targetDir, pathMap, err := organizeDirTriple(org, book, segsFor(srcs...))
	require.Error(t, err, "a directory landing with an unproven occupant must fail the whole book")
	assert.Contains(t, err.Error(), "did not land 1 of 2")
	assert.Contains(t, err.Error(), srcs[1])
	assert.Empty(t, pathMap, "nothing may be recorded from a failed landing")
	assert.Empty(t, targetDir)

	fi, err := os.Lstat(dst1)
	require.NoError(t, err)
	assert.NotZero(t, fi.Mode()&os.ModeSymlink, "the occupant must not have been replaced")
	_, err = os.Stat(planned[srcs[0]])
	assert.True(t, errors.Is(err, fs.ErrNotExist), "the segment that landed must be rolled back — a version row would otherwise claim a directory it shares")

	entries, err := os.ReadDir(filepath.Dir(dst1))
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, isTempName(e.Name()), "temp left behind: %s", e.Name())
	}
}

func isTempName(name string) bool {
	return len(name) > len(tempFileSuffix) && name[len(name)-len(tempFileSuffix):] == tempFileSuffix
}
