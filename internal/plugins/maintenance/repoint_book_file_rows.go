// file: internal/plugins/maintenance/repoint_book_file_rows.go
// version: 1.0.0
// guid: 9f2c4e71-3b8a-4d06-a5e2-7c1d8b6f0a39
// last-edited: 2026-09-26

// Package maintenance — op maintenance.repoint-book-file-rows.
//
// Repoints named MISSING book_file rows at a named file that is on disk and
// that no row owns, keeping the row's ID, track, book and history. On
// 2026-09-26 several books had a missing row while the exact audio file sat at
// another path nobody had a row for.
//
// WHY A NEW OP. The existing repairs derive the new path themselves and cannot
// be pointed at an explicit (row, path) pair:
//   - missing-file-repoint only handles its fixed shape rules;
//   - recover-missing-files needs a unique, unclaimed size match.
//
// GATES, per repoint {row_id, book_id, new_path[, allow_hash_mismatch]}. Each is
// checked at plan time and re-checked with os.Stat (and a fresh hash) at apply
// time, and a row that fails any of them is refused, never guessed:
//   - the row exists under book_id, and its recorded file is MISSING on disk
//     (only fs.ErrNotExist counts; any other stat error refuses);
//   - new_path exists and is a regular file (os.Stat follows a symlink; a
//     symlink is reported as a warning, not refused, because rows organized
//     with OrganizeMethod "symlink" point at one);
//   - neither new_path nor the row's old path is under an iTunes root or the
//     frozen books/itunes tree;
//   - no book_file row owns new_path. Every row counts, a Missing one too. The
//     claim set comes from one GetAllBookFilesCore pass, because
//     GetBookFileByPath is a single-owner CRC index that can hide a row; the
//     path lookup is consulted as well;
//   - new_path's size equals the row's recorded FileSize. A row with no size
//     (0) is refused unless it has a file_hash and new_path's hash equals it;
//   - a row whose hash differs from new_path's (same size) is refused unless
//     the item sets allow_hash_mismatch — a tag edit changes the hash — and the
//     mismatch is reported either way.
//
// The hash is filehash.BookFileHash, the one digest book_file.file_hash may
// hold (whole-file SHA-256 at or below 100 MB, sampled above). No audio decode.
//
// APPLY, per planned row, under the scan stand-down: ModifyBookFile reads the
// stored row under its stripe lock, re-checks that it is the row the plan saw,
// journals the WHOLE stored row as an OperationChange ("book_file_repoint")
// BEFORE changing anything (a journal failure writes nothing), then sets
// FilePath, clears Missing and stores the new hash. ModifyBookFile rewrites the
// path index and memdb and recomputes the book; the op recomputes the book once
// more itself so a failed recompute is reported, not only logged.
//
// It never deletes a row or a book, never changes Book.FilePath and never
// touches a file on disk. DRY RUN unless {"dry_run": false}.
package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

const (
	rbfOpID   = "maintenance.repoint-book-file-rows"
	rbfOpName = "repoint-book-file-rows"

	// rbfMaxRepoints bounds one run. The op is for hand-picked rows.
	rbfMaxRepoints = 500

	// rbfChangeType is the journal ChangeType. OldValue is the whole row as
	// stored before the repoint; NewValue is the new path.
	rbfChangeType = "book_file_repoint"

	rbfStatusPlanned    = "planned"
	rbfStatusRefused    = "refused"
	rbfStatusRepointed  = "repointed"
	rbfStatusFailed     = "failed"
	rbfStatusNotAttempt = "not_attempted"

	rbfGatePass = "pass"
	rbfGateFail = "fail"
	rbfGateSkip = "not_checked"
)

// errRBFRowChanged is returned from the ModifyBookFile callback when the stored
// row is no longer the row the plan approved. It is deliberately not
// database.ErrSkipBookFileWrite, which ModifyBookFile turns into a nil error.
var errRBFRowChanged = errors.New("row changed since it was planned")

// rbfStat and rbfHash are variables only so a test can change the disk between
// the plan and the apply; production never reassigns them.
var (
	rbfStat = os.Stat
	rbfHash = filehash.BookFileHash
)

