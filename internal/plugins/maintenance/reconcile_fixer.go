// file: internal/plugins/maintenance/reconcile_fixer.go
// version: 2.0.0
// guid: 4e4de696-418a-4936-9e25-23a68ff19562
// last-edited: 2026-10-14

package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// reconcileFixerID is the Repairs-lane id of the missing-files fixer. It takes
// over from the Reconcile tab of the old dedup page (the tab is retired in
// 03-PR10): the latest scan's matches, applying picked ones, and the books
// with no candidate file.
const reconcileFixerID = "reconcile.missing-files"

var rmLog = logger.New("repairs.reconcile-missing-files")

// Row class of a book the scan found no file for. A match row's class is the
// scan's match type (hash, original_hash, filename), so every per-type count
// in the lane lists its rows.
const rmClassNoCandidate = "no_candidate"

// Skip kinds of a missing-files row (the framework adds its own guard kinds;
// iTunes uses repairs.SkipITunes).
const (
	// rmSkipNoCandidate: the scan found no untracked file for the book. The
	// reason is exactly "no candidate file".
	rmSkipNoCandidate = "no_candidate"
	// rmSkipGone: the book is gone or retired since the scan.
	rmSkipGone = "book_gone"
	// rmSkipPathChanged: the book's path is no longer the one the scan found
	// missing (another writer, or an earlier apply, repointed it).
	rmSkipPathChanged = "path_changed"
	// rmSkipNotMissing: the book's file is on disk again.
	rmSkipNotMissing = "not_missing"
	// rmSkipFileGone: the matched file is no longer on disk.
	rmSkipFileGone = "file_gone"
	// rmSkipFileTracked: the matched file now belongs to another book (its
	// path, or a book_file row at it).
	rmSkipFileTracked = "file_tracked"
	// rmSkipNoFileRow: the book has no book_file row at its path, or more
	// than one, so there is no single row to repoint.
	rmSkipNoFileRow = "no_file_row"
	// rmSkipOwnRowAtMatch: the book already has a book_file row at the
	// matched file; repointing its row at the old path there would make two
	// rows of one book at one path.
	rmSkipOwnRowAtMatch = "own_row_at_match"
	// rmSkipIndexWarming: the file index (memdb) is warming up, so who holds
	// the matched file cannot be told yet. Transient (Row.RetryLater).
	rmSkipIndexWarming = "index_warming"
	// rmSkipError: something the decision needs could not be read.
	rmSkipError = "error"
)

// rmReasonIndexWarming is the plan's refusal, and a re-plan's hold, while the
// file index is warming up.
const rmReasonIndexWarming = "file index warming up; retry in a few minutes"

// Skip reasons shown to the owner, by kind.
const (
	rmReasonNoCandidate = "no candidate file"
	rmReasonITunes      = "iTunes"
)

