// file: internal/organizer/organizer_regression_test.go
// version: 1.2.0
// guid: e4f5a6b7-c8d9-e0f1-a2b3-organizer-reg
// last-edited: 2026-08-23

package organizer

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// segsFor builds BookFile rows for the given paths, numbered 1..n.
//
// OrganizeBookDirectory took []string until 2026-08-15 and named each
// destination filepath.Base(src) -- it never applied the file naming pattern.
// It takes rows now because the pattern needs the track number, so these tests
// hand it the same shape the real callers do. The patterns below moved to
// "{title} - {track:02d}" for the same reason: a multi-file book under a
// track-less pattern has every file competing for one filename, which is a
// property of that pattern and not something these tests are here to pin.
func segsFor(paths ...string) []database.BookFile {
	files := make([]database.BookFile, 0, len(paths))
	for i, p := range paths {
		files = append(files, database.BookFile{
			ID:          "seg-" + filepath.Base(p),
			FilePath:    p,
			TrackNumber: i + 1,
		})
	}
	return files
}

// ---------------------------------------------------------------------------
// Regression: OrganizeBookDirectory ERRORS when nothing was copied.
//
// This test used to assert the opposite — (targetDir, empty pathMap, nil err) —
// and its own comment recorded why that was survivable: "organizeDirectoryBook
// used to ignore the empty pathMap and mark the book as organized anyway". The
// fix then went in at that ONE caller. The function kept returning success, and
// the other two callers never grew the same check:
//
//   - ensureLibraryCopy (internal/metafetch/service_apply.go) created a
//     version-linked book record pointing at the directory.
//   - organizeMultiFileBook (internal/itunes/service/importer.go) assigned it
//     to book.FilePath.
//
// Both pointed a book at a directory MkdirAll had just created and nothing had
// been copied into. The check now lives inside OrganizeBookDirectory, so the
// contract this test pins has deliberately changed.
// ---------------------------------------------------------------------------

func TestOrganizeBookDirectory_AllFilesMissing_IsAnError(t *testing.T) {
	rootDir := t.TempDir()

	cfg := &config.Config{
		RootDir:              rootDir,
		OrganizationStrategy: "copy",
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title} - {track:02d}",
	}
	org := NewOrganizer(cfg)

	book := &database.Book{
		ID:     "ghost-1",
		Title:  "Ghost Book",
		Format: "mp3",
		Author: &database.Author{Name: "Ghost Author"},
	}

	// Sources that are simply gone from disk. Note that NONE of these rows is
	// flagged Missing — that is the whole point. The "all rows flagged missing"
	// case was already rejected; this is the one that looked like success.
	segmentPaths := []string{
		"/nonexistent/ch01.mp3",
		"/nonexistent/ch02.mp3",
		"/nonexistent/ch03.mp3",
	}

	targetDir, pathMap, err := org.OrganizeBookDirectory(book, segsFor(segmentPaths...))

	require.Error(t, err, "organizing a book whose every source has vanished must not report success")
	assert.Contains(t, err.Error(), "Ghost Book", "the error must name the book")
	assert.Empty(t, pathMap, "no path mapping may be handed back on the failure path")
	assert.Empty(t, targetDir,
		"targetDir must NOT be returned: both callers that lacked the empty-pathMap check "+
			"assigned it straight onto the book, which is how a book ended up pointing at an empty directory")
}

func TestOrganizeBookDirectory_PartialFilesMissing(t *testing.T) {
	rootDir := t.TempDir()
	importDir := t.TempDir()

	// Create only 1 of 3 files
	require.NoError(t, os.WriteFile(filepath.Join(importDir, "ch02.mp3"), []byte("audio"), 0644))

	cfg := &config.Config{
		RootDir:              rootDir,
		OrganizationStrategy: "copy",
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title} - {track:02d}",
	}
	org := NewOrganizer(cfg)

	book := &database.Book{
		Title:  "Partial Book",
		Format: "mp3",
		Author: &database.Author{Name: "Author"},
	}

	segmentPaths := []string{
		filepath.Join(importDir, "ch01.mp3"), // missing
		filepath.Join(importDir, "ch02.mp3"), // exists
		filepath.Join(importDir, "ch03.mp3"), // missing
	}

	targetDir, pathMap, err := org.OrganizeBookDirectory(book, segsFor(segmentPaths...))
	require.NoError(t, err)
	assert.NotEmpty(t, targetDir)
	assert.Len(t, pathMap, 1, "only the one existing file should be in pathMap")

	// Verify the copied file exists
	for _, dstPath := range pathMap {
		assert.FileExists(t, dstPath)
	}
}