// rbfRepoint is one requested repoint.
type rbfRepoint struct {
	RowID             string `json:"row_id"`
	BookID            string `json:"book_id"`
	NewPath           string `json:"new_path"`
	AllowHashMismatch bool   `json:"allow_hash_mismatch,omitempty"`
}

type rbfParams struct {
	Repoints    []rbfRepoint `json:"repoints"`
	DryRunSnake *bool        `json:"dry_run,omitempty"`
	DryRun      *bool        `json:"dryRun,omitempty"`
}

func (p rbfParams) dryRun() (bool, error) {
	return opmode.ResolveDryRun(rbfOpID, p.DryRunSnake, p.DryRun)
}

// rbfGates records each gate's outcome for one item, so the preview says which
// gate passed, which failed and which was never reached.
type rbfGates struct {
	RowFound       string `json:"row_found"`
	OldPathMissing string `json:"old_path_missing"`
	NewPathRegular string `json:"new_path_regular_file"`
	NotITunes      string `json:"not_itunes"`
	NoOtherOwner   string `json:"no_other_owner"`
	SizeMatch      string `json:"size_match"`
	HashMatch      string `json:"hash_match"`
}

func newRBFGates() rbfGates {
	return rbfGates{rbfGateSkip, rbfGateSkip, rbfGateSkip, rbfGateSkip, rbfGateSkip, rbfGateSkip, rbfGateSkip}
}

// rbfResult is one repoint's decision and, for a live run, its outcome.
type rbfResult struct {
	RowID     string   `json:"row_id"`
	BookID    string   `json:"book_id"`
	BookTitle string   `json:"book_title,omitempty"`
	OldPath   string   `json:"old_path,omitempty"`
	NewPath   string   `json:"new_path"`
	Track     int      `json:"track,omitempty"`
	RowSize   int64    `json:"row_size_bytes"`
	DiskSize  int64    `json:"disk_size_bytes,omitempty"`
	RowHash   string   `json:"row_hash,omitempty"`
	DiskHash  string   `json:"disk_hash,omitempty"`
	Gates     rbfGates `json:"gates"`
	// HashMismatchAllowed is true when the sizes match, the hashes differ and
	// the item set allow_hash_mismatch.
	HashMismatchAllowed bool `json:"hash_mismatch_allowed,omitempty"`
	// StubBookIDs are live books whose Book.FilePath is new_path. A book's
	// file_path is not a book_file row, so it does not block the repoint; it
	// is reported so the owner can deal with the stub afterwards.
	StubBookIDs []string `json:"stub_book_ids_at_new_path,omitempty"`
	// OldPathOtherRows are other rows (any book) still on the row's old path.
	// They stay where they are.
	OldPathOtherRows []string `json:"old_path_other_rows,omitempty"`
	Status           string   `json:"status"`
	Reason           string   `json:"reason,omitempty"`
	Warnings         []string `json:"warnings,omitempty"`
	Error            string   `json:"error,omitempty"`
}

// rbfBookState is a book's identity and aggregates as the op saw them.
type rbfBookState struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	FilePath  string `json:"file_path"`
	Duration  int    `json:"duration"`
	FileCount int    `json:"file_count"`
	Missing   int    `json:"missing_rows"`
}

type rbfReport struct {
	DryRun    bool           `json:"dry_run"`
	Planned   int            `json:"planned"`
	Refused   int            `json:"refused"`
	Repointed int            `json:"repointed"`
	Failed    int            `json:"failed"`
	Repoints  []rbfResult    `json:"repoints"`
	Before    []rbfBookState `json:"books_before"`
	After     []rbfBookState `json:"books_after,omitempty"`
}

func (r *rbfReport) summary() string {
	mode := "LIVE"
	if r.DryRun {
		mode = "DRY RUN"
	}
	return fmt.Sprintf("%s: %d planned, %d refused, %d repointed, %d failed", mode, r.Planned, r.Refused, r.Repointed, r.Failed)
}

