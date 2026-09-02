// file: internal/organizer/landing_contract_test.go
// version: 1.2.1
// guid: 5b7d2c19-8e4a-4f63-9a1c-2d7e6f0b3c58
// last-edited: 2026-09-02

package organizer

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
)

// The Landing contract (2026-09-02): rows follow what LANDED, rollback removes
// what was CREATED, and no primitive on the move path can replace a file that
// is already at the destination. Every test here plants the OTHER book's bytes
// at the contested path first — an empty fixture cannot observe a replacement.

func newLandingTestStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	store.WaitForWarmup()
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// A directory book with two rows, of which only ONE landed — and a foreign
// file sits exactly where the second would have gone. Until 2026-09-02 the row
// writer recomputed the plan and adopted anything found at the planned path,
// so the unlanded row was pointed at the other book's audio. It must keep its
// source path.
func TestCreateOrganizedVersion_UnlandedRowKeepsSource_EvenWhenTargetIsOccupied(t *testing.T) {
	store := newLandingTestStore(t)
	rootDir := t.TempDir()
	config.AppConfig = config.Config{RootDir: rootDir}

	srcDir := t.TempDir()
	src1 := filepath.Join(srcDir, "ch01.mp3")
	src2 := filepath.Join(srcDir, "ch02.mp3")
	require.NoError(t, os.WriteFile(src1, []byte("this book ch01"), 0o644))
	require.NoError(t, os.WriteFile(src2, []byte("this book ch02"), 0o644))

	targetDir := filepath.Join(rootDir, "Author", "Title")
	require.NoError(t, os.MkdirAll(targetDir, 0o775))
	dst1 := filepath.Join(targetDir, "Title - 01.mp3")
	dst2 := filepath.Join(targetDir, "Title - 02.mp3")
	require.NoError(t, os.WriteFile(dst1, []byte("this book ch01"), 0o644))
	// The OTHER book's file at the path this book's second row would plan.
	require.NoError(t, os.WriteFile(dst2, []byte("some other book"), 0o644))

	book, err := store.CreateBook(&database.Book{Title: "Title", FilePath: srcDir})
	require.NoError(t, err)
	require.NoError(t, store.CreateBookFile(&database.BookFile{BookID: book.ID, FilePath: src1, TrackNumber: 1}))
	require.NoError(t, store.CreateBookFile(&database.BookFile{BookID: book.ID, FilePath: src2, TrackNumber: 2}))

	landing := &Landing{
		Path:    targetDir,
		Files:   map[string]string{src1: dst1}, // ch02 did NOT land
		Created: []string{dst1},
	}
	created, err := svcCreateOrganized(t, store, book, landing)
	require.NoError(t, err)

	rows, err := store.GetBookFiles(created.ID)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	got := map[int]string{}
	for _, r := range rows {
		got[r.TrackNumber] = r.FilePath
	}
	require.Equal(t, dst1, got[1], "the landed row follows the landing")
	require.Equal(t, src2, got[2], "the unlanded row must keep its SOURCE path, not adopt the foreign file at the planned target")

	foreign, err := os.ReadFile(dst2)
	require.NoError(t, err)
	require.Equal(t, "some other book", string(foreign), "the other book's file must be untouched")
}

func svcCreateOrganized(t *testing.T, store *database.PebbleStore, book *database.Book, landing *Landing) (*database.Book, error) {
	t.Helper()
	svc := NewService(store)
	return svc.CreateOrganizedVersion(book, landing, "", &noopLogger{})
}