// ---------------------------------------------------------------------------
// Regression: OrganizeBookDirectory skips dst-already-exists
// (When re-organizing, files may already exist at destination.)
// ---------------------------------------------------------------------------

func TestOrganizeBookDirectory_DstAlreadyExists(t *testing.T) {
	rootDir := t.TempDir()
	importDir := t.TempDir()

	srcPath := filepath.Join(importDir, "book.m4b")
	require.NoError(t, os.WriteFile(srcPath, []byte("original"), 0644))

	cfg := &config.Config{
		RootDir:              rootDir,
		OrganizationStrategy: "copy",
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title} - {track:02d}",
	}
	org := NewOrganizer(cfg)

	book := &database.Book{
		Title:  "Already There",
		Format: "m4b",
		Author: &database.Author{Name: "Author"},
	}

	// First organize
	targetDir, pathMap, err := org.OrganizeBookDirectory(book, segsFor(srcPath))
	require.NoError(t, err)
	require.Len(t, pathMap, 1)

	// Second organize of same book — dst already exists
	targetDir2, pathMap2, err := org.OrganizeBookDirectory(book, segsFor(srcPath))
	require.NoError(t, err)
	assert.Equal(t, targetDir, targetDir2, "target dir should be the same")
	assert.Len(t, pathMap2, 1, "dst-exists should still be included in pathMap")
}

// ---------------------------------------------------------------------------
// Regression: OrganizeBookDirectory with empty segment list
// ---------------------------------------------------------------------------

func TestOrganizeBookDirectory_EmptySegments(t *testing.T) {
	rootDir := t.TempDir()

	cfg := &config.Config{
		RootDir:              rootDir,
		OrganizationStrategy: "copy",
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title} - {track:02d}",
	}
	org := NewOrganizer(cfg)

	book := &database.Book{
		Title:  "Empty",
		Format: "m4b",
		Author: &database.Author{Name: "Author"},
	}

	_, _, err := org.OrganizeBookDirectory(book, nil)
	assert.Error(t, err, "empty segment list should error")
}

func TestOrganizeBookDirectory_NilBook(t *testing.T) {
	rootDir := t.TempDir()

	cfg := &config.Config{
		RootDir:              rootDir,
		OrganizationStrategy: "copy",
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title} - {track:02d}",
	}
	org := NewOrganizer(cfg)

	_, _, err := org.OrganizeBookDirectory(nil, segsFor("/some/file.m4b"))
	assert.Error(t, err, "nil book should error")
}

// ---------------------------------------------------------------------------
// New: verify copy strategy preserves file content
// ---------------------------------------------------------------------------

func TestOrganizeBookDirectory_CopyPreservesContent(t *testing.T) {
	rootDir := t.TempDir()
	importDir := t.TempDir()

	content1 := []byte("chapter-one-audio-data-here-" + string(make([]byte, 1000)))
	content2 := []byte("chapter-two-audio-data-here-" + string(make([]byte, 2000)))
	require.NoError(t, os.WriteFile(filepath.Join(importDir, "ch01.mp3"), content1, 0644))
	require.NoError(t, os.WriteFile(filepath.Join(importDir, "ch02.mp3"), content2, 0644))

	cfg := &config.Config{
		RootDir:              rootDir,
		OrganizationStrategy: "copy",
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title} - {track:02d}",
	}
	org := NewOrganizer(cfg)

	book := &database.Book{
		Title:  "Content Test",
		Format: "mp3",
		Author: &database.Author{Name: "Author"},
	}

	_, pathMap, err := org.OrganizeBookDirectory(book, segsFor(
		filepath.Join(importDir, "ch01.mp3"),
		filepath.Join(importDir, "ch02.mp3"),
	))
	require.NoError(t, err)
	require.Len(t, pathMap, 2)

	// Verify each file's content matches
	for srcPath, dstPath := range pathMap {
		srcData, _ := os.ReadFile(srcPath)
		dstData, _ := os.ReadFile(dstPath)
		assert.Equal(t, srcData, dstData,
			"content of %s should match source", filepath.Base(dstPath))
	}
}

