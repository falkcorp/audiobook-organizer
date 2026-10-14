// file: internal/plugins/maintenance/reconcile_fixer.go
// version: 1.0.0
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
	"log/slog"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/applycap"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/reconcile"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// reconcileFixerID is the Repairs-lane id of the missing-files fixer. It takes
// over from the Reconcile tab of the old dedup page (the tab is retired in
// 03-PR10): the latest scan's matches, applying picked ones, and the books
// with no candidate file.
const reconcileFixerID = "reconcile.missing-files"

// rmApplyDefID is the operation an apply hands its picked matches to. It is
// the op POST /operations/reconcile starts (internal/server/reconcile_ops.go).
const rmApplyDefID = "reconcile.apply"

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
	// rmSkipError: something the decision needs could not be read.
	rmSkipError = "error"
)

// Skip reasons shown to the owner, by kind.
const (
	rmReasonNoCandidate = "no candidate file"
	rmReasonITunes      = "iTunes"
)

// reconcileFixer lists the latest saved reconcile scan as Repairs rows and
// applies the picked matches through the reconcile.apply operation.
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
//     path is still the one the scan found missing and still not on disk, the
//     matched file is on disk, and no other book holds the file (no live book
//     at that path and no other book's book_file row at it; the scan's own
//     "untracked" test reads book paths only). Every one of those answers is
//     in the fingerprint, so a re-plan at apply refuses a row that drifted.
//   - A book with no candidate file is a skipped row with the reason "no
//     candidate file", so the count stays clickable.
//   - iTunes: a row whose book is an iTunes book, or whose matched file is
//     an iTunes file, is skipped with the reason "iTunes". The predicate is
//     duplicate-copies' (itunesCopyWhy) over the book's path, its book_file
//     rows, its iTunes ids and external ids AND the matched file's path: the
//     scan walks the iTunes Media/Audiobooks folder for candidates, and the
//     framework guard reads only the book's own paths.
//
// Written Evaluate-style for the ops v3 port: evaluate decides ONE candidate
// from reads it makes itself and never writes. Plan reads the scan once and
// evaluates every candidate; Replan evaluates the planned candidate, carried
// in the row's State, without reading the scan again (a newer scan does not
// invalidate a plan; drift in the library does).
//
// APPLY. The fixer writes nothing itself: each applied row's match is
// collected (Apply), and once the rows have settled AfterApply starts ONE
// reconcile.apply run with exactly those matches, never the rest of the scan.
// A selection larger than the bulk-apply cap (reconcile.apply refuses a whole
// set over it) is split into runs of at most the cap. reconcile.apply re-reads
// each book, checks the file is on disk, journals a file_path_update change
// and sets the book's file path; it never deletes or recreates a book_file
// row. The row outcome "applied" therefore means "handed to Reconcile Apply";
// what that run wrote is in its own result on the Operations page. Because
// the fixer writes no library row, it takes no scan stand-down
// (NoScanStandDown) and its Writer refuses every write.
type reconcileFixer struct{ p *Plugin }

func newReconcileFixer(p *Plugin) *reconcileFixer { return &reconcileFixer{p: p} }

var (
	_ repairs.Fixer           = (*reconcileFixer)(nil)
	_ repairs.ApplyScoped     = (*reconcileFixer)(nil)
	_ repairs.AfterApplier    = (*reconcileFixer)(nil)
	_ repairs.NoScanStandDown = (*reconcileFixer)(nil)
)

func (f *reconcileFixer) ID() string    { return reconcileFixerID }
func (f *reconcileFixer) Title() string { return "Missing files" }
func (f *reconcileFixer) Description() string {
	return "Books whose file has gone missing, from the latest reconcile scan (\"Match missing files\"): each " +
		"match to an untracked file on disk with its match type and confidence, and the books the scan found " +
		"no candidate file for (listed as skipped). A match is offered only while the book is still missing " +
		"its file, the matched file is still on disk and no other book holds it; iTunes books and iTunes files " +
		"are skipped. Applying starts one Reconcile Apply operation with exactly the selected matches, which " +
		"points each book at its matched file; its result is on the Operations page. No file row is deleted."
}

// NoScanStandDown: the fixer writes no library row (reconcile.apply does, as
// its own operation), so an apply run parks no scan.
func (f *reconcileFixer) NoScanStandDown() bool { return true }

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