// reconcileFixer lists the latest saved reconcile scan as Repairs rows and
// applies a picked match by repointing the book at the matched file.
//
// PLAN SOURCE. The latest saved scan, picked by the rule GET
// /operations/reconcile/scan/latest uses (ServerDeps.LatestReconcileScan,
// served by the server's pickLatestReconcileScan over recentReconcileScans):
// the newest reconcile.scan run that completed with a result that decodes,
// read on the canonical def id so the nightly runs recorded under the retired
// maintenance.reconcile-scan id count (04-P14c, D27). A newer run that has not
// completed is named in the plan log but does not hide the scan. With no
// usable scan the plan fails with a message to run "Match missing files"
// first, rather than listing nothing, which would read as "no file is
// missing".
//
// ROWS. One row per book: each scan match (book -> untracked file, its match
// type, confidence and score) and each book the scan found no file for. The
// scan matches a book at most once and a file at most once, and unmatched
// books are the broken books it did not match, so the row count is the
// Reconcile tab's matches plus its unmatched books for the same scan.
//
//   - A match row is applicable only while, read now: the book is live, its
//     path is still the one the scan found missing and that path does not
//     exist (fs.ErrNotExist; an existing folder is present, and any other
//     stat error is an error row, never "missing"), the book has exactly one
//     book_file row at that path and none at the matched file (a second row
//     there would duplicate it), the matched file is a regular file on disk,
//     and no other book holds it (no live book at that path and no other
//     book's book_file row at it; the scan's own "untracked" test reads book
//     paths only). Every one of those answers, and the matched file's size
//     and modification time, is in the fingerprint, so a re-plan at apply
//     refuses a row that drifted.
//   - A book with no candidate file is a skipped row with the reason "no
//     candidate file", so the count stays clickable.
//   - iTunes: a row whose book is an iTunes book, or whose matched file is
//     an iTunes file, is skipped with the reason "iTunes". The predicate is
//     duplicate-copies' (itunesCopyWhy) over the book's path, its iTunes path
//     and ids, its book_file rows and external ids AND the matched file's
//     path: the scan walks the iTunes Media/Audiobooks folder for candidates,
//     and the framework guard reads only the book's own paths.
//   - While the file index (memdb) is warming up the "who holds this file"
//     read cannot be complete: a plan then fails with "file index warming
//     up; retry in a few minutes", and a re-plan at apply holds the row
//     retry_later (Row.RetryLater) instead of guessing.
//
// Written Evaluate-style for the ops v3 port: evaluate decides ONE candidate
// from reads it makes itself and never writes. Plan reads the scan once and
// evaluates every candidate; Replan evaluates the planned candidate, carried
// in the row's State, without reading the scan again (a newer scan does not
// invalidate a plan; drift in the library does).
//
// APPLY (owner decision 2026-10-14: the fixer writes directly; it no longer
// hands its picks to reconcile.apply). Right before writing, Apply reads again
// who holds the matched file (the strict book_file read and the live-book
// read), the book, and the iTunes predicate, and checks the file is still the
// one planned (size and modification time); any change refuses the row with
// nothing written. Then, through the Writer (scan stand-down held, fenced,
// journaled first and voided on refusal):
//
//  1. the book's path, as a restorable book_path_update, compare-and-set:
//     the book must still be live and still point at the scan's old path;
//  2. the book_file row at the old path, as a restorable
//     book_file_repoint_location (Writer.RepointBookFile), compare-and-set on
//     the row's location; nothing moves on disk.
//
// A refusal of step 2 after step 1 is reported partially_applied. No
// book_file row is ever deleted or created. Reverting the apply operation
// puts the book's path and the row back.
type reconcileFixer struct{ p *Plugin }

func newReconcileFixer(p *Plugin) *reconcileFixer { return &reconcileFixer{p: p} }

var (
	_ repairs.Fixer            = (*reconcileFixer)(nil)
	_ repairs.PermissionScoped = (*reconcileFixer)(nil)
)

func (f *reconcileFixer) ID() string    { return reconcileFixerID }
func (f *reconcileFixer) Title() string { return "Missing files" }
func (f *reconcileFixer) Description() string {
	return "Books whose file has gone missing, from the latest reconcile scan (\"Match missing files\"): each " +
		"match to an untracked file on disk with its match type and confidence, and the books the scan found " +
		"no candidate file for (listed as skipped). A match is offered only while the book is still missing " +
		"its file, the matched file is still on disk and no other book holds it; iTunes books and iTunes files " +
		"are skipped. Applying a row points the book, and its file row, at the matched file; nothing moves on " +
		"disk and no file row is deleted. Reverting the apply operation puts both back."
}

// RequiredPermissions implements repairs.PermissionScoped: repointing books
// at files is what the reconcile apply op does, and that op is settings.manage
// (owner decision 2026-10-14), above the lane's default library.edit_metadata.
func (f *reconcileFixer) RequiredPermissions() []auth.Permission {
	return []auth.Permission{auth.PermSettingsManage}
}

// rmCandidate is one book of the scan: its match, or none. It is the row's
// State, so a re-plan decides the same candidate without reading the scan.
type rmCandidate struct {
	BookID     string  `json:"book_id"`
	Title      string  `json:"title"`
	OldPath    string  `json:"old_path"`
	NewPath    string  `json:"new_path,omitempty"`
	MatchType  string  `json:"match_type,omitempty"`
	Confidence string  `json:"confidence,omitempty"`
	Score      float64 `json:"score,omitempty"`
	// ScanOpID and ScanCompleted name the scan the candidate came from, for
	// the row's evidence. They are not decision inputs.
	ScanOpID      string `json:"scan_op_id"`
	ScanCompleted string `json:"scan_completed,omitempty"`
}

