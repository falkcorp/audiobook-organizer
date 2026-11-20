// file: internal/plugins/maintenance/repair_merged_user_state.go
// version: 1.1.7
// guid: 8d4b2f67-1a9e-4c35-b7d0-9e6f3a2c1b58
// last-edited: 2026-11-15

// Moves user state stranded under merged-away books onto their survivors.
//
// WHY THIS EXISTS. When books are merged, every piece of a listener's state --
// position, finished flag, progress %, last played, the hide-from-continue-
// listening flag, bookmarks -- must end up on the surviving book (owner
// requirement 2026-09-26). Until then the merge follow was best-effort: a
// failure left the rows under the merged-away id, GET progress on the
// survivor answered 404, and nothing brought them back. The follow now leaves
// a durable pending-repair record on failure (internal/merge/pending_repair.go);
// this op completes those records and also finds rows stranded before the fix.
//
// maintenance.repair-merged-user-state is PREVIEW BY DEFAULT ({} writes
// nothing). It completes the pending records, scans every user's ubs/upos
// rows and bookmarks, resolves each dead book to its live survivor (sync
// redirect chain, then merged_into_book_id) and, with apply=true, moves the
// state under the merge conflict rule. There is deliberately no scheduled
// variant: a scheduled op always runs with {}, which must be a preview (owner
// rule 2026-09-25, testdata/write_op_modes.golden), so pending records are
// completed automatically by a server ticker (merge.PendingRepairLoop, started
// in server_lifecycle.go) and at the start of every merge of the same books.
//
// Trigger: POST /api/v1/operations/v2 with
//
//	{"def_id":"maintenance.repair-merged-user-state","params":{}}              preview
//	{"def_id":"maintenance.repair-merged-user-state","params":{"apply":true}}  move
package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/syncapi/progress"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"

	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

type repairMergedUserStateParams struct {
	// Apply must be explicitly true to move anything. Default false = preview.
	Apply bool `json:"apply"`
	// PendingOnly limits the run to the pending-repair records.
	PendingOnly bool `json:"pending_only,omitempty"`
}

// repairMergedUserStateHelp is the def's Description, which the native kind
// carries as its Help.
const repairMergedUserStateHelp = "Finds listening state (position, finished, progress, last played, hide flag, bookmarks) still stored under merged-away or deleted books whose surviving book is known, and moves it onto the survivor with the merge conflict rule (finished sticky, newest position wins, last played = max, hide kept). Also completes the pending-repair records a failed merge left. Preview by default; apply=true moves. pending_only=true limits it to those records."

func (p *Plugin) repairMergedUserStateDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              merge.UserStateRepairOpID,
		Preview:         sdk.PreviewSpec{Params: repairMergedUserStateParams{}},
		Liveness:        sdk.LivenessNone,
		ProgressTimeout: 30 * time.Minute,
		Plugin:          "maintenance",
		DisplayName:     "Repair user state stranded by merges",
		Description:     repairMergedUserStateHelp,
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  merge.UserStateRepairOpID,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         30 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runRepairMergedUserState,
	}
}

func (p *Plugin) runRepairMergedUserState(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	var params repairMergedUserStateParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}
	return p.repairMergedUserState(ctx, params, reporter)
}

func (p *Plugin) repairMergedUserState(ctx context.Context, params repairMergedUserStateParams, reporter sdk.Reporter) error {
	env := repairMergedUserStateEnv{t6g6Env: t6g6HeadEnv(reporter, params.Apply)}
	// A nil store leaves the env's halves nil (a typed-nil adapter would hide it),
	// so the core's "no store" check still fires.
	if store := p.deps.MergeUserStateStore(); store != nil {
		h := t6g6HeadUserState{UserStateRepairStore: store}
		env.Store, env.Writer = h, h
	}
	_, err := repairMergedUserStateCore(ctx, params, env)
	return err
}

// repairMergedUserStateKind is rule R3's kind for the def above.
const repairMergedUserStateKind = tasks.Kind("books." + merge.UserStateRepairOpID)

