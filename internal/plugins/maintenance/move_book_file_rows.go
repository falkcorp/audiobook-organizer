// file: internal/plugins/maintenance/move_book_file_rows.go
// version: 1.0.0
// guid: 43feca72-a61b-4386-97c9-17d58ba2bf8c
// last-edited: 2026-09-26

// Package maintenance — op maintenance.move-book-file-rows.
//
// Reassigns named book_file rows from the book that owns them to a named target
// book. It exists for rows attached to the wrong primary: on 2026-09-26 the
// chapter-01 row of "Warforged Sorcerer" lived in the target book's folder,
// beside that book's chapters 02-52, but belonged to a different primary whose
// other files are in another folder.
//
// WHY A NEW OP. Nothing else moves an arbitrary row to an arbitrary book:
//   - POST /audiobooks/:id/move-segments (VersionsHandler.MoveSegments) refuses
//     unless both books are in the SAME version group, has no preview, and does
//     not touch track numbers.
//   - The merge/regroup/split callers of MoveBookFilesToBook each derive their
//     own targets and cannot be pointed at an explicit (row, book) pair.
//   - build-folder-book-files only CREATES rows for rowless books.
//
// WHAT IT DOES, per move {row_id, to_book_id[, from_book_id]}:
//   - Resolves the row's current owner (from_book_id when given; otherwise a
//     lookup over every row by ID) and re-reads the full row from that book.
//   - Refuses: a row that is not found; a self-move; a missing or soft-deleted
//     source or target; a row, source or target path under an iTunes root; a
//     row carrying an iTunes link (itunes_path or persistent ID — this op does
//     not move external-ID mappings, so it will not strand them); a filename
//     with no track number; a track number another row of the target already
//     holds; the same row named twice.
//   - Moves the rows with MoveBookFilesToBookBulk, one call per target. That
//     rewrites the whole row under its new owner (hashes, sizes, fingerprints
//     and every other field kept), refreshes memdb and recomputes BOTH books'
//     aggregates (duration, size, file count).
//   - Sets the moved row's track number from its filename when it differs
//     (ModifyBookFile). This is a second write: a failure here is reported per
//     row as track_failed with the move itself already committed.
//
// It never deletes a row or a book, never changes Book.FilePath (a source whose
// file_path IS the moved file is reported as a warning for the owner), and
// never touches files on disk. DRY RUN unless {"dry_run": false}.
package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

const (
	mbfOpID   = "maintenance.move-book-file-rows"
	mbfOpName = "move-book-file-rows"

	// mbfMaxMoves bounds one run. The op is for hand-picked rows; a list this
	// long is a script gone wrong, not a repair.
	mbfMaxMoves = 500

	mbfStatusPlanned     = "planned"
	mbfStatusRefused     = "refused"
	mbfStatusMoved       = "moved"
	mbfStatusTrackFailed = "moved_track_failed"
	mbfStatusMoveFailed  = "move_failed"
	mbfStatusNotAttempt  = "not_attempted"
)

// mbfMove is one requested move.
type mbfMove struct {
	RowID      string `json:"row_id"`
	ToBookID   string `json:"to_book_id"`
	FromBookID string `json:"from_book_id,omitempty"`
}

type mbfParams struct {
	Moves       []mbfMove `json:"moves"`
	DryRunSnake *bool     `json:"dry_run,omitempty"`
	DryRun      *bool     `json:"dryRun,omitempty"`
}

func (p mbfParams) dryRun() (bool, error) {
	return opmode.ResolveDryRun(mbfOpID, p.DryRunSnake, p.DryRun)
}

// mbfBookState is a book's identity and duration as the op saw it.
type mbfBookState struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	FilePath string `json:"file_path"`
	Duration int    `json:"duration"`
}