// rbfStore is the narrow store the op needs. Every method is on
// database.Store, so the production indexedStore satisfies it.
type rbfStore interface {
	GetBookByID(id string) (*database.Book, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetBookFileByID(bookID, fileID string) (*database.BookFile, error)
	GetBookFileByPath(filePath string) (*database.BookFile, error)
	GetAllBookFilesCore() ([]database.BookFileCore, error)
	LiveBookIDsAtPath(path string) ([]string, error)
	ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error)
	RecomputeBookAggregates(bookID string) error
	CreateOperationChange(change *database.OperationChange) error
}

var _ rbfStore = database.Store(nil)

type rbfEnv struct {
	store       rbfStore
	itunesRoots []string
	scan        ScanController
}

func (p *Plugin) repointBookFileRowsDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          rbfOpID,
		DisplayName: "Repoint missing book_file rows at a named file",
		Description: "Repoints named MISSING book_file rows at a file on disk that no row owns: " +
			"{\"repoints\":[{\"row_id\":..., \"book_id\":..., \"new_path\":...}]}. Keeps the row's id, track, book " +
			"and history. Refuses unless the row's file is missing, new_path is a regular file outside iTunes that " +
			"no row owns, and its size equals the row's (or, for a row with no size, its hash equals the row's). " +
			"A differing hash at an equal size needs \"allow_hash_mismatch\":true on the item. Journals the old row " +
			"first. Never deletes anything and never touches files. DRY-RUN BY DEFAULT: {\"dry_run\":false} to write.",
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  rbfOpID,
		Writes:          []sdk.Resource{sdk.ResBookFiles},
		// A re-run is safe: a repointed row's file is present, so it is refused
		// at the old-path-missing gate.
		ResumePolicy: sdk.ResumeDrop,
		Cancellable:  true,
		Liveness:     sdk.LivenessManual,
		Capabilities: []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:          p.runRepointBookFileRows,
	}
}

func (p *Plugin) runRepointBookFileRows(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params rbfParams
	if err := decodeStrictParams(raw, &params); err != nil {
		return fmt.Errorf("%s: decode params: %w", rbfOpName, err)
	}
	if _, err := params.dryRun(); err != nil {
		return err
	}
	ops := p.deps.OpsStore()
	if ops == nil {
		return fmt.Errorf("database not initialized")
	}
	store, ok := any(ops).(rbfStore)
	if !ok {
		return fmt.Errorf("%s: store cannot repoint book_file rows; refusing to run", rbfOpName)
	}
	// Fail closed: without resolvable iTunes roots the op cannot prove a path
	// is outside them.
	roots, err := merge.ITunesProtectedRoots(config.Snapshot().ITunes)
	if err != nil {
		return fmt.Errorf("%s: resolve iTunes roots: %w", rbfOpName, err)
	}
	report, runErr := repointBookFileRows(ctx, rbfEnv{store: store, itunesRoots: roots, scan: p.deps}, params, reporter)
	if report != nil {
		if sErr := registry.ReporterSetResult(reporter, report); sErr != nil {
			reporter.Logger().Warn(rbfOpName+": result not persisted", "err", sErr)
		}
		reporter.Logger().Info(rbfOpName+" complete", "summary", report.summary())
	}
	return runErr
}

// rbfClaims maps each cleaned new_path in the request to the IDs of every row
// holding it, from ONE pass over every row's core. Missing rows count: a row
// flagged missing still names the path.
func rbfClaims(store rbfStore, want map[string]bool) (map[string][]string, error) {
	cores, err := store.GetAllBookFilesCore()
	if err != nil {
		return nil, fmt.Errorf("%s: list book_file rows: %w", rbfOpName, err)
	}
	claims := map[string][]string{}
	for i := range cores {
		if cores[i].FilePath == "" {
			continue
		}
		if p := filepath.Clean(cores[i].FilePath); want[p] {
			claims[p] = append(claims[p], cores[i].ID)
		}
	}
	return claims, nil
}

