// file: internal/server/books_t7_adapter.go
// version: 1.1.0
// guid: 7571e5c9-96d7-4d5e-b964-9cd8c8e6b193
// last-edited: 2026-11-28

package server

// The host adapter that builds t7Deps for the server-t7 kinds (F5-18,
// TODO-F5-18-A6-SERVER-T7): every kind t7KindList builds, forty-five of them. It
// is one adapter over *Server, as registerAccountLifecycle is for the account
// lifecycle: it reuses the head* adapters' methods on the server and keeps every
// unexported server type unexported.
//
// DARK. Nothing outside tests calls the door (BooksT7Kinds, books_t7_door.go),
// and a method refuses until a group fills it in: books_t7_library.go is
// group A and books_t7_catalog.go is group B, each holding the methods only its
// kinds reach. This file holds the types, the deps and the methods both groups'
// kinds reach, or no kind does; the twelve both groups reach are built here
// (booksT7SharedBuilt), and each group marks its own methods built from an init
// in its own file.
//
// Nothing here panics. A method with an error result returns an ErrKindNotBuilt
// that names it and the kinds that reach it. A method without one is
// unreachable by construction: the door builds a kind only when every method of
// its row in booksT7Reaches is built, so such a body never runs; it records the
// entry in booksT7StubEntries and returns zero values, which a cell asserts
// stays at zero through the door.
//
// Why every method is written out and none is promoted from an embedded
// interface: a nil embedded interface satisfies t7Store and t7Writer at
// compile time and then panics, unnamed, at the first call.

