// file: internal/plugins/maintenance/itunes_stale_path_fixer.go
// version: 1.0.0
// guid: 2b2165f1-de86-4181-9a6d-4ed73bb98fbc
// last-edited: 2026-10-07

// Repairs-lane fixer "stale-itunes-path": clear an itunes_path that no iTunes
// track backs (plan: docs/plans/2026-10-07-stale-itunes-path.md).
//
// WHY. A book_file row (or a book) can carry an itunes_path naming a location
// that is in neither iTunes library: the organizer computes one for the files
// it places (ComputeITunesPath), and a clone or a move can leave a path no
// track was ever added at. The merge fixers read any row iTunes path as "this
// is an iTunes book" (itunesCopyWhy) and hold the row. Prod 2026-10-07: 300
// content-proven chapter copies were held that way, each by a path that only
// our database has.
//
// APPLICABLE only when ALL hold (owner decision 2026-10-07, fail closed):
//   - the path's location matches no track of the library the app imports
//     from (itunes.library_read_path, .xml or .itl) AND none of the write-back
//     .itl (itunes.library_write_path). An .itl track counts by both its 0x0D
//     Location and its 0x0B LocalURL (they differ on the write-back library).
//     Locations are compared normalised (itunesLocationKeys); over-matching
//     only leaves a row unapplied;
//   - the book carries no iTunes id: no persistent id on the book or any of
//     its rows, no itunes external id (tombstoned ones included);
//   - both libraries were read and parsed: a missing config path, an open or
//     parse error, or a library with no tracks makes every row
//     library-unreadable, never applicable;
//   - every iTunes path the book carries is stale: a book with one path a
//     track backs is held (partly-backed), since iTunes still tracks it.
//
// APPLY clears only the stale itunes_path fields (the book's and its rows'),
// each journaled first as undo.ChangeTypeITunesPathClear and written as a
// compare-and-set against the planned value (repairs.Writer
// ClearBookFileITunesPath / ClearBookITunesPath). The op revert puts each
// one back while the field is still empty. No book_file row is deleted, no
// file on disk is touched, and no iTunes library is written.
//
// RE-STAMP. recompute-itunes-paths and organize/rename set itunes_path to the
// computed location on every row they touch, so a cleared path can come back
// if one of them runs before whatever the clear was for (a consolidation
// re-plan) runs.
//
// CONCURRENCY. Plan reads each candidate's external ids on a bounded
// registry.RunItems pool. The libraries are parsed once per plan, and once
// per apply run (BeginApply), never per row.
package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	"golang.org/x/text/unicode/norm"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

const staleITPFixerID = "stale-itunes-path"

// Row classes of the stale-itunes-path fixer; every count the lane shows
// filters on one of them.
const (
	staleITPClassStale        = "stale"
	staleITPClassBacked       = "backed"
	staleITPClassPartly       = "partly-backed"
	staleITPClassHasID        = "has-itunes-id"
	staleITPClassUnreadable   = "library-unreadable"
	staleITPClassNoPath       = "no-itunes-path"
	staleITPClassGone         = "gone"
	staleITPSkipBacked        = "skipped_itunes_backed"
	staleITPSkipPartly        = "skipped_itunes_partly_backed"
	staleITPSkipHasID         = "skipped_itunes_id"
	staleITPSkipUnreadable    = "skipped_itunes_library_unreadable"
	staleITPSkipNoPath        = "skipped_no_itunes_path"
	staleITPSkipGone          = "skipped_gone"
	staleITPBookFieldLocation = "book"
)

// staleITPParams are the plan's params: book_ids limits the plan to those
// books (a trial); empty means the whole library.
type staleITPParams struct {
	BookIDs []string `json:"book_ids,omitempty"`
}

type staleITunesPathFixer struct{ p *Plugin }

func newStaleITunesPathFixer(p *Plugin) *staleITunesPathFixer { return &staleITunesPathFixer{p: p} }

var (
	_ repairs.Fixer       = (*staleITunesPathFixer)(nil)
	_ repairs.ApplyScoped = (*staleITunesPathFixer)(nil)
)