// The repair's store, split as the group's rule R10 wants it: reads through the
// env's Store, writes through its Writer. merge.RepairMergedUserState itself
// takes ONE merge.UserStateRepairStore and probes it for the sync-identity,
// bookmark and timestamp capabilities (database.AsSyncIdentityStore,
// AsBookmarkStore, AsBookmarkCopier and AsCapability[UserPositionTimestampWriter]
// are the only probes the pass reaches), so the core composes that object from the
// two halves (t6g6ComposeUserState) instead of handing it a captured store.

// t6g6SyncReads is the read half of database.SyncIdentityStore.
type t6g6SyncReads interface {
	GetSyncIDForBook(bookID string) (string, bool, error)
	ResolveSyncItem(syncID string) (*database.SyncItem, error)
	ListSyncAliases(syncID string) ([]string, error)
}

// t6g6SyncWrites is the write half of it the pass uses: the follow mints the
// survivor's id and records the merge redirect. RepointSyncItem is the
// version-handover path and is not reachable from the repair.
type t6g6SyncWrites interface {
	MintOrGetSyncID(bookID string) (string, error)
	RecordSyncMerge(loserBookID, winnerBookID string) error
}

// t6g6BookmarkReads is the read half of database.BookmarkStore.
type t6g6BookmarkReads interface {
	ListBookmarks(userID, itemID string) ([]progress.Bookmark, error)
	ListBookmarksForUser(userID string) ([]progress.Bookmark, error)
}

// t6g6BookmarkWrites is the one bookmark write the repair makes.
type t6g6BookmarkWrites interface {
	CopyBookmarkIfAbsent(b progress.Bookmark) (bool, error)
}

// repairMergedUserStateReads is every read the pass makes. The capability
// getters answer ok=false when the store has no such surface, so the merge
// package's "this store has no bookmarks" skip behaves as it did on the plugin's
// single store.
type repairMergedUserStateReads interface {
	ListUsers() ([]database.User, error)
	ScanPrefix(prefix string) ([]database.KVPair, error)
	ListUserPositionsSince(userID string, t time.Time) ([]database.UserPosition, error)
	GetBookByID(id string) (*database.Book, error)
	GetUserBookState(userID, bookID string) (*database.UserBookState, error)
	ListUserPositionsForBook(userID, bookID string) ([]database.UserPosition, error)
	UserStateSyncReads() (t6g6SyncReads, bool)
	UserStateBookmarkReads() (t6g6BookmarkReads, bool)
}

// repairMergedUserStateWrites is every write the pass makes: the moved state, the
// pending-record bookkeeping and the capability writes.
type repairMergedUserStateWrites interface {
	repairMergedUserStateRowWrites
	repairMergedUserStateCapWrites
}

// repairMergedUserStateRowWrites are the pass's own writes.
type repairMergedUserStateRowWrites interface {
	SetRaw(key string, value []byte) error
	DeleteRaw(key string) error
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
	SetUserBookState(state *database.UserBookState) error
	SetUserPosition(userID, bookID, segmentID string, positionSeconds float64) error
	ClearUserPositions(userID, bookID string) error
}

// repairMergedUserStateCapWrites are the capability writes the merge package
// probes the store for.
type repairMergedUserStateCapWrites interface {
	UserStateSyncWrites() (t6g6SyncWrites, bool)
	UserStateBookmarkWrites() (t6g6BookmarkWrites, bool)
	UserStateTimestampWriter() (database.UserPositionTimestampWriter, bool)
}

// t6g6UserStateBase is the merge.UserStateRepairStore of the composed pair.
type t6g6UserStateBase struct {
	r repairMergedUserStateReads
	w repairMergedUserStateWrites
}