func (c rmCandidate) hasMatch() bool { return c.NewPath != "" }

// rmDecision is what Apply writes for one fresh row, carried in Row.Detail.
type rmDecision struct {
	bookID, oldPath, newPath string
	// newFile is the matched file as evaluate saw it; Apply refuses a file
	// that changed since.
	newFile rmFileState
	// fileID is the book's book_file row at oldPath; was its location as
	// read, to the location Apply writes.
	fileID  string
	was, to database.BookFileLocation
}

// rmReader is what evaluate reads: the book, its book_file rows and external
// ids (the iTunes predicate), and who holds the matched file's path.
// BookFilesAtPath (through database.BookFileRowsAtPathStrict) is the complete
// "which rows claim this path" read; OpsStore does not declare it, so the
// store is asserted (reader).
type rmReader interface {
	GetBookByID(id string) (*database.Book, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetExternalIDsForBook(bookID string) ([]database.ExternalIDMapping, error)
	LiveBookIDsAtPath(path string) ([]string, error)
	database.BookFilePathLookup
}

// reader returns the store evaluate reads. Production's OpsStore() is the
// server's database.Store, which satisfies rmReader (asserted at compile time
// in reconcile_fixer_test.go). A store that does not refuses the fixer.
func (f *reconcileFixer) reader() (rmReader, error) {
	store := f.p.deps.OpsStore()
	if store == nil {
		return nil, errors.New("database not initialized")
	}
	rd, ok := store.(rmReader)
	if !ok {
		return nil, fmt.Errorf("%s: store %T cannot tell who holds a file path; refusing", reconcileFixerID, store)
	}
	return rd, nil
}

// rmCandidates turns a scan into one candidate per book: its matches, then
// its unmatched books. A book listed twice (the scan never does this) keeps
// its first entry, so row ids stay unique.
func rmCandidates(scan ReconcileScan) []rmCandidate {
	completed := ""
	if scan.CompletedAt != nil {
		completed = scan.CompletedAt.UTC().Format("2006-01-02 15:04 UTC")
	}
	seen := map[string]bool{}
	var out []rmCandidate
	add := func(c rmCandidate) {
		if c.BookID == "" {
			return
		}
		if seen[c.BookID] {
			rmLog.Warn("%s: scan %s lists book %s more than once; keeping its first entry",
				reconcileFixerID, logger.SanitizeLogValue(scan.OpID), logger.SanitizeLogValue(c.BookID))
			return
		}
		seen[c.BookID] = true
		c.ScanOpID, c.ScanCompleted = scan.OpID, completed
		out = append(out, c)
	}
	for _, m := range scan.Preview.Matches {
		add(rmCandidate{BookID: m.BookID, Title: m.BookTitle, OldPath: m.OldPath, NewPath: m.NewPath,
			MatchType: m.MatchType, Confidence: m.Confidence, Score: m.Score})
	}
	for _, u := range scan.Preview.UnmatchedBooks {
		add(rmCandidate{BookID: u.BookID, Title: u.Title, OldPath: u.FilePath})
	}
	return out
}

// rmRowID is a book's row id. The scan lists a book at most once, so the
// book names the row.
func rmRowID(bookID string) string { return "missing:" + bookID }

// Plan reads the latest saved scan once and decides every candidate with
// evaluate on a bounded pool: each makes a few reads and two stats. While the
// file index is warming up it fails rather than offer rows it cannot check.
func (f *reconcileFixer) Plan(ctx context.Context, _ json.RawMessage, rep registry.Reporter) ([]repairs.Row, error) {
	scan, err := f.p.deps.LatestReconcileScan()
	if err != nil {
		return nil, fmt.Errorf("%s: read the latest reconcile scan: %w", reconcileFixerID, err)
	}
	if scan.Preview == nil {
		msg := "no completed reconcile scan to read; run \"Match missing files\" and try again"
		if scan.AttemptOpID != "" {
			msg += fmt.Sprintf(" (the newest scan, %s, is %s)", scan.AttemptOpID, scan.AttemptStatus)
		}
		return nil, fmt.Errorf("%s: %s", reconcileFixerID, msg)
	}
	rd, err := f.reader()
	if err != nil {
		return nil, err
	}
	cands := rmCandidates(scan)
	res := repairs.NewPathResolver()
	rows := make([]repairs.Row, len(cands))
	var done atomic.Int64
	// Each worker writes only rows[i] for its own i; candidates are
	// book-disjoint (rmCandidates), and evaluate writes nothing.
	runErr := registry.RunItems(ctx, rep, indexesOf(len(cands)), func(ictx context.Context, i int) error {
		defer done.Add(1)
		row, err := f.evaluate(ictx, rd, res, cands[i])
		if err != nil {
			row = rmErrorRow(cands[i], err)
		}
		rows[i] = row
		return nil
	}, registry.RunItemsOptions{
		Concurrency: titleRepairWorkers(),
		ErrMode:     registry.ErrModeCollect,
		// Label runs inside the workers: it reads only the atomic.
		Label: func(_, total int) string { return fmt.Sprintf("Missing-file books %d/%d", done.Load(), total) },
	})
	if runErr != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	applicable := 0
	for _, r := range rows {
		if r.RetryLater {
			return nil, fmt.Errorf("%s: %s", reconcileFixerID, rmReasonIndexWarming)
		}
		if r.Applicable() {
			applicable++
		}
	}
	if rep != nil {
		msg := fmt.Sprintf("%s: reconcile scan %s: %d matches, %d books with no candidate file; %d rows, %d applicable",
			reconcileFixerID, scan.OpID, len(scan.Preview.Matches), len(scan.Preview.UnmatchedBooks), len(rows), applicable)
		if scan.AttemptOpID != "" {
			msg += fmt.Sprintf("; a newer scan %s is %s and was not read", scan.AttemptOpID, scan.AttemptStatus)
		}
		_ = rep.Log(slog.LevelInfo, msg)
	}
	return rows, nil
}

// Replan decides the planned candidate (its State) again with fresh reads.
func (f *reconcileFixer) Replan(ctx context.Context, _ json.RawMessage, planned repairs.Row, _ registry.Reporter) (repairs.Row, error) {
	var c rmCandidate
	if err := json.Unmarshal(planned.State, &c); err != nil {
		return repairs.Row{}, fmt.Errorf("%s: row %s: unreadable plan state: %w", reconcileFixerID, planned.RowID, err)
	}
	rd, err := f.reader()
	if err != nil {
		return repairs.Row{}, err
	}
	row, err := f.evaluate(ctx, rd, repairs.NewPathResolver(), c)
	if err != nil {
		return repairs.Row{}, err
	}
	if row.RowID != planned.RowID {
		return repairs.Row{}, fmt.Errorf("%s: candidate re-planned as %s, not %s", reconcileFixerID, row.RowID, planned.RowID)
	}
	return row, nil
}

// rmFileState is the matched file as evaluate found it on disk.
type rmFileState struct {
	exists bool
	size   int64
	mtime  time.Time
}

// same reports whether a and b are the same file state (time.Equal, not ==).
func (a rmFileState) same(b rmFileState) bool {
	return a.exists == b.exists && a.size == b.size && a.mtime.Equal(b.mtime)
}

// rmNewFile stats the matched file: present only as a regular file.
func rmNewFile(path string) rmFileState {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return rmFileState{}
	}
	return rmFileState{exists: true, size: fi.Size(), mtime: fi.ModTime()}
}

