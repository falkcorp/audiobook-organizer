// file: internal/plugins/maintenance/rewrite_path_prefix_test.go
// version: 1.2.0
// guid: b036c68e-a6da-48df-806b-b765cda262ef
// last-edited: 2026-09-19

package maintenance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// rewriteFakeStore records every write so a test can assert BOTH what was
// written and — just as importantly — that nothing else was.
type rewriteFakeStore struct {
	mu sync.Mutex // the write phase runs on RunItems' worker pool

	books     []database.BookCore
	fullBooks map[string]*database.Book
	fileCores []database.BookFileCore
	fullFiles map[string]*database.BookFile // fileID → row

	atPath map[string][]string // path → live book IDs

	bookWrites []database.Book
	fileWrites []database.BookFile
	ledger     []database.BookPathChange

	// modifyBookHook runs inside ModifyBook, under the fake's lock, BEFORE the
	// callback — the seam a test uses to simulate another writer moving the row
	// between the scan and the write.
	modifyBookHook func(b *database.Book)

	// modifyBookErr makes every ModifyBook fail, as a store-level write error
	// would. The callback still runs first, which is the point: it is what sets
	// the op's bookOld/bookNew, so this seam is what proves those values do not
	// reach the ledger when the commit did not happen.
	modifyBookErr error
}

func (f *rewriteFakeStore) GetAllBooksCore(limit, offset int) ([]database.BookCore, error) {
	if offset >= len(f.books) {
		return nil, nil
	}
	end := offset + limit
	if end > len(f.books) {
		end = len(f.books)
	}
	return f.books[offset:end], nil
}

func (f *rewriteFakeStore) GetAllBookFilesCore() ([]database.BookFileCore, error) {
	return f.fileCores, nil
}

func (f *rewriteFakeStore) LiveBookIDsAtPath(path string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.atPath[path], nil
}

func (f *rewriteFakeStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.modifyBookHook != nil {
		// Mutates the STORED row, as a concurrent writer that committed just
		// before we took the lock would have. The op must then read the moved
		// value, not the one its scan saw.
		f.modifyBookHook(f.fullBooks[id])
		f.modifyBookHook = nil
	}
	stored := f.fullBooks[id]
	if stored == nil {
		return nil, nil
	}
	row := *stored
	if err := fn(&row); err != nil {
		if err == database.ErrSkipBookWrite {
			return stored, nil
		}
		return nil, err
	}
	if f.modifyBookErr != nil {
		// The callback has already run (and already set the op's bookOld/
		// bookNew); the COMMIT is what fails.
		return nil, f.modifyBookErr
	}
	f.fullBooks[id] = &row
	f.bookWrites = append(f.bookWrites, row)
	return &row, nil
}

func (f *rewriteFakeStore) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	stored := f.fullFiles[fileID]
	if stored == nil {
		return nil, nil
	}
	row := *stored
	if err := fn(&row); err != nil {
		if err == database.ErrSkipBookFileWrite {
			return stored, nil
		}
		return nil, err
	}
	f.fullFiles[fileID] = &row
	f.fileWrites = append(f.fileWrites, row)
	return &row, nil
}

func (f *rewriteFakeStore) RecordPathChange(change *database.BookPathChange) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ledger = append(f.ledger, *change)
	return nil
}

// seedRewrite builds one book with one file under oldDir, plus a
// source_import_path under the same tree, and creates the NEW tree on disk so
// the default require_target_exists gate passes.
func seedRewrite(t *testing.T, oldDir string) *rewriteFakeStore {
	t.Helper()
	oldFile := filepath.Join(oldDir, "01.mp3")
	src := oldDir
	return &rewriteFakeStore{
		books: []database.BookCore{{ID: "b1", FilePath: oldDir, SourceImportPath: &src}},
		fullBooks: map[string]*database.Book{
			"b1": {ID: "b1", FilePath: oldDir, SourceImportPath: &src},
		},
		fileCores: []database.BookFileCore{{ID: "f1", BookID: "b1", FilePath: oldFile}},
		fullFiles: map[string]*database.BookFile{
			"f1": {ID: "f1", BookID: "b1", FilePath: oldFile,
				AcoustIDFingerprint: []byte("fp-keep-me")},
		},
		atPath: map[string][]string{oldDir: {"b1"}},
	}
}