// A single-file landing for a book that has more than one row cannot say which
// row the one path belongs to. It must fail closed and remove what it wrote —
// and ONLY what it wrote.
func TestCreateOrganizedVersion_SingleFileLandingForMultiRowBook_FailsClosedAndRollsBack(t *testing.T) {
	store := newLandingTestStore(t)
	rootDir := t.TempDir()
	config.AppConfig = config.Config{RootDir: rootDir}

	src := t.TempDir()
	book, err := store.CreateBook(&database.Book{Title: "Title", FilePath: filepath.Join(src, "a.mp3")})
	require.NoError(t, err)
	require.NoError(t, store.CreateBookFile(&database.BookFile{BookID: book.ID, FilePath: filepath.Join(src, "a.mp3")}))
	require.NoError(t, store.CreateBookFile(&database.BookFile{BookID: book.ID, FilePath: filepath.Join(src, "b.mp3")}))

	dst := filepath.Join(rootDir, "Author", "Title.mp3")
	require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o775))
	require.NoError(t, os.WriteFile(dst, []byte("copied"), 0o644))

	_, err = svcCreateOrganized(t, store, book, &Landing{Path: dst, Created: []string{dst}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "book_file rows")

	_, statErr := os.Stat(dst)
	require.True(t, errors.Is(statErr, fs.ErrNotExist), "the copy this organize created must be rolled back")

	books, err := store.GetBooksByVersionGroup(derefStr(mustGetBook(t, store, book.ID).VersionGroupID))
	require.NoError(t, err)
	for _, b := range books {
		require.NotEqual(t, dst, b.FilePath, "no organized row may survive the rollback")
	}
	orig := mustGetBook(t, store, book.ID)
	require.True(t, orig.LibraryState == nil || *orig.LibraryState != "organized_source",
		"the original must not be demoted when the organized copy was never created")
}

// An in-place landing means the book's OWN files moved; the row to update is
// the book. CreateOrganizedVersion refuses it outright — before touching the
// store, and without a rollback, because Created would name files the book
// still owns. The two live callers branch on InPlace first; this is the guard
// for a future caller that does not (the batch-save and folder-autoscan ops
// are slated to be routed through here).
func TestCreateOrganizedVersion_InPlaceLandingIsRefused_NoRowNoRollback(t *testing.T) {
	store := newLandingTestStore(t)
	rootDir := t.TempDir()
	config.AppConfig = config.Config{RootDir: rootDir}

	dst := filepath.Join(rootDir, "Author", "Title.mp3")
	require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o775))
	require.NoError(t, os.WriteFile(dst, []byte("the book's own audio"), 0o644))
	book, err := store.CreateBook(&database.Book{Title: "Title", FilePath: dst})
	require.NoError(t, err)

	_, err = svcCreateOrganized(t, store, book, &Landing{Path: dst, InPlace: true, Created: []string{dst}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "in place")

	got, err := os.ReadFile(dst)
	require.NoError(t, err, "a refused in-place landing must not unlink the file the book still owns")
	require.Equal(t, "the book's own audio", string(got))

	n, err := store.CountAllBooks()
	require.NoError(t, err)
	require.Equal(t, 1, n, "no second row may be minted for an in-place landing")
	require.Nil(t, mustGetBook(t, store, book.ID).VersionGroupID, "the original must not be pulled into a version group")
}

