// file: internal/plugins/maintenance/repoint_unrecorded_renames.go
// version: 1.4.0
// guid: 5a0e7c38-2d94-4b1f-8e63-c4f9b2a17d05
// last-edited: 2026-09-14

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

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
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

func (p *Plugin) repointUnrecordedRenamesDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.repoint-unrecorded-renames",
		Liveness:        sdk.LivenessRunItems,
		ProgressTimeout: 10 * time.Minute,
		Plugin:          "maintenance",
		DisplayName:     "Repoint unrecorded renames",
		Description: "Repairs rows whose file a rename moved on disk but whose new path could not be written " +
			"to the database. Reads the records the rename pipelines leave, and for each one writes new_path " +
			"into the book_file row (or the book row) only when new_path exists on disk and the row still holds " +
			"old_path, as one atomic compare-and-write; anything else is skipped and its record kept. Dry-run by " +
			"default; apply=true writes and clears each record after its repoint succeeds. Never moves files, " +
			"never deletes rows, skips iTunes paths.",
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
	// Fail closed: an iTunes sync with no configured roots is an error here,
	// as it is for every merge-family write (merge.GuardITunesProtected).
	roots, err := merge.ITunesProtectedRoots(config.Snapshot().ITunes)
	if err != nil {
		return fmt.Errorf("repoint-unrecorded-renames: %w", err)
	}

	// The preference store has no list-by-prefix read, so this loads the
	// `_system` keyspace and filters, as clear-apply-rename-failures does.
	prefs, err := store.GetAllPreferencesForUser("_system")
	if err != nil {
		return fmt.Errorf("list _system preferences: %w", err)
	}
	var records []repointRecord
	for _, pref := range prefs {
		// A blank value is an already-cleared record.
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
		return fmt.Errorf("repoint-unrecorded-renames: %w", err)
	}

	res := repointUnrecordedRenamesResult{Apply: params.Apply, Records: len(records), Outcomes: map[string]int{}}
	var mu sync.Mutex
	add := func(row repointUnrecordedRenameRow) {
		mu.Lock()
		defer mu.Unlock()
		res.Outcomes[row.Outcome]++
		res.Rows = append(res.Rows, row)
	}

	// Records are grouped so that every record aiming at one new path, for one
	// row kind, runs on ONE worker in key order. Different units therefore name
	// different rows (a record's key is its row) AND different new paths, so two
	// workers can neither write the same row nor both pass the (d) claim check
	// for the same new path; within a unit, the record after a repoint sees the
	// path as claimed.
	runErr := opsregistry.RunItems(ctx, reporter, groupRepointRecords(records), func(ctx context.Context, unit repointUnit) error {
		claimedBy := ""
		for _, r := range unit {
			row := repointOne(ctx, store, roots, r, params, claims, claimedBy)
			if claimedBy == "" && (row.Outcome == repointOutcomeRepointed || row.Outcome == repointOutcomeWouldRepoint || row.Outcome == repointOutcomeAlreadyAtNew) {
				claimedBy = repointRowID(r.rec)
			}
			add(row)
		}
		return nil
	}, opsregistry.RunItemsOptions{Concurrency: repointUnrecordedRenamesConcurrency, ErrMode: opsregistry.ErrModeCollect})

	sort.Slice(res.Rows, func(i, j int) bool { return res.Rows[i].Key < res.Rows[j].Key })
	summary := fmt.Sprintf("Unrecorded renames: %d records, outcomes %v (apply=%t)", res.Records, res.Outcomes, params.Apply)
	_ = reporter.Log(slog.LevelInfo, summary)
	if err := opsregistry.ReporterSetResult(reporter, res); err != nil {
		_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("could not persist result data: %v", err))
	}
	return runErr
}