import (
	"context"
	"fmt"
	"sync"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance/resources"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// booksT7Adapter is the one host adapter: the server both views read. A method
// that needs a field of s which is unset refuses by name rather than
// dereferencing it (the fixture for that is &Server{}).
type booksT7Adapter struct{ s *Server }

// booksT7Store is the read side of the adapter, the t7Store of t7Deps.
type booksT7Store struct{ a *booksT7Adapter }

// booksT7Writer is the write side of the adapter for one run, the t7Writer a
// kind's core gets from t7NewWriter. w is the run's kernel-gated tasks.Writer,
// the one every write must go through (R10 item 4).
type booksT7Writer struct {
	a *booksT7Adapter
	w tasks.Writer
	// gate is w behind the books row table (booksT7WriterRows): every store write
	// of this view is one gate.Do stamped with the method's whole row, so a write
	// outside the kind's Effects is refused when it is attempted. A nil w (a
	// ReadOnly run, or the stub probe) refuses every gated write.
	gate *writer.Gate
}

var (
	_ t7Store  = (*booksT7Store)(nil)
	_ t7Writer = (*booksT7Writer)(nil)
)

// booksT7Deps is the t7Deps of the adapter over s. standDownKey is the
// concurrency key the library scan kinds run under, the same single source as
// Maintenance.StandDownKey in the registration inputs.
func (s *Server) booksT7Deps(standDownKey tasks.Key) t7Deps {
	a := &booksT7Adapter{s: s}
	return t7Deps{
		Store:        &booksT7Store{a: a},
		StandDownKey: standDownKey,
		NewWriter: func(w tasks.Writer) t7Writer {
			return &booksT7Writer{a: a, w: w, gate: writer.ForRun(w, booksT7WriterRows())}
		},
	}
}

// booksT7Need is the refusal of a method whose backing field on the server is
// unset, by name, so a nil backing never reaches a HEAD body. The door builds a
// kind only when its fields are set (booksT7MissingFields); this guards a
// method called on its own, as the stub probe does on a zero Server.
func (a *booksT7Adapter) need(method string, fields ...string) error {
	for _, f := range fields {
		if check := booksT7Present[f]; check == nil || !check(a.s) {
			return fmt.Errorf("books t7: %s needs Server.%s, which is not set", method, f)
		}
	}
	return nil
}

// The shared reads. Each reads the server's store exactly as HEAD's head*
// adapters did (headAIMergeApply, headDedupScan, headEntities,
// authorReclassifyPair, importRootsCachedBooks: ServerOpsStore, which is
// Server.store), and refuses by name when it is unset.

func (a *booksT7Store) GetAllAuthors() ([]database.Author, error) {
	if err := a.a.need("GetAllAuthors", "store"); err != nil {
		return nil, err
	}
	return a.a.s.store.GetAllAuthors()
}

func (a *booksT7Store) GetAllImportPaths() ([]database.ImportPath, error) {
	if err := a.a.need("GetAllImportPaths", "store"); err != nil {
		return nil, err
	}
	return a.a.s.store.GetAllImportPaths()
}

func (a *booksT7Store) GetAuthorAliases(_ int) ([]database.AuthorAlias, error) {
	return nil, booksT7NotBuilt("GetAuthorAliases")
}

func (a *booksT7Store) GetAuthorByID(id int) (*database.Author, error) {
	if err := a.a.need("GetAuthorByID", "store"); err != nil {
		return nil, err
	}
	return a.a.s.store.GetAuthorByID(id)
}

func (a *booksT7Store) GetAuthorByName(name string) (*database.Author, error) {
	if err := a.a.need("GetAuthorByName", "store"); err != nil {
		return nil, err
	}
	return a.a.s.store.GetAuthorByName(name)
}

func (a *booksT7Store) GetBookAuthors(bookID string) ([]database.BookAuthor, error) {
	if err := a.a.need("GetBookAuthors", "store"); err != nil {
		return nil, err
	}
	return a.a.s.store.GetBookAuthors(bookID)
}

func (a *booksT7Store) GetBookByID(id string) (*database.Book, error) {
	if err := a.a.need("GetBookByID", "store"); err != nil {
		return nil, err
	}
	return a.a.s.store.GetBookByID(id)
}

func (a *booksT7Store) GetBookTagsDetailed(_ string) ([]database.BookTag, error) {
	return nil, booksT7NotBuilt("GetBookTagsDetailed")
}

func (a *booksT7Store) GetBooksByAuthorIDWithRoleCore(authorID int) ([]database.BookCore, error) {
	if err := a.a.need("GetBooksByAuthorIDWithRoleCore", "store"); err != nil {
		return nil, err
	}
	return a.a.s.store.GetBooksByAuthorIDWithRoleCore(authorID)
}

// booksT7WriterRows is the row table of the writer's gated methods: the shared
// table's row for a store method (resources.Table), or, for an opaque HEAD
// pipeline that is one method of the writer, the union of its traced store
// writes. It is built once and only read. A method with no row is refused by
// the gate (fail closed), and TestBooksT7WriterRows_Resolve fails when a row
// below does not resolve.
var booksT7WriterRows = sync.OnceValue(func() writer.Rows {
	shared := writer.SharedRows()
	rows := writer.Rows{}
	for _, m := range booksT7SharedRowMethods {
		if row, ok := shared[m]; ok {
			rows[m] = row
		}
	}
	rows["WriteBackBooks"] = booksT7WriteBackBooksRow()
	return rows
})

// booksT7SharedRowMethods are the writer methods whose row is the shared table's
// row of the store method of the same name.
var booksT7SharedRowMethods = []string{"CreateAuthor"}

// booksT7WriteBackBooksStoreWrites are the store writes HEAD's runBulkWriteBack
// can make, each with where the static walk of the call map found it (the
// headBulkWriteBackWriter.WriteBackBooks entry of t7_call_map_head_targets.txt).
// The pipeline is metafetch's rename-only apply (a library copy it creates, the
// organize rename, the version-primary rule), the tag and cover write, and the
// activity batch. The v1 checkpoint (SaveOperationState, DeleteOperationState,
// metadata_ops.go in HEAD) is not here: its opstate: family is retired under the
// tasks flip, and the run's own checkpoint (env.Checkpoint) replaces it.
var booksT7WriteBackBooksStoreWrites = []string{
	"BatchCreateBookFiles",     // organizer/service.go:2586
	"ClearUserPositions",       // merge/sync_follow.go:639
	"CopyBookmarkIfAbsent",     // merge/bookmark_copy.go:97
	"CreateBook",               // organizer/service.go:2529
	"CreateOperationChange",    // books/store/change_type_class.go:208
	"DeleteBook",               // organizer/service.go:2219
	"DeleteRaw",                // merge/group_redirect.go:137
	"MergeBooks",               // dedup/merge_journaled.go:298
	"MintOrGetSyncID",          // merge/service.go:1498
	"ModifyBook",               // versionprimary/ensure.go:746
	"ReassignExternalIDs",      // merge/service.go:1430
	"RecordOperationChange",    // merge/undo_pointer.go:105
	"RecordPathChange",         // organizer/service.go:2544
	"RecordSyncMerge",          // merge/sync_follow.go:198
	"Reflink",                  // organizer/organizer.go:1354
	"RepointSyncFileToBook",    // merge/sync_follow.go:319
	"SetBookAuthors",           // organizer/service.go:2573
	"SetBookNarrators",         // metafetch/service_apply.go:554
	"SetUserBookState",         // merge/sync_follow.go:617
	"SetUserPosition",          // merge/user_state_merge.go:299
	"SetUserPositionAt",        // merge/user_state_merge.go:297
	"SetUserPreferenceForUser", // organizer/apply_failure.go:159
	"UpdateBook",               // organizer/service.go:2648
	"UpdateBookFile",           // organizer/inplace_multifile.go:263
}

// booksT7WriteBackBooksRow is the union of the shared table's rows of
// booksT7WriteBackBooksStoreWrites: every resource the pipeline can write.
func booksT7WriteBackBooksRow() []ops.Resource {
	shared := writer.SharedRows()
	var rows [][]ops.Resource
	for _, m := range booksT7WriteBackBooksStoreWrites {
		rows = append(rows, shared[m])
	}
	return resources.Union(rows...)
}

// CreateAuthor is one gated write of the authors row, as HEAD's
// authorReclassifyPair and headAIMergeApply made it (store.CreateAuthor).
func (a *booksT7Writer) CreateAuthor(name string) (*database.Author, error) {
	if err := a.a.need("CreateAuthor", "store"); err != nil {
		return nil, err
	}
	return writer.Value(context.Background(), a.gate, "CreateAuthor", writer.Ref(string(ops.ResAuthors), ""), func() (*database.Author, error) {
		return a.a.s.store.CreateAuthor(name)
	})
}

// EmitActivityInfo and FlushActivityOp are the activity log's writes, made as
// HEAD made them (headT7Activity over the server's activity writer, which
// drops the call when it is nil). They are not gated: the activity log is not
// one of the books resources a kind declares (the dedup, series and
// reconcile kinds that call them declare no activity resource), and
// internal/maintenance/resources has no row for them.
func (a *booksT7Writer) EmitActivityInfo(opID string, entryType string, source string, summary string, tags ...string) {
	headT7Activity{w: a.a.s.activityWriter}.EmitActivityInfo(opID, entryType, source, summary, tags...)
}

func (a *booksT7Writer) FlushActivityOp(opID string) {
	headT7Activity{w: a.a.s.activityWriter}.FlushActivityOp(opID)
}

// InvalidateDedupCache drops every cached duplicate list, as headAIMergeApply
// and headDedupOps do (dedupCache.InvalidateAll), not Server.InvalidateDedupCache,
// which drops the author list alone. It is an in-memory cache, not a store write,
// so it is not gated (the shared table has no row for it).
func (a *booksT7Writer) InvalidateDedupCache() {
	if a.a.s.dedupCache != nil {
		a.a.s.dedupCache.InvalidateAll()
	}
}

// WriteBackBooks is HEAD's runBulkWriteBack, one gated call stamped with the
// whole row of booksT7WriteBackBooksRow. The pipeline is one opaque store-wide
// call (metafetch's service over the server's store), so the fence admits it as
// one write, the way the maintenance group's pipeline writers do
// (deps_t6g2.go). The v1 checkpoint it used to write is gone from
// runBulkWriteBack (retired opstate: family).
func (a *booksT7Writer) WriteBackBooks(ctx context.Context, opID string, bookIDs []string, doRename bool, startIdx int, progress operations.ProgressReporter, onDone func(bookID string)) error {
	if err := a.a.need("WriteBackBooks", "store", "metadataFetchService"); err != nil {
		return err
	}
	return a.gate.Err(ctx, "WriteBackBooks", writer.Ref(string(ops.ResBooks), ""), func() error {
		return a.a.s.runBulkWriteBack(ctx, opID, bookIDs, doRename, startIdx, progress, onDone)
	})
}

// booksT7SharedBuilt is the methods this file builds: both groups' kinds reach
// them. Each group registers its own methods from its own file (an init there),
// so the two groups never edit one table literal.
var booksT7SharedBuilt = []string{
	"CreateAuthor", "EmitActivityInfo", "FlushActivityOp", "GetAllAuthors", "GetAllImportPaths",
	"GetAuthorByID", "GetAuthorByName", "GetBookAuthors", "GetBooksByAuthorIDWithRoleCore",
	"GetBookByID", "InvalidateDedupCache", "WriteBackBooks",
}

func init() {
	for _, m := range booksT7SharedBuilt {
		booksT7Built[m] = true
	}
}