func (f *staleITunesPathFixer) ID() string    { return staleITPFixerID }
func (f *staleITunesPathFixer) Title() string { return "Stale iTunes paths (no track behind them)" }
func (f *staleITunesPathFixer) Description() string {
	return "Books whose iTunes path (on the book or a file row) names a location no track of either iTunes library has: " +
		"not the library the app imports from, not the write-back library. Both libraries are read, never written; if " +
		"either cannot be read or parsed, nothing is applicable. A book with an iTunes id, or with any path a track " +
		"does back, is held. Apply clears only those iTunes path fields, journaled; the operation's revert puts them " +
		"back. No file and no iTunes library is touched. A cleared book is no longer written back to iTunes, and " +
		"recompute-itunes-paths or an organize can set the computed path again."
}

// ---- the libraries --------------------------------------------------------

// staleITPLibrary is the set of locations the iTunes libraries have tracks
// at (itunesLocationKeys of each), or why it could not be built.
type staleITPLibrary struct {
	locs map[string]bool
	// desc names each library read and its track count, for the evidence.
	desc []string
	err  error
}

func (l *staleITPLibrary) has(loc string) bool {
	for _, k := range itunesLocationKeys(loc) {
		if l.locs[k] {
			return true
		}
	}
	return false
}

func (f *staleITunesPathFixer) libraryPaths() (read, write string) {
	if f.p.itunesLibraryPaths != nil {
		return f.p.itunesLibraryPaths()
	}
	cfg := config.Snapshot().ITunes
	return cfg.LibraryReadPath, cfg.LibraryWritePath
}

// loadLibrary reads and parses both libraries (read-only). Any failure is
// kept in err: fail closed, nothing is applicable.
func (f *staleITunesPathFixer) loadLibrary() *staleITPLibrary {
	read, write := f.libraryPaths()
	lib := &staleITPLibrary{locs: map[string]bool{}}
	for _, l := range []struct{ role, path string }{{"imported library", read}, {"write-back library", write}} {
		if strings.TrimSpace(l.path) == "" {
			lib.err = fmt.Errorf("the %s path is not configured", l.role)
			return lib
		}
		n, err := addITunesLocations(l.path, lib.locs)
		if err != nil {
			lib.err = fmt.Errorf("the %s %s cannot be read: %w", l.role, l.path, err)
			return lib
		}
		lib.desc = append(lib.desc, fmt.Sprintf("%s %s (%d tracks)", l.role, l.path, n))
	}
	return lib
}

// addITunesLocations adds every track location of the library at path (an
// .itl, by its "hdfm" magic, or an XML plist) to locs and returns how many
// tracks it has. A library with no tracks is an error: an empty parse cannot
// be told from a library that failed to parse.
func addITunesLocations(path string, locs map[string]bool) (int, error) {
	magic, err := readMagic(path)
	if err != nil {
		return 0, err
	}
	n := 0
	add := func(loc string) {
		for _, k := range itunesLocationKeys(loc) {
			locs[k] = true
		}
	}
	if magic == "hdfm" {
		lib, err := itunes.ParseITL(path)
		if err != nil {
			return 0, err
		}
		for _, t := range lib.Tracks {
			add(t.Location)
			add(t.LocalURL)
		}
		n = len(lib.Tracks)
	} else {
		lib, err := itunes.ParseLibrary(path)
		if err != nil {
			return 0, err
		}
		for _, t := range lib.Tracks {
			if t != nil {
				add(t.Location)
			}
		}
		n = len(lib.Tracks)
	}
	if n == 0 {
		return 0, errors.New("it parsed with no tracks")
	}
	return n, nil
}

func readMagic(path string) (string, error) {
	fh, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer fh.Close()
	b := make([]byte, 4)
	if _, err := io.ReadFull(fh, b); err != nil {
		return "", fmt.Errorf("read the first bytes: %w", err)
	}
	return string(b), nil
}