// ---------------------------------------------------------------------------
// New: src == dst should be included in pathMap (already in place)
// ---------------------------------------------------------------------------

func TestOrganizeBookDirectory_SrcEqualsDst(t *testing.T) {
	rootDir := t.TempDir()

	// Create a file already in the target location
	cfg := &config.Config{
		RootDir:              rootDir,
		OrganizationStrategy: "copy",
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title} - {track:02d}",
	}
	org := NewOrganizer(cfg)

	book := &database.Book{
		Title:  "SamePlace",
		Format: "m4b",
		Author: &database.Author{Name: "Author"},
	}

	// Pre-create the target directory and the file AT ITS PATTERN NAME. This
	// used to be "book.m4b": under the old builder the destination was
	// filepath.Base(src), so any filename at all was already "in place". The
	// file pattern decides the name now, so only the pattern's own answer is.
	targetDir := filepath.Join(rootDir, "Author", "SamePlace")
	require.NoError(t, os.MkdirAll(targetDir, 0755))
	filePath := filepath.Join(targetDir, "SamePlace.m4b")
	require.NoError(t, os.WriteFile(filePath, []byte("data"), 0644))

	_, pathMap, err := org.OrganizeBookDirectory(book, segsFor(filePath))
	require.NoError(t, err)
	assert.Len(t, pathMap, 1, "src==dst should still be included in pathMap")
	assert.Equal(t, filePath, pathMap[filePath])
}

// TestOrganizeBookDirectory_AppliesFileNamingPattern is the F8 assertion: a
// directory book must be FILE aware, not just folder aware.
//
// Until 2026-08-15 OrganizeBookDirectory expanded the folder pattern and then
// kept filepath.Base(src) as the filename, so a book whose files were named
// "01 - track.mp3" landed in the right directory under the wrong names -- and
// ComputeTargetPaths, running the file pattern on the metadata-apply path for
// the same book, planned different ones. Each path then renamed toward its own
// answer for as long as both kept running.
func TestOrganizeBookDirectory_AppliesFileNamingPattern(t *testing.T) {
	rootDir := t.TempDir()
	importDir := t.TempDir()

	srcNames := []string{"aaa-junk-name.mp3", "zzz-other-junk.mp3", "mmm-more-junk.mp3"}
	var srcPaths []string
	for _, n := range srcNames {
		p := filepath.Join(importDir, n)
		require.NoError(t, os.WriteFile(p, []byte("audio-"+n), 0644))
		srcPaths = append(srcPaths, p)
	}

	cfg := &config.Config{
		RootDir:              rootDir,
		OrganizationStrategy: "copy",
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title} - {track:02d}",
	}
	org := NewOrganizer(cfg)

	book := &database.Book{
		Title:  "Foundation",
		Format: "mp3",
		Author: &database.Author{Name: "Isaac Asimov"},
	}

	targetDir, pathMap, err := org.OrganizeBookDirectory(book, segsFor(srcPaths...))
	require.NoError(t, err)
	require.Len(t, pathMap, 3)

	// segsFor numbers 1..n in the order given, so the junk names map to tracks
	// in THAT order -- not alphabetically, which is the whole reason this takes
	// rows instead of paths.
	want := map[string]string{
		srcPaths[0]: filepath.Join(targetDir, "Foundation - 01.mp3"),
		srcPaths[1]: filepath.Join(targetDir, "Foundation - 02.mp3"),
		srcPaths[2]: filepath.Join(targetDir, "Foundation - 03.mp3"),
	}
	assert.Equal(t, want, pathMap, "each file must be renamed by the file naming pattern")
	for _, dst := range want {
		assert.FileExists(t, dst)
	}
}