// rmDecision is what Apply hands on for one fresh row, carried in Row.Detail.
type rmDecision struct {
	bookID, newPath string
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
// evaluate on a bounded pool: each makes a few reads and two stats.
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
	if rep != nil {
		applicable := 0
		for _, r := range rows {
			if r.Applicable() {
				applicable++
			}
		}
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

// rmFileState is what evaluate found at a path on disk.
type rmFileState struct {
	exists bool
	size   int64
}

func rmStat(path string) rmFileState {
	if path == "" {
		return rmFileState{}
	}
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		// The scan counts any stat failure as "missing"; so does this.
		return rmFileState{}
	}
	return rmFileState{exists: true, size: fi.Size()}
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
		r.Proposed = map[string]string{"path": c.NewPath, "match_type": c.MatchType, "confidence": c.Confidence}
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
	old := rmStat(book.FilePath)
	fmt.Fprintf(&fp, "|old-exists=%t", old.exists)
	if !old.exists {
		r.Current["path"] = c.OldPath + " (missing)"
	}
	if old.exists {
		return skip(rmSkipNotMissing, "the book's file is on disk again",
			fmt.Sprintf("%s is on disk again; there is nothing to repair.", book.FilePath)), nil
	}
	if !c.hasMatch() {
		return skip(rmSkipNoCandidate, rmReasonNoCandidate,
			fmt.Sprintf("The file %s is missing and the scan found no untracked file on disk that matches it.", c.OldPath)), nil
	}
	nf := rmStat(c.NewPath)
	fmt.Fprintf(&fp, "|new-exists=%t|new-size=%d", nf.exists, nf.size)
	if !nf.exists {
		return skip(rmSkipFileGone, "the matched file is no longer on disk",
			fmt.Sprintf("The matched file %s is no longer on disk.", c.NewPath)), nil
	}
	owners, err := rmPathOwners(rd, c.NewPath, c.BookID)
	if err != nil {
		fp.WriteString("|owners-unreadable")
		return skip(rmSkipError, err.Error(), "Could not tell whether another book holds the matched file: "+err.Error()), nil
	}
	fmt.Fprintf(&fp, "|owners=%s", strings.Join(owners, ","))
	if len(owners) > 0 {
		return skip(rmSkipFileTracked, "the matched file belongs to another book",
			fmt.Sprintf("The matched file %s now belongs to book(s) %s.", c.NewPath, strings.Join(owners, ", "))), nil
	}

	r.Reason = fmt.Sprintf("The file %s is missing; the scan matched the untracked file %s by %s (confidence %s). "+
		"Applying hands this match to Reconcile Apply, which points the book at that file.",
		c.OldPath, c.NewPath, c.MatchType, c.Confidence)
	r.Detail = &rmDecision{bookID: c.BookID, newPath: c.NewPath}
	r.Fingerprint = rmFingerprint(r, fp.String())
	return r, nil
}

// rmITunesWhy is duplicate-copies' iTunes predicate (itunesCopyWhy) for the
// book together with the matched file: why the row is iTunes ("" when it is
// not), or unsure when a path could not be settled.
func rmITunesWhy(rd rmReader, res *repairs.PathResolver, b *database.Book, files []database.BookFile, newPath string) (why string, unsure bool, err error) {
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
// the strict one: while memdb is warming up an incomplete answer is an
// error, not "nobody holds it".
func rmPathOwners(rd rmReader, path, self string) ([]string, error) {
	live, err := rd.LiveBookIDsAtPath(path)
	if err != nil {
		return nil, fmt.Errorf("books at %s: %w", path, err)
	}
	rows, err := database.BookFileRowsAtPathStrict(rd, path)
	if err != nil {
		return nil, fmt.Errorf("file rows at %s: %w", path, err)
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

// rmSession is one apply run's picked matches (repairs.ApplyScoped): Apply
// records each applied row's match, AfterApply hands them to reconcile.apply.
type rmSession struct {
	mu     sync.Mutex
	picked map[string]string // book id -> matched path
}

type rmSessionKey struct{}

// BeginApply implements repairs.ApplyScoped.
func (f *reconcileFixer) BeginApply(ctx context.Context, _ bool) (context.Context, func()) {
	return context.WithValue(ctx, rmSessionKey{}, &rmSession{picked: map[string]string{}}), func() {}
}

// Apply records one fresh row's match for the run's reconcile.apply. It
// writes nothing: the row was re-planned just before (the fingerprint
// matched), and AfterApply starts the run once every row has settled.
func (f *reconcileFixer) Apply(ctx context.Context, _ *repairs.Writer, fresh repairs.Row) error {
	d, ok := fresh.Detail.(*rmDecision)
	if !ok || d == nil {
		return fmt.Errorf("%s: row %s carries no match", reconcileFixerID, fresh.RowID)
	}
	s, _ := ctx.Value(rmSessionKey{}).(*rmSession)
	if s == nil {
		return fmt.Errorf("%s: row %s: no apply session; nothing recorded", reconcileFixerID, fresh.RowID)
	}
	s.mu.Lock()
	s.picked[d.bookID] = d.newPath
	s.mu.Unlock()
	return nil
}

// rmApplyParams is reconcile.apply's params (the server's
// reconcileApplyOpParams): the matches to apply.
type rmApplyParams struct {
	Matches []reconcile.ReconcileApplyItem `json:"matches"`
}

// AfterApply starts reconcile.apply with the matches of the applied rows
// (bookIDs, from the engine) and nothing else, in runs of at most the
// bulk-apply cap. It returns the started run ids, comma-separated.
func (f *reconcileFixer) AfterApply(ctx context.Context, bookIDs []string) (string, error) {
	s, _ := ctx.Value(rmSessionKey{}).(*rmSession)
	if s == nil {
		return "", fmt.Errorf("%s: no apply session; reconcile.apply not started for %d books", reconcileFixerID, len(bookIDs))
	}
	if f.p.deps == nil {
		return "", errNoEnqueuer
	}
	s.mu.Lock()
	var (
		matches []reconcile.ReconcileApplyItem
		lost    []string
	)
	for _, id := range bookIDs {
		p, ok := s.picked[id]
		if !ok {
			lost = append(lost, id)
			continue
		}
		matches = append(matches, reconcile.ReconcileApplyItem{BookID: id, NewPath: p})
	}
	s.mu.Unlock()
	limit := applycap.Effective(config.Snapshot().BulkApplyMaxItems)
	var (
		ids  []string
		errs []error
	)
	for chunk := range slices.Chunk(matches, limit) {
		opID, err := f.p.deps.EnqueueOp(ctx, rmApplyDefID, rmApplyParams{Matches: chunk})
		if err != nil {
			errs = append(errs, fmt.Errorf("enqueue %s for %d matches: %w", rmApplyDefID, len(chunk), err))
			continue
		}
		ids = append(ids, opID)
	}
	if len(lost) > 0 {
		errs = append(errs, fmt.Errorf("%s: no recorded match for applied book(s) %s; not handed to %s",
			reconcileFixerID, strings.Join(lost, ", "), rmApplyDefID))
	}
	return strings.Join(ids, ","), errors.Join(errs...)
}