// mbfResult is one move's decision and, for a live run, its outcome.
type mbfResult struct {
	RowID      string `json:"row_id"`
	Path       string `json:"path,omitempty"`
	FromBookID string `json:"from_book_id,omitempty"`
	FromTitle  string `json:"from_title,omitempty"`
	ToBookID   string `json:"to_book_id"`
	ToTitle    string `json:"to_title,omitempty"`
	OldTrack   int    `json:"old_track,omitempty"`
	NewTrack   int    `json:"new_track,omitempty"`
	RowSeconds int    `json:"row_duration,omitempty"`
	FileHash   string `json:"file_hash,omitempty"`
	// SameFolderAsTarget is true when at least one of the target's existing
	// rows sits in the moved row's directory: the evidence the owner's rule
	// ("move it to the book its sibling files belong to") asks for.
	SameFolderAsTarget bool     `json:"same_folder_as_target"`
	Status             string   `json:"status"`
	Reason             string   `json:"reason,omitempty"`
	Warnings           []string `json:"warnings,omitempty"`
	Error              string   `json:"error,omitempty"`
}

type mbfReport struct {
	DryRun  bool           `json:"dry_run"`
	Planned int            `json:"planned"`
	Refused int            `json:"refused"`
	Moved   int            `json:"moved"`
	Failed  int            `json:"failed"`
	Moves   []mbfResult    `json:"moves"`
	Before  []mbfBookState `json:"books_before"`
	After   []mbfBookState `json:"books_after,omitempty"`
}

func (r *mbfReport) summary() string {
	mode := "LIVE"
	if r.DryRun {
		mode = "DRY RUN"
	}
	return fmt.Sprintf("%s: %d planned, %d refused, %d moved, %d failed", mode, r.Planned, r.Refused, r.Moved, r.Failed)
}

// mbfStore is the narrow store the op needs. Every method is on
// database.Store, so the production indexedStore satisfies it.
type mbfStore interface {
	GetBookByID(id string) (*database.Book, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetBookFileByID(bookID, fileID string) (*database.BookFile, error)
	GetAllBookFilesCore() ([]database.BookFileCore, error)
	MoveBookFilesToBookBulk(moves []database.BookFileMove, targetBookID string) error
	ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error)
}

var _ mbfStore = database.Store(nil)

type mbfEnv struct {
	store       mbfStore
	itunesRoots []string
	scan        ScanController
}

func (p *Plugin) moveBookFileRowsDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          mbfOpID,
		DisplayName: "Move book_file rows to another book",
		Description: "Reassigns named book_file rows to a named book: {\"moves\":[{\"row_id\":..., \"to_book_id\":...}]} " +
			"(optional from_book_id pins the current owner). Keeps every field of the row (hashes, size, fingerprints), " +
			"sets the track number from the filename, and recomputes both books' duration and size. Refuses missing or " +
			"soft-deleted books, iTunes paths or iTunes-linked rows, filenames without a track number, and a track the " +
			"target already has. Never deletes anything and never changes Book.FilePath. DRY-RUN BY DEFAULT: " +
			"{\"dry_run\":false} to write.",
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  mbfOpID,
		Writes:          []sdk.Resource{sdk.ResBookFiles},
		// A re-run is safe: a moved row is found under its new owner and
		// refused as a self-move.
		ResumePolicy: sdk.ResumeDrop,
		Cancellable:  true,
		Capabilities: []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:          p.runMoveBookFileRows,
	}
}

func (p *Plugin) runMoveBookFileRows(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params mbfParams
	if err := decodeStrictParams(raw, &params); err != nil {
		return fmt.Errorf("%s: decode params: %w", mbfOpName, err)
	}
	if _, err := params.dryRun(); err != nil {
		return err
	}
	ops := p.deps.OpsStore()
	if ops == nil {
		return fmt.Errorf("database not initialized")
	}
	store, ok := any(ops).(mbfStore)
	if !ok {
		return fmt.Errorf("%s: store cannot move book_file rows; refusing to run", mbfOpName)
	}
	// Fail closed: without resolvable iTunes roots the op cannot prove a path
	// is outside them.
	roots, err := merge.ITunesProtectedRoots(config.Snapshot().ITunes)
	if err != nil {
		return fmt.Errorf("%s: resolve iTunes roots: %w", mbfOpName, err)
	}
	report, runErr := moveBookFileRows(ctx, mbfEnv{store: store, itunesRoots: roots, scan: p.deps}, params, reporter)
	if report != nil {
		if sErr := registry.ReporterSetResult(reporter, report); sErr != nil {
			reporter.Logger().Warn(mbfOpName+": result not persisted", "err", sErr)
		}
		reporter.Logger().Info(mbfOpName+" complete", "summary", report.summary())
	}
	return runErr
}