// repointBookFileRows plans every repoint and, on a live run, applies the
// planned ones. The list is hand-picked and small (rbfMaxRepoints), so it runs
// sequentially; the one whole-library read is the claim pass, done once per
// phase.
func repointBookFileRows(ctx context.Context, env rbfEnv, params rbfParams, reporter sdk.Reporter) (*rbfReport, error) {
	dryRun, err := params.dryRun()
	if err != nil {
		return nil, err
	}
	if len(params.Repoints) == 0 {
		return nil, fmt.Errorf("%s: repoints must not be empty", rbfOpName)
	}
	if len(params.Repoints) > rbfMaxRepoints {
		return nil, fmt.Errorf("%s: %d repoints exceeds the limit of %d", rbfOpName, len(params.Repoints), rbfMaxRepoints)
	}

	// Both the new paths (to find owners) and the old paths (to report other
	// rows left on them) are resolved in the one claim pass.
	want := map[string]bool{}
	for _, rp := range params.Repoints {
		if rp.NewPath != "" {
			want[filepath.Clean(rp.NewPath)] = true
		}
	}
	oldPaths := map[string]string{}
	for _, rp := range params.Repoints {
		if rp.RowID == "" || rp.BookID == "" {
			continue
		}
		row, rErr := env.store.GetBookFileByID(rp.BookID, rp.RowID)
		if rErr != nil {
			return nil, fmt.Errorf("%s: read row %s: %w", rbfOpName, rp.RowID, rErr)
		}
		if row != nil && row.FilePath != "" {
			oldPaths[rp.RowID] = row.FilePath
			want[filepath.Clean(row.FilePath)] = true
		}
	}
	claims, err := rbfClaims(env.store, want)
	if err != nil {
		return nil, err
	}

	report := &rbfReport{DryRun: dryRun}
	books := map[string]*database.Book{}
	seenRow := map[string]bool{}
	seenPath := map[string]string{}
	var touched []string
	touchedSet := map[string]bool{}

	for i, rp := range params.Repoints {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		rbfProgress(reporter, i, len(params.Repoints), "planning row "+rp.RowID)
		r, planErr := rbfPlanOne(env, rp, claims, books, seenRow, seenPath)
		if planErr != nil {
			return report, planErr
		}
		if rp.RowID != "" {
			seenRow[rp.RowID] = true
		}
		if rp.NewPath != "" {
			if _, dup := seenPath[filepath.Clean(rp.NewPath)]; !dup {
				seenPath[filepath.Clean(rp.NewPath)] = rp.RowID
			}
		}
		if r.Status == rbfStatusPlanned {
			report.Planned++
			if !touchedSet[r.BookID] {
				touchedSet[r.BookID] = true
				touched = append(touched, r.BookID)
			}
		} else {
			report.Refused++
		}
		report.Repoints = append(report.Repoints, r)
	}
	report.Before = rbfBookStates(env.store, touched)

	if dryRun || report.Planned == 0 {
		return report, nil
	}

	holderID, held, release, sdErr := acquireScanStandDownForApply(ctx, env.scan, reporter, rbfOpName+" apply")
	if sdErr != nil {
		return report, fmt.Errorf("%s: scan stand-down: %w", rbfOpName, sdErr)
	}
	defer release()

	opID := registry.ReporterOpID(reporter)
	if opID == "" {
		reporter.Logger().Warn(rbfOpName + ": no operation id; journal rows will be uncorrelated")
	}

	// The claim set is re-read for the apply: a row created at a new path
	// between the plan and now must block the repoint.
	applyWant := map[string]bool{}
	for _, r := range report.Repoints {
		if r.Status == rbfStatusPlanned {
			applyWant[filepath.Clean(r.NewPath)] = true
		}
	}
	applyClaims, err := rbfClaims(env.store, applyWant)
	if err != nil {
		return report, err
	}

	for i := range report.Repoints {
		r := &report.Repoints[i]
		if r.Status != rbfStatusPlanned {
			continue
		}
		rbfProgress(reporter, i, len(report.Repoints), "repointing row "+r.RowID)
		if ctx.Err() != nil || scanStandDownLostForApply(env.scan, holderID, held) {
			r.Status = rbfStatusNotAttempt
			r.Error = "run canceled or scan stand-down lost before this row"
			continue
		}
		if reason := rbfApplyOne(env, r, applyClaims, opID); reason != "" {
			r.Status = rbfStatusFailed
			r.Error = reason
			report.Failed++
			continue
		}
		r.Status = rbfStatusRepointed
		report.Repointed++
	}

	report.After = rbfBookStates(env.store, touched)
	return report, nil
}