func mustGetBook(t *testing.T, store *database.PebbleStore, id string) *database.Book {
	t.Helper()
	b, err := store.GetBookByID(id)
	require.NoError(t, err)
	require.NotNil(t, b)
	return b
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// rollback removes Created and nothing else. An adopted file (present before
// this run, mode "" from OrganizeBook) and a foreign file sharing the target
// directory both survive; the directory is left in place because it is not
// empty.
func TestRollbackOrganizedVersion_RemovesOnlyCreated(t *testing.T) {
	rootDir := t.TempDir()
	config.AppConfig = config.Config{RootDir: rootDir}
	svc := NewService(mocks.NewMockStore(t))

	targetDir := filepath.Join(rootDir, "Author", "Title")
	require.NoError(t, os.MkdirAll(targetDir, 0o775))
	ours := filepath.Join(targetDir, "Title - 01.mp3")
	theirs := filepath.Join(targetDir, "Title - 02.mp3")
	require.NoError(t, os.WriteFile(ours, []byte("ours"), 0o644))
	require.NoError(t, os.WriteFile(theirs, []byte("theirs"), 0o644))

	svc.rollbackOrganizedVersion("", &Landing{
		Path:    targetDir,
		Files:   map[string]string{"/src/01.mp3": ours},
		Created: []string{ours},
	}, &noopLogger{})

	_, err := os.Stat(ours)
	require.True(t, errors.Is(err, fs.ErrNotExist), "the file this organize created must be removed")
	got, err := os.ReadFile(theirs)
	require.NoError(t, err, "the foreign file in the same directory must survive")
	require.Equal(t, "theirs", string(got))
	_, err = os.Stat(targetDir)
	require.NoError(t, err, "a directory that still holds another book's file must not be removed")
}

func TestRollbackOrganizedVersion_EmptyDirectoryIsRemoved(t *testing.T) {
	rootDir := t.TempDir()
	config.AppConfig = config.Config{RootDir: rootDir}
	svc := NewService(mocks.NewMockStore(t))

	targetDir := filepath.Join(rootDir, "Author", "Title")
	require.NoError(t, os.MkdirAll(targetDir, 0o775))
	ours := filepath.Join(targetDir, "Title - 01.mp3")
	require.NoError(t, os.WriteFile(ours, []byte("ours"), 0o644))

	svc.rollbackOrganizedVersion("", &Landing{Path: targetDir, Files: map[string]string{"/s": ours}, Created: []string{ours}}, &noopLogger{})

	_, err := os.Stat(targetDir)
	require.True(t, errors.Is(err, fs.ErrNotExist), "a directory this organize emptied must be removed")
}

// Single-file organize that wrote nothing (OrganizeBook mode "": the target was
// already this book's file). Created is empty, so a rollback must leave the
// target — it was there before this run.
func TestRollbackOrganizedVersion_AdoptedSingleFileSurvives(t *testing.T) {
	rootDir := t.TempDir()
	config.AppConfig = config.Config{RootDir: rootDir}
	svc := NewService(mocks.NewMockStore(t))

	dst := filepath.Join(rootDir, "Author", "Title.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o775))
	require.NoError(t, os.WriteFile(dst, []byte("earlier copy"), 0o644))

	svc.rollbackOrganizedVersion("", &Landing{Path: dst}, &noopLogger{})

	got, err := os.ReadFile(dst)
	require.NoError(t, err, "an adopted file was not created by this run and must not be removed")
	require.Equal(t, "earlier copy", string(got))
}

func TestRollbackOrganizedVersion_RefusesPathsOutsideRoot(t *testing.T) {
	rootDir := t.TempDir()
	config.AppConfig = config.Config{RootDir: rootDir}
	svc := NewService(mocks.NewMockStore(t))

	outside := filepath.Join(t.TempDir(), "elsewhere.m4b")
	require.NoError(t, os.WriteFile(outside, []byte("x"), 0o644))

	svc.rollbackOrganizedVersion("", &Landing{Path: outside, Created: []string{outside}}, &noopLogger{})

	_, err := os.Stat(outside)
	require.NoError(t, err, "rollback must never remove a file outside RootDir")
}

// Two RenameFiles batches, one target, different bytes, many iterations.
// Exactly one publishes; the other rolls back to its source. Both files'
// bytes are readable afterwards — nothing was replaced.
func TestRenameFiles_TwoWritersOneTarget_NeitherFileIsLost(t *testing.T) {
	a := bytes.Repeat([]byte{'A'}, 64<<10)
	b := bytes.Repeat([]byte{'B'}, 64<<10)
	for iter := range 40 {
		dir := t.TempDir()
		srcA := filepath.Join(dir, "a", "book.m4b")
		srcB := filepath.Join(dir, "b", "book.m4b")
		dst := filepath.Join(dir, "lib", "Author", "Title.m4b")
		require.NoError(t, os.MkdirAll(filepath.Dir(srcA), 0o755))
		require.NoError(t, os.MkdirAll(filepath.Dir(srcB), 0o755))
		require.NoError(t, os.WriteFile(srcA, a, 0o644))
		require.NoError(t, os.WriteFile(srcB, b, 0o644))

		var start, done sync.WaitGroup
		start.Add(1)
		results := make([]*RenameFilesResult, 2)
		errs := make([]error, 2)
		for i, src := range []string{srcA, srcB} {
			done.Add(1)
			go func() {
				defer done.Done()
				start.Wait()
				results[i], errs[i] = RenameFiles([]FileRenameEntry{{SegmentID: "s", SourcePath: src, TargetPath: dst}})
			}()
		}
		start.Done()
		done.Wait()

		wins := 0
		for i := range errs {
			if errs[i] == nil {
				wins++
				require.Len(t, results[i].Succeeded, 1, "iter %d", iter)
			} else {
				require.Empty(t, results[i].Succeeded, "iter %d: a failed batch must not report success", iter)
				require.Empty(t, results[i].Errors, "iter %d: the loser must roll back cleanly, got %v", iter, results[i].Errors)
			}
		}
		require.Equal(t, 1, wins, "iter %d: exactly one writer may publish (errs=%v)", iter, errs)

		got, err := os.ReadFile(dst)
		require.NoError(t, err)
		var want, loserBytes []byte
		var loserSrc string
		if errs[0] == nil {
			want, loserBytes, loserSrc = a, b, srcB
		} else {
			want, loserBytes, loserSrc = b, a, srcA
		}
		require.True(t, bytes.Equal(got, want), "iter %d: destination must hold the WINNER's bytes intact (len=%d)", iter, len(got))
		back, err := os.ReadFile(loserSrc)
		require.NoError(t, err, "iter %d: the loser must be rolled back to its source", iter)
		require.True(t, bytes.Equal(back, loserBytes), "iter %d: the loser's bytes must survive at its source", iter)
		assertNoRenameTemps(t, dst)
	}
}

// Stranded-temp resume across the two temp-name generations, and the
// ambiguous case that must refuse.
func TestRenameFiles_ResumesANonceSuffixedStrandedTemp(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "old", "book.m4b") // gone
	dst := filepath.Join(dir, "new", "book.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o755))
	parked := renameTempPath(dst)
	require.True(t, strings.HasPrefix(parked, dst+TmpRenameSuffix+"-"), "temp name shape: %s", parked)
	require.NoError(t, os.WriteFile(parked, []byte("stranded"), 0o644))

	result, err := RenameFiles([]FileRenameEntry{{SegmentID: "s1", SourcePath: src, TargetPath: dst, ExpectedSize: int64(len("stranded"))}})
	require.NoError(t, err)
	require.Empty(t, result.Skipped)
	require.Len(t, result.Succeeded, 1)
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	require.Equal(t, "stranded", string(got))
	assertNoRenameTemps(t, dst)
}

// A stranded temp is bytes of unproven identity at a name every same-titled
// book plans. It is resumed only when its size matches the row; a mismatch
// or a row with no size refuses, leaves the temp where it is (it may be
// another book's file), and publishes nothing under this row.
func TestRenameFiles_StrandedTemp_RefusedWhenUnverifiable(t *testing.T) {
	for _, tc := range []struct {
		name     string
		expected int64
		reason   string
	}{
		{"size mismatch", 3, "is 8 bytes but the row records 3"},
		{"row has no size", 0, "no recorded file size"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "old", "book.m4b") // gone
			dst := filepath.Join(dir, "new", "book.m4b")
			require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o755))
			parked := renameTempPath(dst)
			require.NoError(t, os.WriteFile(parked, []byte("stranded"), 0o644))

			result, err := RenameFiles([]FileRenameEntry{{SegmentID: "s1", SourcePath: src, TargetPath: dst, ExpectedSize: tc.expected}})
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.reason)
			require.Contains(t, err.Error(), parked, "the error must name the temp an operator has to look at")
			require.Empty(t, result.Succeeded)
			require.Len(t, result.Errors, 1)
			_, statErr := os.Stat(dst)
			require.True(t, errors.Is(statErr, fs.ErrNotExist), "nothing may be published under this row")
			got, err := os.ReadFile(parked)
			require.NoError(t, err, "the temp is left in place for the operator, not removed")
			require.Equal(t, "stranded", string(got))
		})
	}
}

