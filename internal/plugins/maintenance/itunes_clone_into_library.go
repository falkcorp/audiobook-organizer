// file: internal/plugins/maintenance/itunes_clone_into_library.go
// version: 1.18.0
// guid: 9c4e1b27-6a3f-4d80-b5e2-3f7a0c8d1e64
// last-edited: 2026-11-13

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/tasks"
)

// --- itunes-clone-into-library ---
//
// WHY THIS EXISTS. After the 2026-09-24 version-group repair, 552 held groups
// had a live `organized` member whose files are all (513) or partly (55) in
// the iTunes library under books/itunes/**, outside the library root. ABS
// lists only a copy under the root, so those books are invisible. The owner
// chose (2026-09-24) to CLONE them into the library rather than move them:
// iTunes files are never mutated.
//
//   - kind "version" (every active file under iTunes): reflink the files into
//     the organize destination and create the library copy as a new version
//     row through the organize service (CreateOrganizedVersion: the iTunes PID
//     moves to the new rows, the source becomes organized_source, the group's
//     primary is handed on). Owner: the library copy owns the PID.
//   - kind "mixed" (some files already in the library folder): reflink only
//     the iTunes-side files into that same folder and repoint those rows.
//     Any conflict (the planned folder is not the book's library folder, a
//     destination exists) and the group is reported, never written.
//
// REFLINK ONLY. No copy fallback (copy_file_range full-copies silently across
// datasets), no hardlink (it would share the iTunes file's inode).
//
// ROLLBACK. Each clone writes a durable record (a `_system` preference keyed
// by group) of exactly what it changed: the new book, each PID's old and new
// row, each repointed row's old path, the source's prior state. mode rollback
// with group_ids reverses from the record, never by inference.
//
// ALREADY IN LIBRARY. A group is only one view of a book: the same book is
// often organized in the library under ANOTHER group (a separate import that
// was never linked). Cloning then adds a duplicate. The 2026-09-24 canary did
// that for about 11 of 20. So before planning a clone the op looks for a book
// outside the group that is organized with a live file under the root and is
// the same book by ASIN, or by author plus title (icTitleForms); a hit skips
// the group as already_in_library and names those books for a merge. A copy
// filed under "Unknown Author" or no author at all matches on title alone and
// skips the group as possibly_in_library, a separate reason because a title
// with no author behind it is weaker evidence (owner, 2026-09-24).
//
// Guards: dry run by default; apply and rollback need explicit group_ids;
// refuses while library.scan runs and holds the scan stand-down; Doctor Who /
// Big Finish / Torchwood are never touched (applygate.IsOwnerManualOnly);
// each group is re-read before writing and skipped if it changed.
//
// CONCURRENCY: registry.RunItems, Concurrency 4, one item per group. Groups
// are disjoint sets of books, so no two workers touch the same row. The work
// is I/O on one pool; four is the same bound the repair op uses.

const (
	icWorkers      = 4
	icRecordPrefix = "itunes_clone_record:"
	icSource       = "itunes-clone-into-library"

	icKindVersion = "version"
	icKindMixed   = "mixed"

	icDecisionClone  = "clone"
	icDecisionSkip   = "skip"
	icOutcomeApplied = "applied"
	icOutcomeChanged = "changed_since_plan"
	icOutcomeFailed  = "failed"
	icOutcomeNoUndo  = "applied_without_rollback_record"
	icOutcomeRolled  = "rolled_back"
	icOutcomeFixed   = "repaired"
)

type icParams struct {
	Apply    bool `json:"apply"`
	Rollback bool `json:"rollback"`
	// Repair re-runs, on groups already cloned (they have a record), the two
	// steps the first apply lacked before 2026-09-24 21:00: copy the source's
	// chapter table onto a version clone that has none, then the primary
	// hand-off. It fills the record's PriorPrimaries (as they stand before
	// the hand-off) when missing, so rollback can still restore them.
	Repair   bool     `json:"repair"`
	GroupIDs []string `json:"group_ids"`
}

// icPair is one file of a clone: the source row, the row that now holds its
// place, the PID that moved (if any), and the two paths.
type icPair struct {
	SourceFileID string `json:"source_file_id"`
	CloneFileID  string `json:"clone_file_id,omitempty"`
	PID          string `json:"pid,omitempty"`
	SourcePath   string `json:"source_path"`
	ClonePath    string `json:"clone_path"`
	// OldITunesPath is the mixed repoint's prior ITunesPath.
	OldITunesPath string `json:"old_itunes_path,omitempty"`
}

// icRecord is the durable undo record of one group's clone.
type icRecord struct {
	GroupID            string `json:"group_id"`
	Kind               string `json:"kind"`
	SourceBookID       string `json:"source_book_id"`
	CloneBookID        string `json:"clone_book_id,omitempty"`
	SourcePriorState   string `json:"source_prior_state"`
	SourcePriorPrimary string `json:"source_prior_primary"`
	// PriorPrimaries is every planned member's stored primary flag ("true",
	// "false", "nil") before the clone. The post-clone hand-off can crown the
	// clone over an ineligible incumbent (files missing), demoting it; undo
	// puts each flag back. Records written before 2026-09-24 21:00 have none.
	PriorPrimaries map[string]string `json:"prior_primaries,omitempty"`
	Pairs          []icPair          `json:"pairs"`
	OpID           string            `json:"op_id"`
	At             time.Time         `json:"at"`
}