// rmOldPathMissing says whether the book's path is gone. Only fs.ErrNotExist
// is "missing": an existing folder (a folder book) or file is present, and
// any other stat error (a permission error, an I/O error on a share) is
// returned, because it does not say the file is gone.
func rmOldPathMissing(path string) (bool, error) {
	_, err := os.Stat(path)
	switch {
	case err == nil:
		return false, nil
	case errors.Is(err, fs.ErrNotExist):
		return true, nil
	default:
		return false, err
	}
}

// evaluate decides one candidate. It reads the book, its book_file rows and
// external ids, who holds the matched path, and stats both paths; it never
// writes. An error is returned only when ctx is done or the state cannot be
// encoded; a failed read is an error row (skipped).
func (f *reconcileFixer) evaluate(ctx context.Context, rd rmReader, res *repairs.PathResolver, c rmCandidate) (repairs.Row, error) {
	if err := ctx.Err(); err != nil {
		return repairs.Row{}, err
	}
	st, err := json.Marshal(c)
	if err != nil {
		return repairs.Row{}, fmt.Errorf("encode row state of book %s: %w", c.BookID, err)
	}
	r := repairs.Row{
		RowID:   rmRowID(c.BookID),
		BookIDs: []string{c.BookID},
		Title:   c.Title,
		Class:   rmClassNoCandidate,
		Risk:    repairs.RiskReview,
		State:   st,
	}
	r.Evidence = []string{fmt.Sprintf("reconcile scan %s (completed %s)", c.ScanOpID, c.ScanCompleted)}
	if c.hasMatch() {
		r.Class = c.MatchType
		if c.MatchType == "hash" || c.MatchType == "original_hash" {
			r.Risk = repairs.RiskLow
		}
		r.Evidence = append(r.Evidence, fmt.Sprintf("matched by %s, confidence %s, score %.2f", c.MatchType, c.Confidence, c.Score))
		r.Proposed = map[string]string{"path": c.NewPath}
	}
	r.Current = map[string]string{"path": c.OldPath}

	var fp strings.Builder
	fmt.Fprintf(&fp, "book=%s|old=%s|new=%s|type=%s|conf=%s", c.BookID, c.OldPath, c.NewPath, c.MatchType, c.Confidence)
	skip := func(kind, reason, why string) repairs.Row {
		r.Skipped, r.SkipReason, r.Reason = kind, reason, why
		r.Fingerprint = rmFingerprint(r, fp.String())
		return r
	}

	book, err := rd.GetBookByID(c.BookID)
	if err != nil {
		fp.WriteString("|unreadable")
		return skip(rmSkipError, err.Error(), fmt.Sprintf("Book %s could not be read: %v", c.BookID, err)), nil
	}
	if book == nil || book.IsSoftDeleted() {
		fp.WriteString("|gone")
		return skip(rmSkipGone, "book gone", fmt.Sprintf("Book %s is gone or retired since the scan.", c.BookID)), nil
	}
	r.Title = book.Title
	files, err := rd.GetBookFiles(c.BookID)
	if err != nil {
		fp.WriteString("|files-unreadable")
		return skip(rmSkipError, err.Error(), fmt.Sprintf("The file rows of %s could not be read: %v", c.BookID, err)), nil
	}
	member := repairs.RowMember{BookID: book.ID, Title: book.Title, Role: "missing", Files: len(files)}
	for _, bf := range files {
		if bf.Missing {
			member.MissingFiles++
		}
	}
	r.Members = []repairs.RowMember{member}
	fmt.Fprintf(&fp, "|path=%s", book.FilePath)

	// iTunes first: a row whose book or matched file is iTunes is skipped as
	// iTunes whatever else holds.
	why, unsure, err := rmITunesWhy(rd, res, book, files, c.NewPath)
	switch {
	case err != nil:
		fp.WriteString("|itunes-unreadable")
		return skip(rmSkipError, err.Error(), "Could not tell whether this is an iTunes book: "+err.Error()), nil
	case unsure:
		// A path that cannot be resolved is usually one that cannot be
		// stat'ed (an unreadable parent): name that error when it is the
		// book's path, rather than only "could not tell".
		if _, serr := rmOldPathMissing(book.FilePath); serr != nil {
			fp.WriteString("|old-unreadable")
			return skip(rmSkipError, serr.Error(),
				fmt.Sprintf("Could not tell whether %s is missing: %v", book.FilePath, serr)), nil
		}
		fp.WriteString("|itunes-unsure")
		return skip(repairs.SkipGuardUnreadable, "could not tell whether a path is under books/itunes/**",
			"Could not tell whether the book or the matched file is in the iTunes library (a path could not be resolved)."), nil
	case why != "":
		fmt.Fprintf(&fp, "|itunes=%s", why)
		return skip(repairs.SkipITunes, rmReasonITunes, "iTunes books and iTunes files are never written: "+why), nil
	}

	if book.FilePath != c.OldPath {
		return skip(rmSkipPathChanged, "the book's path changed since the scan",
			fmt.Sprintf("The scan found %q missing; the book now points at %q.", c.OldPath, book.FilePath)), nil
	}
	missing, serr := rmOldPathMissing(book.FilePath)
	if serr != nil {
		fp.WriteString("|old-unreadable")
		return skip(rmSkipError, serr.Error(),
			fmt.Sprintf("Could not tell whether %s is missing: %v", book.FilePath, serr)), nil
	}
	fmt.Fprintf(&fp, "|old-missing=%t", missing)
	if !missing {
		return skip(rmSkipNotMissing, "the book's file is on disk again",
			fmt.Sprintf("%s is on disk again; there is nothing to repair.", book.FilePath)), nil
	}
	r.Current["path"] = c.OldPath + " (missing)"
	if !c.hasMatch() {
		return skip(rmSkipNoCandidate, rmReasonNoCandidate,
			fmt.Sprintf("The file %s is missing and the scan found no untracked file on disk that matches it.", c.OldPath)), nil
	}
	var atOld []database.BookFile
	for _, bf := range files {
		if bf.FilePath == c.OldPath {
			atOld = append(atOld, bf)
		}
	}
	if len(atOld) != 1 {
		fmt.Fprintf(&fp, "|rows-at-old=%d", len(atOld))
		return skip(rmSkipNoFileRow, "no single file row at the book's path",
			fmt.Sprintf("The book has %d file rows at %s, not exactly one to repoint.", len(atOld), c.OldPath)), nil
	}
	if rmHasRowAt(files, c.NewPath) {
		fp.WriteString("|own-row-at-new")
		return skip(rmSkipOwnRowAtMatch, "the book already has a file row at the matched file",
			fmt.Sprintf("The book already has a file row at %s; repointing its row at %s there would make two rows for one file.",
				c.NewPath, c.OldPath)), nil
	}
	row := atOld[0]
	was := database.BookFileLocationOf(&row)
	fmt.Fprintf(&fp, "|row=%s|row-loc=%s|%t", row.ID, was.Path, was.Missing)
	nf := rmNewFile(c.NewPath)
	fmt.Fprintf(&fp, "|new-exists=%t|new-size=%d|new-mtime=%d", nf.exists, nf.size, nf.mtime.UnixNano())
	if !nf.exists {
		return skip(rmSkipFileGone, "the matched file is no longer on disk",
			fmt.Sprintf("The matched file %s is no longer on disk.", c.NewPath)), nil
	}
	owners, err := rmPathOwners(rd, c.NewPath, c.BookID)
	if errors.Is(err, database.ErrOwnershipLookupIncomplete) {
		// Transient: the index finishes warming up without the row changing.
		// RetryFingerprint is this row's fingerprint once it does and finds
		// no other owner (the applicable row's), so the engine reports the
		// hold retry_later only when every other input is as planned.
		clear := repairs.Row{RowID: r.RowID, Class: r.Class}
		retryFP := rmFingerprint(clear, fp.String()+"|owners=")
		fp.WriteString("|owners-warming")
		row := skip(rmSkipIndexWarming, rmReasonIndexWarming, "Who holds the matched file cannot be told while the file index is warming up.")
		row.RetryLater, row.RetryFingerprint = true, retryFP
		return row, nil
	}
	if err != nil {
		fp.WriteString("|owners-unreadable")
		return skip(rmSkipError, err.Error(), "Could not tell whether another book holds the matched file: "+err.Error()), nil
	}
	fmt.Fprintf(&fp, "|owners=%s", strings.Join(owners, ","))
	if len(owners) > 0 {
		return skip(rmSkipFileTracked, "the matched file belongs to another book",
			fmt.Sprintf("The matched file %s now belongs to book(s) %s.", c.NewPath, strings.Join(owners, ", "))), nil
	}

	to := database.BookFileLocation{Path: c.NewPath, Missing: false, Size: nf.size}
	if c.MatchType == "hash" || c.MatchType == "original_hash" {
		// The scan proved the content: the row keeps its hash. A filename
		// match proves nothing about content, so the hash is cleared for the
		// next hash pass to fill.
		to.Hash = was.Hash
	}
	r.Reason = fmt.Sprintf("The file %s is missing; the scan matched the untracked file %s by %s (confidence %s). "+
		"Applying points the book and its file row at that file; nothing moves on disk.",
		c.OldPath, c.NewPath, c.MatchType, c.Confidence)
	r.Detail = &rmDecision{bookID: c.BookID, oldPath: c.OldPath, newPath: c.NewPath, newFile: nf,
		fileID: row.ID, was: was, to: to}
	r.Fingerprint = rmFingerprint(r, fp.String())
	return r, nil
}