// rbfProgress stamps liveness. A failed progress write is logged, not fatal.
func rbfProgress(reporter sdk.Reporter, done, total int, msg string) {
	if reporter == nil {
		return
	}
	if err := reporter.UpdateProgress(done, total, msg); err != nil {
		reporter.Logger().Warn(rbfOpName+": progress not recorded", "err", err)
	}
}

// rbfIsITunes reports whether p is under a configured iTunes root or the frozen
// books/itunes tree (either spelling).
func rbfIsITunes(env rbfEnv, p string) bool {
	return underITunes(p, env.itunesRoots) || underITunesTree(p)
}

// rbfPlanOne evaluates every gate for one item. It returns an error only for a
// store read failure; a failed gate is a refused result.
func rbfPlanOne(env rbfEnv, rp rbfRepoint, claims map[string][]string, books map[string]*database.Book,
	seenRow map[string]bool, seenPath map[string]string) (rbfResult, error) {
	r := rbfResult{RowID: rp.RowID, BookID: rp.BookID, NewPath: rp.NewPath, Gates: newRBFGates(), Status: rbfStatusRefused}
	refuse := func(reason string) (rbfResult, error) {
		r.Status = rbfStatusRefused
		r.Reason = reason
		return r, nil
	}
	if rp.RowID == "" || rp.BookID == "" || rp.NewPath == "" {
		return refuse("row_id, book_id and new_path are required")
	}
	if !filepath.IsAbs(rp.NewPath) {
		return refuse("new_path must be absolute")
	}
	if seenRow[rp.RowID] {
		return refuse("row named more than once in this run")
	}
	if other, dup := seenPath[filepath.Clean(rp.NewPath)]; dup {
		return refuse("new_path is already the target of row " + other + " in this run")
	}

	row, err := env.store.GetBookFileByID(rp.BookID, rp.RowID)
	if err != nil {
		return r, fmt.Errorf("%s: read row %s: %w", rbfOpName, rp.RowID, err)
	}
	if row == nil {
		r.Gates.RowFound = rbfGateFail
		return refuse("row not found under book " + rp.BookID)
	}
	if row.FilePath == "" {
		r.Gates.RowFound = rbfGateFail
		return refuse("row has no file_path")
	}
	r.Gates.RowFound = rbfGatePass
	r.OldPath = row.FilePath
	r.Track = row.TrackNumber
	r.RowSize = row.FileSize
	r.RowHash = row.FileHash

	book, ok := books[rp.BookID]
	if !ok {
		book, err = env.store.GetBookByID(rp.BookID)
		if err != nil {
			return r, fmt.Errorf("%s: read book %s: %w", rbfOpName, rp.BookID, err)
		}
		books[rp.BookID] = book
	}
	if book != nil {
		r.BookTitle = book.Title
		if isSoftDeleted(book) {
			r.Warnings = append(r.Warnings, "the row's book is soft-deleted")
		}
	}
	if filepath.Clean(row.FilePath) == filepath.Clean(rp.NewPath) {
		return refuse("row already points at new_path")
	}

	// Other rows on the old path: reported only.
	for _, id := range claims[filepath.Clean(row.FilePath)] {
		if id != row.ID {
			r.OldPathOtherRows = append(r.OldPathOtherRows, id)
		}
	}
	// Stub books whose Book.FilePath is new_path: reported only.
	if ids, lErr := env.store.LiveBookIDsAtPath(rp.NewPath); lErr != nil {
		r.Warnings = append(r.Warnings, "stub-book check unavailable: "+lErr.Error())
	} else {
		for _, id := range ids {
			if id != rp.BookID {
				r.StubBookIDs = append(r.StubBookIDs, id)
			}
		}
		if len(r.StubBookIDs) > 0 {
			r.Warnings = append(r.Warnings, fmt.Sprintf(
				"%d other book(s) have file_path = new_path (not a book_file row, so not an owner); they are left unchanged",
				len(r.StubBookIDs)))
		}
	}

	if reason := rbfCheckDisk(env, rp, row, &r, claims); reason != "" {
		return refuse(reason)
	}
	r.Status = rbfStatusPlanned
	return r, nil
}