// TestOrganizeBookDirectory_TracklessPatternDoesNotCollapseBook is the guard on
// the most dangerous consequence of making organize file-aware.
//
// The production file naming pattern on 2026-08-15 was
// "{title} - {author} - read by {narrator}" — no {track} placeholder. Expanding
// it per file gives every file of a multi-file book the SAME name, so copying
// to those paths would collapse a 40-part book into one file. That could not
// happen while the destination was filepath.Base(src), which is exactly why it
// has to be tested now: the collapse is a NEW failure mode introduced by the
// fix, not an old one, and it would have destroyed multi-file books library-wide.
func TestOrganizeBookDirectory_TracklessPatternDoesNotCollapseBook(t *testing.T) {
	rootDir := t.TempDir()
	importDir := t.TempDir()

	var srcPaths []string
	for i := 1; i <= 12; i++ {
		p := filepath.Join(importDir, fmt.Sprintf("part%02d.mp3", i))
		require.NoError(t, os.WriteFile(p, []byte(fmt.Sprintf("audio-%02d", i)), 0644))
		srcPaths = append(srcPaths, p)
	}

	cfg := &config.Config{
		RootDir:              rootDir,
		OrganizationStrategy: "copy",
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title} - {author} - read by {narrator}", // the live prod pattern
	}
	org := NewOrganizer(cfg)

	book := &database.Book{
		Title:  "Foundation",
		Format: "mp3",
		Author: &database.Author{Name: "Isaac Asimov"},
	}

	targetDir, pathMap, err := org.OrganizeBookDirectory(book, segsFor(srcPaths...))
	require.NoError(t, err)
	require.Len(t, pathMap, 12, "every file must be planned")

	// Distinct targets...
	distinct := map[string]struct{}{}
	for _, dst := range pathMap {
		distinct[dst] = struct{}{}
	}
	assert.Len(t, distinct, 12, "a track-less pattern must not map 12 files onto one name")

	// ...and 12 real files on disk, with their content intact.
	dirEntries, err := os.ReadDir(targetDir)
	require.NoError(t, err)
	assert.Len(t, dirEntries, 12, "all 12 files must survive organize")
	for src, dst := range pathMap {
		srcData, readErr := os.ReadFile(src)
		require.NoError(t, readErr)
		dstData, readErr := os.ReadFile(dst)
		require.NoError(t, readErr)
		assert.Equal(t, srcData, dstData, "content must match for %s", filepath.Base(dst))
	}

	// Zero padded to a uniform width so 9 and 10 still sort correctly.
	assert.Contains(t, pathMap[srcPaths[8]], " - 09.mp3")
	assert.Contains(t, pathMap[srcPaths[9]], " - 10.mp3")
}

// ---------------------------------------------------------------------------
// New: path sanitization — no directory traversal
// ---------------------------------------------------------------------------

func TestOrganizeBookDirectory_PathTraversalPrevented(t *testing.T) {
	rootDir := t.TempDir()
	importDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(importDir, "file.m4b"), []byte("x"), 0644))

	cfg := &config.Config{
		RootDir:              rootDir,
		OrganizationStrategy: "copy",
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title} - {track:02d}",
	}
	org := NewOrganizer(cfg)

	// sanitizeFilename now strips ".." → "__", so this should stay inside rootDir
	book := &database.Book{
		Title:  "../../../etc/passwd",
		Format: "m4b",
		Author: &database.Author{Name: "../../../root"},
	}

	targetDir, _, err := org.OrganizeBookDirectory(book, segsFor(
		filepath.Join(importDir, "file.m4b"),
	))

	// With our security fix, ".." is replaced with "__" so the path stays inside rootDir
	if err == nil {
		absTarget, _ := filepath.Abs(targetDir)
		absRoot, _ := filepath.Abs(rootDir)
		assert.Contains(t, absTarget, absRoot,
			"target directory must stay inside rootDir even with traversal attempt")
	}
	// Either way, it should NOT create files outside rootDir
}