// rmHasRowAt reports whether any of files is at path.
func rmHasRowAt(files []database.BookFile, path string) bool {
	for _, bf := range files {
		if bf.FilePath == path {
			return true
		}
	}
	return false
}

// rmITunesWhy is duplicate-copies' iTunes predicate (itunesCopyWhy) for the
// book together with the matched file: why the row is iTunes ("" when it is
// not), or unsure when a path could not be settled. The book's own iTunes
// path counts like a row's.
func rmITunesWhy(rd rmReader, res *repairs.PathResolver, b *database.Book, files []database.BookFile, newPath string) (why string, unsure bool, err error) {
	//lint:ignore SA1019 the legacy book.itunes_path still marks an iTunes book until it is drained; reading it only widens the skip
	if itp := b.ITunesPath; itp != nil && *itp != "" {
		return "book iTunes path " + *itp, false, nil
	}
	exts, err := rd.GetExternalIDsForBook(b.ID)
	if err != nil {
		return "", false, fmt.Errorf("external ids of %s: %w", b.ID, err)
	}
	paths := []string{b.FilePath}
	if newPath != "" {
		paths = append(paths, newPath)
	}
	ff := make([]fragFile, 0, len(files))
	for _, bf := range files {
		paths = append(paths, bf.FilePath)
		ff = append(ff, fragFile{ID: bf.ID, ITunesPID: bf.ITunesPersistentID, ITunesPath: bf.ITunesPath})
	}
	pid := ""
	if b.ITunesPersistentID != nil {
		pid = *b.ITunesPersistentID
	}
	why, unsure = itunesCopyWhy(res, b.ID, pid, paths, ff, exts)
	return why, unsure, nil
}