// A legacy fixed-name temp beside a PRESENT source: the pre-nonce binary
// refused to park on the taken name, which is what surfaced such a file.
// Proceeding on a nonce name beside it would orphan it forever, so the batch
// refuses before anything moves — the source stays where it is.
func TestRenameFiles_LegacyTempBesidePresentSource_Refuses(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "old", "book.m4b")
	dst := filepath.Join(dir, "new", "book.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(src), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o755))
	require.NoError(t, os.WriteFile(src, []byte("live"), 0o644))
	legacy := dst + TmpRenameSuffix
	require.NoError(t, os.WriteFile(legacy, []byte("orphan"), 0o644))

	result, err := RenameFiles([]FileRenameEntry{{SegmentID: "s1", SourcePath: src, TargetPath: dst, ExpectedSize: 4}})
	require.Error(t, err)
	require.Contains(t, err.Error(), legacy)
	require.Empty(t, result.Succeeded)
	got, err := os.ReadFile(src)
	require.NoError(t, err, "the source must not have moved")
	require.Equal(t, "live", string(got))
	got, err = os.ReadFile(legacy)
	require.NoError(t, err, "the legacy temp is the operator's to resolve, not ours to remove")
	require.Equal(t, "orphan", string(got))
	_, statErr := os.Stat(dst)
	require.True(t, errors.Is(statErr, fs.ErrNotExist))
	matches, _ := filepath.Glob(globEscape(dst+TmpRenameSuffix) + "-*")
	require.Empty(t, matches, "no nonce temp may have been parked beside the legacy one")
}