// Retargeted from the deleted sanitizeFilename onto SanitizePathComponent, the
// package's single sanitizer. The traversal guard has to live on the surviving
// function or it does not live anywhere.
func TestSanitizePathComponent_StripsDotDot(t *testing.T) {
	result := SanitizePathComponent("../../../etc/passwd")
	assert.NotContains(t, result, "..", "dot-dot sequences must be neutralized")

	// Single ".." also stripped
	result2 := SanitizePathComponent("..evil")
	assert.NotContains(t, result2, "..")
}

func TestSanitizePathComponent_PreservesNormalDots(t *testing.T) {
	result := SanitizePathComponent("Dr. Who - Season 1.m4b")
	assert.Contains(t, result, "Dr.")
	assert.Contains(t, result, "1.m4b")
}

func TestEnsureUnderRoot_RejectsEscape(t *testing.T) {
	err := ensureUnderRoot("/tmp/evil/../../../etc/passwd", "/tmp/library")
	assert.Error(t, err, "path escaping root should be rejected")
}

func TestEnsureUnderRoot_AcceptsValid(t *testing.T) {
	err := ensureUnderRoot("/tmp/library/Author/Title/file.m4b", "/tmp/library")
	assert.NoError(t, err, "valid path inside root should be accepted")
}

// ---------------------------------------------------------------------------
// Regression: the destination-adoption check must PROVE sameness.
//
// OrganizeBookDirectory used to adopt a file already sitting at the destination
// whenever it merely had the same SIZE as the source. Two different audiobooks
// of identical byte length are not rare, and adopting one writes this book's
// row to point at the other book's audio — unrecoverable from inside the app.
// The three tests below pin the new contract from both sides:
//
//   - same size, DIFFERENT content -> not adopted (the bug)
//   - same size, SAME content, different inode -> still adopted (the case the
//     size check was originally added for; without this a change that simply
//     never adopts would pass every negative test)
//   - destination unreadable -> not adopted (fails closed on "unknown")
// ---------------------------------------------------------------------------

// adoptionFixture organizes a two-segment book once and returns the organizer,
// the book, the source paths and the destination paths the first pass produced.
// Two segments, not one: OrganizeBookDirectory errors when NOTHING lands, so a
// single-segment negative test would fail on that error instead of on the
// adoption decision it means to check.
func adoptionFixture(t *testing.T) (org *Organizer, book *database.Book, srcPaths, dstPaths []string) {
	t.Helper()

	rootDir := t.TempDir()
	importDir := t.TempDir()

	srcPaths = []string{
		filepath.Join(importDir, "ch01.m4b"),
		filepath.Join(importDir, "ch02.m4b"),
	}
	require.NoError(t, os.WriteFile(srcPaths[0], []byte("chapter-one-audio-payload"), 0644))
	require.NoError(t, os.WriteFile(srcPaths[1], []byte("chapter-two-audio-payload"), 0644))

	cfg := &config.Config{
		RootDir:              rootDir,
		OrganizationStrategy: "copy",             // a real copy: dst gets its own inode, so
		FolderNamingPattern:  "{author}/{title}", // os.SameFile cannot short-circuit
		FileNamingPattern:    "{title} - {track:02d}",
	}
	org = NewOrganizer(cfg)
	book = &database.Book{
		Title:  "Adoption Check",
		Format: "m4b",
		Author: &database.Author{Name: "Author"},
	}

	_, pathMap, err := org.OrganizeBookDirectory(book, segsFor(srcPaths...))
	require.NoError(t, err)
	require.Len(t, pathMap, 2, "first organize should place both segments")

	for _, src := range srcPaths {
		dst, ok := pathMap[src]
		require.True(t, ok, "first organize should map %s", src)
		dstPaths = append(dstPaths, dst)
	}

	// The whole point of these tests is the non-SameFile branch. If the strategy
	// ever starts hardlinking, os.SameFile answers first and none of this proves
	// anything, so assert the inodes really are distinct.
	for i := range srcPaths {
		srcInfo, err := os.Stat(srcPaths[i])
		require.NoError(t, err)
		dstInfo, err := os.Stat(dstPaths[i])
		require.NoError(t, err)
		require.False(t, os.SameFile(srcInfo, dstInfo),
			"fixture requires a distinct inode at the destination, else the SameFile fast path hides the check under test")
	}
	return org, book, srcPaths, dstPaths
}