// itunesLocationKeys normalises an iTunes location (a native Windows path,
// a file:// URL, or a stored itunes_path) for comparison: the file:// prefix
// dropped, '\' as '/', lower case, Unicode NFC. The raw and the
// percent-decoded spellings are both returned ('+' is not a space in a file
// URL), so a location that does not decode still compares as written. It is
// deliberately loose: a false match only keeps a row from being cleared.
func itunesLocationKeys(raw string) []string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil
	}
	low := strings.ToLower(s)
	for _, pre := range []string{"file://localhost/", "file:///", "file://"} {
		if strings.HasPrefix(low, pre) {
			s = s[len(pre):]
			break
		}
	}
	canon := func(v string) string {
		v = strings.ReplaceAll(v, `\`, "/")
		v = strings.TrimLeft(v, "/")
		return norm.NFC.String(strings.ToLower(v))
	}
	out := []string{canon(s)}
	if dec, err := url.PathUnescape(s); err == nil {
		if d := canon(dec); d != out[0] {
			out = append(out, d)
		}
	}
	return out
}

// ---- apply-run scope ------------------------------------------------------

type staleITPLibKey struct{}

// BeginApply parses the libraries once for the whole apply run: every
// Replan reads them from the context instead of parsing per row.
func (f *staleITunesPathFixer) BeginApply(ctx context.Context, _ bool) (context.Context, func()) {
	return context.WithValue(ctx, staleITPLibKey{}, f.loadLibrary()), func() {}
}

func (f *staleITunesPathFixer) libraryFor(ctx context.Context) *staleITPLibrary {
	if lib, ok := ctx.Value(staleITPLibKey{}).(*staleITPLibrary); ok && lib != nil {
		return lib
	}
	return f.loadLibrary()
}

// ---- plan -----------------------------------------------------------------

// staleITPField is one itunes_path the book carries: the book's own
// (FileID "") or a book_file row's.
type staleITPField struct {
	FileID string
	Path   string
	Backed bool
}

func (x staleITPField) where() string {
	if x.FileID == "" {
		return staleITPBookFieldLocation
	}
	return "book_file:" + x.FileID
}

// staleITPInput is what one book's row is decided from.
type staleITPInput struct {
	Book  database.BookCore
	Gone  bool
	Rows  []database.BookFileCore
	Exts  []database.ExternalIDMapping
	ExErr error
}

func (f *staleITunesPathFixer) Plan(ctx context.Context, raw json.RawMessage, rep registry.Reporter) ([]repairs.Row, error) {
	store := f.p.deps.OpsStore()
	if store == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	var params staleITPParams
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, fmt.Errorf("%s: invalid params: %w", staleITPFixerID, err)
		}
	}
	lib := f.loadLibrary()
	var inputs []*staleITPInput
	if len(params.BookIDs) > 0 {
		seen := map[string]bool{}
		for _, id := range params.BookIDs {
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			in, err := f.readInput(store, id)
			if err != nil {
				return nil, err
			}
			inputs = append(inputs, in)
		}
	} else {
		books, err := store.GetAllBooksCore(0, 0)
		if err != nil {
			return nil, fmt.Errorf("%s: list books: %w", staleITPFixerID, err)
		}
		files, err := store.GetAllBookFilesCore()
		if err != nil {
			return nil, fmt.Errorf("%s: list book files: %w", staleITPFixerID, err)
		}
		rowsOf := map[string][]database.BookFileCore{}
		withPath := map[string]bool{}
		for i := range files {
			rowsOf[files[i].BookID] = append(rowsOf[files[i].BookID], files[i])
			if files[i].ITunesPath != "" {
				withPath[files[i].BookID] = true
			}
		}
		for i := range books {
			b := books[i]
			if b.IsSoftDeleted() {
				continue
			}
			if !withPath[b.ID] && (b.ITunesPath == nil || *b.ITunesPath == "") {
				continue
			}
			inputs = append(inputs, &staleITPInput{Book: b, Rows: rowsOf[b.ID]})
		}
		// External ids are point reads: on the pool. Each worker writes
		// only its own input.
		var done atomic.Int64
		err = registry.RunItems(ctx, rep, indexesOf(len(inputs)), func(_ context.Context, i int) error {
			defer done.Add(1)
			in := inputs[i]
			in.Exts, in.ExErr = store.GetExternalIDsForBook(in.Book.ID)
			return nil
		}, registry.RunItemsOptions{
			Concurrency: runtime.NumCPU(),
			ErrMode:     registry.ErrModeCollect,
			Label: func(_, total int) string {
				return fmt.Sprintf("Stale iTunes paths: external ids %d/%d", done.Load(), total)
			},
		})
		if err != nil && ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	rows := make([]repairs.Row, 0, len(inputs))
	for _, in := range inputs {
		rows = append(rows, f.evaluate(in, lib))
	}
	return rows, nil
}

// readInput reads one book fresh (Replan, and a plan limited to book_ids).
func (f *staleITunesPathFixer) readInput(store OpsStore, id string) (*staleITPInput, error) {
	b, err := store.GetBookByID(id)
	if err != nil {
		return nil, fmt.Errorf("%s: read book %s: %w", staleITPFixerID, id, err)
	}
	if b == nil || b.IsSoftDeleted() {
		return &staleITPInput{Book: database.BookCore{ID: id}, Gone: true}, nil
	}
	in := &staleITPInput{Book: b.Core()}
	files, err := store.GetBookFiles(id)
	if err != nil {
		return nil, fmt.Errorf("%s: files of %s: %w", staleITPFixerID, id, err)
	}
	for i := range files {
		in.Rows = append(in.Rows, files[i].Core())
	}
	in.Exts, in.ExErr = store.GetExternalIDsForBook(id)
	return in, nil
}

// evaluate decides one book's row.
func (f *staleITunesPathFixer) evaluate(in *staleITPInput, lib *staleITPLibrary) repairs.Row {
	b := in.Book
	r := repairs.Row{RowID: b.ID, BookIDs: []string{b.ID}, Title: b.Title, Risk: repairs.RiskReview}
	hold := func(class, skip, why string, parts ...string) repairs.Row {
		r.Class, r.Skipped, r.SkipReason, r.Reason = class, skip, why, why
		r.Fingerprint = staleITPFingerprint(append([]string{r.RowID, class}, parts...)...)
		return r
	}
	if in.Gone {
		return hold(staleITPClassGone, staleITPSkipGone, "the book no longer exists or was retired")
	}
	var fields []staleITPField
	if b.ITunesPath != nil && *b.ITunesPath != "" {
		fields = append(fields, staleITPField{Path: *b.ITunesPath})
	}
	rows := append([]database.BookFileCore(nil), in.Rows...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	for _, x := range rows {
		if x.ITunesPath != "" {
			fields = append(fields, staleITPField{FileID: x.ID, Path: x.ITunesPath})
		}
	}
	var fp []string
	for _, x := range fields {
		fp = append(fp, x.where()+"="+x.Path)
	}
	r.Current = map[string]string{"itunes_paths": strconv.Itoa(len(fields))}
	if len(fields) == 0 {
		return hold(staleITPClassNoPath, staleITPSkipNoPath, "the book and its files carry no iTunes path")
	}
	r.Current["itunes_path"] = fields[0].Path
	// An iTunes id: the book is a real iTunes book, whatever its path says.
	if id := staleITPITunesID(b, rows, in.Exts, in.ExErr); id != "" {
		return hold(staleITPClassHasID, staleITPSkipHasID, "it carries "+id+"; an iTunes book's path is never cleared", fp...)
	}
	if lib.err != nil {
		return hold(staleITPClassUnreadable, staleITPSkipUnreadable,
			"the iTunes libraries could not be read in full, so no path can be called stale: "+lib.err.Error(), fp...)
	}
	backed := 0
	for i := range fields {
		fields[i].Backed = lib.has(fields[i].Path)
		if fields[i].Backed {
			backed++
			fp[i] += "|backed"
		}
	}
	libs := strings.Join(lib.desc, " and ")
	for _, x := range fields {
		verdict := "no track at this location in " + libs
		if x.Backed {
			verdict = "a track is at this location"
		}
		r.Evidence = append(r.Evidence, fmt.Sprintf("%s: %s: %s", x.where(), x.Path, verdict))
	}
	switch {
	case backed == len(fields):
		return hold(staleITPClassBacked, staleITPSkipBacked, "a track of the iTunes library is at every iTunes path it carries", fp...)
	case backed > 0:
		return hold(staleITPClassPartly, staleITPSkipPartly, fmt.Sprintf(
			"%d of its %d iTunes paths have a track behind them: iTunes still tracks the book, so none is cleared; decide by hand",
			backed, len(fields)), fp...)
	}
	r.Class = staleITPClassStale
	r.Reason = fmt.Sprintf("%d iTunes path(s) name a location no track of %s has", len(fields), libs)
	r.Proposed = map[string]string{"itunes_paths": "0", "action": fmt.Sprintf("clear %d iTunes path(s); nothing else changes", len(fields))}
	r.Detail = fields
	r.Fingerprint = staleITPFingerprint(append([]string{r.RowID, staleITPClassStale}, fp...)...)
	return r
}

// staleITPITunesID names an iTunes id the book carries ("" none): on the
// book, on a row, or an itunes external id (tombstoned ones too: fail
// closed). External ids that cannot be read count as one.
func staleITPITunesID(b database.BookCore, rows []database.BookFileCore, exts []database.ExternalIDMapping, exErr error) string {
	if b.ITunesPersistentID != nil && *b.ITunesPersistentID != "" {
		return "book iTunes id " + *b.ITunesPersistentID
	}
	for _, x := range rows {
		if x.ITunesPersistentID != "" {
			return "row " + x.ID + " iTunes id " + x.ITunesPersistentID
		}
	}
	if exErr != nil {
		return "external ids that cannot be read (" + exErr.Error() + ")"
	}
	for _, e := range exts {
		if e.Source == "itunes" && e.ExternalID != "" {
			if e.Tombstoned {
				return "tombstoned itunes external id " + e.ExternalID
			}
			return "itunes external id " + e.ExternalID
		}
	}
	return ""
}

func staleITPFingerprint(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:])
}

// ---- replan / apply -------------------------------------------------------

func (f *staleITunesPathFixer) Replan(ctx context.Context, _ json.RawMessage, planned repairs.Row, _ registry.Reporter) (repairs.Row, error) {
	store := f.p.deps.OpsStore()
	if store == nil {
		return repairs.Row{}, fmt.Errorf("database not initialized")
	}
	in, err := f.readInput(store, planned.RowID)
	if err != nil {
		return repairs.Row{}, err
	}
	return f.evaluate(in, f.libraryFor(ctx)), nil
}

// Apply clears each stale path, the book's first, each journaled and
// compare-and-set against the re-plan's value.
func (f *staleITunesPathFixer) Apply(ctx context.Context, w *repairs.Writer, fresh repairs.Row) error {
	fields, ok := fresh.Detail.([]staleITPField)
	if !ok || len(fields) == 0 {
		return fmt.Errorf("%s: row %s carries no decision", staleITPFixerID, fresh.RowID)
	}
	done := 0
	for _, x := range fields {
		if err := ctx.Err(); err != nil {
			return staleITPPartial(done, err)
		}
		var err error
		if x.FileID == "" {
			err = w.ClearBookITunesPath(fresh.RowID, x.Path)
		} else {
			err = w.ClearBookFileITunesPath(fresh.RowID, x.FileID, x.Path)
		}
		if err != nil {
			return staleITPPartial(done, err)
		}
		done++
	}
	return nil
}

func staleITPPartial(done int, err error) error {
	if done == 0 {
		return err
	}
	return fmt.Errorf("%w: after %d of the row's paths: %w", repairs.ErrPartiallyApplied, done, err)
}
