// file: internal/plugins/maintenance/repoint_unrecorded_renames.go
// version: 1.8.0
// guid: 5a0e7c38-2d94-4b1f-8e63-c4f9b2a17d05
// last-edited: 2026-11-19

// Repoints rows whose file a rename MOVED ON DISK but whose new path the
// rename could not write to the database, even after retrying.
//
// The rename pipelines (metafetch writeMovedPath) record each such pair under
// organizer.RenamePathWriteFailurePrefix in the `_system` preference
// keyspace. This op reads the records and, for each one, writes new_path into
// the row when the row still holds old_path -- checked and written as ONE step
// under the row's store lock (ModifyBookFile / ModifyBook) -- and, for that
// row: (b) new_path exists on disk as the right kind of entry, (c) old_path is
// GONE from disk, and (d) no other row of the same kind already holds new_path.
// (c) and (d) close the A->B->A hazard: a row put back at A with its files back
// at A fails (c), and a B someone else now owns fails (d). A multi-file book's
// old_path is its old directory, which the rename leaves behind (sometimes with
// cover art or .nfo/.cue files in it); a directory holding no audio file counts
// as gone for (c). Nothing on disk is deleted. Anything else is skipped and the
// record is KEPT, so nothing is guessed. A record is blanked only after its
// repoint write succeeded, or when the row already holds new_path -- whatever
// (b)-(d) would say, since those guard a write that is then not needed.
//
// It never moves a file, never deletes a row, and refuses any record with a
// path under an iTunes library root. A dry run writes and clears nothing.
package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance/resources"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/tasks"
)

// repointUnrecordedRenamesConcurrency: the set is normally tiny (writes that
// failed after a disk move), and each unit is a few stats plus one row write
// per record.
const repointUnrecordedRenamesConcurrency = 4

type repointUnrecordedRenamesParams struct {
	// Apply must be explicitly true to write. Default false = report only.
	Apply bool `json:"apply"`
}

// Outcomes, one per record.
const (
	repointOutcomeRepointed    = "repointed"
	repointOutcomeWouldRepoint = "would_repoint"
	repointOutcomeAlreadyAtNew = "already_at_new_path"
	repointOutcomeITunes       = "skipped_itunes"
	repointOutcomeNewMissing   = "skipped_new_path_missing"
	repointOutcomeNewWrongKind = "skipped_new_path_wrong_kind"
	repointOutcomeRowChanged   = "skipped_row_changed"
	repointOutcomeOldPresent   = "skipped_old_path_still_present"
	repointOutcomeNewClaimed   = "skipped_new_path_claimed"
	repointOutcomeRowGone      = "skipped_row_gone"
	repointOutcomeUnreadable   = "skipped_unreadable_record"
	repointOutcomeError        = "error"
)