func rewriteParams(old, nw string, dryRun bool) rewritePathPrefixParams {
	return rewritePathPrefixParams{OldPrefix: old, NewPrefix: nw, DryRun: &dryRun}
}

// makeTree creates <dir>/01.mp3 so the stat gate has something to find.
func makeTree(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "01.mp3"), []byte("x"), 0o644))
}

// The happy path, and the whole point of the op: EVERY enumerated field follows
// the prefix, and the fingerprint (which a full-record replacement would wipe)
// survives.
func TestRewritePathPrefix_ApplyRewritesEveryEnumeratedField(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, "Christopher Paolin - The Inheritance Cycle")
	newDir := filepath.Join(root, "Christopher Paolini - The Inheritance Cycle")
	makeTree(t, newDir)
	store := seedRewrite(t, oldDir)

	plan, err := planRewritePathPrefix(context.Background(), store, nil,
		rewriteParams(oldDir, newDir, false), &fakeReporter{})
	require.NoError(t, err)

	require.Equal(t, 1, plan.MatchedBooks)
	require.Equal(t, 3, plan.MatchedFields, "book.file_path + book.source_import_path + book_file.file_path")
	require.Equal(t, 1, plan.BooksRewritten)
	require.Equal(t, 3, plan.FieldsRewritten)
	require.Zero(t, plan.UpdateErrs)

	require.Len(t, store.bookWrites, 1)
	require.Equal(t, newDir, store.bookWrites[0].FilePath)
	require.NotNil(t, store.bookWrites[0].SourceImportPath)
	require.Equal(t, newDir, *store.bookWrites[0].SourceImportPath)

	require.Len(t, store.fileWrites, 1)
	require.Equal(t, filepath.Join(newDir, "01.mp3"), store.fileWrites[0].FilePath)
	require.Equal(t, []byte("fp-keep-me"), store.fileWrites[0].AcoustIDFingerprint,
		"a full-record replacement would have wiped the fingerprint")

	// The ledger: one entry, AFTER the write, with the old value as read.
	require.Len(t, store.ledger, 1)
	require.Equal(t, "prefix-rewrite", store.ledger[0].ChangeType)
	require.Equal(t, oldDir, store.ledger[0].OldPath)
	require.Equal(t, newDir, store.ledger[0].NewPath)
}

// The separator-boundary trap: "/x/ab" must NOT match "/x/abooks/…".
func TestRewritePathPrefix_SeparatorBoundaryDoesNotMatchSibling(t *testing.T) {
	root := t.TempDir()
	oldPrefix := filepath.Join(root, "ab")
	sibling := filepath.Join(root, "abooks")
	newPrefix := filepath.Join(root, "cd")
	makeTree(t, newPrefix)

	store := seedRewrite(t, sibling)
	plan, err := planRewritePathPrefix(context.Background(), store, nil,
		rewriteParams(oldPrefix, newPrefix, false), &fakeReporter{})
	require.NoError(t, err)
	require.Zero(t, plan.MatchedBooks, "%q must not be swept by prefix %q", sibling, oldPrefix)
	require.Empty(t, store.bookWrites)
	require.Empty(t, store.fileWrites)
}

// A new path already occupied by a DIFFERENT live book is refused and reported,
// not written.
func TestRewritePathPrefix_BookCollisionRefusedAndReported(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, "old")
	newDir := filepath.Join(root, "new")
	makeTree(t, newDir)
	store := seedRewrite(t, oldDir)
	store.atPath[newDir] = []string{"b-other"}

	plan, err := planRewritePathPrefix(context.Background(), store, nil,
		rewriteParams(oldDir, newDir, false), &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, plan.TargetCollision)
	require.Zero(t, plan.Rewritable)
	require.Empty(t, store.bookWrites)
	require.Empty(t, store.fileWrites)

	// The verdict is book-level but the report is per-field, so the colliding
	// book is named on the row that CAUSED the refusal; the other rows of the
	// same book point at that row instead of repeating a reason about a path
	// they are not about. Asserting "b-other" on every row would be asserting
	// that older, misleading behaviour.
	var named, crossReferenced int
	for _, d := range plan.all {
		if d.Bucket != "collision" {
			continue
		}
		switch {
		case strings.Contains(d.Reason, "b-other"):
			named++
		case strings.Contains(d.Reason, "refused with its book"):
			crossReferenced++
		default:
			t.Fatalf("collision row %s explains nothing: %q", d.RowID, d.Reason)
		}
	}
	require.Positive(t, named, "the collision must be REPORTED, not just counted")
	require.Equal(t, 1, named, "only the causing row should name the colliding book")
	_ = crossReferenced
}