func TestRenameFiles_TwoStrandedTempsForOneTarget_RefusesToGuess(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "old", "book.m4b") // gone
	dst := filepath.Join(dir, "new", "book.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o755))
	p1 := renameTempPath(dst)
	p2 := renameTempPath(dst)
	require.NotEqual(t, p1, p2)
	require.NoError(t, os.WriteFile(p1, []byte("one"), 0o644))
	require.NoError(t, os.WriteFile(p2, []byte("two"), 0o644))

	result, err := RenameFiles([]FileRenameEntry{{SegmentID: "s1", SourcePath: src, TargetPath: dst}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "refusing to guess")
	require.Len(t, result.Errors, 1)
	require.Empty(t, result.Succeeded)
	_, statErr := os.Stat(dst)
	require.True(t, errors.Is(statErr, fs.ErrNotExist), "nothing may be published when the file's identity is ambiguous")
	for _, p := range []string{p1, p2} {
		_, err := os.Stat(p)
		require.NoError(t, err, "both parked files must be left for the operator: %s", p)
	}
}

func TestRenameFiles_LegacyAndNonceTempsTogether_IsAmbiguous(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "old", "book.m4b") // gone
	dst := filepath.Join(dir, "new", "book.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o755))
	require.NoError(t, os.WriteFile(dst+TmpRenameSuffix, []byte("legacy"), 0o644))
	require.NoError(t, os.WriteFile(renameTempPath(dst), []byte("nonce"), 0o644))

	_, err := RenameFiles([]FileRenameEntry{{SegmentID: "s1", SourcePath: src, TargetPath: dst}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "2 stranded temp files")
}

// A stranded temp whose target path carries glob metacharacters — common in a
// library ("[Unabridged]", "Vol. 1 *", "What?") — must still be found.
func TestStrandedRenameTemps_TargetWithGlobMetacharacters(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "Title [Unabridged] What? *.m4b")
	parked := renameTempPath(dst)
	require.NoError(t, os.WriteFile(parked, []byte("x"), 0o644))
	// A neighbour that an UNESCAPED glob would also match.
	decoy := filepath.Join(dir, "Title U What! X.m4b"+TmpRenameSuffix+"-decoy")
	require.NoError(t, os.WriteFile(decoy, []byte("y"), 0o644))

	found, err := strandedRenameTemps(dst)
	require.NoError(t, err)
	require.Equal(t, []string{parked}, found)
}

// Two books already under RootDir, same author and title, different audio,
// re-organized concurrently into the same target. One must be refused with the
// collision error; both files' bytes must still exist afterwards.
func TestReOrganizeInPlace_TwoBooksSameTarget_NeitherIsDestroyed(t *testing.T) {
	for iter := range 20 {
		rootDir := t.TempDir()
		config.AppConfig = config.Config{
			RootDir:             rootDir,
			FolderNamingPattern: "{author}/{title}",
			FileNamingPattern:   "{title}",
		}
		store := newLandingTestStore(t)
		svc := NewService(store)

		mk := func(id, name string, content []byte) (*database.Book, string) {
			p := filepath.Join(rootDir, "incoming", name, "Title.m4b")
			require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
			require.NoError(t, os.WriteFile(p, content, 0o644))
			b, err := store.CreateBook(&database.Book{
				ID:       id,
				Title:    "Title",
				FilePath: p,
				Format:   "m4b",
				Author:   &database.Author{Name: "Author"},
			})
			require.NoError(t, err)
			b.Author = &database.Author{Name: "Author"}
			return b, p
		}
		contentA := bytes.Repeat([]byte{'A'}, 8<<10)
		contentB := bytes.Repeat([]byte{'B'}, 8<<10)
		bookA, pathA := mk("book-a", "a", contentA)
		bookB, pathB := mk("book-b", "b", contentB)

		var start, done sync.WaitGroup
		start.Add(1)
		paths := make([]string, 2)
		errs := make([]error, 2)
		for i, b := range []*database.Book{bookA, bookB} {
			done.Add(1)
			go func() {
				defer done.Done()
				start.Wait()
				paths[i], errs[i] = svc.ReOrganizeInPlace(b, &noopLogger{})
			}()
		}
		start.Done()
		done.Wait()

		wins := 0
		for i, err := range errs {
			if err == nil {
				wins++
				continue
			}
			require.Contains(t, err.Error(), "destination already exists", "iter %d book %d: the loser must be refused, not silently merged: %v", iter, i, err)
		}
		require.Equal(t, 1, wins, "iter %d: exactly one book may take the target (errs=%v)", iter, errs)

		// Both books' bytes must be readable somewhere: the winner at the target,
		// the loser still at its original path.
		var target string
		if errs[0] == nil {
			target = paths[0]
		} else {
			target = paths[1]
		}
		got, err := os.ReadFile(target)
		require.NoError(t, err)
		winnerBytes, loserBytes, loserPath := contentA, contentB, pathB
		if errs[0] != nil {
			winnerBytes, loserBytes, loserPath = contentB, contentA, pathA
		}
		require.True(t, bytes.Equal(got, winnerBytes), "iter %d: target holds the winner's bytes", iter)
		back, err := os.ReadFile(loserPath)
		require.NoError(t, err, "iter %d: the loser's file must still be at its original path", iter)
		require.True(t, bytes.Equal(back, loserBytes), "iter %d: the loser's bytes must be intact", iter)
	}
}

// A book whose FilePath names a FILE but which has more than one book_file
// row is a multi-file book (the P0 shape: a consolidated book whose FilePath
// is its first chapter). It must take the directory path, never the
// single-file path that would stamp every row with one file.
func TestOrganizeOneBook_MultiRowBookWithFileFilePath_TakesDirectoryPath(t *testing.T) {
	rootDir := t.TempDir()
	srcDir := t.TempDir()
	config.AppConfig = config.Config{
		RootDir:              rootDir,
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title}",
		OrganizationStrategy: "copy",
	}
	ch1 := filepath.Join(srcDir, "ch01.mp3")
	ch2 := filepath.Join(srcDir, "ch02.mp3")
	require.NoError(t, os.WriteFile(ch1, []byte("one"), 0o644))
	require.NoError(t, os.WriteFile(ch2, []byte("two"), 0o644))

	book := &database.Book{
		ID:       "book-multi",
		Title:    "Title",
		FilePath: ch1, // a FILE, not a directory
		Format:   "mp3",
		Author:   &database.Author{Name: "Author"},
	}
	mockStore := mocks.NewMockStore(t)
	mockStore.EXPECT().GetBookFiles("book-multi").Return([]database.BookFile{
		{ID: "bf-1", BookID: "book-multi", FilePath: ch1, TrackNumber: 1},
		{ID: "bf-2", BookID: "book-multi", FilePath: ch2, TrackNumber: 2},
	}, nil).Once()
	mockStore.On("GetBookFileByPath", mock.Anything).Return(nil, nil).Maybe()

	svc := NewService(mockStore)
	landing, err := svc.OrganizeOneBook(NewOrganizer(&config.AppConfig), book, &noopLogger{})
	require.NoError(t, err)
	require.True(t, landing.IsDir(), "a 2-row book must land as a directory, got %+v", landing)
	require.Len(t, landing.Files, 2)
	require.Len(t, landing.Created, 2)
	require.False(t, landing.InPlace, "a book outside the root is copied in, not renamed in place")
	for src, dst := range landing.Files {
		want, _ := os.ReadFile(src)
		got, err := os.ReadFile(dst)
		require.NoError(t, err)
		require.Equal(t, want, got, "%s -> %s", src, dst)
	}
}