// repointUnit is the records one worker processes in order.
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
// row is removed, so both GetAllBookFilesCore arms (memdb walk, Pebble scan)
// list only live rows. Every row listed is a claim; a row flagged Missing still
// holds the path and counts too.
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
	files, err := store.GetAllBookFilesCore()
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
	if config.UnderFrozenITunesTree(p) {
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
	GetAllBookFilesCore() ([]database.BookFileCore, error)
	LiveBookIDsAtPath(path string) ([]string, error)
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

func repointOne(ctx context.Context, store unrecordedRenameStore, roots []string, r repointRecord, params repointUnrecordedRenamesParams, claims map[string][]string, claimedBy string) repointUnrecordedRenameRow {
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
		row = repointBookRow(store, r, row, chk, params.Apply)
	} else {
		row = repointBookFileRow(store, r, row, chk, params.Apply)
	}
	// Clear only after a successful write, or when the row already holds
	// new_path (a write that committed but reported failure). Never in a dry run.
	if params.Apply && (row.Outcome == repointOutcomeRepointed || row.Outcome == repointOutcomeAlreadyAtNew) {
		if err := organizer.ClearRenamePathWriteFailure(store, r.rec.BookID, r.rec.BookFileID); err != nil {
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

// repointBookFileRow is one atomic compare-and-write under the row's
// book_file stripe: the old_path compare is made on the row as stored at write
// time, never on an earlier read, and the preconditions are checked only once
// it has passed.
func repointBookFileRow(store unrecordedRenameStore, r repointRecord, row repointUnrecordedRenameRow, chk repointCheck, apply bool) repointUnrecordedRenameRow {
	ran := false
	_, err := store.ModifyBookFile(r.rec.BookID, r.rec.BookFileID, func(bf *database.BookFile) error {
		ran = true
		switch {
		case bf.FilePath == r.rec.NewPath:
			row.Outcome = repointOutcomeAlreadyAtNew
			return database.ErrSkipBookFileWrite
		case bf.FilePath != r.rec.OldPath:
			row.Outcome, row.Detail = repointOutcomeRowChanged, "row holds "+bf.FilePath
			return database.ErrSkipBookFileWrite
		}
		if blk, err := chk.blockers(); err != nil {
			row.Outcome, row.Detail = repointOutcomeError, err.Error()
			return database.ErrSkipBookFileWrite
		} else if blk.outcome != "" {
			row.Outcome, row.Detail = blk.outcome, blk.detail
			return database.ErrSkipBookFileWrite
		}
		if !apply {
			row.Outcome = repointOutcomeWouldRepoint
			return database.ErrSkipBookFileWrite
		}
		bf.FilePath = r.rec.NewPath
		if r.rec.NewITunesPath != "" {
			bf.ITunesPath = r.rec.NewITunesPath
		}
		row.Outcome = repointOutcomeRepointed
		return nil
	})
	switch {
	case err != nil:
		row.Outcome, row.Detail = repointOutcomeError, err.Error()
	case !ran:
		row.Outcome = repointOutcomeRowGone
	}
	return row
}

// repointBookRow is the book-row twin, under the book's write stripe.
func repointBookRow(store unrecordedRenameStore, r repointRecord, row repointUnrecordedRenameRow, chk repointCheck, apply bool) repointUnrecordedRenameRow {
	ran := false
	_, err := store.ModifyBook(r.rec.BookID, func(fresh *database.Book) error {
		ran = true
		switch {
		case fresh.FilePath == r.rec.NewPath:
			row.Outcome = repointOutcomeAlreadyAtNew
			return database.ErrSkipBookWrite
		case fresh.FilePath != r.rec.OldPath:
			row.Outcome, row.Detail = repointOutcomeRowChanged, "row holds "+fresh.FilePath
			return database.ErrSkipBookWrite
		}
		if blk, err := chk.blockers(); err != nil {
			row.Outcome, row.Detail = repointOutcomeError, err.Error()
			return database.ErrSkipBookWrite
		} else if blk.outcome != "" {
			row.Outcome, row.Detail = blk.outcome, blk.detail
			return database.ErrSkipBookWrite
		}
		if !apply {
			row.Outcome = repointOutcomeWouldRepoint
			return database.ErrSkipBookWrite
		}
		fresh.FilePath = r.rec.NewPath
		row.Outcome = repointOutcomeRepointed
		return nil
	})
	switch {
	case err != nil:
		row.Outcome, row.Detail = repointOutcomeError, err.Error()
	case !ran:
		row.Outcome = repointOutcomeRowGone
	}
	return row
}