// moveBookFileRows plans every move, and on a live run applies the planned
// ones. The move list is hand-picked and small (mbfMaxMoves), so it runs
// sequentially: the only whole-library read is the one owner lookup, done once.
func moveBookFileRows(ctx context.Context, env mbfEnv, params mbfParams, reporter sdk.Reporter) (*mbfReport, error) {
	dryRun, err := params.dryRun()
	if err != nil {
		return nil, err
	}
	if len(params.Moves) == 0 {
		return nil, fmt.Errorf("%s: moves must not be empty", mbfOpName)
	}
	if len(params.Moves) > mbfMaxMoves {
		return nil, fmt.Errorf("%s: %d moves exceeds the limit of %d", mbfOpName, len(params.Moves), mbfMaxMoves)
	}

	owners, err := mbfResolveOwners(env.store, params.Moves)
	if err != nil {
		return nil, err
	}

	report := &mbfReport{DryRun: dryRun}
	books := map[string]*database.Book{}
	getBook := func(id string) (*database.Book, error) {
		if b, ok := books[id]; ok {
			return b, nil
		}
		b, err := env.store.GetBookByID(id)
		if err != nil {
			return nil, err
		}
		books[id] = b
		return b, nil
	}
	targetRows := map[string][]database.BookFile{}
	getTargetRows := func(id string) ([]database.BookFile, error) {
		if rows, ok := targetRows[id]; ok {
			return rows, nil
		}
		rows, err := env.store.GetBookFiles(id)
		if err != nil {
			return nil, err
		}
		targetRows[id] = rows
		return rows, nil
	}

	seenRow := map[string]bool{}
	// claimed[target][track] = row ID planned onto that track in this run, so
	// two moves cannot both land on one track.
	claimed := map[string]map[int]string{}
	var touched []string
	touchedSet := map[string]bool{}
	touch := func(id string) {
		if id != "" && !touchedSet[id] {
			touchedSet[id] = true
			touched = append(touched, id)
		}
	}

	for _, mv := range params.Moves {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		r, planErr := mbfPlanOne(env, mv, owners[mv.RowID], seenRow, claimed, getBook, getTargetRows)
		if planErr != nil {
			return report, planErr
		}
		seenRow[mv.RowID] = true
		if r.Status == mbfStatusPlanned {
			report.Planned++
			touch(r.FromBookID)
			touch(r.ToBookID)
		} else {
			report.Refused++
		}
		report.Moves = append(report.Moves, r)
	}
	report.Before = mbfBookStates(touched, books)

	if dryRun || report.Planned == 0 {
		return report, nil
	}

	holderID, held, release, sdErr := acquireScanStandDownForApply(ctx, env.scan, reporter, mbfOpName+" apply")
	if sdErr != nil {
		return report, fmt.Errorf("%s: scan stand-down: %w", mbfOpName, sdErr)
	}
	defer release()

	// One MoveBookFilesToBookBulk per target: each call is one atomic batch
	// and recomputes each distinct book once.
	byTarget := map[string][]int{}
	var targets []string
	for i, r := range report.Moves {
		if r.Status != mbfStatusPlanned {
			continue
		}
		if _, ok := byTarget[r.ToBookID]; !ok {
			targets = append(targets, r.ToBookID)
		}
		byTarget[r.ToBookID] = append(byTarget[r.ToBookID], i)
	}
	for _, target := range targets {
		idx := byTarget[target]
		if ctx.Err() != nil || scanStandDownLostForApply(env.scan, holderID, held) {
			for _, i := range idx {
				report.Moves[i].Status = mbfStatusNotAttempt
				report.Moves[i].Error = "run canceled or scan stand-down lost before this target"
			}
			continue
		}
		if err := env.store.MoveBookFilesToBookBulk(mbfRegroup(report.Moves, idx), target); err != nil {
			for _, i := range idx {
				report.Moves[i].Status = mbfStatusMoveFailed
				report.Moves[i].Error = err.Error()
				report.Failed++
			}
			continue
		}
		for _, i := range idx {
			r := &report.Moves[i]
			r.Status = mbfStatusMoved
			report.Moved++
			if r.NewTrack == r.OldTrack {
				continue
			}
			want := r.NewTrack
			_, mErr := env.store.ModifyBookFile(target, r.RowID, func(f *database.BookFile) error {
				if f.TrackNumber == want {
					return database.ErrSkipBookFileWrite
				}
				f.TrackNumber = want
				return nil
			})
			if mErr != nil && !errors.Is(mErr, database.ErrSkipBookFileWrite) {
				r.Status = mbfStatusTrackFailed
				r.Error = "row moved; track number not set: " + mErr.Error()
				report.Failed++
			}
		}
	}

	after := map[string]*database.Book{}
	for _, id := range touched {
		b, err := env.store.GetBookByID(id)
		if err == nil && b != nil {
			after[id] = b
		}
	}
	report.After = mbfBookStates(touched, after)
	return report, nil
}

