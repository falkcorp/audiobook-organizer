// file: internal/plugins/maintenance/repoint_unrecorded_renames.go
// version: 1.2.0
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
// row's store lock (ModifyBookFile / ModifyBook). Anything else is skipped and
// the record is KEPT, so nothing is guessed. A record is blanked only after its
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
	// AllowModifiedSinceRecord repoints a book_file row even when it was
	// written after the record was made (it still must hold old_path). Off by
	// default: a later write means someone else touched the row, so an
	// operator should look at those rows first (outcome
	// skipped_modified_since_record).
	AllowModifiedSinceRecord bool `json:"allow_modified_since_record"`
}

// Outcomes, one per record.
const (
	repointOutcomeRepointed     = "repointed"
	repointOutcomeWouldRepoint  = "would_repoint"
	repointOutcomeAlreadyAtNew  = "already_at_new_path"
	repointOutcomeITunes        = "skipped_itunes"
	repointOutcomeNewMissing    = "skipped_new_path_missing"
	repointOutcomeNewWrongKind  = "skipped_new_path_wrong_kind"
	repointOutcomeRowChanged    = "skipped_row_changed"
	repointOutcomeModifiedSince = "skipped_modified_since_record"
	repointOutcomeRowGone       = "skipped_row_gone"
	repointOutcomeUnreadable    = "skipped_unreadable_record"
	repointOutcomeError         = "error"
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
	recordedAt, tErr := time.Parse(time.RFC3339Nano, r.rec.RecordedAt)
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
	case tErr != nil:
		row.Outcome, row.Detail = repointOutcomeUnreadable, "recorded_at: "+tErr.Error()
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

	if r.rec.BookFileID == "" {
		row = repointBookRow(store, r, row, params.Apply)
	} else {
		row = repointBookFileRow(store, r, row, recordedAt, params)
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

// repointBookFileRow is one atomic compare-and-write under the row's
// book_file stripe: every precondition is checked on the row as stored at
// write time, never on an earlier read.
//
// Modified-since uses BookFile.UpdatedAt, which every book_file write sets
// (updateBookFileLocked, PatchBookFileFields).
func repointBookFileRow(store unrecordedRenameStore, r repointRecord, row repointUnrecordedRenameRow, recordedAt time.Time, params repointUnrecordedRenamesParams) repointUnrecordedRenameRow {
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
		case !params.AllowModifiedSinceRecord && bf.UpdatedAt.After(recordedAt):
			row.Outcome, row.Detail = repointOutcomeModifiedSince, "row updated_at "+bf.UpdatedAt.UTC().Format(time.RFC3339Nano)
			return database.ErrSkipBookFileWrite
		case !params.Apply:
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
//
// No modified-since check here: Book.UpdatedAt is bumped by the aggregate
// recompute that follows every book_file write and by any metadata edit, so a
// later bump is routine after a rename and would strand every book-row
// record. The old_path compare inside ModifyBook is the guard.
func repointBookRow(store unrecordedRenameStore, r repointRecord, row repointUnrecordedRenameRow, apply bool) repointUnrecordedRenameRow {
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