// A book_file target already claimed by another row is refused too.
func TestRewritePathPrefix_FileCollisionRefused(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, "old")
	newDir := filepath.Join(root, "new")
	makeTree(t, newDir)
	store := seedRewrite(t, oldDir)
	// Another row already sits on the target file.
	store.fileCores = append(store.fileCores, database.BookFileCore{
		ID: "f-other", BookID: "b-other", FilePath: filepath.Join(newDir, "01.mp3")})

	plan, err := planRewritePathPrefix(context.Background(), store, nil,
		rewriteParams(oldDir, newDir, false), &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, plan.TargetCollision)
	require.Empty(t, store.fileWrites)
}

// The stat gate: with the default require_target_exists, a rewrite whose target
// is not on disk is refused. This is also what a dry run BEFORE the rename looks
// like.
func TestRewritePathPrefix_MissingTargetRefused(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, "old")
	newDir := filepath.Join(root, "new") // never created
	store := seedRewrite(t, oldDir)

	plan, err := planRewritePathPrefix(context.Background(), store, nil,
		rewritePathPrefixParams{OldPrefix: oldDir, NewPrefix: newDir, DryRun: boolPtr(false)},
		&fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, plan.TargetMissing)
	require.Zero(t, plan.BooksRewritten)
	require.Empty(t, store.bookWrites)

	// …and the opt-out lets it through.
	store2 := seedRewrite(t, oldDir)
	plan2, err := planRewritePathPrefix(context.Background(), store2, nil,
		rewritePathPrefixParams{OldPrefix: oldDir, NewPrefix: newDir,
			DryRun: boolPtr(false), RequireTargetExists: boolPtr(false)},
		&fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, plan2.BooksRewritten)
}

// Dry run is the DEFAULT and it writes nothing.
func TestRewritePathPrefix_DryRunIsDefaultAndWritesNothing(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, "old")
	newDir := filepath.Join(root, "new")
	makeTree(t, newDir)
	store := seedRewrite(t, oldDir)

	// DryRun left NIL: the omitted key must mean dry run, not apply.
	plan, err := planRewritePathPrefix(context.Background(), store, nil,
		rewritePathPrefixParams{OldPrefix: oldDir, NewPrefix: newDir}, &fakeReporter{})
	require.NoError(t, err)
	require.True(t, plan.DryRun)
	require.Equal(t, 1, plan.Rewritable)
	require.Zero(t, plan.BooksRewritten)
	require.Empty(t, store.bookWrites)
	require.Empty(t, store.fileWrites)
	require.Empty(t, store.ledger)
}

