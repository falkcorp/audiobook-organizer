// file: internal/plugins/maintenance/kinds_t6_g5_resources.go
// version: 1.0.0
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
// only the store method it names: the chains are traced per method in
// docs/agent-tasks/platform-core/seams.d/F5-10.md. An empty list is a method
// that writes nothing itself: it runs a closure whose write arrives as its own
// method call, drops an in-memory cache, or only reads.
//
// The resource names are the pkg/ops constants. Vocabulary mappings the row
// accepts, because pkg/ops has no finer name: narrator, book-narrator and
// chapter rows are `books`; user preferences and user playlists are `user_state`.
// TestT6G5Resources_EveryNameIsAnOpsConstant checks the values.
var t6g5WriterResources = map[string][]ops.Resource{
	"CarryStateBeforeHardDelete": {ops.ResUserState},
	"CarryStateBetweenLiveBooks": {ops.ResUserState},
	"CarryStateThenHardDelete":   {ops.ResUserState},
	"CloneBookIntoLibrary":       {ops.ResBooks, ops.ResBookFiles, ops.ResFiles, ops.ResAuthors, ops.ResUserState, ops.ResOperations},
	"CreateBook":                 {ops.ResBooks},
	"CreateNarrator":             {ops.ResBooks},
	"DeleteBook":                 {ops.ResBooks, ops.ResBookFiles},
	"DeleteBookFilesByIDs":       {ops.ResBookFiles},
	"DeleteMetadataCache":        {ops.ResMetadataCache},
	"DeleteNarrator":             {ops.ResBooks},
	"HardDeleteWithoutUserState": {},
	"ImportSmartPlaylists":       {ops.ResUserState},
	"InvalidateAuthorsCache":     {},
	"MergeBooks":                 {ops.ResBooks, ops.ResBookFiles, ops.ResExternalIDs, ops.ResUserState, ops.ResOperations},
	"ModifyBook":                 {ops.ResBooks},
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
	"SetUserPreferenceForUser":   {ops.ResUserState},
	"UpdateBook":                 {ops.ResBooks},
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
// the union of t6g5WriterResources over the methods it can call and this list.
var t6g5OutsideWriterWrites = map[string][]t6g5OutsideWrite{
	"itunes.clone-into-library": {
		{ops.ResFiles, "the mixed-cleanup, rollback and rmdir steps call os.Remove directly; F5-18 should route them through a writer method stamped files, or accept that they run after the last fenced write"},
	},
}
