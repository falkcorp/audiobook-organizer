// file: internal/database/single_owner_index_lock_test.go
// version: 1.2.0
// guid: b4e07d19-6a3c-4825-9f71-3d8c05e2a916
// last-edited: 2026-10-26

package database

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// distinctStripeID returns a fresh ULID whose stripe differs from every id in
// avoid, so a competing writer in these tests can never wait on a stripe the
// retire holds (a stripe collision would otherwise make the test hang or pass
// for the wrong reason roughly once in 256 runs).
func distinctStripeID(t *testing.T, avoid ...string) string {
	t.Helper()
	for range 10000 {
		id, err := newULID()
		require.NoError(t, err)
		ok := true
		for _, a := range avoid {
			if stripeFor(a) == stripeFor(id) {
				ok = false
				break
			}
		}
		if ok {
			return id
		}
	}
	t.Fatal("no id on a distinct stripe")
	return ""
}

// installSingleOwnerHooks wires the two test hooks for one test. afterVerify
// runs once, inside the retire's [verify, apply] window; contended is signalled
// when a writer finds the single-owner index mutex held.
func installSingleOwnerHooks(t *testing.T, afterVerify func(contended <-chan struct{})) {
	t.Helper()
	contended := make(chan struct{}, 8)
	prevContended, prevAfter := singleOwnerIndexContendedHook, singleOwnerIndexAfterVerifyHook
	t.Cleanup(func() {
		singleOwnerIndexContendedHook, singleOwnerIndexAfterVerifyHook = prevContended, prevAfter
	})
	singleOwnerIndexContendedHook = func() {
		select {
		case contended <- struct{}{}:
		default:
		}
	}
	var fired atomic.Int32
	singleOwnerIndexAfterVerifyHook = func() {
		if fired.Add(1) == 1 {
			afterVerify(contended)
		}
	}
}

