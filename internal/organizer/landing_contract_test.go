// file: internal/organizer/landing_contract_test.go
// version: 1.0.0
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
		Skipped: []string{src2},
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
		Skipped: []string{"/src/02.mp3"},
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

	result, err := RenameFiles([]FileRenameEntry{{SegmentID: "s1", SourcePath: src, TargetPath: dst}})
	require.NoError(t, err)
	require.Empty(t, result.Skipped)
	require.Len(t, result.Succeeded, 1)
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	require.Equal(t, "stranded", string(got))
	assertNoRenameTemps(t, dst)
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
	require.Empty(t, landing.Skipped)
	for src, dst := range landing.Files {
		want, _ := os.ReadFile(src)
		got, err := os.ReadFile(dst)
		require.NoError(t, err)
		require.Equal(t, want, got, "%s -> %s", src, dst)
	}
}

// ENOSYS (FUSE / network filesystems with no link(2)) and EOPNOTSUPP (distinct
// from ENOTSUP on Darwin) both mean "this filesystem does not do hard links".
func TestLinkUnsupported_ENOSYSAndEOPNOTSUPP(t *testing.T) {
	wrap := func(e syscall.Errno) error { return &os.LinkError{Op: "link", Old: "a", New: "b", Err: e} }
	require.True(t, linkUnsupported(wrap(syscall.ENOSYS)))
	require.True(t, linkUnsupported(wrap(syscall.EOPNOTSUPP)))
}