// mbfRegroup builds the per-source move list for one target's planned moves.
func mbfRegroup(moves []mbfResult, idx []int) []database.BookFileMove {
	pos := map[string]int{}
	var out []database.BookFileMove
	for _, i := range idx {
		src := moves[i].FromBookID
		p, ok := pos[src]
		if !ok {
			p = len(out)
			pos[src] = p
			out = append(out, database.BookFileMove{SourceBookID: src})
		}
		out[p].FileIDs = append(out[p].FileIDs, moves[i].RowID)
	}
	return out
}

// mbfResolveOwners maps every row ID to its current owner. A move that pins
// from_book_id is taken at its word here (the full re-read under that book
// then confirms it). The others are looked up in ONE pass over every row's
// core; a row absent from that pass (including while memdb is still warming
// up) resolves to "" and is refused as not found, never guessed.
func mbfResolveOwners(store mbfStore, moves []mbfMove) (map[string]string, error) {
	owners := make(map[string]string, len(moves))
	need := map[string]bool{}
	for _, mv := range moves {
		if mv.FromBookID != "" {
			owners[mv.RowID] = mv.FromBookID
		} else {
			need[mv.RowID] = true
		}
	}
	if len(need) == 0 {
		return owners, nil
	}
	all, err := store.GetAllBookFilesCore()
	if err != nil {
		return nil, fmt.Errorf("%s: list book_file rows: %w", mbfOpName, err)
	}
	for i := range all {
		if need[all[i].ID] {
			owners[all[i].ID] = all[i].BookID
		}
	}
	return owners, nil
}