type repointUnrecordedRenameRow struct {
	Key        string `json:"key"`
	BookID     string `json:"book_id"`
	BookFileID string `json:"book_file_id,omitempty"`
	OldPath    string `json:"old_path"`
	NewPath    string `json:"new_path"`
	Outcome    string `json:"outcome"`
	Cleared    bool   `json:"cleared,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

type repointUnrecordedRenamesResult struct {
	Apply    bool                         `json:"apply"`
	Records  int                          `json:"records"`
	Outcomes map[string]int               `json:"outcomes"`
	Rows     []repointUnrecordedRenameRow `json:"rows"`
}

// repointUnrecordedRenamesHelp is the def's Description, which the native kind
// carries as its Help.
const repointUnrecordedRenamesHelp = "Repairs rows whose file a rename moved on disk but whose new path could not be written " +
	"to the database. Reads the records the rename pipelines leave, and for each one writes new_path " +
	"into the book_file row (or the book row) only when new_path exists on disk and the row still holds " +
	"old_path, as one atomic compare-and-write; anything else is skipped and its record kept. Dry-run by " +
	"default; apply=true writes and clears each record after its repoint succeeds. Never moves files, " +
	"never deletes rows, skips iTunes paths."

// repointUnrecordedRenamesKind is rule R3's kind for the def below.
const repointUnrecordedRenamesKind = tasks.Kind("books.maintenance.repoint-unrecorded-renames")

func (p *Plugin) repointUnrecordedRenamesDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.repoint-unrecorded-renames",
		Preview:         sdk.PreviewSpec{Params: repointUnrecordedRenamesParams{}},
		Liveness:        sdk.LivenessRunItems,
		ProgressTimeout: 10 * time.Minute,
		Plugin:          "maintenance",
		DisplayName:     "Repoint unrecorded renames",
		Description:     repointUnrecordedRenamesHelp,
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.repoint-unrecorded-renames",
		Cancellable:     true,
		Timeout:         30 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runRepointUnrecordedRenames,
	}
}

type repointRecord struct {
	key string
	rec organizer.RenamePathWriteFailure
	err error
}

func (p *Plugin) runRepointUnrecordedRenames(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	var params repointUnrecordedRenamesParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("repoint-unrecorded-renames: no store")
	}
	env := repointUnrecordedRenamesEnv{
		t6g7Env: t6g7HeadEnv(reporter, params.Apply),
		Store:   repointUnrecordedRenamesHeadStore{store},
		Writer:  store,
	}
	res, runErr := repointUnrecordedRenamesCore(ctx, params.Apply, env)
	if res.Outcomes != nil {
		if err := env.SetResult(res); err != nil {
			env.Log(slog.LevelWarn, fmt.Sprintf("could not persist result data: %v", err))
		}
	}
	return runErr
}

// repointUnrecordedRenamesHeadStore is HEAD's reads: the retired-aware list of
// book_file rows resolves through the store's capability (it fails closed).
type repointUnrecordedRenamesHeadStore struct {
	repointHeadReads
}

// repointHeadReads is the calls HEAD's store answers directly.
type repointHeadReads interface {
	GetAllPreferencesForUser(userID string) ([]database.UserPreferenceKV, error)
	LiveBookIDsAtPath(path string) ([]string, error)
	GetBookByID(id string) (*database.Book, error)
	ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error)
}

// GetBookFileByID reads the row the way HEAD's dry run always did: through the
// row's own compare-and-write with a callback that skips the write, so the row
// is read under its stripe and nothing is written.
func (s repointUnrecordedRenamesHeadStore) GetBookFileByID(bookID, fileID string) (*database.BookFile, error) {
	var got *database.BookFile
	_, err := s.ModifyBookFile(bookID, fileID, func(bf *database.BookFile) error {
		c := *bf
		got = &c
		return database.ErrSkipBookFileWrite
	})
	return got, err
}

func (s repointUnrecordedRenamesHeadStore) GetAllBookFilesCoreIncludingRetired() ([]database.BookFileCore, error) {
	return database.AllBookFilesCoreIncludingRetired(s.repointHeadReads)
}

// unrecordedRenameBase is the reads the core takes.
type unrecordedRenameBase interface {
	GetAllPreferencesForUser(userID string) ([]database.UserPreferenceKV, error)
	LiveBookIDsAtPath(path string) ([]string, error)
	GetBookFileByID(bookID, fileID string) (*database.BookFile, error)
	GetBookByID(id string) (*database.Book, error)
}

// repointUnrecordedRenamesEnv is what the core takes from its shell.
type repointUnrecordedRenamesEnv struct {
	t6g7Env
	Store unrecordedRenameStore
	// Writer is nil in a dry run: a dry run reads the rows and decides, it never
	// takes a row's write lock.
	Writer unrecordedRenameWriter
}

// repointUnrecordedRenamesNative is the native kind. Preview is the dry run
// HEAD's default was; Live repoints and clears the records. The result is the
// per-record report HEAD stored on the op row.
func (p *Plugin) repointUnrecordedRenamesNative(deps t6g7Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := t6g7Common(repointUnrecordedRenamesKind, sched)
	c.Title = "Repoint unrecorded renames"
	c.Help = repointUnrecordedRenamesHelp
	c.Effects = tasks.Writes(t6g7Res(resources.Union(t6WriterResources["ModifyBookFile"],
		t6WriterResources["ModifyBook"], t6WriterResources["SetUserPreferenceForUser"])...)...)
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = tasks.Key("books.maintenance.repoint-unrecorded-renames")
	c.Timeout = 30 * time.Minute
	c.ProgressTimeout = 10 * time.Minute
	return tasks.Task(repointUnrecordedRenamesKind, tasks.TaskSpec[json.RawMessage, repointUnrecordedRenamesResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (repointUnrecordedRenamesResult, error) {
			// HEAD's only param is apply, which the run's mode decides.
			if err := t6g7RefuseModeKeys(repointUnrecordedRenamesKind, raw); err != nil {
				return repointUnrecordedRenamesResult{}, err
			}
			if err := t6g7NeedDep(repointUnrecordedRenamesKind, "Store", deps.Store != nil); err != nil {
				return repointUnrecordedRenamesResult{}, err
			}
			live := rc.Mode() == tasks.ModeLive
			env := repointUnrecordedRenamesEnv{t6g7Env: t6g7NativeEnv(rc), Store: deps.Store}
			if live {
				w, err := t6g7NewWriter(repointUnrecordedRenamesKind, deps, rc)
				if err != nil {
					return repointUnrecordedRenamesResult{}, err
				}
				env.Writer = w
			}
			return repointUnrecordedRenamesCore(rc.Context(), live, env)
		},
	})
}

// repointUnrecordedRenamesParallelism is the op's concurrency, as HEAD's pool:
// units of records that share one new path run in one worker, so no two workers
// ever decide about the same destination.
func repointUnrecordedRenamesParallelism() tasks.Parallelism {
	return tasks.Fixed(repointUnrecordedRenamesConcurrency, "units of records that share a destination path run in one worker, so no two workers decide about the same new path; the writes are row-locked compare-and-writes")
}

// repointUnrecordedRenamesCore is the body of runRepointUnrecordedRenames.
func repointUnrecordedRenamesCore(ctx context.Context, apply bool, env repointUnrecordedRenamesEnv) (repointUnrecordedRenamesResult, error) {
	store := env.Store
	if store == nil {
		return repointUnrecordedRenamesResult{}, fmt.Errorf("repoint-unrecorded-renames: no store")
	}
	if apply && env.Writer == nil {
		return repointUnrecordedRenamesResult{}, fmt.Errorf("repoint-unrecorded-renames: no writer for a live run")
	}
	roots, err := itunesProtectedRoots()
	if err != nil {
		return repointUnrecordedRenamesResult{}, fmt.Errorf("repoint-unrecorded-renames: %w", err)
	}

	prefs, err := store.GetAllPreferencesForUser("_system")
	if err != nil {
		return repointUnrecordedRenamesResult{}, fmt.Errorf("list _system preferences: %w", err)
	}
	var records []repointRecord
	for _, pref := range prefs {
		if !strings.HasPrefix(pref.Key, organizer.RenamePathWriteFailurePrefix) || strings.TrimSpace(pref.Value) == "" {
			continue
		}
		r := repointRecord{key: pref.Key}
		r.err = json.Unmarshal([]byte(pref.Value), &r.rec)
		records = append(records, r)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].key < records[j].key })

	claims, err := bookFileClaims(store, records)
	if err != nil {
		return repointUnrecordedRenamesResult{}, fmt.Errorf("repoint-unrecorded-renames: %w", err)
	}

	res := repointUnrecordedRenamesResult{Apply: apply, Records: len(records), Outcomes: map[string]int{}}
	var mu sync.Mutex
	add := func(row repointUnrecordedRenameRow) {
		mu.Lock()
		defer mu.Unlock()
		res.Outcomes[row.Outcome]++
		res.Rows = append(res.Rows, row)
	}

	units := groupRepointRecords(records)
	runErr := env.Items(ctx, repointUnrecordedRenamesParallelism(), len(units), t6g7ItemsOptions{Collect: true}, func(ctx context.Context, i int) error {
		claimedBy := ""
		for _, r := range units[i] {
			row := repointOne(ctx, store, env.Writer, roots, r, apply, claims, claimedBy)
			if claimedBy == "" && (row.Outcome == repointOutcomeRepointed || row.Outcome == repointOutcomeWouldRepoint || row.Outcome == repointOutcomeAlreadyAtNew) {
				claimedBy = repointRowID(r.rec)
			}
			add(row)
		}
		return nil
	})

	sort.Slice(res.Rows, func(i, j int) bool { return res.Rows[i].Key < res.Rows[j].Key })
	summary := fmt.Sprintf("Unrecorded renames: %d records, outcomes %v (apply=%t)", res.Records, res.Outcomes, apply)
	env.Log(slog.LevelInfo, summary)
	return res, runErr
}

type repointUnit []repointRecord

// groupRepointRecords puts every readable record with the same row kind and
// cleaned new path into one unit, in input order. An unreadable record is a
// unit of its own.
func groupRepointRecords(records []repointRecord) []repointUnit {
	idx := map[string]int{}
	var units []repointUnit
	for _, r := range records {
		gk := "unreadable\x00" + r.key
		if r.err == nil && r.rec.NewPath != "" {
			kind := "book_file"
			if r.rec.BookFileID == "" {
				kind = "book"
			}
			gk = kind + "\x00" + filepath.Clean(r.rec.NewPath)
		}
		if i, ok := idx[gk]; ok {
			units[i] = append(units[i], r)
			continue
		}
		idx[gk] = len(units)
		units = append(units, repointUnit{r})
	}
	return units
}

// repointRowID is the ID of the row a record repoints.
func repointRowID(rec organizer.RenamePathWriteFailure) string {
	if rec.BookFileID != "" {
		return rec.BookFileID
	}
	return rec.BookID
}

// bookFileClaims maps the cleaned new path of every book_file record to the IDs
// of the book_file rows holding it, from one read of the whole core list
// (memdb-served). It is built once per run and only read afterwards, so the
// workers share it without a lock. nil when no record is for a book_file row.
//
// GetBookFileByPath cannot answer (d): book_file_path is a single-owner CRC
// index, so it names one row per path, a CRC collision with another path hides
// the row, and an entry dropped by an earlier write hides it too. A row the
// lookup missed would have been repointed onto a path another live row holds.
// book_file rows have no soft-delete marker: BookFile carries no
// MarkedForDeletion (that flag is on book rows only), and a deleted book_file
// row is removed. The list is read through AllBookFilesCoreIncludingRetired,
// NOT GetAllBookFilesCore: both arms of the latter list only LIVE rows
// (Retired == false), and a retired version's row is a claim on its path too.
// Every row listed is a claim; a row flagged Missing still holds the path and
// counts too. If the retired-inclusive reader is unavailable the call fails
// closed rather than answering from live rows only.
func bookFileClaims(store unrecordedRenameStore, records []repointRecord) (map[string][]string, error) {
	want := map[string]bool{}
	for _, r := range records {
		if r.err == nil && r.rec.BookFileID != "" && r.rec.NewPath != "" {
			want[filepath.Clean(r.rec.NewPath)] = true
		}
	}
	if len(want) == 0 {
		return nil, nil
	}
	files, err := store.GetAllBookFilesCoreIncludingRetired()
	if err != nil {
		return nil, fmt.Errorf("list book files: %w", err)
	}
	claims := map[string][]string{}
	for i := range files {
		if files[i].FilePath == "" {
			continue
		}
		if p := filepath.Clean(files[i].FilePath); want[p] {
			claims[p] = append(claims[p], files[i].ID)
		}
	}
	return claims, nil
}

// underITunes reports whether p is inside the frozen books/itunes tree or a
// configured iTunes root.
func underITunes(p string, roots []string) bool {
	if p == "" {
		return false
	}
	if pathutil.UnderFrozenITunesTree(p) {
		return true
	}
	clean := filepath.Clean(p)
	for _, root := range roots {
		if pathutil.IsWithin(clean, root) {
			return true
		}
	}
	return false
}

// unrecordedRenameStore is what one run needs of OpsStore.
type unrecordedRenameStore interface {
	unrecordedRenameBase
	GetAllBookFilesCoreIncludingRetired() ([]database.BookFileCore, error)
}

// unrecordedRenameWriter is the three writes: the compare-and-write of a
// book_file row's path, of a book row's path, and the clearing of a record.
type unrecordedRenameWriter interface {
	ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error)
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
	SetUserPreferenceForUser(userID string, key string, value string) error
}

// newPathKindOK reports whether what is on disk at the new path can back the
// row. A book_file row needs a regular file. A book row holds a regular file
// for a single-file book and a directory for a multi-file one, and the record
// cannot tell which from the path alone (a directory name such as
// "Vol. 2" has an extension), so either is accepted there. A symlink, device
// or socket never is.
func newPathKindOK(rec organizer.RenamePathWriteFailure, fi fs.FileInfo) bool {
	if rec.BookFileID != "" {
		return fi.Mode().IsRegular()
	}
	return fi.Mode().IsRegular() || fi.IsDir()
}

func repointOne(ctx context.Context, store unrecordedRenameStore, w unrecordedRenameWriter, roots []string, r repointRecord, apply bool, claims map[string][]string, claimedBy string) repointUnrecordedRenameRow {
	row := repointUnrecordedRenameRow{
		Key: r.key, BookID: r.rec.BookID, BookFileID: r.rec.BookFileID, OldPath: r.rec.OldPath, NewPath: r.rec.NewPath,
	}
	switch {
	case r.err != nil:
		row.Outcome, row.Detail = repointOutcomeUnreadable, r.err.Error()
		return row
	case r.rec.BookID == "" || r.rec.OldPath == "" || r.rec.NewPath == "":
		row.Outcome, row.Detail = repointOutcomeUnreadable, "missing book_id, old_path or new_path"
		return row
	case organizer.RenamePathWriteFailureKey(r.rec.BookID, r.rec.BookFileID) != r.key:
		row.Outcome, row.Detail = repointOutcomeUnreadable, "record body does not match its key"
		return row
	}
	if underITunes(r.rec.OldPath, roots) || underITunes(r.rec.NewPath, roots) {
		row.Outcome = repointOutcomeITunes
		return row
	}
	if err := ctx.Err(); err != nil {
		row.Outcome, row.Detail = repointOutcomeError, err.Error()
		return row
	}

	chk := repointCheck{rec: r.rec, claims: claims, claimedBy: claimedBy}
	if r.rec.BookFileID == "" {
		// A store read, so it is made here and not inside ModifyBook's callback,
		// which runs under the store's write lock and could re-enter it. Its
		// error is applied only to a row that still holds old_path.
		chk.bookIDs, chk.bookIDsErr = store.LiveBookIDsAtPath(r.rec.NewPath)
		row = repointBookRow(store, w, r, row, chk, apply)
	} else {
		row = repointBookFileRow(store, w, r, row, chk, apply)
	}
	// Clear only after a successful write, or when the row already holds
	// new_path (a write that committed but reported failure). Never in a dry run.
	if apply && (row.Outcome == repointOutcomeRepointed || row.Outcome == repointOutcomeAlreadyAtNew) {
		if err := organizer.ClearRenamePathWriteFailure(w, r.rec.BookID, r.rec.BookFileID); err != nil {
			row.Detail = strings.TrimSpace(row.Detail + " record could not be cleared: " + err.Error())
		} else {
			row.Cleared = true
		}
	}
	return row
}

// repointBlocker is a precondition that refuses a repoint of a row that still
// holds old_path. outcome is empty when nothing blocks.
type repointBlocker struct {
	outcome, detail string
}

// repointCheck evaluates (b), (c) and (d) of the file comment for a row that
// still holds old_path. It runs inside the row's compare-and-write callback and
// makes no store call there: the disk checks are os.Lstat / a directory walk,
// and the claim data (the book_file claims map, the live book IDs at new_path)
// was read before the lock. The disk checks are made at write time; the claim
// data is a snapshot from just before it.
type repointCheck struct {
	rec        organizer.RenamePathWriteFailure
	claims     map[string][]string
	claimedBy  string // row an earlier record in this unit took new_path for
	bookIDs    []string
	bookIDsErr error
}

func (c repointCheck) blockers() (repointBlocker, error) {
	// (b) new_path is on disk as the right kind of entry.
	fi, err := os.Lstat(c.rec.NewPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return repointBlocker{outcome: repointOutcomeNewMissing}, nil
	case err != nil:
		return repointBlocker{}, fmt.Errorf("stat new path: %w", err)
	case !newPathKindOK(c.rec, fi):
		return repointBlocker{repointOutcomeNewWrongKind, "new path is " + fi.Mode().Type().String()}, nil
	}
	// (c) old_path is gone.
	present, detail, err := oldPathPresent(c.rec)
	if err != nil {
		return repointBlocker{}, err
	}
	if present {
		return repointBlocker{repointOutcomeOldPresent, detail}, nil
	}
	// (d) no other row of the same kind holds new_path.
	own := repointRowID(c.rec)
	if c.claimedBy != "" && c.claimedBy != own {
		return repointBlocker{repointOutcomeNewClaimed, "an earlier record in this run took new path for " + c.claimedBy}, nil
	}
	if c.rec.BookFileID != "" {
		for _, id := range c.claims[filepath.Clean(c.rec.NewPath)] {
			if id != own {
				return repointBlocker{repointOutcomeNewClaimed, "book_file " + id + " holds new path"}, nil
			}
		}
		return repointBlocker{}, nil
	}
	if c.bookIDsErr != nil {
		return repointBlocker{}, fmt.Errorf("look up books at new path: %w", c.bookIDsErr)
	}
	for _, id := range c.bookIDs {
		if id != own {
			return repointBlocker{repointOutcomeNewClaimed, "book " + id + " holds new path"}, nil
		}
	}
	return repointBlocker{}, nil
}

// oldPathPresent reports whether old_path is still on disk. Only "does not
// exist" counts as gone; a permission or I/O error proves nothing and is
// returned (record kept).
//
// A book row's old_path is a directory for a multi-file book. The rename moves
// the audio files out and leaves the directory -- nothing removes it, and the
// rename-only path can leave cover.jpg, .nfo or .cue files in it -- so a
// directory holding no audio file anywhere below it is what a completed move
// looks like, and counts as gone. It is not deleted. Any audio file in it
// (the files may have been moved back) counts as present.
func oldPathPresent(rec organizer.RenamePathWriteFailure) (bool, string, error) {
	fi, err := os.Lstat(rec.OldPath)
	if errors.Is(err, fs.ErrNotExist) {
		return false, "", nil
	}
	if err != nil {
		return false, "", fmt.Errorf("stat old path: %w", err)
	}
	const onDisk = "old path exists on disk too; the files may have been moved back"
	if rec.BookFileID != "" || !fi.IsDir() {
		return true, onDisk, nil
	}
	audio := config.SupportedExtensionSet()
	found := ""
	err = filepath.WalkDir(rec.OldPath, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if !d.IsDir() && audio.MatchPath(p) {
			found = p
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return false, "", fmt.Errorf("walk old directory: %w", err)
	}
	if found != "" {
		return true, "old directory still holds audio file " + found, nil
	}
	return false, "", nil
}

// repointStep is the decision both a live write and a dry run make about one
// row, given the path the row holds NOW: it records the outcome and reports
// stop when the row must not be written.
func repointStep(r repointRecord, row *repointUnrecordedRenameRow, chk repointCheck, held string) (stop bool) {
	switch {
	case held == r.rec.NewPath:
		row.Outcome = repointOutcomeAlreadyAtNew
		return true
	case held != r.rec.OldPath:
		row.Outcome, row.Detail = repointOutcomeRowChanged, "row holds "+held
		return true
	}
	blk, err := chk.blockers()
	if err != nil {
		row.Outcome, row.Detail = repointOutcomeError, err.Error()
		return true
	}
	if blk.outcome != "" {
		row.Outcome, row.Detail = blk.outcome, blk.detail
		return true
	}
	return false
}

// repointFinish folds a row write's (or dry read's) result into the row.
func repointFinish(row repointUnrecordedRenameRow, ran bool, err error) repointUnrecordedRenameRow {
	switch {
	case err != nil:
		row.Outcome, row.Detail = repointOutcomeError, err.Error()
	case !ran:
		row.Outcome = repointOutcomeRowGone
	}
	return row
}

// repointBookFileRow is one atomic compare-and-write under the row's
// book_file stripe: the old_path compare is made on the row as stored at write
// time, never on an earlier read, and the preconditions are checked only once
// it has passed. A dry run (no writer) decides the same way on a read of the
// row and takes no lock.
func repointBookFileRow(store unrecordedRenameStore, w unrecordedRenameWriter, r repointRecord, row repointUnrecordedRenameRow, chk repointCheck, apply bool) repointUnrecordedRenameRow {
	if !apply {
		bf, err := store.GetBookFileByID(r.rec.BookID, r.rec.BookFileID)
		if err != nil || bf == nil {
			return repointFinish(row, bf != nil, err)
		}
		if !repointStep(r, &row, chk, bf.FilePath) {
			row.Outcome = repointOutcomeWouldRepoint
		}
		return row
	}
	ran := false
	_, err := w.ModifyBookFile(r.rec.BookID, r.rec.BookFileID, func(bf *database.BookFile) error {
		ran = true
		if repointStep(r, &row, chk, bf.FilePath) {
			return database.ErrSkipBookFileWrite
		}
		bf.FilePath = r.rec.NewPath
		if r.rec.NewITunesPath != "" {
			bf.ITunesPath = r.rec.NewITunesPath
		}
		row.Outcome = repointOutcomeRepointed
		return nil
	})
	return repointFinish(row, ran, err)
}

// repointBookRow is the book-row twin, under the book's write stripe.
func repointBookRow(store unrecordedRenameStore, w unrecordedRenameWriter, r repointRecord, row repointUnrecordedRenameRow, chk repointCheck, apply bool) repointUnrecordedRenameRow {
	if !apply {
		b, err := store.GetBookByID(r.rec.BookID)
		if err != nil || b == nil {
			return repointFinish(row, b != nil, err)
		}
		if !repointStep(r, &row, chk, b.FilePath) {
			row.Outcome = repointOutcomeWouldRepoint
		}
		return row
	}
	ran := false
	_, err := w.ModifyBook(r.rec.BookID, func(fresh *database.Book) error {
		ran = true
		if repointStep(r, &row, chk, fresh.FilePath) {
			return database.ErrSkipBookWrite
		}
		fresh.FilePath = r.rec.NewPath
		row.Outcome = repointOutcomeRepointed
		return nil
	})
	return repointFinish(row, ran, err)
}