// rmPathOwners lists the books other than self that hold path: live books
// whose path it is, and books with a book_file row at it (a Missing row
// counts: it is a claim on the file). Sorted, unique. The book_file read is
// the strict one: while memdb is warming up it returns an error wrapping
// database.ErrOwnershipLookupIncomplete, never "nobody holds it".
func rmPathOwners(rd rmReader, path, self string) ([]string, error) {
	rows, err := database.BookFileRowsAtPathStrict(rd, path)
	if err != nil {
		return nil, fmt.Errorf("file rows at %s: %w", path, err)
	}
	live, err := rd.LiveBookIDsAtPath(path)
	if err != nil {
		return nil, fmt.Errorf("books at %s: %w", path, err)
	}
	set := map[string]bool{}
	for _, id := range live {
		set[id] = true
	}
	for _, bf := range rows {
		set[bf.BookID] = true
	}
	delete(set, self)
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

// rmFingerprint hashes the row's identity, class, skip and every decision
// input (extra).
func rmFingerprint(r repairs.Row, extra string) string {
	sum := sha256.Sum256([]byte(r.RowID + "\n" + r.Class + "\n" + r.Skipped + "\n" + extra))
	return hex.EncodeToString(sum[:])[:32]
}

// rmErrorRow is a candidate Plan could not decide.
func rmErrorRow(c rmCandidate, err error) repairs.Row {
	r := repairs.Row{RowID: rmRowID(c.BookID), BookIDs: []string{c.BookID}, Title: c.Title, Class: rmClassNoCandidate,
		Skipped: rmSkipError, SkipReason: err.Error(), Reason: err.Error(), Risk: repairs.RiskReview}
	if c.hasMatch() {
		r.Class = c.MatchType
	}
	r.Fingerprint = rmFingerprint(r, "error")
	return r
}

// Apply repoints one fresh row's book and its file row at the matched file
// (see APPLY on reconcileFixer). It re-reads, right before writing, what a
// write could otherwise get wrong between the re-plan and now; every refusal
// before the first write leaves nothing written.
func (f *reconcileFixer) Apply(_ context.Context, w *repairs.Writer, fresh repairs.Row) error {
	d, ok := fresh.Detail.(*rmDecision)
	if !ok || d == nil {
		return fmt.Errorf("%s: row %s carries no match", reconcileFixerID, fresh.RowID)
	}
	rd, err := f.reader()
	if err != nil {
		return err
	}
	if err := rmPrecheck(rd, d); err != nil {
		return err
	}
	// 1. The book's path, compare-and-set, journaled first (voided on a
	// refusal).
	err = w.JournalStep(d.bookID, repairs.UndoEntry{ChangeType: undo.ChangeTypeBookPathUpdate, Field: "file_path",
		Old: d.oldPath, New: d.newPath}, func() error {
		_, merr := w.Modify(d.bookID, func(b *database.Book) error {
			switch {
			case b.IsSoftDeleted():
				return fmt.Errorf("%w: book %s was retired", repairs.ErrChangedSincePlan, d.bookID)
			case b.FilePath != d.oldPath:
				return fmt.Errorf("%w: book %s now points at %q, not %q", repairs.ErrChangedSincePlan, d.bookID, b.FilePath, d.oldPath)
			}
			b.FilePath = d.newPath
			return nil
		})
		return merr
	})
	if err != nil {
		return err
	}
	// 2. The file row at the old path, compare-and-set on its location.
	if err := w.RepointBookFile(d.bookID, d.fileID, d.was, d.to); err != nil {
		return fmt.Errorf("%w: book %s now points at %s but its file row %s was not repointed: %w",
			repairs.ErrPartiallyApplied, d.bookID, d.newPath, d.fileID, err)
	}
	return nil
}

// rmPrecheck re-reads, just before the first write, who holds the matched
// file, the book and the iTunes predicate, and checks the file is the one
// planned. The re-plan read all of it moments ago; this closes the window up
// to the write. ErrChangedSincePlan on any change; ErrRetryLater while the
// file index is warming up.
func rmPrecheck(rd rmReader, d *rmDecision) error {
	owners, err := rmPathOwners(rd, d.newPath, d.bookID)
	switch {
	case errors.Is(err, database.ErrOwnershipLookupIncomplete):
		return fmt.Errorf("%w: %s: %w", repairs.ErrRetryLater, rmReasonIndexWarming, err)
	case err != nil:
		return err
	case len(owners) > 0:
		return fmt.Errorf("%w: %s now belongs to book(s) %s", repairs.ErrChangedSincePlan, d.newPath, strings.Join(owners, ", "))
	}
	b, err := rd.GetBookByID(d.bookID)
	if err != nil {
		return fmt.Errorf("read book %s: %w", d.bookID, err)
	}
	switch {
	case b == nil || b.IsSoftDeleted():
		return fmt.Errorf("%w: book %s is gone or retired", repairs.ErrChangedSincePlan, d.bookID)
	case b.FilePath != d.oldPath:
		return fmt.Errorf("%w: book %s now points at %q", repairs.ErrChangedSincePlan, d.bookID, b.FilePath)
	}
	files, err := rd.GetBookFiles(d.bookID)
	if err != nil {
		return fmt.Errorf("files of %s: %w", d.bookID, err)
	}
	if rmHasRowAt(files, d.newPath) {
		return fmt.Errorf("%w: book %s now has a file row at %s", repairs.ErrChangedSincePlan, d.bookID, d.newPath)
	}
	why, unsure, err := rmITunesWhy(rd, repairs.NewPathResolver(), b, files, d.newPath)
	switch {
	case err != nil:
		return err
	case unsure:
		return fmt.Errorf("%w: could not tell whether book %s or %s is in the iTunes library", repairs.ErrChangedSincePlan, d.bookID, d.newPath)
	case why != "":
		return fmt.Errorf("%w: iTunes: %s", repairs.ErrChangedSincePlan, why)
	}
	if nf := rmNewFile(d.newPath); !nf.same(d.newFile) {
		return fmt.Errorf("%w: the matched file %s changed or is gone", repairs.ErrChangedSincePlan, d.newPath)
	}
	return nil
}