// A re-run after a PARTIAL apply completes the rest and does not touch what is
// already done: a rewritten row no longer matches old_prefix, so it is simply
// not selected again.
func TestRewritePathPrefix_RerunAfterPartialApplyCompletesTheRest(t *testing.T) {
	root := t.TempDir()
	// The real shape: one renamed PARENT, one subdirectory per book, so the two
	// books never contend for a path and the SAME prefix pair can be re-run.
	oldParent := filepath.Join(root, "old parent")
	newParent := filepath.Join(root, "new parent")
	makeTree(t, filepath.Join(newParent, "BookA"))
	makeTree(t, filepath.Join(newParent, "BookB"))

	store := seedRewrite(t, filepath.Join(oldParent, "BookA"))
	// b2 was already rewritten by the interrupted first run: it sits under the
	// NEW parent, with its file row moved too.
	b2Dir := filepath.Join(newParent, "BookB")
	store.books = append(store.books, database.BookCore{ID: "b2", FilePath: b2Dir})
	store.fullBooks["b2"] = &database.Book{ID: "b2", FilePath: b2Dir}
	store.fileCores = append(store.fileCores, database.BookFileCore{
		ID: "f2", BookID: "b2", FilePath: filepath.Join(b2Dir, "01.mp3")})
	store.fullFiles["f2"] = &database.BookFile{
		ID: "f2", BookID: "b2", FilePath: filepath.Join(b2Dir, "01.mp3")}
	store.atPath[b2Dir] = []string{"b2"}

	// The SAME old_prefix → new_prefix pair the interrupted run used.
	plan, err := planRewritePathPrefix(context.Background(), store, nil,
		rewriteParams(oldParent, newParent, false), &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, plan.MatchedBooks, "the already-rewritten book must not be re-selected")
	require.Equal(t, 1, plan.BooksRewritten)
	require.Len(t, store.bookWrites, 1)
	require.Equal(t, "b1", store.bookWrites[0].ID)
	require.Equal(t, filepath.Join(newParent, "BookA"), store.bookWrites[0].FilePath)
	require.Equal(t, b2Dir, store.fullBooks["b2"].FilePath,
		"the already-moved book must be left exactly as it was — it cannot collide with itself")
	for _, w := range store.fileWrites {
		require.Equal(t, "f1", w.ID, "only b1's file row should be written")
	}
}

// A book whose own FilePath does NOT match old_prefix but whose file rows do
// still gets a journal entry — under the distinct "prefix-rewrite-files" type,
// carrying the prefixes rather than a book path it never held.
func TestRewritePathPrefix_FileOnlyRewriteStillJournals(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, "old")
	newDir := filepath.Join(root, "new")
	makeTree(t, newDir)

	store := seedRewrite(t, oldDir)
	// The book row itself lives outside the prefix; only its file matches.
	outside := filepath.Join(root, "elsewhere")
	store.books[0].FilePath = outside
	store.books[0].SourceImportPath = nil
	store.fullBooks["b1"].FilePath = outside
	store.fullBooks["b1"].SourceImportPath = nil

	plan, err := planRewritePathPrefix(context.Background(), store, nil,
		rewriteParams(oldDir, newDir, false), &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, plan.FieldsRewritten)
	require.Empty(t, store.bookWrites)
	require.Len(t, store.fileWrites, 1)

	require.Len(t, store.ledger, 1, "a file-only rewrite must still be journalled")
	require.Equal(t, "prefix-rewrite-files", store.ledger[0].ChangeType)
	require.Equal(t, oldDir, store.ledger[0].OldPath)
	require.Equal(t, newDir, store.ledger[0].NewPath)
}

// Another writer moved the row between the scan and the write: the op SKIPS it
// rather than clobbering it with this run's stale idea of where it was.
func TestRewritePathPrefix_RowChangedUnderneathIsSkippedNotClobbered(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, "old")
	newDir := filepath.Join(root, "new")
	elsewhere := filepath.Join(root, "elsewhere")
	makeTree(t, newDir)
	store := seedRewrite(t, oldDir)
	// Between the plan's read and the write, the row moved out of old_prefix.
	store.modifyBookHook = func(b *database.Book) { b.FilePath = elsewhere }

	plan, err := planRewritePathPrefix(context.Background(), store, nil,
		rewriteParams(oldDir, newDir, false), &fakeReporter{})
	require.NoError(t, err)
	require.Positive(t, plan.SkippedChanged)
	require.Zero(t, plan.BooksRewritten, "the book row must not have been written")
	require.Empty(t, store.bookWrites)
	require.Equal(t, elsewhere, store.fullBooks["b1"].FilePath,
		"the concurrent writer's value must survive")
	require.Empty(t, store.ledger, "no ledger line for a write that did not happen")
}

// Params that cannot be meant are rejected before any store read.
func TestRewritePathPrefix_ParamValidation(t *testing.T) {
	for _, c := range []struct {
		name   string
		params rewritePathPrefixParams
	}{
		{"empty old", rewritePathPrefixParams{NewPrefix: "/a"}},
		{"empty new", rewritePathPrefixParams{OldPrefix: "/a"}},
		{"relative old", rewritePathPrefixParams{OldPrefix: "a", NewPrefix: "/b"}},
		{"relative new", rewritePathPrefixParams{OldPrefix: "/a", NewPrefix: "b"}},
		{"identical", rewritePathPrefixParams{OldPrefix: "/a", NewPrefix: "/a"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			require.Error(t, c.params.validate())
		})
	}
	require.NoError(t, rewritePathPrefixParams{OldPrefix: "/a", NewPrefix: "/b"}.validate())
}