// A book already under the library root is renamed in place, and the Landing
// says so. Callers (the HTTP handler, the batch worker) branch on InPlace to
// stamp the existing row instead of creating a version; until 2026-09-02 the
// handler re-derived the decision from a RootDir snapshot taken at startup
// and, after a runtime root_dir change, created a second row at the path the
// in-place move had just produced.
func TestOrganizeOneBook_BookUnderRoot_LandsInPlace(t *testing.T) {
	rootDir := t.TempDir()
	config.AppConfig = config.Config{
		RootDir:              rootDir,
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title}",
		OrganizationStrategy: "copy",
	}
	stale := filepath.Join(rootDir, "Old Author", "Old Title.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(stale), 0o775))
	require.NoError(t, os.WriteFile(stale, []byte("audio"), 0o644))

	book := &database.Book{
		ID:       "book-inplace",
		Title:    "New Title",
		FilePath: stale,
		Format:   "m4b",
		Author:   &database.Author{Name: "New Author"},
	}
	mockStore := mocks.NewMockStore(t)
	mockStore.EXPECT().GetBookFiles("book-inplace").Return([]database.BookFile{
		{ID: "bf-1", BookID: "book-inplace", FilePath: stale},
	}, nil).Maybe()
	mockStore.EXPECT().GetBookByID("book-inplace").Return(book, nil).Maybe()
	mockStore.EXPECT().UpdateBook("book-inplace", mock.Anything).Return(book, nil).Maybe()
	mockStore.EXPECT().UpdateBookFile(mock.Anything, mock.Anything).Return(nil).Maybe()
	mockStore.On("GetBookFileByPath", mock.Anything).Return(nil, nil).Maybe()

	svc := NewService(mockStore)
	landing, err := svc.OrganizeOneBook(NewOrganizer(&config.AppConfig), book, &noopLogger{})
	require.NoError(t, err)
	require.True(t, landing.InPlace, "a book under the root is renamed in place, and the Landing must say so: %+v", landing)
	require.Equal(t, filepath.Join(rootDir, "New Author", "New Title", "New Title.m4b"), landing.Path)
	require.Empty(t, landing.Created, "an in-place rename creates no copy a rollback could remove")
	require.FileExists(t, landing.Path)
	_, statErr := os.Stat(stale)
	require.True(t, errors.Is(statErr, fs.ErrNotExist), "the file was MOVED, not copied")

	// And the copy-in path says the opposite.
	outside := filepath.Join(t.TempDir(), "in.m4b")
	require.NoError(t, os.WriteFile(outside, []byte("audio2"), 0o644))
	other := &database.Book{ID: "book-outside", Title: "Other", FilePath: outside, Format: "m4b", Author: &database.Author{Name: "Someone"}}
	mockStore.EXPECT().GetBookFiles("book-outside").Return(nil, nil).Once()
	landing, err = svc.OrganizeOneBook(NewOrganizer(&config.AppConfig), other, &noopLogger{})
	require.NoError(t, err)
	require.False(t, landing.InPlace)
	require.FileExists(t, outside, "a copy-in leaves the source where it was")
}

