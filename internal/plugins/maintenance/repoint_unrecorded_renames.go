// file: internal/plugins/maintenance/repoint_unrecorded_renames.go
// version: 1.3.0
// guid: 5a0e7c38-2d94-4b1f-8e63-c4f9b2a17d05
// last-edited: 2026-09-14

// Repoints rows whose file a rename MOVED ON DISK but whose new path the
// rename could not write to the database, even after retrying.
//
// The rename pipelines (metafetch writeMovedPath) record each such pair under
// organizer.RenamePathWriteFailurePrefix in the `_system` preference
// keyspace. This op reads the records and, for each one, writes new_path into
// the row when (a) new_path exists on disk as the right kind of entry and
// (b) the row still holds old_path, checked and written as ONE step under the
// row's store lock (ModifyBookFile / ModifyBook), (c) old_path is GONE from
// disk, and (d) no other row of the same kind already holds new_path. (c) and
// (d) close the A->B->A hazard: a row put back at A with its files back at A
// fails (c), and a B someone else now owns fails (d). Anything else is skipped
// and the record is KEPT, so nothing is guessed. A record is blanked only after its
// repoint write succeeded, or when the row already holds new_path.
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
// failed after a disk move), and each item is one stat plus one row write.
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

	res := repointUnrecordedRenamesResult{Apply: params.Apply, Records: len(records), Outcomes: map[string]int{}}
	var mu sync.Mutex
	add := func(row repointUnrecordedRenameRow) {
		mu.Lock()
		defer mu.Unlock()
		res.Outcomes[row.Outcome]++
		res.Rows = append(res.Rows, row)
	}

	// Each record names a distinct row (its key is book ID + book_file ID, or
	// the book row), so parallel workers never write the same row twice.
	runErr := opsregistry.RunItems(ctx, reporter, records, func(ctx context.Context, r repointRecord) error {
		add(repointOne(ctx, store, roots, r, params))
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

// unrecordedRenameStore is what one repoint needs of OpsStore.
type unrecordedRenameStore interface {
	GetBookFileByPath(filePath string) (*database.BookFile, error)
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

func repointOne(ctx context.Context, store unrecordedRenameStore, roots []string, r repointRecord, params repointUnrecordedRenamesParams) repointUnrecordedRenameRow {
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
	fi, err := os.Lstat(r.rec.NewPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		row.Outcome = repointOutcomeNewMissing
		return row
	case err != nil:
		row.Outcome, row.Detail = repointOutcomeError, err.Error()
		return row
	case !newPathKindOK(r.rec, fi):
		row.Outcome, row.Detail = repointOutcomeNewWrongKind, "new path is "+fi.Mode().Type().String()
		return row
	}
	if err := ctx.Err(); err != nil {
		row.Outcome, row.Detail = repointOutcomeError, err.Error()
		return row
	}

	blocked, err := repointBlockers(store, r.rec)
	if err != nil {
		row.Outcome, row.Detail = repointOutcomeError, err.Error()
		return row
	}

	if r.rec.BookFileID == "" {
		row = repointBookRow(store, r, row, blocked, params.Apply)
	} else {
		row = repointBookFileRow(store, r, row, blocked, params.Apply)
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

// repointBlocker is a content precondition that refuses a repoint of a row
// that still holds old_path. outcome is empty when nothing blocks.
type repointBlocker struct {
	outcome, detail string
}

// repointBlockers checks (c) and (d) of the file comment. They are read
// before the row's compare-and-write rather than inside it: the callback runs
// under the store's write lock, and a store read from inside it could
// re-enter that lock. The disk check and the claim check are therefore a
// snapshot taken just before the write; the old_path compare is the atomic
// part. They are applied only to a row that still holds old_path, so a row
// already at new_path still clears its record.
func repointBlockers(store unrecordedRenameStore, rec organizer.RenamePathWriteFailure) (repointBlocker, error) {
	// (c) Only "does not exist" counts as gone; a permission or I/O error
	// proves nothing about the path and is returned as an error (record kept).
	if _, err := os.Lstat(rec.OldPath); err == nil {
		return repointBlocker{repointOutcomeOldPresent, "old path exists on disk too; the files may have been moved back"}, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return repointBlocker{}, fmt.Errorf("stat old path: %w", err)
	}
	// (d) Another row of the same kind already at new_path.
	if rec.BookFileID != "" {
		// book_file_path is a single-owner index (last writer wins), so it
		// names at most one row; it is the only by-path book_file read the
		// store has.
		other, err := store.GetBookFileByPath(rec.NewPath)
		if err != nil {
			return repointBlocker{}, fmt.Errorf("look up book_file at new path: %w", err)
		}
		if other != nil && other.ID != rec.BookFileID {
			return repointBlocker{repointOutcomeNewClaimed, "book_file " + other.ID + " holds new path"}, nil
		}
		return repointBlocker{}, nil
	}
	ids, err := store.LiveBookIDsAtPath(rec.NewPath)
	if err != nil {
		return repointBlocker{}, fmt.Errorf("look up books at new path: %w", err)
	}
	for _, id := range ids {
		if id != rec.BookID {
			return repointBlocker{repointOutcomeNewClaimed, "book " + id + " holds new path"}, nil
		}
	}
	return repointBlocker{}, nil
}

// repointBookFileRow is one atomic compare-and-write under the row's
// book_file stripe: the old_path compare is made on the row as stored at write
// time, never on an earlier read.
func repointBookFileRow(store unrecordedRenameStore, r repointRecord, row repointUnrecordedRenameRow, blocked repointBlocker, apply bool) repointUnrecordedRenameRow {
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
		case blocked.outcome != "":
			row.Outcome, row.Detail = blocked.outcome, blocked.detail
			return database.ErrSkipBookFileWrite
		case !apply:
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
func repointBookRow(store unrecordedRenameStore, r repointRecord, row repointUnrecordedRenameRow, blocked repointBlocker, apply bool) repointUnrecordedRenameRow {
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
		case blocked.outcome != "":
			row.Outcome, row.Detail = blocked.outcome, blocked.detail
			return database.ErrSkipBookWrite
		case !apply:
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