func (b t6g6UserStateBase) ListUsers() ([]database.User, error) { return b.r.ListUsers() }
func (b t6g6UserStateBase) ScanPrefix(prefix string) ([]database.KVPair, error) {
	return b.r.ScanPrefix(prefix)
}
func (b t6g6UserStateBase) ListUserPositionsSince(userID string, t time.Time) ([]database.UserPosition, error) {
	return b.r.ListUserPositionsSince(userID, t)
}
func (b t6g6UserStateBase) GetBookByID(id string) (*database.Book, error) { return b.r.GetBookByID(id) }
func (b t6g6UserStateBase) GetUserBookState(userID, bookID string) (*database.UserBookState, error) {
	return b.r.GetUserBookState(userID, bookID)
}
func (b t6g6UserStateBase) ListUserPositionsForBook(userID, bookID string) ([]database.UserPosition, error) {
	return b.r.ListUserPositionsForBook(userID, bookID)
}
func (b t6g6UserStateBase) SetRaw(key string, value []byte) error { return b.w.SetRaw(key, value) }
func (b t6g6UserStateBase) DeleteRaw(key string) error            { return b.w.DeleteRaw(key) }
func (b t6g6UserStateBase) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	return b.w.ModifyBook(id, fn)
}
func (b t6g6UserStateBase) SetUserBookState(state *database.UserBookState) error {
	return b.w.SetUserBookState(state)
}
func (b t6g6UserStateBase) SetUserPosition(userID, bookID, segmentID string, positionSeconds float64) error {
	return b.w.SetUserPosition(userID, bookID, segmentID, positionSeconds)
}
func (b t6g6UserStateBase) ClearUserPositions(userID, bookID string) error {
	return b.w.ClearUserPositions(userID, bookID)
}

// t6g6ComposedSync is the database.SyncIdentityStore the pass sees. The
// version-handover write is outside the repair and refuses.
type t6g6ComposedSync struct {
	t6g6SyncReads
	t6g6SyncWrites
}

func (t6g6ComposedSync) RepointSyncItem(string, string) error {
	return errors.New("repair-merged-user-state: RepointSyncItem is not part of the repair")
}

// t6g6ComposedBookmarks is the database.BookmarkStore (plus BookmarkCopier) the
// pass sees; only the reads and the copy are part of the repair.
type t6g6ComposedBookmarks struct {
	t6g6BookmarkReads
	t6g6BookmarkWrites
}

func (t6g6ComposedBookmarks) CreateBookmark(progress.Bookmark) error {
	return errors.New("repair-merged-user-state: CreateBookmark is not part of the repair")
}

func (t6g6ComposedBookmarks) UpdateBookmarkTitle(string, string, float64, string) error {
	return errors.New("repair-merged-user-state: UpdateBookmarkTitle is not part of the repair")
}

func (t6g6ComposedBookmarks) DeleteBookmark(string, string, float64) error {
	return errors.New("repair-merged-user-state: DeleteBookmark is not part of the repair")
}

// The composed store's capability layers. A layer embeds the CONCRETE type of
// the one below it, not the merge.UserStateRepairStore interface: embedding the
// interface would promote only the interface's own methods and hide the
// capabilities of every layer already stacked (the probes are type assertions on
// the whole object).
type (
	t6g6UsSync struct {
		merge.UserStateRepairStore
		database.SyncIdentityStore
	}
	t6g6UsBook struct {
		merge.UserStateRepairStore
		database.BookmarkStore
		database.BookmarkCopier
	}
	t6g6UsStamp struct {
		merge.UserStateRepairStore
		database.UserPositionTimestampWriter
	}
	t6g6UsSyncBook struct {
		t6g6UsSync
		database.BookmarkStore
		database.BookmarkCopier
	}
	t6g6UsSyncStamp struct {
		t6g6UsSync
		database.UserPositionTimestampWriter
	}
	t6g6UsBookStamp struct {
		t6g6UsBook
		database.UserPositionTimestampWriter
	}
	t6g6UsAll struct {
		t6g6UsSyncBook
		database.UserPositionTimestampWriter
	}
)

