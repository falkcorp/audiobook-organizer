// file: internal/plugins/maintenance/kinds_t6_g5_resources.go
// version: 1.2.2
// guid: d2a4e8b7-6f13-4c95-b0e1-3a7c95d4f208
// last-edited: 2026-11-15

package maintenance

import (
	"reflect"

	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// t6g5WriterResources is what each method of t6g5Writer writes, as pkg/ops
// resources: this group's view of t6WriterResources (kinds_resources.go), the one
// method-to-resource table of every converted maintenance group. It is a view, not
// a copy: a row is the shared slice, so this group cannot disagree with the others
// about what ModifyBook or DeleteBook write. Three readers use it: the native
// tests' recording spy (which checks every run's writes against the kind's
// declared Effects), the test that checks each kind's Effects equal the union over
// the methods it can call, and F5-18's books writer, which must stamp each method
// with exactly these resources or a live run is refused with ErrOutsideEffects.
//
// A row is the resources the method writes through its whole call chain, not
// only the store method it names, and it is conservative where a callee is
// (ModifyBook and UpdateBook stamp metadata_cache because the store writes the
// stale mark whenever a call changes a book's identity, whoever calls). The
// chains are traced per method in
// docs/agent-tasks/platform-core/seams.d/F5-10.md. An empty list is a method
// that writes nothing itself: it runs a closure whose write arrives as its own
// method call, drops an in-memory cache, or only reads.
//
// The resource names are the pkg/ops constants. Vocabulary mappings the row
// accepts, because pkg/ops has no finer name: narrator, book-narrator and
// chapter rows are `books`; user preferences and user playlists are `user_state`;
// the ident: rows (the identification driver's derived state, the set
// RebuildIdentIndex rebuilds) are `indexes`, which is also the search and memdb
// index set, so every method that stages them declares it (CreateBook, ModifyBook,
// UpdateBook, DeleteBook, DeleteMetadataCache, MergeBooks, CloneBookIntoLibrary);
// the other derived secondary keys (path and version-group indexes) belong to the
// row that owns them. `operations` is the undo journal and
// nothing else: the OperationChange rows (opchange:), and the merge sibling-move
// journal (merge:sibling-journal:<id>), which is the undo payload of one merge.
// A record keyed on a book or a version group belongs to `books`: path_history:
// rows (a per-book path log), and the merge group-redirect keys. Sync ids and
// their redirects (the sync_item:, sync_file: and sync_alias_use families) are
// `sync_identity`, F5-06's constant, wherever a merge follow or a handover writes
// them; a method that also moves real listening state keeps `user_state` too.
// TestT6G5Resources_EveryNameIsAnOpsConstant checks the values.
var t6g5WriterResources = t6WriterView(nil, reflect.TypeFor[t6g5Writer]())

// t6g5OutsideWrite is one write a kind's core makes without going through the
// writer, and why it is not a writer method.
type t6g5OutsideWrite struct {
	Resource ops.Resource
	Why      string
}

// t6g5OutsideWriterWrites lists, per kind (keyed by HEAD ID), the resources its
// core writes OUTSIDE t6g5Writer. The kind still declares them: its Effects are
// the union of t6g5WriterResources over the methods it can call, this list, and
// t6g5HeadParityDeclared.
//
// Empty since F5-18g: the clone's mixed-cleanup, rollback and rmdir removals
// were its only entry, and they now go through t6g5Writer.RemoveClonePath.
var t6g5OutsideWriterWrites = map[string][]t6g5OutsideWrite{}

// t6g5HeadParityDeclared lists, per kind (keyed by HEAD ID), a resource the kind
// declares only because HEAD's own definition did, and which nothing on its path
// writes. It is not an outside write: it is a kept capability, named so a reader
// does not take it for a traced write.
var t6g5HeadParityDeclared = map[string][]t6g5OutsideWrite{
	"maintenance.merge-same-path-dupes": {
		{ops.ResBookFiles, "HEAD's own definition declares book_files for this op, and the native kind keeps that declared capability; the trace of MergeBooks found no book_file write on its path (only CombineBooks writes file rows), so no writer method carries it"},
	},
}