// Every writer of a single-owner index key is held out of a retire's
// [re-check, apply] window. Without that, a live row's unconditional Set of the
// key could commit after verify passed and before the retire's batch was
// applied, and the batch would overwrite it with the retired row's reference.
// The window is held open by a hook; the writer runs in a goroutine and must
// reach the mutex (the contended hook) and not finish until the retire has
// committed.
func TestSingleOwnerIndexWriters_AreHeldOutOfARetiresVerifyCommitWindow(t *testing.T) {
	const hash = "hash-single-owner-window"
	const path = "/lib/ret/window/live.mp3"
	// The retire holds x's file stripe from before its verify until after its commit
	// (lockBookFiles(setKeys(retiring))), and a writer that takes a file stripe
	// (CreateBookFile and ClaimBookFilePathKey lock the row they write) would wait on
	// it instead of reaching the index mutex when its id hashes to the same stripe,
	// and the test then failed with "never reached" about 1 run in 256. So every live
	// row's id avoids x's stripe as well as the holder's and the shell's.
	writers := []struct {
		name  string
		setup func(t *testing.T, fx *retiredFixture, other *Book, x *BookFile) func() error
		// check runs after both the retire and the writer finished.
		check func(t *testing.T, fx *retiredFixture, other *Book)
	}{
		{
			name: "CreateBookFile takes the hash key",
			setup: func(t *testing.T, fx *retiredFixture, other *Book, x *BookFile) func() error {
				return func() error {
					return fx.ps.CreateBookFile(&BookFile{ID: distinctStripeID(t, fx.holder.ID, fx.shell.ID, other.ID, x.ID), BookID: other.ID, FilePath: path, FileHash: hash, Format: "mp3"})
				}
			},
			check: func(t *testing.T, fx *retiredFixture, other *Book) {
				owner := indexOwner(t, fx.ps, "book_file_hash:"+hash)
				require.True(t, strings.HasPrefix(owner, other.ID+":"),
					"the hash key names the LIVE row of %s, not the retired one the retire wrote: %q", other.ID, owner)
				row, err := fx.ps.getBookFileByID(other.ID, strings.TrimPrefix(owner, other.ID+":"))
				require.NoError(t, err)
				require.NotNil(t, row)
				require.False(t, row.Retired)
			},
		},
		{
			name: "ClaimBookFilePathKey sets the path key",
			setup: func(t *testing.T, fx *retiredFixture, other *Book, x *BookFile) func() error {
				live := &BookFile{ID: distinctStripeID(t, fx.holder.ID, fx.shell.ID, other.ID, x.ID), BookID: other.ID, FilePath: path, Format: "mp3"}
				require.NoError(t, fx.ps.CreateBookFile(live))
				return func() error {
					_, err := fx.ps.ClaimBookFilePathKey(other.ID, live.ID, path)
					return err
				}
			},
		},
		{
			name: "DeleteBookFile drops the row's index keys",
			setup: func(t *testing.T, fx *retiredFixture, other *Book, x *BookFile) func() error {
				live := &BookFile{ID: distinctStripeID(t, fx.holder.ID, fx.shell.ID, other.ID, x.ID), BookID: other.ID, FilePath: path, FileHash: "hash-del-one", Format: "mp3"}
				require.NoError(t, fx.ps.CreateBookFile(live))
				return func() error { return fx.ps.DeleteBookFile(live.ID) }
			},
		},
		{
			name: "DeleteBookFilesForBook drops the rows' index keys",
			setup: func(t *testing.T, fx *retiredFixture, other *Book, x *BookFile) func() error {
				live := &BookFile{ID: distinctStripeID(t, fx.holder.ID, fx.shell.ID, other.ID, x.ID), BookID: other.ID, FilePath: path, FileHash: "hash-del-book", Format: "mp3"}
				require.NoError(t, fx.ps.CreateBookFile(live))
				return func() error { return fx.ps.DeleteBookFilesForBook(other.ID) }
			},
		},
		{
			name: "DeleteBookFilesByIDs drops the rows' index keys",
			setup: func(t *testing.T, fx *retiredFixture, other *Book, x *BookFile) func() error {
				live := &BookFile{ID: distinctStripeID(t, fx.holder.ID, fx.shell.ID, other.ID, x.ID), BookID: other.ID, FilePath: path, FileHash: "hash-del-ids", Format: "mp3"}
				require.NoError(t, fx.ps.CreateBookFile(live))
				return func() error {
					_, err := fx.ps.DeleteBookFilesByIDs([]BookFileRef{{BookID: live.BookID, FileID: live.ID}})
					return err
				}
			},
		},
	}
	for _, w := range writers {
		t.Run(w.name, func(t *testing.T) {
			fx := newRetiredFixture(t)
			otherID := distinctStripeID(t, fx.holder.ID, fx.shell.ID)
			other, err := fx.ps.CreateBook(&Book{ID: otherID, Title: "Other", FilePath: "/lib/ret/window/other"})
			require.NoError(t, err)
			x := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/window/x.mp3", FileHash: hash, Format: "mp3"})
			run := w.setup(t, fx, other, x)

			done := make(chan error, 1)
			var reachedMutex, finishedInsideWindow atomic.Bool
			installSingleOwnerHooks(t, func(contended <-chan struct{}) {
				go func() { done <- run() }()
				select {
				case <-contended:
					reachedMutex.Store(true)
				case err := <-done:
					finishedInsideWindow.Store(true)
					done <- err
				case <-time.After(10 * time.Second):
				}
			})
			require.NoError(t, fx.retire(t, x))
			require.False(t, finishedInsideWindow.Load(), "the writer committed inside the retire's verify-to-apply window")
			require.True(t, reachedMutex.Load(), "the writer never reached the single-owner index mutex while the retire held it")

			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(10 * time.Second):
				t.Fatal("the writer never finished after the retire released the mutex")
			}
			if w.check != nil {
				w.check(t, fx, other)
			}
		})
	}
}