type icGroupReport struct {
	GroupID      string   `json:"version_group_id"`
	Decision     string   `json:"decision"`
	Kind         string   `json:"kind,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	SourceBookID string   `json:"source_book_id,omitempty"`
	Title        string   `json:"title,omitempty"`
	Files        int      `json:"files,omitempty"`
	Bytes        int64    `json:"bytes,omitempty"`
	Destinations []string `json:"destinations,omitempty"`
	CloneBookID  string   `json:"clone_book_id,omitempty"`
	// LibraryCopies are the books outside the group that already hold this
	// book in the library (reason already_in_library).
	LibraryCopies []string `json:"library_copies,omitempty"`
	Outcome       string   `json:"outcome,omitempty"`
	Error         string   `json:"error,omitempty"`
}

type icReport struct {
	DryRun     bool            `json:"dry_run"`
	Rollback   bool            `json:"rollback"`
	Candidates int             `json:"candidates"`
	ByDecision map[string]int  `json:"by_decision"`
	ByKind     map[string]int  `json:"by_kind"`
	BySkip     map[string]int  `json:"by_skip_reason"`
	ByOutcome  map[string]int  `json:"by_outcome"`
	Bytes      int64           `json:"bytes_to_clone"`
	Aborted    string          `json:"aborted,omitempty"`
	Groups     []icGroupReport `json:"groups"`
}

func (r *icReport) summary() string {
	s := fmt.Sprintf("dry_run=%v rollback=%v candidates=%d decisions=%v kinds=%v skips=%v outcomes=%v bytes=%d",
		r.DryRun, r.Rollback, r.Candidates, r.ByDecision, r.ByKind, r.BySkip, r.ByOutcome, r.Bytes)
	if r.Aborted != "" {
		s += " ABORTED: " + r.Aborted
	}
	return s
}

func (p *Plugin) itunesCloneIntoLibraryDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID: "itunes.clone-into-library",
		// apply, rollback and repair are three kinds of write and Run refuses
		// more than one; with none it is a dry run. A preview writes all three
		// false; a live run must name exactly one itself.
		Preview: sdk.PreviewSpec{Params: icParams{}, LiveOneOf: []string{"apply", "rollback", "repair"}},
		// Renamed 2026-09-25 (naming audit class 8): every iTunes op lives in itunes.*, whichever Go package implements it.
		FormerIDs:   []string{"maintenance.itunes-clone-into-library"},
		Liveness:    sdk.LivenessManual,
		Plugin:      "maintenance",
		DisplayName: "Clone iTunes-only books into the library",
		Description: "For held version groups whose only organized copy lives in the iTunes library, reflinks the " +
			"files into the library root (reflink only: never a copy or hardlink; iTunes files are never " +
			"modified) and makes the library copy primary. Books with some files already in the library get " +
			"only the iTunes-side files cloned into that folder. DRY-RUN BY DEFAULT: apply=true needs group_ids " +
			"and refuses while library.scan runs; rollback=true with group_ids reverses a clone from its record; " +
			"repair=true with group_ids copies a missing chapter table onto an existing clone and re-runs the " +
			"primary hand-off.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "itunes.clone-into-library",
		Cancellable:     true,
		Timeout:         4 * time.Hour,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runITunesCloneIntoLibrary,
	}
}

func (p *Plugin) runITunesCloneIntoLibrary(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params icParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}
	report, err := p.itunesCloneIntoLibrary(ctx, params, config.AppConfig.RootDir, reporter)
	if report != nil {
		if serr := registry.ReporterSetResult(reporter, report); serr != nil {
			reporter.Logger().Warn("itunes-clone-into-library: report not persisted", "err", serr)
		}
	}
	return err
}

// icStore is what the op reads and writes: the book/file/preference calls
// (OpsStore field, a narrow view since F5-10) plus the chapter table
// versionprimary's loader and hand-off need. The forwarding methods below are
// the whole surface the runner, versionprimary and applygate reach it by.
type icStore struct {
	OpsStore icOpsView
	database.ChapterReader
}

// icReader is the reads the op makes.
type icReader interface {
	icBookReader
	database.BookAuthorReader
	GetSeriesByID(id int) (*database.Series, error)
}

// icBookReader is the book, file, group and preference reads.
type icBookReader interface {
	GetBookByID(id string) (*database.Book, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetBooksByVersionGroup(groupID string) ([]database.Book, error)
	GetAllPreferencesForUser(userID string) ([]database.UserPreferenceKV, error)
}

// icWriter is the writes the op makes through the store.
type icWriter interface {
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
	ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error)
	DeleteBook(id string) error
	DeleteBookFilesByIDs(refs []database.BookFileRef) ([]database.BookFile, error)
	SetBookAuthors(bookID string, authors []database.BookAuthor) error
	SetUserPreferenceForUser(userID, key, value string) error
}

// icOpsView is icReader and icWriter together: the one view the runner holds.
type icOpsView interface {
	icReader
	icWriter
}

func (s icStore) GetBookByID(id string) (*database.Book, error) { return s.OpsStore.GetBookByID(id) }
func (s icStore) GetBookFiles(bookID string) ([]database.BookFile, error) {
	return s.OpsStore.GetBookFiles(bookID)
}
func (s icStore) GetBooksByVersionGroup(groupID string) ([]database.Book, error) {
	return s.OpsStore.GetBooksByVersionGroup(groupID)
}
func (s icStore) GetAllPreferencesForUser(userID string) ([]database.UserPreferenceKV, error) {
	return s.OpsStore.GetAllPreferencesForUser(userID)
}
func (s icStore) GetBookAuthors(bookID string) ([]database.BookAuthor, error) {
	return s.OpsStore.GetBookAuthors(bookID)
}
func (s icStore) GetAuthorByID(id int) (*database.Author, error) { return s.OpsStore.GetAuthorByID(id) }
func (s icStore) GetSeriesByID(id int) (*database.Series, error) { return s.OpsStore.GetSeriesByID(id) }
func (s icStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	return s.OpsStore.ModifyBook(id, fn)
}
func (s icStore) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	return s.OpsStore.ModifyBookFile(bookID, fileID, fn)
}
func (s icStore) DeleteBook(id string) error { return s.OpsStore.DeleteBook(id) }
func (s icStore) DeleteBookFilesByIDs(refs []database.BookFileRef) ([]database.BookFile, error) {
	return s.OpsStore.DeleteBookFilesByIDs(refs)
}
func (s icStore) SetBookAuthors(bookID string, authors []database.BookAuthor) error {
	return s.OpsStore.SetBookAuthors(bookID, authors)
}
func (s icStore) SetUserPreferenceForUser(userID, key, value string) error {
	return s.OpsStore.SetUserPreferenceForUser(userID, key, value)
}

// icUserState is the three user-state moves undo makes. The default (HEAD)
// implementation runs the merge package's functions over a
// merge.UserProgressMerger; the native engine's is its writer.
type icUserState interface {
	// CarryStateBeforeHardDelete moves every user's state from doomedID onto
	// keepID, all or nothing (merge.CarryStateBeforeHardDelete).
	CarryStateBeforeHardDelete(keepID, doomedID string) error
	// HardDeleteWithoutUserState runs del under the merge lock, refusing when
	// the book has acquired user state (merge.HardDeleteWithoutUserState).
	HardDeleteWithoutUserState(bookID string, del func() error) error
	// CarryStateBetweenLiveBooks moves state between two live books
	// (merge.CarryStateBetweenLiveBooks).
	CarryStateBetweenLiveBooks(toID, fromID string) error
}

// icMergeUserState is icUserState over a merge.UserProgressMerger.
type icMergeUserState struct{ um merge.UserProgressMerger }

func (m icMergeUserState) CarryStateBeforeHardDelete(keepID, doomedID string) error {
	return merge.CarryStateBeforeHardDelete(m.um, keepID, doomedID)
}
func (m icMergeUserState) HardDeleteWithoutUserState(bookID string, del func() error) error {
	return merge.HardDeleteWithoutUserState(m.um, bookID, del)
}
func (m icMergeUserState) CarryStateBetweenLiveBooks(toID, fromID string) error {
	return merge.CarryStateBetweenLiveBooks(m.um, toID, fromID)
}

type icRunner struct {
	store  icStore
	cloner LibraryCloner
	// reflink clones one file for the mixed kind (see Plugin.reflinkFile).
	reflink func(src, dst string) error
	rootDir string
	opID    string
	apply   bool
	library *icLibraryIndex
	// chapters copies the source's chapter table onto a version clone and
	// removes it on undo. Resolved with database.AsCapability: the chapter
	// methods are not on database.Store, so a bare assertion fails behind a
	// Store-embedding decorator (see chapters_backfill.go).
	chapters chapterPersister
	// tags reads the source's franchise: tags for the owner-manual check
	// (OpsStore does not carry tags). It is required: applygate.BookManualOnly
	// refuses a nil reader, so with none every group is skipped as
	// owner_manual_check_failed rather than checked without its tags.
	tags applygate.ManualOnlyTagReader
	// userState carries a version clone's listening state back onto its
	// source before a rollback deletes the clone (undo). The clone is the
	// organized copy, usually crowned the group's primary, so Audiobookshelf
	// lists it, users play it, and merges send their state to it as the flag
	// holder. nil resolves from the store (icUserStateStore); with none the
	// rollback is refused rather than delete state it cannot move.
	userState merge.UserProgressMerger
	// reads is the store the run was built from. icStore narrows it to OpsStore
	// and hides the retired-row and path reads, so the rollback's shell and
	// path-claim checks resolve them from here with database.AsCapability. nil
	// resolves nothing: the checks then refuse (fail closed) instead of
	// reading a missing capability as "no retired rows".
	reads any
	// userOps, when set, makes the user-state moves instead of userState (the
	// native engine's writer). nil resolves from userState / the store.
	userOps icUserState
	// pathRows, when set, answers clonePathClaim instead of reads (the native
	// engine's store). nil resolves from reads.
	pathRows func(path string) ([]database.BookFile, error)
}

// icUserStateStore is the store undo moves a clone's user state through.
func (r *icRunner) icUserStateStore() merge.UserProgressMerger {
	if r.userState != nil {
		return r.userState
	}
	if um, ok := database.AsCapability[merge.UserProgressMerger](r.store.OpsStore); ok {
		return um
	}
	return nil
}

// icUserStateOps is the user-state moves undo makes, nil when the store
// cannot make them.
func (r *icRunner) icUserStateOps() icUserState {
	if r.userOps != nil {
		return r.userOps
	}
	if um := r.icUserStateStore(); um != nil {
		return icMergeUserState{um}
	}
	return nil
}

// carryCloneStateToSource moves every user's state on the clone (progress,
// read status, positions), their bookmarks and the clone's ABS identity back
// onto the source with merge.CarryStateBeforeHardDelete, which is all or
// nothing: a carry that does not fully land -- a user's state, a bookmark
// copy or the sync redirect -- is put back on the clone and reported, so the
// clone, which stays live and primary when the rollback is refused, keeps
// every user's state instead of some of it having moved to the hidden source.
//
// It refuses before moving anything when the source is missing or in the
// trash: the state would land on a book nobody sees, which the rollback is
// about to make the only copy.
func (r *icRunner) carryCloneStateToSource(rec *icRecord) error {
	if err := r.checkUndoSource(rec); err != nil {
		return err
	}
	um := r.icUserStateOps()
	if um == nil {
		return fmt.Errorf("store cannot read or move users' listening state")
	}
	return um.CarryStateBeforeHardDelete(rec.SourceBookID, rec.CloneBookID)
}

// itunesCloneIntoLibraryKind is the native kind of itunes.clone-into-library (F5-10).
const itunesCloneIntoLibraryKind tasks.Kind = "books.itunes.clone-into-library"

// icCloneReader is the cloner's reads: where a book's files would land.
type icCloneReader interface {
	// PlanLibraryClone returns the library paths book's active files would land
	// at, in files order. Nothing is written (LibraryCloner.PlanLibraryClone).
	PlanLibraryClone(book *database.Book, files []database.BookFile) ([]string, error)
	// LibraryITunesPath is the iTunes path the organize service records for a
	// library file (LibraryCloner.LibraryITunesPath).
	LibraryITunesPath(path string) string
}

// icCloneWriter is the cloner's write.
type icCloneWriter interface {
	// CloneBookIntoLibrary reflinks the files and creates the organized version
	// (LibraryCloner.CloneBookIntoLibrary).
	CloneBookIntoLibrary(book *database.Book, files []database.BookFile, opID string) (string, error)
	// Reflink clones one file for the mixed kind (Plugin.reflinkFile).
	Reflink(src, dst string) error
}

// icChapterWriter is the chapter-table write.
type icChapterWriter interface {
	SaveChaptersForBook(bookID string, chapters []database.Chapter) error
}

// icStoreReader is the reads the core makes on the store itself, beyond
// icReader.
type icStoreReader interface {
	icReader
	icCloneReader
	database.ChapterReader
	GetAllBooksCoreComplete(limit, offset int) ([]database.BookCore, error)
	GetAuthorByName(name string) (*database.Author, error)
	GetBookTagsDetailed(bookID string) ([]database.BookTag, error)
	// BookFileRowsAtPathStrict is database.BookFileRowsAtPathStrict over the
	// store: the complete multi-row lookup, an error when it cannot be had.
	BookFileRowsAtPathStrict(path string) ([]database.BookFile, error)
	// RefuseWhileLibraryScanActive errors while a library.scan is queued or
	// running, or when that cannot be verified (refuseWhileLibraryScanActive).
	RefuseWhileLibraryScanActive(opName string) error
}

// icStoreWriter is the writes the core makes: the store's, the cloner's, the
// chapter table's and the user-state moves.
type icStoreWriter interface {
	icWriter
	icCloneWriter
	icChapterWriter
	icUserState
}

// icClonerJoin is a LibraryCloner over a reader and a writer.
type icClonerJoin struct {
	icCloneReader
	icCloneWriter
}

// icChaptersJoin is a chapterPersister over a reader and a writer.
type icChaptersJoin struct {
	database.ChapterReader
	icChapterWriter
}

// icOpsJoin is the runner's one store view over a reader and a writer.
type icOpsJoin struct {
	icReader
	icWriter
}

// itunesCloneEnv is what the clone's core needs from its engine.
type itunesCloneEnv struct {
	t6g5Base
	Items t6g5ItemsFunc
	Lease t6g5LeaseFunc
	// RootDir is the library root.
	RootDir string
	// NewRunner builds the engine's runner; writes is true for an apply,
	// rollback or repair.
	NewRunner func(writes bool) (*icRunner, error)
	// Reads is the reads the core makes before the runner exists.
	Reads icStoreReader
}

// itunesCloneIntoLibrary is the v2 shell: HEAD's nil-store check and runner,
// then the shared core.
func (p *Plugin) itunesCloneIntoLibrary(ctx context.Context, params icParams, rootDir string, reporter sdk.Reporter) (*icReport, error) {
	ops, vps := p.deps.OpsStore(), p.deps.VersionPrimaryStore()
	if ops == nil || vps == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	base := p.t6g5HeadBase(ctx, reporter, params.Apply || params.Rollback || params.Repair)
	env := itunesCloneEnv{
		t6g5Base: base, Items: t6g5HeadItems(reporter), Lease: p.t6g5HeadLease(reporter),
		RootDir: rootDir, Reads: headT6g5Store{p},
		NewRunner: func(writes bool) (*icRunner, error) {
			reflink := p.reflinkFile
			if reflink == nil {
				reflink = fileops.Reflink
			}
			run := &icRunner{store: icStore{OpsStore: ops, ChapterReader: vps}, reads: ops, cloner: p.deps, reflink: reflink, tags: p.deps.BookTagReader()}
			if um := p.deps.MergeUserStateStore(); um != nil {
				run.userState = um
			}
			if writes {
				cp, ok := database.AsCapability[chapterPersister](ops)
				if !ok {
					return nil, fmt.Errorf("itunes-clone-into-library: store cannot write chapter tables; refusing to write")
				}
				run.chapters = cp
			}
			return run, nil
		},
	}
	return itunesCloneCore(ctx, params, env)
}

// itunesCloneIntoLibraryKind builds the native kind. Writes found: books,
// book_files and the chapter table through the store calls, the book rows the
// clone creates (CloneBookIntoLibrary) and the undo records in _system
// preferences (SetUserPreferenceForUser); HEAD declares only capabilities.
// apply, rollback and repair stay params (HEAD's LiveOneOf): a preview forces
// all three false, a live run must name exactly one.
func (p *Plugin) itunesCloneIntoLibraryKind(deps t6g5Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	eff := tasks.Writes(tasks.Res("books", "books"), tasks.Res("books", "book_files"),
		tasks.Res("books", "chapters"), tasks.Res("books", "user_preferences"))
	return tasks.Task(itunesCloneIntoLibraryKind, tasks.TaskSpec[json.RawMessage, *icReport]{
		Common: t6g5Common(p.itunesCloneIntoLibraryDef(), itunesCloneIntoLibraryKind, eff, sched),
		Run: func(rc *tasks.RC, raw json.RawMessage) (*icReport, error) {
			var params icParams
			if err := t6g5DecodeParams(itunesCloneIntoLibraryKind, raw, &params); err != nil {
				return nil, err
			}
			base, err := t6g5NativeBase(itunesCloneIntoLibraryKind, rc, deps, true)
			if err != nil {
				return nil, err
			}
			if !base.Live {
				params.Apply, params.Rollback, params.Repair = false, false, false
			} else if !params.Apply && !params.Rollback && !params.Repair {
				return nil, fmt.Errorf("%s: a live run must name one of apply, rollback or repair", itunesCloneIntoLibraryKind)
			}
			env := itunesCloneEnv{
				t6g5Base: base, Items: t6g5NativeItems(rc), Lease: t6g5NativeLease(rc, deps),
				RootDir: base.Store.RootDir(), Reads: base.Store,
				NewRunner: func(writes bool) (*icRunner, error) {
					run := &icRunner{
						store:    icStore{OpsStore: icOpsJoin{base.Store, base.Writer}, ChapterReader: base.Store},
						cloner:   icClonerJoin{base.Store, base.Writer},
						reflink:  base.Writer.Reflink,
						tags:     base.Store,
						userOps:  base.Writer,
						pathRows: base.Store.BookFileRowsAtPathStrict,
					}
					if writes {
						run.chapters = icChaptersJoin{base.Store, base.Writer}
					}
					return run, nil
				},
			}
			return itunesCloneCore(rc.Context(), params, env)
		},
	})
}

// itunesCloneCore is the shared body of both shells. Which write the run makes
// (apply, rollback or repair) is params'; whether it writes at all is the
// engine's, and the shells have already cleared the three for a preview.
func itunesCloneCore(ctx context.Context, params icParams, env itunesCloneEnv) (*icReport, error) {
	requested := normalizeGroupIDs(params.GroupIDs)
	report := &icReport{DryRun: !params.Apply && !params.Rollback && !params.Repair, Rollback: params.Rollback,
		ByDecision: map[string]int{}, ByKind: map[string]int{}, BySkip: map[string]int{}, ByOutcome: map[string]int{}}
	modes := 0
	for _, m := range []bool{params.Apply, params.Rollback, params.Repair} {
		if m {
			modes++
		}
	}
	if modes > 1 {
		return report, fmt.Errorf("itunes-clone-into-library: apply, rollback and repair are exclusive")
	}
	if modes == 1 && len(requested) == 0 {
		return report, fmt.Errorf("itunes-clone-into-library: apply, rollback and repair need explicit group_ids; pick them from the dry run")
	}
	if env.RootDir == "" {
		return report, fmt.Errorf("itunes-clone-into-library: root_dir is not set")
	}
	opID := env.OpID
	writes := modes == 1
	if writes && opID == "" {
		return report, fmt.Errorf("itunes-clone-into-library: no operation id; refusing to write")
	}
	run, err := env.NewRunner(writes)
	if err != nil {
		return report, err
	}
	run.rootDir, run.opID, run.apply = env.RootDir, opID, params.Apply

	work := requested
	if !params.Rollback && !params.Repair {
		books, err := env.Reads.GetAllBooksCoreComplete(0, 0)
		if err != nil {
			return report, fmt.Errorf("list books: %w", err)
		}
		unknown, err := unknownAuthorIDs(env.Reads)
		if err != nil {
			return report, err
		}
		run.library = newICLibraryIndex(books, unknown)
		if len(work) == 0 {
			work = discoverITunesCloneGroups(books)
		}
	}
	report.Candidates = len(work)

	lease := t6g5Lease{Lost: func() bool { return false }, Release: func() {}}
	if writes {
		if err := env.Reads.RefuseWhileLibraryScanActive("itunes-clone-into-library"); err != nil {
			return report, err
		}
		var sdErr error
		lease, sdErr = env.Lease(ctx, "itunes-clone-into-library")
		if sdErr != nil {
			return report, fmt.Errorf("itunes-clone-into-library: could not acquire scan stand-down; refusing to write: %w", sdErr)
		}
		defer lease.Release()
	}

	var (
		mu     sync.Mutex
		groups []icGroupReport
		lost   atomic.Bool
		done   atomic.Int64
	)
	runErr := env.Items(ctx, len(work), t6g5ItemsOpts{
		// Set explicitly: groups are disjoint sets of books, so no two
		// workers touch the same row.
		Pool:    t6g5FixedPool(icWorkers, "I/O on one pool, the bound the repair op uses"),
		Collect: true,
		// Label runs inside the workers: it reads only the atomic.
		Label: func(_, total int) string { return fmt.Sprintf("Groups %d/%d", done.Load(), total) },
	}, func(gctx context.Context, i int) error {
		gid := work[i]
		defer done.Add(1)
		if lost.Load() {
			return nil
		}
		if writes && lease.Lost() {
			lost.Store(true)
			return nil
		}
		var g icGroupReport
		switch {
		case params.Rollback:
			g = run.rollback(gctx, gid)
		case params.Repair:
			g = run.repair(gctx, gid)
		default:
			g = run.planAndApply(gctx, gid)
		}
		mu.Lock()
		groups = append(groups, g)
		mu.Unlock()
		return nil
	})

	sort.Slice(groups, func(i, j int) bool { return groups[i].GroupID < groups[j].GroupID })
	for i := range groups {
		g := &groups[i]
		report.ByDecision[g.Decision]++
		if g.Kind != "" {
			report.ByKind[g.Kind]++
		}
		if g.Decision == icDecisionSkip {
			report.BySkip[g.Reason]++
		}
		if g.Outcome != "" {
			report.ByOutcome[g.Outcome]++
		}
		if g.Decision == icDecisionClone {
			report.Bytes += g.Bytes
		}
	}
	report.Groups = groups
	if lost.Load() {
		report.Aborted = errVGRepairStandDownLost.Error()
		return report, errors.New("itunes-clone-into-library: scan stand-down lost; remaining groups abandoned")
	}
	if runErr != nil {
		return report, runErr
	}
	env.Logger.Info("itunes-clone-into-library complete", "summary", report.summary())
	env.Progress(1, 1, report.summary())
	return report, nil
}

// unknownAuthorIDs is the id of the "Unknown Author" placeholder row, if one
// exists: a copy filed under it has no known author to compare.
func unknownAuthorIDs(store interface {
	GetAuthorByName(string) (*database.Author, error)
}) ([]int, error) {
	a, err := store.GetAuthorByName(database.UnknownAuthorName)
	if err != nil {
		return nil, fmt.Errorf("resolve %q author: %w", database.UnknownAuthorName, err)
	}
	if a == nil {
		return nil, nil
	}
	return []int{a.ID}, nil
}

// discoverITunesCloneGroups lists groups with a live organized member that
// is iTunes-linked (a PID, an iTunes import source, or a path under
// books/itunes). The plan then reads each one's files and decides.
func discoverITunesCloneGroups(books []database.BookCore) []string {
	seen := map[string]bool{}
	var out []string
	for i := range books {
		b := &books[i]
		if b.VersionGroupID == nil || *b.VersionGroupID == "" || b.IsSoftDeleted() || seen[*b.VersionGroupID] {
			continue
		}
		if b.LibraryState == nil || *b.LibraryState != "organized" {
			continue
		}
		linked := (b.ITunesPersistentID != nil && *b.ITunesPersistentID != "") ||
			(b.ITunesImportSource != nil && *b.ITunesImportSource != "") || pathutil.UnderFrozenITunesTree(b.FilePath)
		if linked {
			seen[*b.VersionGroupID] = true
			out = append(out, *b.VersionGroupID)
		}
	}
	sort.Strings(out)
	return out
}

// icLeadingNumber is a track or series number an iTunes title often carries
// ("02 - Chaos Vector").
var icLeadingNumber = regexp.MustCompile(`^\s*\d+\s*[-.\x{2013}]\s*`)

// icTitle is a title folded for matching: the whole title, its base (no
// leading number, nothing after a colon or an opening parenthesis), and
// whether a colon subtitle was cut off. A form with no letter ("01") is
// empty: bare numbers are exactly the mis-titled rows that must not match.
type icTitle struct {
	full, base string
	subtitled  bool
}

// icUnknownAuthorSuffix is the " - Unknown Author" a filename-derived title
// carries when the parser had no author, plus anything after it (" - read by
// ..."): "Deadeye Dick - Unknown Author" is the book "Deadeye Dick". It is cut
// before any other folding so both sides of a match see the same title.
var icUnknownAuthorSuffix = regexp.MustCompile(`(?i)\s*[-\x{2013}]\s*unknown author\b.*$`)

func icTitleForms(title string) icTitle {
	title = icUnknownAuthorSuffix.ReplaceAllString(title, "")
	base, rest := title, ""
	if i := strings.IndexAny(title, ":("); i > 0 {
		base, rest = title[:i], title[i:]
	}
	fold := func(s string) string {
		k := dedup.NormalizeTitle(s)
		if !strings.ContainsFunc(k, func(r rune) bool { return r >= 'a' && r <= 'z' }) {
			return ""
		}
		return k
	}
	return icTitle{full: fold(title), base: fold(icLeadingNumber.ReplaceAllString(base, "")),
		subtitled: strings.Contains(rest, ":")}
}

// keys are the index keys a title is filed under.
func (t icTitle) keys() []string {
	var out []string
	for _, k := range []string{t.full, t.base} {
		if k != "" && (len(out) == 0 || out[0] != k) {
			out = append(out, k)
		}
	}
	return out
}

// icTitlesMatch: whole titles agree, one title's base is the other whole
// title ("Champion of Deania: A ... Adventure" / "Champion of Deania"), or
// the bases agree and at most one of them had a colon subtitle cut off. Two
// DIFFERENT subtitles under one base are a series prefix, not one book ("The
// Land: Founding" / "The Land: Predators"; "Jack Reacher 4: The Visitor" /
// "Jack Reacher 4: Running Blind").
func icTitlesMatch(a, b icTitle) bool {
	switch {
	case a.full != "" && (a.full == b.full || a.full == b.base):
		return true
	case a.base != "" && a.base == b.full:
		return true
	default:
		return a.base != "" && a.base == b.base && !(a.subtitled && b.subtitled)
	}
}

// icMatch says how a candidate copy matched: by ASIN or author plus title
// (strong), or by title alone against an Unknown/no-author copy (weak).
const (
	icMatchStrong = iota + 1
	icMatchUnknownAuthor
)

type icIndexEntry struct {
	id    string
	title icTitle
}

// icLibraryIndex finds organized books by author+title key and by ASIN. It
// is built once per run from the full book list; each hit is re-read and its
// files checked at plan time (libraryCopies), so a stale entry cannot cause a
// skip on its own. Books with no author, or the "Unknown Author" row, are
// filed under icUnknownAuthorKey.
type icLibraryIndex struct {
	byTitle map[string][]icIndexEntry // "<author key>|<title key>"
	byASIN  map[string][]string
	unknown map[int]bool // author ids that mean "no known author"
}

const icUnknownAuthorKey = "unknown"

func icASINKey(asin *string) string {
	if asin == nil {
		return ""
	}
	return strings.ToUpper(strings.TrimSpace(*asin))
}

func (ix *icLibraryIndex) authorKey(authorID *int) string {
	if authorID == nil || ix.unknown[*authorID] {
		return icUnknownAuthorKey
	}
	return fmt.Sprint(*authorID)
}

func newICLibraryIndex(books []database.BookCore, unknownAuthorIDs []int) *icLibraryIndex {
	ix := &icLibraryIndex{byTitle: map[string][]icIndexEntry{}, byASIN: map[string][]string{}, unknown: map[int]bool{}}
	for _, id := range unknownAuthorIDs {
		ix.unknown[id] = true
	}
	for i := range books {
		b := &books[i]
		if b.IsSoftDeleted() || b.LibraryState == nil || *b.LibraryState != "organized" {
			continue
		}
		t, ak := icTitleForms(b.Title), ix.authorKey(b.AuthorID)
		for _, k := range t.keys() {
			tk := ak + "|" + k
			ix.byTitle[tk] = append(ix.byTitle[tk], icIndexEntry{id: b.ID, title: t})
		}
		if a := icASINKey(b.ASIN); a != "" {
			ix.byASIN[a] = append(ix.byASIN[a], b.ID)
		}
	}
	return ix
}

// candidates returns the ids that may be this book, with how each matched: a
// shared ASIN, or the same known author and a matching title (strong); or a
// matching title on a copy with no known author (weak). A strong match wins
// when an id matches both ways.
func (ix *icLibraryIndex) candidates(book *database.Book) map[string]int {
	out := map[string]int{}
	add := func(id string, how int) {
		if id == book.ID {
			return
		}
		if cur, ok := out[id]; !ok || how < cur {
			out[id] = how
		}
	}
	if a := icASINKey(book.ASIN); a != "" {
		for _, id := range ix.byASIN[a] {
			add(id, icMatchStrong)
		}
	}
	t := icTitleForms(book.Title)
	lookup := func(ak string, how int) {
		for _, k := range t.keys() {
			for _, e := range ix.byTitle[ak+"|"+k] {
				if icTitlesMatch(t, e.title) {
					add(e.id, how)
				}
			}
		}
	}
	if ak := ix.authorKey(book.AuthorID); ak != icUnknownAuthorKey {
		lookup(ak, icMatchStrong)
	}
	lookup(icUnknownAuthorKey, icMatchUnknownAuthor)
	return out
}

// libraryCopies returns the books outside group gid that hold this book in
// the library (live, organized, and at least one present file under the root
// that is not an iTunes file) and whether any of them matched strongly. A
// read error fails the group closed.
func (r *icRunner) libraryCopies(book *database.Book, gid string) ([]string, bool, error) {
	if r.library == nil {
		return nil, false, nil
	}
	cands := r.library.candidates(book)
	ids := make([]string, 0, len(cands))
	for id := range cands {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []string
	strong := false
	for _, id := range ids {
		b, err := r.store.GetBookByID(id)
		if err != nil {
			return nil, false, fmt.Errorf("read %s: %w", id, err)
		}
		if b == nil || b.IsSoftDeleted() || b.LibraryState == nil || *b.LibraryState != "organized" {
			continue
		}
		if b.VersionGroupID != nil && *b.VersionGroupID == gid {
			continue
		}
		files, err := r.store.GetBookFiles(id)
		if err != nil {
			return nil, false, fmt.Errorf("read files of %s: %w", id, err)
		}
		for _, f := range files {
			if !f.Missing && !pathutil.UnderFrozenITunesTree(f.FilePath) && pathutil.IsWithin(f.FilePath, r.rootDir) {
				out = append(out, id)
				strong = strong || cands[id] == icMatchStrong
				break
			}
		}
	}
	return out, strong, nil
}

// icPlan is one group's decided clone.
type icPlan struct {
	members []database.Book
	source  *database.Book
	files   []database.BookFile // the source's active rows
	dests   []string            // planned library path per files[i]
	kind    string
}

func skipped(g icGroupReport, reason string) icGroupReport {
	g.Decision, g.Reason = icDecisionSkip, reason
	return g
}

func (r *icRunner) plan(ctx context.Context, gid string) (icGroupReport, *icPlan) {
	g := icGroupReport{GroupID: gid}
	members, err := r.store.GetBooksByVersionGroup(gid)
	if err != nil {
		g.Decision, g.Error = icDecisionSkip, "read group: "+err.Error()
		return g, nil
	}
	loader := versionprimary.Loader{Files: r.store, Chapters: r.store, RootDir: r.rootDir}
	alive := func(id string) bool {
		// A read error counts as alive, as vgStoreAlive does: the group is left
		// as it is rather than crowning a book that may still be merged away.
		b, err := r.store.GetBookByID(id)
		return err != nil || (b != nil && !b.IsSoftDeleted())
	}
	ms, err := loader.LoadMembers(ctx, members, alive)
	if err != nil {
		g.Decision, g.Error = icDecisionSkip, "load signals: "+err.Error()
		return g, nil
	}
	d := versionprimary.Elect(ms)
	if d.Kind != versionprimary.DecisionHeld {
		return skipped(g, "group_not_held"), nil
	}
	// The source: the best-tier live organized member whose only reason for
	// being ineligible is files outside the library root.
	var src *versionprimary.MemberEval
	for i := range d.Members {
		e := &d.Members[i]
		if !e.Live || e.LibraryState != "organized" || e.Ineligible != versionprimary.IneligibleOutsideLibrary {
			continue
		}
		if src == nil || e.Tier > src.Tier || (e.Tier == src.Tier && e.MetadataScore > src.MetadataScore) {
			src = e
		}
	}
	if src == nil {
		return skipped(g, "no_organized_member_outside_library"), nil
	}
	if d.BestTier > src.Tier {
		return skipped(g, "better_copy_elsewhere"), nil
	}
	var book *database.Book
	for i := range members {
		if members[i].ID == src.BookID {
			book = &members[i]
		}
	}
	g.SourceBookID, g.Title = book.ID, book.Title
	// The whole-book owner-manual check (row, series, credits, tags, every
	// book_file path and its transcribed fields), not the row alone. A read
	// failure skips the group (fail closed) under its own reason.
	if held, _, err := applygate.BookManualOnly(applygate.ManualOnlyReaders{Files: r.store, Series: r.store, Authors: r.store, Tags: r.tags}, book); err != nil {
		g.Decision, g.Reason, g.Error = icDecisionSkip, applygate.ReasonOwnerManualCheckFailed, "owner-manual check: "+err.Error()
		return g, nil
	} else if held {
		return skipped(g, applygate.ReasonOwnerManualOnly), nil
	}
	copies, strong, err := r.libraryCopies(book, gid)
	if err != nil {
		g.Decision, g.Error = icDecisionSkip, "already-in-library check: "+err.Error()
		return g, nil
	}
	if len(copies) > 0 {
		g.LibraryCopies = copies
		if strong {
			return skipped(g, "already_in_library"), nil
		}
		return skipped(g, "possibly_in_library"), nil
	}
	all, err := r.store.GetBookFiles(book.ID)
	if err != nil {
		g.Decision, g.Error = icDecisionSkip, "read files: "+err.Error()
		return g, nil
	}
	var active []database.BookFile
	nITunes, nRoot := 0, 0
	for _, f := range all {
		if f.Missing {
			return skipped(g, "has_missing_rows"), nil
		}
		active = append(active, f)
		switch {
		case pathutil.UnderFrozenITunesTree(f.FilePath):
			nITunes++
		case pathutil.IsWithin(f.FilePath, r.rootDir):
			nRoot++
		}
	}
	if len(active) == 0 || nITunes == 0 || nITunes+nRoot != len(active) {
		return skipped(g, "files_not_itunes_or_library"), nil
	}
	dests, err := r.cloner.PlanLibraryClone(book, active)
	if err != nil {
		g.Decision, g.Error = icDecisionSkip, "plan destinations: "+err.Error()
		return g, nil
	}
	p := &icPlan{members: members, source: book, files: active, dests: dests, kind: icKindVersion}
	if nRoot > 0 {
		p.kind = icKindMixed
		if reason := mixedConflict(active, dests, r.rootDir); reason != "" {
			g.Kind = icKindMixed
			return skipped(g, reason), nil
		}
	}
	g.Kind, g.Files, g.Destinations = p.kind, len(active), dests
	for i, f := range active {
		if !pathutil.UnderFrozenITunesTree(f.FilePath) {
			continue
		}
		if !pathutil.IsWithin(dests[i], r.rootDir) {
			return skipped(g, "destination_outside_root"), nil
		}
		if _, err := os.Lstat(dests[i]); err == nil {
			return skipped(g, "destination_exists"), nil
		}
		g.Bytes += f.FileSize
	}
	g.Decision = icDecisionClone
	return g, p
}

// mixedConflict returns why a mixed book cannot be completed in place, or "".
// Every library-side file must already sit at its planned path (so the plan's
// folder IS the book's library folder), and every iTunes file must land in
// that same folder.
func mixedConflict(files []database.BookFile, dests []string, root string) string {
	dir := ""
	for i, f := range files {
		if pathutil.UnderFrozenITunesTree(f.FilePath) {
			continue
		}
		if filepath.Clean(dests[i]) != filepath.Clean(f.FilePath) {
			return "mixed_library_files_not_at_planned_path"
		}
		d := filepath.Dir(f.FilePath)
		if dir != "" && d != dir {
			return "mixed_library_files_in_several_folders"
		}
		dir = d
	}
	for i, f := range files {
		if pathutil.UnderFrozenITunesTree(f.FilePath) && filepath.Dir(dests[i]) != dir {
			return "mixed_itunes_files_planned_elsewhere"
		}
	}
	return ""
}

func (r *icRunner) planAndApply(ctx context.Context, gid string) icGroupReport {
	g, p := r.plan(ctx, gid)
	if p == nil || !r.apply {
		return g
	}
	fresh, err := r.store.GetBooksByVersionGroup(gid)
	if err != nil {
		g.Outcome, g.Error = icOutcomeFailed, "re-read group: "+err.Error()
		return g
	}
	if !sameVGKeys(vgKeys(p.members), vgKeys(fresh)) {
		g.Outcome = icOutcomeChanged
		return g
	}
	if p.kind == icKindMixed {
		return r.applyMixed(ctx, g, p)
	}
	return r.applyVersion(ctx, g, p)
}

func priorPrimaryFlags(members []database.Book) map[string]string {
	out := make(map[string]string, len(members))
	for i := range members {
		out[members[i].ID] = storedPrimaryFlag(members[i].IsPrimaryVersion)
	}
	return out
}

func (r *icRunner) applyVersion(ctx context.Context, g icGroupReport, p *icPlan) icGroupReport {
	rec := icRecord{GroupID: g.GroupID, Kind: icKindVersion, SourceBookID: p.source.ID,
		SourcePriorState: derefString(p.source.LibraryState), SourcePriorPrimary: storedPrimaryFlag(p.source.IsPrimaryVersion),
		PriorPrimaries: priorPrimaryFlags(p.members), OpID: r.opID, At: time.Now()}
	byPID := map[string]database.BookFile{}
	for _, f := range p.files {
		if f.ITunesPersistentID != "" {
			byPID[f.ITunesPersistentID] = f
		}
	}
	newID, err := r.cloner.CloneBookIntoLibrary(p.source, p.files, r.opID)
	if err != nil {
		g.Outcome, g.Error = icOutcomeFailed, err.Error()
		return g
	}
	g.CloneBookID = newID
	rec.CloneBookID = newID
	landed, err := r.store.GetBookFiles(newID)
	if err != nil {
		g.Outcome, g.Error = icOutcomeNoUndo, "read clone rows: "+err.Error()
		return g
	}
	want := map[string]bool{}
	for _, d := range p.dests {
		want[filepath.Clean(d)] = true
	}
	offPlan := len(landed) != len(p.dests)
	for _, f := range landed {
		pair := icPair{CloneFileID: f.ID, ClonePath: f.FilePath, PID: f.ITunesPersistentID}
		if src, ok := byPID[f.ITunesPersistentID]; ok && f.ITunesPersistentID != "" {
			pair.SourceFileID, pair.SourcePath = src.ID, src.FilePath
		}
		rec.Pairs = append(rec.Pairs, pair)
		if !want[filepath.Clean(f.FilePath)] {
			offPlan = true
		}
	}
	if err := r.saveRecord(&rec); err != nil {
		g.Outcome, g.Error = icOutcomeNoUndo, "rollback record not saved: "+err.Error()
		return g
	}
	if offPlan {
		// A destination appeared after the plan (organize takes a _copyN
		// name then): undo rather than keep a copy nobody planned.
		rb := r.undo(context.Background(), &rec)
		g.Outcome, g.Error = icOutcomeFailed, "landed off plan; rolled back: "+rb.Outcome+" "+rb.Error
		return g
	}
	// The clone's files are the source's bytes, so its chapter table is the
	// source's. Without it the clone ranks m4b_without_chapters, the election
	// holds the group (better_copy_not_in_library: the iTunes source, with
	// chapters, outranks it) and a clone whose source was not the primary is
	// never crowned: 67 of the 240 clones on 2026-09-24, invisible in ABS.
	// Copy the table, then hand off again now that the clone ranks fairly.
	// Errors are reported on an applied group; rollback still reverses it.
	if chs, err := r.chapters.GetChaptersForBook(p.source.ID); err != nil {
		g.Error = "read source chapters: " + err.Error()
	} else if len(chs) > 0 {
		if err := r.chapters.SaveChaptersForBook(newID, chs); err != nil {
			g.Error = "copy chapters: " + err.Error()
		}
	}
	if _, err := versionprimary.EnsureSinglePrimary(ctx, r.store, g.GroupID, versionprimary.Env{RootDir: r.rootDir}); err != nil {
		g.Error = strings.TrimSpace(g.Error + " hand-off: " + err.Error())
	}
	g.Outcome = icOutcomeApplied
	return g
}

func (r *icRunner) applyMixed(ctx context.Context, g icGroupReport, p *icPlan) icGroupReport {
	rec := icRecord{GroupID: g.GroupID, Kind: icKindMixed, SourceBookID: p.source.ID,
		SourcePriorState: derefString(p.source.LibraryState), SourcePriorPrimary: storedPrimaryFlag(p.source.IsPrimaryVersion),
		PriorPrimaries: priorPrimaryFlags(p.members), OpID: r.opID, At: time.Now()}
	var made []string
	cleanup := func() {
		for _, m := range made {
			_ = os.Remove(m)
		}
	}
	for i, f := range p.files {
		if !pathutil.UnderFrozenITunesTree(f.FilePath) {
			continue
		}
		if err := r.reflink(f.FilePath, p.dests[i]); err != nil {
			cleanup()
			g.Outcome, g.Error = icOutcomeFailed, fmt.Sprintf("reflink %s: %v", f.FilePath, err)
			return g
		}
		made = append(made, p.dests[i])
		rec.Pairs = append(rec.Pairs, icPair{SourceFileID: f.ID, SourcePath: f.FilePath, ClonePath: p.dests[i],
			PID: f.ITunesPersistentID, OldITunesPath: f.ITunesPath})
	}
	// The record goes down BEFORE any row moves, so a failure part way can
	// always be reversed from it.
	if err := r.saveRecord(&rec); err != nil {
		cleanup()
		g.Outcome, g.Error = icOutcomeFailed, "rollback record not saved; clones removed: "+err.Error()
		return g
	}
	for _, pr := range rec.Pairs {
		src, dst := pr.SourcePath, pr.ClonePath
		_, err := r.store.ModifyBookFile(p.source.ID, pr.SourceFileID, func(bf *database.BookFile) error {
			if bf.FilePath != src {
				return errVGChangedSincePlan
			}
			bf.FilePath = dst
			bf.ITunesPath = r.cloner.LibraryITunesPath(dst)
			return nil
		})
		if err != nil {
			rb := r.undo(ctx, &rec)
			g.Outcome, g.Error = icOutcomeFailed, fmt.Sprintf("repoint %s: %v; rolled back: %s %s", pr.SourceFileID, err, rb.Outcome, rb.Error)
			return g
		}
	}
	if _, err := versionprimary.EnsureSinglePrimary(ctx, r.store, g.GroupID, versionprimary.Env{RootDir: r.rootDir}); err != nil {
		g.Error = strings.TrimSpace(g.Error + " hand-off: " + err.Error())
	}
	g.Outcome = icOutcomeApplied
	return g
}

func (r *icRunner) saveRecord(rec *icRecord) error {
	blob, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return r.store.SetUserPreferenceForUser("_system", icRecordPrefix+rec.GroupID, string(blob))
}

func (r *icRunner) loadRecord(gid string) (*icRecord, error) {
	prefs, err := r.store.GetAllPreferencesForUser("_system")
	if err != nil {
		return nil, err
	}
	for _, p := range prefs {
		if p.Key == icRecordPrefix+gid && strings.TrimSpace(p.Value) != "" {
			var rec icRecord
			if err := json.Unmarshal([]byte(p.Value), &rec); err != nil {
				return nil, fmt.Errorf("decode record: %w", err)
			}
			return &rec, nil
		}
	}
	return nil, nil
}

// repair finishes a clone applied before the chapter copy and the post-clone
// hand-off existed: see icParams.Repair.
func (r *icRunner) repair(ctx context.Context, gid string) icGroupReport {
	g := icGroupReport{GroupID: gid, Decision: "repair"}
	rec, err := r.loadRecord(gid)
	switch {
	case err != nil:
		g.Outcome, g.Error = icOutcomeFailed, err.Error()
		return g
	case rec == nil:
		g.Outcome, g.Reason = icOutcomeFailed, "no_clone_record"
		return g
	}
	g.Kind, g.SourceBookID, g.CloneBookID = rec.Kind, rec.SourceBookID, rec.CloneBookID
	if rec.PriorPrimaries == nil {
		members, err := r.store.GetBooksByVersionGroup(gid)
		if err != nil {
			g.Outcome, g.Error = icOutcomeFailed, "read group: "+err.Error()
			return g
		}
		rec.PriorPrimaries = priorPrimaryFlags(members)
		// Before any write: a record that cannot remember the flags would
		// leave the hand-off's demotions irreversible.
		if err := r.saveRecord(rec); err != nil {
			g.Outcome, g.Error = icOutcomeFailed, "record not updated: "+err.Error()
			return g
		}
	}
	if rec.Kind == icKindVersion && rec.CloneBookID != "" {
		have, err := r.chapters.GetChaptersForBook(rec.CloneBookID)
		if err != nil {
			g.Outcome, g.Error = icOutcomeFailed, "read clone chapters: "+err.Error()
			return g
		}
		if len(have) == 0 {
			chs, err := r.chapters.GetChaptersForBook(rec.SourceBookID)
			if err != nil {
				g.Outcome, g.Error = icOutcomeFailed, "read source chapters: "+err.Error()
				return g
			}
			if len(chs) > 0 {
				if err := r.chapters.SaveChaptersForBook(rec.CloneBookID, chs); err != nil {
					g.Outcome, g.Error = icOutcomeFailed, "copy chapters: "+err.Error()
					return g
				}
			}
		}
	}
	if _, err := versionprimary.EnsureSinglePrimary(ctx, r.store, gid, versionprimary.Env{RootDir: r.rootDir}); err != nil {
		g.Outcome, g.Error = icOutcomeFailed, "hand-off: "+err.Error()
		return g
	}
	g.Outcome = icOutcomeFixed
	return g
}

func (r *icRunner) rollback(ctx context.Context, gid string) icGroupReport {
	g := icGroupReport{GroupID: gid, Decision: "rollback"}
	rec, err := r.loadRecord(gid)
	switch {
	case err != nil:
		g.Outcome, g.Error = icOutcomeFailed, err.Error()
		return g
	case rec == nil:
		g.Outcome, g.Reason = icOutcomeFailed, "no_clone_record"
		return g
	}
	res := r.undo(ctx, rec)
	res.GroupID, res.Decision = gid, "rollback"
	return res
}

// undo reverses one clone from its record. A version clone: the source put
// back as it was before the clone (library state and primary flag), then the
// clone's users' listening state carried back to it (refused, with the
// source's restore undone, when it cannot fully move), then PIDs back to the
// source's rows and the clone's rows, authors and book removed. The primary
// is handed on and the record cleared last. A step that finds a row no longer
// as the record left it stops and keeps the record; every step is safe to
// repeat, so a retry finishes what a failed attempt started.
//
// The source is restored BEFORE the clone is removed so the books never pass
// through a state where the users' state sits on a hidden source while the
// clone stays live: when a step after the carry fails, the state is put on
// whichever of the two ABS lists (keepCloneStateVisible) and the failure
// says where it is.
func (r *icRunner) undo(ctx context.Context, rec *icRecord) icGroupReport {
	g := icGroupReport{GroupID: rec.GroupID, Kind: rec.Kind, SourceBookID: rec.SourceBookID, CloneBookID: rec.CloneBookID}
	fail := func(format string, a ...any) icGroupReport {
		g.Outcome, g.Error = icOutcomeFailed, fmt.Sprintf(format, a...)
		return g
	}
	switch rec.Kind {
	case icKindVersion:
		// A clone the retired-versions repair has since turned into a shell
		// (its rows re-keyed onto another book as retired versions) is not
		// removed: deleting it would drop the only record of where those
		// versions came from. Refused first, before the source is restored or a
		// user's state is carried: nothing has been written yet.
		if err := database.RefuseRetiredShell(r.reads, rec.CloneBookID, "roll back the clone"); err != nil {
			return fail("refusing to delete clone %s: %v", rec.CloneBookID, err)
		}
		if err := r.checkUndoSource(rec); err != nil {
			return fail("refusing to delete clone %s: its users' listening state could not be moved to source %s: %v",
				rec.CloneBookID, rec.SourceBookID, err)
		}
		// First the source, as it was before the clone.
		undoRestore, err := r.restoreSourceState(rec)
		if err != nil {
			return fail("restore source state: %v", err)
		}
		// Then, before the clone is touched: its users' state goes back to
		// the source, all or nothing. If it does not fully move, refuse with
		// the source's restore undone, so nothing has changed and a retry
		// can finish it.
		if err := r.carryCloneStateToSource(rec); err != nil {
			msg := fmt.Sprintf("refusing to delete clone %s: its users' listening state could not be moved to source %s: %v",
				rec.CloneBookID, rec.SourceBookID, err)
			if uerr := undoRestore(); uerr != nil {
				msg += fmt.Sprintf("; the source's restored state could not be undone (%v), so both copies may be listed until a retry", uerr)
			}
			return fail("%s", msg)
		}
		if err := r.removeVersionClone(rec); err != nil {
			return fail("%v; %s", err, r.keepCloneStateVisible(rec))
		}
		// Saving nil deletes the table; a clone with none is a no-op.
		if err := r.chapters.SaveChaptersForBook(rec.CloneBookID, nil); err != nil {
			g.Error = strings.TrimSpace(g.Error + " clone chapters not removed: " + err.Error())
		}
	case icKindMixed:
		for _, pr := range rec.Pairs {
			if _, err := r.store.ModifyBookFile(rec.SourceBookID, pr.SourceFileID, func(bf *database.BookFile) error {
				if bf.FilePath == pr.SourcePath {
					return database.ErrSkipBookFileWrite
				}
				if bf.FilePath != pr.ClonePath {
					return errVGChangedSincePlan
				}
				bf.FilePath, bf.ITunesPath = pr.SourcePath, pr.OldITunesPath
				return nil
			}); err != nil && !errors.Is(err, database.ErrSkipBookFileWrite) {
				return fail("repoint %s back: %v", pr.SourceFileID, err)
			}
		}
	default:
		return fail("unknown record kind %q", rec.Kind)
	}
	var leftover, kept []string
	for _, pr := range rec.Pairs {
		if pr.ClonePath == "" || !pathutil.IsWithin(pr.ClonePath, r.rootDir) {
			continue
		}
		// The clone's own rows are gone by now. A row that still names the path
		// (a retired version's included: it is audio a purge of the shell
		// would also have left alone) means the file is not the clone's to
		// remove. An unanswerable lookup keeps the file too.
		if claim, err := r.clonePathClaim(pr.ClonePath); err != nil {
			leftover = append(leftover, pr.ClonePath+" (kept: "+err.Error()+")")
			continue
		} else if claim != "" {
			kept = append(kept, pr.ClonePath+" ("+claim+")")
			continue
		}
		if err := os.Remove(pr.ClonePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			// Reported in the group result, not just logged: a clone left on
			// disk with no row is picked up by the next scan as a new book.
			leftover = append(leftover, pr.ClonePath+" ("+err.Error()+")")
		}
		if rec.Kind == icKindVersion {
			_ = os.Remove(filepath.Dir(pr.ClonePath)) // only if now empty
		}
	}
	if len(leftover) > 0 {
		g.Error = strings.TrimSpace(g.Error + " clone files left on disk: " + strings.Join(leftover, "; "))
	}
	if len(kept) > 0 {
		g.Error = strings.TrimSpace(g.Error + " clone files kept, a row still names them: " + strings.Join(kept, "; "))
	}
	// Reported, not fatal: the rows and files are already reversed, and a
	// second rollback could not redo them.
	if err := r.restorePriorPrimaries(rec); err != nil {
		g.Error = strings.TrimSpace(g.Error + " restore primary flags: " + err.Error())
	}
	if _, err := versionprimary.EnsureSinglePrimary(ctx, r.store, rec.GroupID, versionprimary.Env{RootDir: r.rootDir}); err != nil {
		g.Error = strings.TrimSpace(g.Error + " hand-off: " + err.Error())
	}
	if err := r.store.SetUserPreferenceForUser("_system", icRecordPrefix+rec.GroupID, ""); err != nil {
		g.Error = strings.TrimSpace(g.Error + " record not cleared: " + err.Error())
	}
	g.Outcome = icOutcomeRolled
	return g
}

// clonePathClaim returns "" when no book_file row, live or retired, names path,
// and a description of the first row that does otherwise. It needs the
// complete multi-row lookup (database.BookFileRowsAtPathStrict): a store that
// cannot give it is an error, never "nobody claims it".
func (r *icRunner) clonePathClaim(path string) (string, error) {
	var rows []database.BookFile
	var err error
	if r.pathRows != nil {
		rows, err = r.pathRows(path)
	} else {
		lk, ok := database.AsCapability[database.BookFilePathLookup](r.reads)
		if !ok {
			return "", errors.New("the store cannot look up which rows name a path")
		}
		rows, err = database.BookFileRowsAtPathStrict(lk, path)
	}
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", nil
	}
	return fmt.Sprintf("book_file %s of book %s, retired=%v", rows[0].ID, rows[0].BookID, rows[0].Retired), nil
}

// checkUndoSource refuses a rollback whose source is missing or in the trash
// before anything changes: the clone's users' state would land on a book
// nobody sees, which the rollback is about to make the only copy.
func (r *icRunner) checkUndoSource(rec *icRecord) error {
	src, err := r.store.GetBookByID(rec.SourceBookID)
	if err != nil {
		return fmt.Errorf("read source %s: %w", rec.SourceBookID, err)
	}
	if src == nil {
		return fmt.Errorf("source %s no longer exists", rec.SourceBookID)
	}
	if src.IsSoftDeleted() {
		return fmt.Errorf("source %s is in the trash", rec.SourceBookID)
	}
	return nil
}

// restoreSourceState puts the source's library state and primary flag back
// as they were before the clone. The returned func undoes that write (back
// to what the source held when this ran), for a rollback refused right after.
// Safe to repeat.
func (r *icRunner) restoreSourceState(rec *icRecord) (func() error, error) {
	var prevState *string
	var prevPrimary *bool
	_, err := r.store.ModifyBook(rec.SourceBookID, func(b *database.Book) error {
		prevState, prevPrimary = b.LibraryState, b.IsPrimaryVersion
		st := rec.SourcePriorState
		b.LibraryState = &st
		switch rec.SourcePriorPrimary {
		case "true", "false":
			v := rec.SourcePriorPrimary == "true"
			b.IsPrimaryVersion = &v
		default:
			b.IsPrimaryVersion = nil
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return func() error {
		_, err := r.store.ModifyBook(rec.SourceBookID, func(b *database.Book) error {
			b.LibraryState, b.IsPrimaryVersion = prevState, prevPrimary
			return nil
		})
		return err
	}, nil
}

// removeVersionClone gives each PID back to the source's row and removes the
// clone's rows, authors and book. Every step is safe to repeat: a clone row
// whose PID is already released, a row or a book already gone, is skipped.
// Right before the book is removed, the clone is re-checked for listening
// state under the merge lock (merge.HardDeleteWithoutUserState): a client
// that wrote to it since the carry refuses the delete.
func (r *icRunner) removeVersionClone(rec *icRecord) error {
	for _, pr := range rec.Pairs {
		if pr.PID == "" || pr.SourceFileID == "" {
			continue
		}
		if _, err := r.store.ModifyBookFile(rec.CloneBookID, pr.CloneFileID, func(bf *database.BookFile) error {
			switch bf.ITunesPersistentID {
			case pr.PID:
				bf.ITunesPersistentID = ""
				return nil
			case "":
				return database.ErrSkipBookFileWrite // released by an earlier attempt
			default:
				return errVGChangedSincePlan
			}
		}); err != nil {
			return fmt.Errorf("release PID %s from clone row %s: %w", pr.PID, pr.CloneFileID, err)
		}
		if _, err := r.store.ModifyBookFile(rec.SourceBookID, pr.SourceFileID, func(bf *database.BookFile) error {
			if bf.ITunesPersistentID == pr.PID {
				return database.ErrSkipBookFileWrite
			}
			bf.ITunesPersistentID = pr.PID
			return nil
		}); err != nil {
			return fmt.Errorf("return PID %s to source row %s: %w", pr.PID, pr.SourceFileID, err)
		}
	}
	rows, err := r.store.GetBookFiles(rec.CloneBookID)
	if err != nil {
		return fmt.Errorf("read clone rows: %w", err)
	}
	present := make(map[string]bool, len(rows))
	for _, f := range rows {
		present[f.ID] = true
	}
	var refs []database.BookFileRef
	for _, pr := range rec.Pairs {
		if present[pr.CloneFileID] {
			// The clone book is the one these rows were found under: a row a
			// regroup moved off it meanwhile is refused, not deleted from
			// wherever it went.
			refs = append(refs, database.BookFileRef{BookID: rec.CloneBookID, FileID: pr.CloneFileID})
		}
	}
	if _, err := r.store.DeleteBookFilesByIDs(refs); err != nil {
		return fmt.Errorf("delete clone rows: %w", err)
	}
	clone, err := r.store.GetBookByID(rec.CloneBookID)
	if err != nil {
		return fmt.Errorf("read clone book %s: %w", rec.CloneBookID, err)
	}
	if clone == nil {
		return nil // removed by an earlier attempt
	}
	if err := r.store.SetBookAuthors(rec.CloneBookID, nil); err != nil {
		return fmt.Errorf("clear clone authors: %w", err)
	}
	um := r.icUserStateOps()
	if um == nil {
		return fmt.Errorf("delete clone book %s: store cannot re-check users' listening state", rec.CloneBookID)
	}
	if err := um.HardDeleteWithoutUserState(rec.CloneBookID, func() error {
		return r.store.DeleteBook(rec.CloneBookID)
	}); err != nil {
		return fmt.Errorf("delete clone book %s: %w", rec.CloneBookID, err)
	}
	return nil
}

// keepCloneStateVisible runs when a rollback fails after the clone's users'
// state was carried to the source: the clone is still there and may still be
// the copy ABS lists. The state must not be left on a book ABS does not list
// while one it does is live, so: a source ABS lists keeps it; otherwise a
// clone ABS lists gets it back (merge.CarryStateBetweenLiveBooks, source ->
// clone, all or nothing; both books stay live, so no ABS sync redirect is
// left between them in either direction); otherwise it stays on the source. It returns where
// the state is, for the group's error. A retry carries it to the source
// again.
func (r *icRunner) keepCloneStateVisible(rec *icRecord) string {
	listed := func(id string) bool {
		b, err := r.store.GetBookByID(id)
		return err == nil && database.ABSLibraryFilter().Matches(b)
	}
	note := "the source and the clone may both be listed until a retry finishes the rollback"
	if listed(rec.SourceBookID) {
		return fmt.Sprintf("users' listening state is on source %s, which ABS lists; %s", rec.SourceBookID, note)
	}
	if !listed(rec.CloneBookID) {
		return fmt.Sprintf("users' listening state is on source %s; neither it nor clone %s is listed by ABS", rec.SourceBookID, rec.CloneBookID)
	}
	um := r.icUserStateOps()
	if um == nil {
		return fmt.Sprintf("users' listening state is on source %s, which ABS does not list, and the store cannot move it back to clone %s", rec.SourceBookID, rec.CloneBookID)
	}
	if err := um.CarryStateBetweenLiveBooks(rec.CloneBookID, rec.SourceBookID); err != nil {
		if errors.Is(err, merge.ErrSyncRedirectNotCleared) {
			// The state did move; only the redirect between the two
			// live books is left.
			return fmt.Sprintf("users' listening state was moved back to clone %s, which ABS lists, but the ABS sync redirect between it and source %s could not be cleared: %v", rec.CloneBookID, rec.SourceBookID, err)
		}
		return fmt.Sprintf("users' listening state is on source %s, which ABS does not list; moving it back to clone %s failed: %v", rec.SourceBookID, rec.CloneBookID, err)
	}
	return fmt.Sprintf("users' listening state was moved back to clone %s, which ABS lists", rec.CloneBookID)
}

// restorePriorPrimaries puts back each member's stored primary flag from the
// record. A member that is gone or has left the group is skipped: its flag
// no longer decides this group.
func (r *icRunner) restorePriorPrimaries(rec *icRecord) error {
	ids := make([]string, 0, len(rec.PriorPrimaries))
	for id := range rec.PriorPrimaries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		flag := rec.PriorPrimaries[id]
		b, err := r.store.GetBookByID(id)
		if err != nil {
			return fmt.Errorf("read %s: %w", id, err)
		}
		if b == nil || b.IsSoftDeleted() || b.VersionGroupID == nil || *b.VersionGroupID != rec.GroupID {
			continue
		}
		if storedPrimaryFlag(b.IsPrimaryVersion) == flag {
			continue
		}
		if _, err := r.store.ModifyBook(id, func(row *database.Book) error {
			switch flag {
			case "true", "false":
				v := flag == "true"
				row.IsPrimaryVersion = &v
			default:
				row.IsPrimaryVersion = nil
			}
			return nil
		}); err != nil {
			return fmt.Errorf("write %s: %w", id, err)
		}
	}
	return nil
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
