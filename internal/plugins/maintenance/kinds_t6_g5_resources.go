// file: internal/plugins/maintenance/kinds_t6_g5_resources.go
// version: 1.2.0
// guid: d2a4e8b7-6f13-4c95-b0e1-3a7c95d4f208
// last-edited: 2026-11-15

package maintenance

import "github.com/falkcorp/audiobook-organizer/pkg/ops"

// t6g5WriterResources is the single source of what each method of t6g5Writer
// writes, as pkg/ops resources. Three readers share it so they cannot drift:
// the native tests' recording spy (which checks every run's writes against the
// kind's declared Effects), the test that checks each kind's Effects equal the
// union over the methods it can call, and F5-18's books writer, which must stamp
// each method with exactly these resources or a live run is refused with
// ErrOutsideEffects.
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
// the store's derived secondary keys (the ident: rows, path and version-group
// indexes) belong to the row that owns them, so they are not `indexes`, which
// names the search and memdb index set. `operations` is the undo journal and
// nothing else: the OperationChange rows (opchange:), and the merge sibling-move
// journal (merge:sibling-journal:<id>), which is the undo payload of one merge.
// A record keyed on a book or a version group belongs to `books`: path_history:
// rows (a per-book path log), and the merge group-redirect keys.
// TestT6G5Resources_EveryNameIsAnOpsConstant checks the values.
var t6g5WriterResources = map[string][]ops.Resource{
	// The carry moves listening state (user_state) and, when the loser was
	// finished, stamps the play-count mark on the winner's book row (books).
	"CarryStateBeforeHardDelete": {ops.ResBooks, ops.ResUserState},
	"CarryStateBetweenLiveBooks": {ops.ResBooks, ops.ResUserState},
	"CarryStateThenHardDelete":   {ops.ResBooks, ops.ResUserState},
	"CloneBookIntoLibrary":       {ops.ResBooks, ops.ResBookFiles, ops.ResFiles, ops.ResAuthors, ops.ResUserState, ops.ResOperations},
	"CreateBook":                 {ops.ResBooks},
	"CreateNarrator":             {ops.ResBooks},
	"DeleteBook":                 {ops.ResBooks, ops.ResAuthors, ops.ResFieldStates, ops.ResEmbeddings, ops.ResMetadataCache, ops.ResUserState},
	"DeleteBookFilesByIDs":       {ops.ResBookFiles, ops.ResBooks, ops.ResFingerprintIndex},
	"DeleteMetadataCache":        {ops.ResMetadataCache},
	"DeleteNarrator":             {ops.ResBooks},
	"HardDeleteWithoutUserState": {},
	"ImportSmartPlaylists":       {ops.ResUserState},
	"InvalidateAuthorsCache":     {},
	"MergeBooks":                 {ops.ResBooks, ops.ResExternalIDs, ops.ResUserState, ops.ResOperations},
	"ModifyBook":                 {ops.ResBooks, ops.ResMetadataCache},
	"ModifyBookFile":             {ops.ResBookFiles, ops.ResBooks},
	"MoveBookFilesToBook":        {ops.ResBookFiles, ops.ResBooks},
	"MoveBookFilesToBookBulk":    {ops.ResBookFiles, ops.ResBooks},
	"NewUserStateProbe":          {},
	"Reflink":                    {ops.ResFiles},
	"ReassignExternalID":         {ops.ResExternalIDs},
	"RecomputeBookAggregates":    {ops.ResBooks},
	"RecordOperationChange":      {ops.ResOperations},
	"SaveChaptersForBook":        {ops.ResBooks},
	"SetBookAuthors":             {ops.ResAuthors},
	"SetBookNarrators":           {ops.ResBooks},
	"SetLibraryStateOrganized":   {ops.ResBooks},
	"SetUserPreferenceForUser":   {ops.ResUserState},
	"UpdateBook":                 {ops.ResBooks, ops.ResMetadataCache},
	"UpdateBookFiles":            {ops.ResBookFiles, ops.ResBooks},
}

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
var t6g5OutsideWriterWrites = map[string][]t6g5OutsideWrite{
	"itunes.clone-into-library": {
		{ops.ResFiles, "the mixed-cleanup, rollback and rmdir steps call os.Remove directly; F5-18 should route them through a writer method stamped files, or accept that they run after the last fenced write"},
	},
}

// t6g5HeadParityDeclared lists, per kind (keyed by HEAD ID), a resource the kind
// declares only because HEAD's own definition did, and which nothing on its path
// writes. It is not an outside write: it is a kept capability, named so a reader
// does not take it for a traced write.
var t6g5HeadParityDeclared = map[string][]t6g5OutsideWrite{
	"maintenance.merge-same-path-dupes": {
		{ops.ResBookFiles, "HEAD's own definition declares book_files for this op, and the native kind keeps that declared capability; the trace of MergeBooks found no book_file write on its path (only CombineBooks writes file rows), so no writer method carries it"},
	},
}