// singleOwnerIndexStagers is every function in this package that stages a write
// or delete of a single-owner index key (it calls writeBookFileSecondaryIndexes,
// writeBookFileSecondaryIndexesFor, deleteBookFileSecondaryIndexes,
// deleteSingleOwnerIndexIfOwned or singleOwnerKeysOf, or names one of the key
// families in a string literal), with how its batch reaches the disk. Each
// commit path takes lockSingleOwnerIndex (pebble_store_book_lock.go); a function
// not listed here is a new writer, and whoever adds it must route its commit
// through one of these paths (or take the mutex around its own Commit) and then
// list it, so the retire's re-check cannot be raced by it.
var singleOwnerIndexStagers = map[string]string{
	// -> commitBookFileBatch / commitBookFileBatchVerified (mutex inside)
	"createBookFileAttempt":          "commitBookFileBatch",
	"batchCreateBookFilesAttempt":    "commitBookFileBatch",
	"stagePIDTransfer":               "its caller's commitBookFileBatch",
	"updateBookFileOver":             "commitBookFileBatch",
	"batchUpsertBookFilesStaged":     "commitBookFileBatch",
	"moveBookFilesToBookBulkAttempt": "commitBookFileBatch",
	"retireBookFilesAttempt":         "commitBookFileBatchVerified",
	"unretireBookFilesAttempt":       "commitBookFileBatchVerified",
	"rehomeRetiredVersionAttempt":    "commitBookFileBatchVerified",
	// -> commitDeleteWithWindowCascade, which commits through commitBookFileBatch
	// (mutex inside): every book_file delete path (DeleteBookFile, DeleteBookFilesForBook,
	// DeleteBookFilesByIDs) reaches it through commitBookFileDeletes.
	"commitBookFileDeletes": "commitDeleteWithWindowCascade -> commitBookFileBatch",
	// -> commitBookFileSweepChunk, which commits each chunk through
	// commitBookFileBatch (mutex inside)
	"ClearAllAcoustIDFingerprints": "commitBookFileSweepChunk -> commitBookFileBatch",
	"SweepBookFileSegDrop":         "commitBookFileSweepChunk -> commitBookFileBatch",
	// -> its own lockSingleOwnerIndex
	// (the shared body of ClaimBookFilePathKey and ClaimRetiredBookFilePathKey)
	"claimPathKey": "lockSingleOwnerIndex around db.Set",
	// helpers that only stage into a caller's batch
	"deleteBookFileSecondaryIndexes":   "its caller's commit",
	"writeBookFileSecondaryIndexes":    "its caller's commit",
	"writeBookFileSecondaryIndexesFor": "its caller's commit",
}

// stagersOfSingleOwnerKeys parses the package's non-test sources and returns the
// name of every function that stages a write or delete of a single-owner index
// key: one that calls an index-staging helper, or that names a key family in a
// string literal and also calls Set or Delete.
func stagersOfSingleOwnerKeys(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	// go/build applies the default build constraints and leaves out _test.go
	// files, so the files read are the ones the compiler builds into the package
	// (parser.ParseDir, deprecated since Go 1.25, ignored build tags).
	bp, err := build.ImportDir(".", 0)
	require.NoError(t, err)
	var files []*ast.File
	for _, name := range bp.GoFiles {
		f, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err, "parse %s", name)
		files = append(files, f)
	}
	require.NotEmpty(t, files)
	helpers := map[string]bool{
		"writeBookFileSecondaryIndexes": true, "writeBookFileSecondaryIndexesFor": true,
		"deleteBookFileSecondaryIndexes": true, "deleteSingleOwnerIndexIfOwned": true,
		"singleOwnerKeysOf": true,
	}
	families := []string{"book_file_path:", "book_file_pid:", "book_file_hash:", "book_file_orig_hash:", "book_file_acoustid:"}
	var out []string
	for _, f := range files {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			var callsHelper, namesFamily, writes bool
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					name := ""
					switch fun := x.Fun.(type) {
					case *ast.Ident:
						name = fun.Name
					case *ast.SelectorExpr:
						name = fun.Sel.Name
					}
					if helpers[name] {
						callsHelper = true
					}
					if name == "Set" || name == "Delete" || name == "DeleteRange" {
						writes = true
					}
				case *ast.BasicLit:
					if x.Kind == token.STRING {
						for _, fam := range families {
							if strings.Contains(x.Value, fam) {
								namesFamily = true
							}
						}
					}
				}
				return true
			})
			if callsHelper || (namesFamily && writes) {
				out = append(out, fn.Name.Name)
			}
		}
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// Ratchet: the set of functions that stage a single-owner index key write is
// exactly singleOwnerIndexStagers. A new stager fails this test until it is
// listed, which is the moment to route its commit through a path that takes
// lockSingleOwnerIndex (or to take it around its own Commit).
func TestSingleOwnerIndexStagers_AreAllListed(t *testing.T) {
	got := stagersOfSingleOwnerKeys(t)
	var want []string
	for name := range singleOwnerIndexStagers {
		want = append(want, name)
	}
	sort.Strings(want)
	require.Equal(t, want, got, "a function that writes or deletes a single-owner index key is not listed in singleOwnerIndexStagers (or a listed one no longer does): route its commit through lockSingleOwnerIndex and list it")
}