func TestOrganizeBookDirectory_DstSameSizeDifferentContent_NotAdopted(t *testing.T) {
	org, book, srcPaths, dstPaths := adoptionFixture(t)

	// Replace segment two's destination with an UNRELATED file of exactly the
	// same length — the collision the old size-equality check could not see.
	srcBytes, err := os.ReadFile(srcPaths[1])
	require.NoError(t, err)
	occupant := []byte("a-totally-different-book!")
	require.Len(t, occupant, len(srcBytes), "the occupant must be byte-length identical or this test proves nothing")
	require.NotEqual(t, srcBytes, occupant)
	require.NoError(t, os.WriteFile(dstPaths[1], occupant, 0644))

	_, pathMap, err := org.OrganizeBookDirectory(book, segsFor(srcPaths...))
	require.NoError(t, err, "one segment still lands, so the book must not error")

	assert.NotContains(t, pathMap, srcPaths[1],
		"a same-size but different-content occupant must NOT be adopted: doing so points this book's row at another book's audio")
	assert.Contains(t, pathMap, srcPaths[0],
		"the untouched segment must still be adopted — the guard must not suppress everything")
	assert.Len(t, pathMap, 1)

	// And the occupant is left exactly as it was: declining to adopt must not
	// quietly overwrite whatever was there either.
	after, err := os.ReadFile(dstPaths[1])
	require.NoError(t, err)
	assert.Equal(t, occupant, after, "the unrelated occupant's bytes must be untouched")
}

func TestOrganizeBookDirectory_DstSameSizeSameContent_Adopted(t *testing.T) {
	org, book, srcPaths, dstPaths := adoptionFixture(t)

	// Rewrite both destinations with byte-identical content through a fresh
	// file, so they are genuinely new inodes holding the same bytes: the
	// interrupted-copy-resume case the size check was originally added for.
	for i := range dstPaths {
		srcBytes, err := os.ReadFile(srcPaths[i])
		require.NoError(t, err)
		require.NoError(t, os.Remove(dstPaths[i]))
		require.NoError(t, os.WriteFile(dstPaths[i], srcBytes, 0644))

		srcInfo, err := os.Stat(srcPaths[i])
		require.NoError(t, err)
		dstInfo, err := os.Stat(dstPaths[i])
		require.NoError(t, err)
		require.False(t, os.SameFile(srcInfo, dstInfo),
			"the rewritten destination must be a different inode, else SameFile adopts it and the content check is untested")
	}

	_, pathMap, err := org.OrganizeBookDirectory(book, segsFor(srcPaths...))
	require.NoError(t, err)

	assert.Len(t, pathMap, 2, "byte-identical destinations must still be adopted")
	for i := range srcPaths {
		assert.Equal(t, dstPaths[i], pathMap[srcPaths[i]])
	}
}

func TestOrganizeBookDirectory_DstUnreadable_NotAdopted(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission bits this test relies on")
	}
	org, book, srcPaths, dstPaths := adoptionFixture(t)

	// Same size, same content, but we cannot read it to prove that. "Cannot
	// tell" must resolve to "do not adopt", never to "probably fine".
	require.NoError(t, os.Chmod(dstPaths[1], 0000))
	t.Cleanup(func() { _ = os.Chmod(dstPaths[1], 0644) })

	_, pathMap, err := org.OrganizeBookDirectory(book, segsFor(srcPaths...))
	require.NoError(t, err)

	assert.NotContains(t, pathMap, srcPaths[1],
		"an unreadable destination is an unknown, and the check must fail closed")
	assert.Contains(t, pathMap, srcPaths[0])
}
