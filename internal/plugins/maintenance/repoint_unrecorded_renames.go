// file: internal/plugins/maintenance/repoint_unrecorded_renames.go
// version: 1.0.0
// guid: 5a0e7c38-2d94-4b1f-8e63-c4f9b2a17d05
// last-edited: 2026-09-14

// Repoints rows whose file a rename MOVED ON DISK but whose new path the
// rename could not write to the database, even after retrying.
//
// The rename pipelines (metafetch writeMovedPath) record each such pair under
// organizer.RenamePathWriteFailurePrefix in the `_system` preference
// keyspace. This op reads the records and, for each one, writes new_path into
// the row when (a) new_path exists on disk and (b) the row still holds
// old_path. Anything else is skipped and the record is KEPT, so nothing is
// guessed. A record is blanked only after its repoint write succeeded.
//
// It never moves a file, never deletes a row, and refuses any record with a
// path under an iTunes library root.
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
	repointOutcomeITunes       = "skipped_itunes"
	repointOutcomeNewMissing   = "skipped_new_path_missing"
	repointOutcomeRowChanged   = "skipped_row_changed"
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
			"old_path; anything else is skipped and its record kept. Dry-run by default; apply=true writes and " +
			"clears each record after its repoint succeeds. Never moves files, never deletes rows, skips iTunes paths.",
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

// errRepointRowChanged aborts the book-row ModifyBook when the row no longer
// holds old_path.
var errRepointRowChanged = errors.New("row no longer holds old_path")

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
	// as it is for every merge-family write.
	roots, err := merge.ITunesProtectedRoots(config.AppConfig.ITunes)
	if err != nil {
		return fmt.Errorf("repoint-unrecorded-renames: %w", err)
	}

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

	// Each record names a distinct row (book_file ID, or the book row for
	// BookFileID ""), so parallel workers never write the same row twice.
	runErr := opsregistry.RunItems(ctx, reporter, records, func(ctx context.Context, r repointRecord) error {
		add(repointOne(ctx, store, roots, r, params.Apply))
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
	GetBookFiles(bookID string) ([]database.BookFile, error)
	UpdateBookFile(id string, file *database.BookFile) error
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
	SetUserPreferenceForUser(userID string, key string, value string) error
}

func repointOne(ctx context.Context, store unrecordedRenameStore, roots []string, r repointRecord, apply bool) repointUnrecordedRenameRow {
	row := repointUnrecordedRenameRow{
		Key: r.key, BookID: r.rec.BookID, BookFileID: r.rec.BookFileID, OldPath: r.rec.OldPath, NewPath: r.rec.NewPath,
	}
	if r.err != nil || r.rec.BookID == "" || r.rec.OldPath == "" || r.rec.NewPath == "" {
		row.Outcome = repointOutcomeUnreadable
		if r.err != nil {
			row.Detail = r.err.Error()
		}
		return row
	}
	if underITunes(r.rec.OldPath, roots) || underITunes(r.rec.NewPath, roots) {
		row.Outcome = repointOutcomeITunes
		return row
	}
	if _, err := os.Lstat(r.rec.NewPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			row.Outcome = repointOutcomeNewMissing
		} else {
			row.Outcome, row.Detail = repointOutcomeError, err.Error()
		}
		return row
	}
	if err := ctx.Err(); err != nil {
		row.Outcome, row.Detail = repointOutcomeError, err.Error()
		return row
	}

	if r.rec.BookFileID == "" {
		return repointBookRow(store, r, row, apply)
	}
	return repointBookFileRow(store, r, row, apply)
}

func repointBookFileRow(store unrecordedRenameStore, r repointRecord, row repointUnrecordedRenameRow, apply bool) repointUnrecordedRenameRow {
	files, err := store.GetBookFiles(r.rec.BookID)
	if err != nil {
		row.Outcome, row.Detail = repointOutcomeError, err.Error()
		return row
	}
	var bf *database.BookFile
	for i := range files {
		if files[i].ID == r.rec.BookFileID {
			bf = &files[i]
			break
		}
	}
	switch {
	case bf == nil:
		row.Outcome = repointOutcomeRowGone
		return row
	case bf.FilePath != r.rec.OldPath:
		row.Outcome, row.Detail = repointOutcomeRowChanged, "row holds "+bf.FilePath
		return row
	case !apply:
		row.Outcome = repointOutcomeWouldRepoint
		return row
	}
	// UpdateBookFile replaces the whole row, so mutate the full row just read.
	bf.FilePath = r.rec.NewPath
	if r.rec.NewITunesPath != "" {
		bf.ITunesPath = r.rec.NewITunesPath
	}
	if err := store.UpdateBookFile(bf.ID, bf); err != nil {
		row.Outcome, row.Detail = repointOutcomeError, err.Error()
		return row
	}
	return clearAfterRepoint(store, r, row)
}

func repointBookRow(store unrecordedRenameStore, r repointRecord, row repointUnrecordedRenameRow, apply bool) repointUnrecordedRenameRow {
	var held string
	ran := false
	_, err := store.ModifyBook(r.rec.BookID, func(fresh *database.Book) error {
		ran = true
		held = fresh.FilePath
		if fresh.FilePath != r.rec.OldPath {
			return errRepointRowChanged
		}
		if !apply {
			return database.ErrSkipBookWrite
		}
		fresh.FilePath = r.rec.NewPath
		return nil
	})
	switch {
	case errors.Is(err, errRepointRowChanged):
		row.Outcome, row.Detail = repointOutcomeRowChanged, "row holds "+held
		return row
	case err != nil:
		row.Outcome, row.Detail = repointOutcomeError, err.Error()
		return row
	case !ran:
		// ModifyBook ran no closure: the book row does not exist.
		row.Outcome = repointOutcomeRowGone
		return row
	case !apply:
		row.Outcome = repointOutcomeWouldRepoint
		return row
	}
	return clearAfterRepoint(store, r, row)
}

func clearAfterRepoint(store unrecordedRenameStore, r repointRecord, row repointUnrecordedRenameRow) repointUnrecordedRenameRow {
	row.Outcome = repointOutcomeRepointed
	if err := store.SetUserPreferenceForUser("_system", r.key, ""); err != nil {
		row.Detail = "repointed, but the record could not be cleared: " + err.Error()
	}
	return row
}