// t6g6ComposeUserState builds the merge.UserStateRepairStore the pass runs on
// from the env's two halves. A capability is present on the result only when the
// halves both carry it, which is what the merge package's interface probes look
// for; one either half lacks is left out, as on a store without it.
func t6g6ComposeUserState(r repairMergedUserStateReads, w repairMergedUserStateWrites) merge.UserStateRepairStore {
	base := merge.UserStateRepairStore(t6g6UserStateBase{r: r, w: w})
	var sync database.SyncIdentityStore
	if sr, ok := r.UserStateSyncReads(); ok {
		if sw, ok := w.UserStateSyncWrites(); ok {
			sync = t6g6ComposedSync{sr, sw}
		}
	}
	var bm *t6g6ComposedBookmarks
	if br, ok := r.UserStateBookmarkReads(); ok {
		if bw, ok := w.UserStateBookmarkWrites(); ok {
			bm = &t6g6ComposedBookmarks{br, bw}
		}
	}
	tw, hasStamp := w.UserStateTimestampWriter()
	switch {
	case sync != nil && bm != nil && hasStamp:
		return t6g6UsAll{t6g6UsSyncBook{t6g6UsSync{base, sync}, bm, bm}, tw}
	case sync != nil && bm != nil:
		return t6g6UsSyncBook{t6g6UsSync{base, sync}, bm, bm}
	case sync != nil && hasStamp:
		return t6g6UsSyncStamp{t6g6UsSync{base, sync}, tw}
	case bm != nil && hasStamp:
		return t6g6UsBookStamp{t6g6UsBook{base, bm, bm}, tw}
	case sync != nil:
		return t6g6UsSync{base, sync}
	case bm != nil:
		return t6g6UsBook{base, bm, bm}
	case hasStamp:
		return t6g6UsStamp{base, tw}
	}
	return base
}

// t6g6HeadUserState is HEAD's store for the pass: the plugin's one
// merge.UserStateRepairStore, which is both halves. Its capability getters are
// the merge package's own probes, which look through a store decorator.
type t6g6HeadUserState struct{ merge.UserStateRepairStore }

func (h t6g6HeadUserState) UserStateSyncReads() (t6g6SyncReads, bool) {
	s := database.AsSyncIdentityStore(h.UserStateRepairStore)
	return s, s != nil
}

func (h t6g6HeadUserState) UserStateBookmarkReads() (t6g6BookmarkReads, bool) {
	s := database.AsBookmarkStore(h.UserStateRepairStore)
	return s, s != nil
}

func (h t6g6HeadUserState) UserStateSyncWrites() (t6g6SyncWrites, bool) {
	s := database.AsSyncIdentityStore(h.UserStateRepairStore)
	return s, s != nil
}

func (h t6g6HeadUserState) UserStateBookmarkWrites() (t6g6BookmarkWrites, bool) {
	s := database.AsBookmarkCopier(h.UserStateRepairStore)
	return s, s != nil
}

func (h t6g6HeadUserState) UserStateTimestampWriter() (database.UserPositionTimestampWriter, bool) {
	return database.AsCapability[database.UserPositionTimestampWriter](h.UserStateRepairStore)
}

// repairMergedUserStateEnv is what the core takes from its shell.
type repairMergedUserStateEnv struct {
	t6g6Env
	Store  repairMergedUserStateReads
	Writer repairMergedUserStateWrites
}