// rbfCheckDisk runs the gates that read the disk and the claim set. It is
// shared by the plan and the apply, so the apply re-checks exactly what the
// plan checked. It fills r's gate outcomes, sizes and disk hash and returns ""
// when every gate passes, else the refusal reason.
func rbfCheckDisk(env rbfEnv, rp rbfRepoint, row *database.BookFile, r *rbfResult, claims map[string][]string) string {
	// Old file must be missing. Only "does not exist" counts: a permission or
	// I/O error is not proof the file is gone.
	if _, err := rbfStat(row.FilePath); err == nil {
		r.Gates.OldPathMissing = rbfGateFail
		return "the row's file is present on disk: " + row.FilePath
	} else if !errors.Is(err, fs.ErrNotExist) {
		r.Gates.OldPathMissing = rbfGateFail
		return "cannot prove the row's file is missing: " + err.Error()
	}
	r.Gates.OldPathMissing = rbfGatePass

	info, err := rbfStat(rp.NewPath)
	if err != nil {
		r.Gates.NewPathRegular = rbfGateFail
		return "new_path cannot be read: " + err.Error()
	}
	if !info.Mode().IsRegular() {
		r.Gates.NewPathRegular = rbfGateFail
		return "new_path is not a regular file"
	}
	r.Gates.NewPathRegular = rbfGatePass
	if li, lErr := os.Lstat(rp.NewPath); lErr == nil && li.Mode()&fs.ModeSymlink != 0 {
		r.addWarning("new_path is a symlink")
	}
	r.DiskSize = info.Size()

	for _, p := range []string{rp.NewPath, row.FilePath} {
		if rbfIsITunes(env, p) {
			r.Gates.NotITunes = rbfGateFail
			return "itunes-path: " + p
		}
	}
	r.Gates.NotITunes = rbfGatePass

	var owners []string
	for _, id := range claims[filepath.Clean(rp.NewPath)] {
		if id != row.ID {
			owners = append(owners, id)
		}
	}
	if len(owners) == 0 {
		// The single-owner path index as a second opinion.
		f, lErr := env.store.GetBookFileByPath(rp.NewPath)
		if lErr != nil {
			r.Gates.NoOtherOwner = rbfGateFail
			return "path lookup failed: " + lErr.Error()
		}
		if f != nil && f.ID != row.ID {
			owners = append(owners, f.ID)
		}
	}
	if len(owners) > 0 {
		r.Gates.NoOtherOwner = rbfGateFail
		sort.Strings(owners)
		return fmt.Sprintf("new_path is owned by row(s) %v", owners)
	}
	r.Gates.NoOtherOwner = rbfGatePass

	sizeKnown := row.FileSize > 0
	if sizeKnown {
		if info.Size() != row.FileSize {
			r.Gates.SizeMatch = rbfGateFail
			return fmt.Sprintf("size differs: row %d bytes, new_path %d bytes", row.FileSize, info.Size())
		}
		r.Gates.SizeMatch = rbfGatePass
	} else if row.FileHash == "" {
		r.Gates.SizeMatch = rbfGateFail
		return "the row has no recorded size and no file_hash; nothing proves new_path is its file"
	}

	hash, err := rbfHash(rp.NewPath)
	if err != nil {
		r.Gates.HashMatch = rbfGateFail
		return "hash new_path: " + err.Error()
	}
	r.DiskHash = hash
	switch {
	case row.FileHash == "":
		// Size matched; there is no hash to compare.
		r.Gates.HashMatch = rbfGateSkip
	case hash == row.FileHash:
		r.Gates.HashMatch = rbfGatePass
		if !sizeKnown {
			r.Gates.SizeMatch = rbfGateSkip
		}
	case !sizeKnown:
		r.Gates.HashMatch = rbfGateFail
		return "the row has no recorded size and new_path's hash differs from the row's"
	case rp.AllowHashMismatch:
		r.Gates.HashMatch = rbfGateFail
		r.HashMismatchAllowed = true
		r.addWarning("hash differs from the row's at an equal size; allowed by allow_hash_mismatch")
	default:
		r.Gates.HashMatch = rbfGateFail
		return "hash differs from the row's at an equal size (a tag edit changes the hash); set allow_hash_mismatch to accept"
	}
	return ""
}