// OrganizeBook returns mode "" when the target already IS this file (here: a
// hard link to the source, so os.SameFile). OrganizeSingleFile must then leave
// Created empty, and a rollback of that landing must leave the target alone —
// it was there before this run.
func TestOrganizeSingleFile_AdoptedTarget_IsNotCreatedAndSurvivesRollback(t *testing.T) {
	rootDir := t.TempDir()
	config.AppConfig = config.Config{
		RootDir:              rootDir,
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title}",
		OrganizationStrategy: "copy",
	}
	src := filepath.Join(t.TempDir(), "solo.m4b")
	require.NoError(t, os.WriteFile(src, []byte("audio"), 0o644))
	book := &database.Book{ID: "b1", Title: "Title", FilePath: src, Format: "m4b", Author: &database.Author{Name: "Author"}}

	org := NewOrganizer(&config.AppConfig)
	target, err := org.GenerateTargetPath(book)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o775))
	require.NoError(t, os.Link(src, target), "pre-existing hard link: the target already IS this file")

	landing, err := org.OrganizeSingleFile(book)
	require.NoError(t, err)
	require.Equal(t, target, landing.Path)
	require.Empty(t, landing.Created, "an adopted target was not created by this run")

	svc := NewService(mocks.NewMockStore(t))
	svc.rollbackOrganizedVersion("", landing, &noopLogger{})
	got, err := os.ReadFile(target)
	require.NoError(t, err, "rollback must not remove a file this run only adopted")
	require.Equal(t, "audio", string(got))
}