// repairMergedUserStateNative is the native kind. Preview is the report-only pass
// HEAD's apply=false was; Live moves the state.
func (p *Plugin) repairMergedUserStateNative(deps t6g6Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := t6g6Common(repairMergedUserStateKind, sched)
	c.Title = "Repair user state stranded by merges"
	c.Help = repairMergedUserStateHelp
	// HEAD declared no Writes. The pass moves listening state (position rows,
	// progress rows, bookmarks) and clears the pending-repair records (user_state)
	// and carries the play-count marks onto the survivor with ModifyBook (books).
	// MintOrGetSyncID and RecordSyncMerge (t6g6SyncWrites) write the sync-item keys
	// (sync_identity), which are not user state. ModifyBook also stamps the metadata
	// cache stale on an identity change and stages the ident: rows (metadata_cache
	// and indexes: the shared row of ModifyBook, kinds_resources.go).
	c.Effects = tasks.Writes(tasks.Res("books", string(ops.ResBooks)), tasks.Res("books", string(ops.ResUserState)),
		tasks.Res("books", string(ops.ResSyncIdentity)), tasks.Res("books", string(ops.ResMetadataCache)),
		tasks.Res("books", string(ops.ResIndexes)))
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = tasks.Key("books." + merge.UserStateRepairOpID)
	c.Timeout = 30 * time.Minute
	c.ProgressTimeout = 30 * time.Minute
	return tasks.Task(repairMergedUserStateKind, tasks.TaskSpec[json.RawMessage, merge.UserStateRepairReport]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (merge.UserStateRepairReport, error) {
			in, err := t6g6DecodeNative[repairMergedUserStateNativeParams](repairMergedUserStateKind, raw)
			if err != nil {
				return merge.UserStateRepairReport{}, err
			}
			if err := t6g6NeedDep(repairMergedUserStateKind, "Store", deps.Store != nil); err != nil {
				return merge.UserStateRepairReport{}, err
			}
			params := repairMergedUserStateParams{Apply: rc.Mode() == tasks.ModeLive, PendingOnly: in.PendingOnly}
			env := repairMergedUserStateEnv{t6g6Env: t6g6NativeEnv(rc), Store: deps.Store}
			// Built in Preview too: a report-only pass writes nothing through it
			// (the kernel records the intent), and the composed store needs both
			// halves.
			w, err := t6g6NewWriter(repairMergedUserStateKind, deps, rc)
			if err != nil {
				return merge.UserStateRepairReport{}, err
			}
			env.Writer = w
			rep, err := repairMergedUserStateCore(rc.Context(), params, env)
			if rep == nil {
				return merge.UserStateRepairReport{}, err
			}
			return *rep, err
		},
	})
}

// repairMergedUserStateNativeParams are the native run's params: HEAD's without
// Apply, which the run's mode decides.
type repairMergedUserStateNativeParams struct {
	PendingOnly bool `json:"pending_only,omitempty"`
}

// repairMergedUserStateCore is the body of repairMergedUserState. It returns the
// report so a native shell can hand it back as the run's result.
func repairMergedUserStateCore(ctx context.Context, params repairMergedUserStateParams, env repairMergedUserStateEnv) (*merge.UserStateRepairReport, error) {
	if env.Store == nil || env.Writer == nil {
		return nil, fmt.Errorf("repair-merged-user-state: no store")
	}
	rep, err := merge.RepairMergedUserState(ctx, t6g6ComposeUserState(env.Store, env.Writer),
		merge.UserStateRepairOptions{Apply: params.Apply, PendingOnly: params.PendingOnly})
	if err != nil {
		return nil, fmt.Errorf("repair-merged-user-state: %w", err)
	}
	summary := fmt.Sprintf("Merged user state (apply=%t pending_only=%t): pending %d (completed %d), stranded books %d, state rows %d, position rows %d, moved %d, bookmarks owed %d (copied %d), unresolved books %d, errors %d",
		rep.Apply, rep.PendingOnly, rep.PendingRecords, rep.PendingCompleted, rep.StrandedBooks, rep.StateRows,
		rep.PositionRows, rep.StateMoved, rep.BookmarksOwed, rep.BookmarksCopied, rep.UnresolvedBooks, rep.Errors)
	env.Log(slog.LevelInfo, summary)
	env.Progress(1, 1, summary)
	if env.SetResult != nil {
		if err := env.SetResult(rep); err != nil {
			env.Log(slog.LevelWarn, fmt.Sprintf("could not persist result data: %v", err))
		}
	}
	if rep.Errors > 0 {
		return rep, fmt.Errorf("repair-merged-user-state: %d item(s) failed; see result items", rep.Errors)
	}
	return rep, nil
}