// C1: the book row's own write FAILS. Its file rows must not move — that would
// be the half-written book the op's whole-or-nothing rule exists to prevent —
// and no path_history entry may be written, because bookOld/bookNew are set
// inside the callback (before the commit) and would otherwise journal a move
// the store rejected. A row heals on re-run; a false ledger line is permanent.
func TestRewritePathPrefix_BookWriteFailureLeavesFilesAndLedgerUntouched(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, "old")
	newDir := filepath.Join(root, "new")
	makeTree(t, newDir)
	store := seedRewrite(t, oldDir)
	store.modifyBookErr = errors.New("pebble: write failed")

	plan, err := planRewritePathPrefix(context.Background(), store, nil,
		rewriteParams(oldDir, newDir, false), &fakeReporter{})
	// C2: a run whose writes failed must NOT return success.
	require.Error(t, err)
	require.Contains(t, err.Error(), "FAILED")

	require.Empty(t, store.bookWrites)
	require.Empty(t, store.fileWrites, "the file rows must not move without their book row")
	require.Empty(t, store.ledger, "no path_history entry may assert a move that never committed")
	require.Zero(t, plan.BooksRewritten)
	require.Zero(t, plan.FieldsRewritten)
	require.Positive(t, plan.UpdateErrs)

	// The file rows are REFUSED into a bucket, not silently dropped.
	var sawFileRefusal bool
	for _, d := range plan.all {
		if d.Field == "book_file.file_path" && d.Bucket == "update-error" {
			sawFileRefusal = true
			require.Contains(t, d.Reason, "not attempted")
		}
	}
	require.True(t, sawFileRefusal, "the un-attempted file rows must land in a bucket")
}

// C3: an old_prefix that CONTAINS an iTunes root is refused. Testing only
// "is the prefix inside a root" passes this and would rewrite books/itunes/**.
func TestRewritePathPrefix_PrefixContainingITunesRootIsRefused(t *testing.T) {
	roots := []string{"/mnt/bigdata/books/itunes"}
	require.True(t, prefixTouchesITunes("/mnt/bigdata/books", roots),
		"a prefix that CONTAINS an iTunes root must be refused")
	require.True(t, prefixTouchesITunes("/mnt/bigdata/books/itunes/Sub", roots),
		"a prefix INSIDE an iTunes root must be refused")
	require.True(t, prefixTouchesITunes("/mnt/bigdata/books/itunes", roots))
	require.False(t, prefixTouchesITunes("/mnt/bigdata/newbooks", roots),
		"a sibling tree must still be allowed")
	require.False(t, prefixTouchesITunes("/mnt/bigdata/books2", roots),
		"the boundary rule must hold here too")
	require.False(t, prefixTouchesITunes("", roots))
}

// Nested prefixes are neither idempotent nor reversible by swapping, so they
// are rejected before any store read.
func TestRewritePathPrefix_NestedPrefixesRejected(t *testing.T) {
	require.Error(t, rewritePathPrefixParams{OldPrefix: "/books/A", NewPrefix: "/books/A/sub"}.validate())
	require.Error(t, rewritePathPrefixParams{OldPrefix: "/books/A/sub", NewPrefix: "/books/A"}.validate())
	require.NoError(t, rewritePathPrefixParams{OldPrefix: "/books/A", NewPrefix: "/books/B"}.validate())
	require.NoError(t, rewritePathPrefixParams{OldPrefix: "/books/A", NewPrefix: "/books/AB"}.validate(),
		"a sibling whose name merely starts the same is not nested")
}