// The control: a copy that WAS written this run is in Created and a rollback
// removes it.
func TestOrganizeSingleFile_FreshCopy_IsCreatedAndRolledBack(t *testing.T) {
	rootDir := t.TempDir()
	config.AppConfig = config.Config{
		RootDir:              rootDir,
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title}",
		OrganizationStrategy: "copy",
	}
	src := filepath.Join(t.TempDir(), "solo.m4b")
	require.NoError(t, os.WriteFile(src, []byte("audio"), 0o644))
	book := &database.Book{ID: "b1", Title: "Title", FilePath: src, Format: "m4b", Author: &database.Author{Name: "Author"}}

	landing, err := NewOrganizer(&config.AppConfig).OrganizeSingleFile(book)
	require.NoError(t, err)
	require.Equal(t, []string{landing.Path}, landing.Created)

	svc := NewService(mocks.NewMockStore(t))
	svc.rollbackOrganizedVersion("", landing, &noopLogger{})
	_, err = os.Stat(landing.Path)
	require.True(t, errors.Is(err, fs.ErrNotExist), "the copy this run wrote must be removed")
	_, err = os.Stat(src)
	require.NoError(t, err, "the source is never touched")
}

// ENOSYS (FUSE / network filesystems with no link(2)) and EOPNOTSUPP (distinct
// from ENOTSUP on Darwin) both mean "this filesystem does not do hard links".
// The `symlink` organization strategy leaves a symlink in the library; a later
// metadata fix moves it with moveExclusive. The LINK is the library entry, so
// the move must rename the link itself — not follow it (Darwin link(2)) or
// refuse it as "not a regular file" (the 2026-09-02 regression). Mutant M16.
func TestMoveExclusive_SymlinkSource_MovesTheLinkItself(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "downloads", "book.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
	require.NoError(t, os.WriteFile(target, []byte("audio"), 0o644))
	src := filepath.Join(dir, "lib", "Old Title.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(src), 0o755))
	require.NoError(t, os.Symlink(target, src))
	dst := filepath.Join(dir, "lib", "New Title.m4b")

	require.NoError(t, moveExclusive(src, dst))

	info, err := os.Lstat(dst)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&fs.ModeSymlink, "the destination must be the moved LINK, not a copy of its target")
	got, err := os.Readlink(dst)
	require.NoError(t, err)
	require.Equal(t, target, got)
	_, err = os.Lstat(src)
	require.True(t, errors.Is(err, fs.ErrNotExist), "the old link must be gone")
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "audio", string(data), "the link target is untouched")

	// And a dangling symlink moves the same way — the library entry exists even
	// when what it points at does not.
	require.NoError(t, os.Remove(target))
	dst2 := filepath.Join(dir, "lib", "Newer Title.m4b")
	require.NoError(t, moveExclusive(dst, dst2))
	info, err = os.Lstat(dst2)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&fs.ModeSymlink)
}

func TestLinkUnsupported_ENOSYSAndEOPNOTSUPP(t *testing.T) {
	wrap := func(e syscall.Errno) error { return &os.LinkError{Op: "link", Old: "a", New: "b", Err: e} }
	require.True(t, linkUnsupported(wrap(syscall.ENOSYS)))
	require.True(t, linkUnsupported(wrap(syscall.EOPNOTSUPP)))
}