func mbfPlanOne(env mbfEnv, mv mbfMove, owner string, seenRow map[string]bool, claimed map[string]map[int]string,
	getBook func(string) (*database.Book, error), getTargetRows func(string) ([]database.BookFile, error)) (mbfResult, error) {
	r := mbfResult{RowID: mv.RowID, ToBookID: mv.ToBookID, FromBookID: owner, Status: mbfStatusRefused}
	refuse := func(reason string) (mbfResult, error) {
		r.Status = mbfStatusRefused
		r.Reason = reason
		return r, nil
	}
	if mv.RowID == "" || mv.ToBookID == "" {
		return refuse("row_id and to_book_id are required")
	}
	if seenRow[mv.RowID] {
		return refuse("row named more than once in this run")
	}
	if owner == "" {
		return refuse("row not found")
	}
	row, err := env.store.GetBookFileByID(owner, mv.RowID)
	if err != nil {
		return r, fmt.Errorf("%s: read row %s: %w", mbfOpName, mv.RowID, err)
	}
	if row == nil {
		return refuse("row not found under book " + owner)
	}
	r.Path = row.FilePath
	r.OldTrack = row.TrackNumber
	r.RowSeconds = row.Duration
	r.FileHash = row.FileHash
	if owner == mv.ToBookID {
		return refuse("row already belongs to the target book")
	}

	src, err := getBook(owner)
	if err != nil {
		return r, fmt.Errorf("%s: read book %s: %w", mbfOpName, owner, err)
	}
	dst, err := getBook(mv.ToBookID)
	if err != nil {
		return r, fmt.Errorf("%s: read book %s: %w", mbfOpName, mv.ToBookID, err)
	}
	if src != nil {
		r.FromTitle = src.Title
	}
	if dst != nil {
		r.ToTitle = dst.Title
	}
	switch {
	case src == nil:
		return refuse("source book not found")
	case dst == nil:
		return refuse("target book not found")
	case isSoftDeleted(src):
		return refuse("source book is soft-deleted")
	case isSoftDeleted(dst):
		return refuse("target book is soft-deleted")
	}
	for _, p := range []string{row.FilePath, src.FilePath, dst.FilePath} {
		if underITunes(p, env.itunesRoots) {
			return refuse("itunes-path: " + p)
		}
	}
	if row.ITunesPath != "" || row.ITunesPersistentID != "" {
		return refuse("row carries an iTunes link; this op does not move external-ID mappings")
	}

	track, ok := trackFromFilename(row.FilePath)
	if !ok {
		return refuse("no track number in filename " + filepath.Base(row.FilePath))
	}
	r.NewTrack = track

	rows, err := getTargetRows(mv.ToBookID)
	if err != nil {
		return r, fmt.Errorf("%s: list rows of book %s: %w", mbfOpName, mv.ToBookID, err)
	}
	dir := filepath.Dir(row.FilePath)
	for i := range rows {
		if filepath.Dir(rows[i].FilePath) == dir {
			r.SameFolderAsTarget = true
		}
		if !rows[i].Missing && rows[i].TrackNumber == track {
			return refuse(fmt.Sprintf("target already has track %d (row %s)", track, rows[i].ID))
		}
	}
	if other, ok := claimed[mv.ToBookID][track]; ok {
		return refuse(fmt.Sprintf("track %d is already claimed in this run by row %s", track, other))
	}
	if claimed[mv.ToBookID] == nil {
		claimed[mv.ToBookID] = map[int]string{}
	}
	claimed[mv.ToBookID][track] = mv.RowID

	if !r.SameFolderAsTarget {
		r.Warnings = append(r.Warnings, "none of the target's rows are in this row's folder")
	}
	if src.FilePath == row.FilePath {
		r.Warnings = append(r.Warnings, "source book's file_path is this file; it is left unchanged")
	}
	r.Status = mbfStatusPlanned
	return r, nil
}

func mbfBookStates(ids []string, books map[string]*database.Book) []mbfBookState {
	out := make([]mbfBookState, 0, len(ids))
	for _, id := range ids {
		b := books[id]
		if b == nil {
			continue
		}
		st := mbfBookState{ID: b.ID, Title: b.Title, FilePath: b.FilePath}
		if b.Duration != nil {
			st.Duration = *b.Duration
		}
		out = append(out, st)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

var (
	// "Title - 01", "Title_01", "Title 01", "01"
	mbfTrailingNum = regexp.MustCompile(`(?:^|[\s_.\-])(\d{1,4})$`)
	// "01 - Title", "01_Title", "01. Title". Not "1-00-Title": a disc-track
	// prefix is not a track number, and guessing one would misnumber the row.
	mbfLeadingNum = regexp.MustCompile(`^(\d{1,4})(?:[\s_.]|$)`)
)

// trackFromFilename reads a track number from a file's name: a number ending
// the stem ("Warforged Sorcerer - 01.mp3"), else one starting it
// ("01 - Warforged Sorcerer.mp3"). A name with neither ("Slant.mp3") has no
// track, and 0 is not a track.
func trackFromFilename(path string) (int, bool) {
	base := filepath.Base(path)
	stem := strings.TrimSpace(strings.TrimSuffix(base, filepath.Ext(base)))
	for _, re := range []*regexp.Regexp{mbfTrailingNum, mbfLeadingNum} {
		if m := re.FindStringSubmatch(stem); m != nil {
			n, err := strconv.Atoi(m[1])
			if err == nil && n > 0 {
				return n, true
			}
		}
	}
	return 0, false
}