// A book matched ONLY through source_import_path is rewritten, but reported in
// its own bucket and counted apart — nothing about it can be stat- or
// collision-checked, so the operator has to be able to see that population.
func TestRewritePathPrefix_SourceImportOnlyIsBucketedApart(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, "old")
	newDir := filepath.Join(root, "new") // deliberately NOT created on disk
	store := seedRewrite(t, oldDir)
	// Only source_import_path matches: the book and its file live elsewhere.
	outside := filepath.Join(root, "elsewhere")
	store.books[0].FilePath = outside
	store.fullBooks["b1"].FilePath = outside
	store.fileCores[0].FilePath = filepath.Join(outside, "01.mp3")
	store.fullFiles["f1"].FilePath = filepath.Join(outside, "01.mp3")

	plan, err := planRewritePathPrefix(context.Background(), store, nil,
		rewriteParams(oldDir, newDir, true), &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, plan.Rewritable)
	require.Equal(t, 1, plan.SourceImportOnly)
	require.Zero(t, plan.TargetMissing, "a provenance field is not stat-gated")

	var bucket string
	for _, d := range plan.all {
		if d.Field == "book.source_import_path" {
			bucket = d.Bucket
		}
	}
	require.Equal(t, "rewritable-source-import-only", bucket)
}

// Books above the cap land in their own bucket instead of vanishing from the
// report the operator sizes the next run against.
func TestRewritePathPrefix_CappedBooksAreReported(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, "old")
	newDir := filepath.Join(root, "new")
	makeTree(t, newDir)
	store := seedRewrite(t, oldDir)
	// A second book in a subdirectory of the same tree, so both are matched.
	b2Old := filepath.Join(oldDir, "sub")
	store.books = append(store.books, database.BookCore{ID: "b2", FilePath: b2Old})
	store.fullBooks["b2"] = &database.Book{ID: "b2", FilePath: b2Old}
	require.NoError(t, os.MkdirAll(filepath.Join(newDir, "sub"), 0o755))

	plan, err := planRewritePathPrefix(context.Background(), store, nil,
		rewritePathPrefixParams{OldPrefix: oldDir, NewPrefix: newDir, Max: 1}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, plan.CappedAt)
	var capped int
	for _, d := range plan.all {
		if d.Bucket == "capped" {
			capped++
		}
	}
	require.Positive(t, capped, "the truncated tail must still be reported")
}

// --- the completeness proof, against a REAL store ---
//
// The op never writes the book_atpath: index itself; it relies on ModifyBook's
// commit to do it. This test is what turns "I read updateBookLockedMode and it
// deletes the old key" into demonstrated behaviour: after an apply,
// LiveBookIDsAtPath must answer at the NEW path and no longer at the old one.
func TestRewritePathPrefix_ApplyAtPathIndexFollows(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, "old book")
	newDir := filepath.Join(root, "new book")
	makeTree(t, newDir)

	s, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	book, err := s.CreateBook(&database.Book{Title: "T", FilePath: oldDir})
	require.NoError(t, err)
	require.NoError(t, s.CreateBookFile(&database.BookFile{
		ID: "f1", BookID: book.ID, FilePath: filepath.Join(oldDir, "01.mp3")}))

	before, err := s.LiveBookIDsAtPath(oldDir)
	require.NoError(t, err)
	require.Equal(t, []string{book.ID}, before)

	plan, err := planRewritePathPrefix(context.Background(), s, nil,
		rewriteParams(oldDir, newDir, false), &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, plan.BooksRewritten)

	after, err := s.LiveBookIDsAtPath(newDir)
	require.NoError(t, err)
	require.Equal(t, []string{book.ID}, after, "the book_atpath: index must follow the row")

	gone, err := s.LiveBookIDsAtPath(oldDir)
	require.NoError(t, err)
	require.Empty(t, gone, "the OLD index key must be gone, or a later move reads a taken path as free")

	// The book_file row and its path index followed too.
	got, err := s.GetBookFileByPath(filepath.Join(newDir, "01.mp3"))
	require.NoError(t, err)
	require.NotNil(t, got, "book_file_path: secondary index must resolve the new path")
	require.Equal(t, "f1", got.ID)

	// And the ledger recorded it.
	hist, err := s.GetBookPathHistory(book.ID)
	require.NoError(t, err)
	var sawRewrite bool
	for _, h := range hist {
		if h.ChangeType == "prefix-rewrite" {
			sawRewrite = true
			require.Equal(t, oldDir, h.OldPath)
			require.Equal(t, newDir, h.NewPath)
		}
	}
	require.True(t, sawRewrite, "path_history must carry a prefix-rewrite entry")
}