func (r *rbfResult) addWarning(w string) {
	for _, have := range r.Warnings {
		if have == w {
			return
		}
	}
	r.Warnings = append(r.Warnings, w)
}

// rbfApplyOne re-checks every gate against the disk and the fresh claim set,
// then repoints the row, journaling the stored row first. It returns "" on
// success, else why nothing was written (or, for a failed recompute, that the
// row WAS written but the book's totals may be stale).
func rbfApplyOne(env rbfEnv, r *rbfResult, claims map[string][]string, opID string) string {
	row, err := env.store.GetBookFileByID(r.BookID, r.RowID)
	if err != nil {
		return "re-read row: " + err.Error()
	}
	if row == nil {
		return "row vanished since the plan"
	}
	planned := *r
	rp := rbfRepoint{RowID: r.RowID, BookID: r.BookID, NewPath: r.NewPath, AllowHashMismatch: r.HashMismatchAllowed}
	if reason := rbfCheckDisk(env, rp, row, r, claims); reason != "" {
		return "apply-time re-check: " + reason
	}
	if r.DiskHash != planned.DiskHash || r.DiskSize != planned.DiskSize || row.FilePath != planned.OldPath {
		return "apply-time re-check: new_path or the row changed since the plan"
	}

	newHash, newSize := r.DiskHash, r.DiskSize
	updated, err := env.store.ModifyBookFile(r.BookID, r.RowID, func(f *database.BookFile) error {
		if f.FilePath != planned.OldPath || f.FileSize != planned.RowSize || f.FileHash != planned.RowHash {
			return errRBFRowChanged
		}
		blob, mErr := json.Marshal(f)
		if mErr != nil {
			return fmt.Errorf("serialize row for the undo ledger: %w", mErr)
		}
		// Journal first: a failure here returns before any field changes, so
		// ModifyBookFile writes nothing.
		if jErr := env.store.CreateOperationChange(&database.OperationChange{
			ID:          ulid.Make().String(),
			OperationID: opID,
			BookID:      f.BookID,
			ChangeType:  rbfChangeType,
			FieldName:   f.ID,
			OldValue:    string(blob), // the ENTIRE row, so the repoint can be replayed back
			NewValue:    planned.NewPath,
		}); jErr != nil {
			return fmt.Errorf("journal row: %w", jErr)
		}
		f.FilePath = planned.NewPath
		f.Missing = false
		f.FileHash = newHash
		if f.FileSize == 0 {
			f.FileSize = newSize
		}
		return nil
	})
	if err != nil {
		return err.Error()
	}
	if updated == nil {
		return "row vanished before the write"
	}
	if rErr := env.store.RecomputeBookAggregates(r.BookID); rErr != nil {
		r.addWarning("row repointed; book aggregate recompute failed: " + rErr.Error())
	}
	return ""
}

// rbfBookStates reads each touched book's aggregates. A book that cannot be
// read is left out of the list rather than failing the report.
func rbfBookStates(store rbfStore, ids []string) []rbfBookState {
	out := make([]rbfBookState, 0, len(ids))
	for _, id := range ids {
		b, err := store.GetBookByID(id)
		if err != nil || b == nil {
			continue
		}
		st := rbfBookState{ID: b.ID, Title: b.Title, FilePath: b.FilePath}
		if b.Duration != nil {
			st.Duration = *b.Duration
		}
		if rows, rErr := store.GetBookFiles(id); rErr == nil {
			st.FileCount = len(rows)
			for i := range rows {
				if rows[i].Missing {
					st.Missing++
				}
			}
		}
		out = append(out, st)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
