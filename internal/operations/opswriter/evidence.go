// file: internal/operations/opswriter/evidence.go
// version: 2.4.0
// guid: 86a05fa0-d0ef-43d9-a47d-4e1763aad9cc
// last-edited: 2026-10-14

package opswriter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// MarkRowApplied and RowApplied (ops.Writer) are the evidence a resumed
// Fixer apply needs before it reports a row as applied by an earlier attempt
// of the same run (05-PR5b). The evidence is per ROW: applyRow records a
// marker for the row as soon as the row's own Apply returns nil, naming the
// history Apply's writes recorded (each write notes its metadata history
// batch or operation journal row ids on the item, ops.NoteHistory). A marker
// can only exist because this run applied that row, so the attempt that
// wrote it does not matter. History is still read, but only to see whether
// what the marker names still stands: not undone, and not followed on what
// it wrote by a later write recorded in either store (RowApplied).
//
// Why not per subject: a run's history for a book proves the run wrote SOME
// row of that book, not this one. Two rows of one book (title, then
// publisher), the first written and the second not, with a foreign edit
// setting the publisher to the second row's Proposed between the attempts,
// made per-subject evidence report the foreign edit as the run's own.

// RowMarkStore stores the per-row apply markers (opv3:rowmark:).
type RowMarkStore interface {
	PutRowMarkV3(id string, m database.RowMarkV3) error
	GetRowMarkV3(id, rowID string) (*database.RowMarkV3, error)
}

// ChangeReader reads operation change rows: one by id (a point read), and a
// book's whole journal across every operation (the opchange_by_book index
// once it is trusted, a full journal scan until then).
type ChangeReader interface {
	GetOperationChange(operationID, changeID string) (*database.OperationChange, error)
	GetBookChanges(bookID string) ([]*database.OperationChange, error)
}

// BookHistoryReader reads a book's whole metadata history, uncapped, and
// counts the rows that did not decode (database.PebbleStore
// ReadBookChangeHistory). GetBookChangeHistory is not enough: it drops an
// undecodable row silently, and the row it drops could be the one that
// overwrote a batch.
type BookHistoryReader interface {
	ReadBookChangeHistory(bookID string) (rows []database.MetadataChangeRecord, undecodable int, err error)
}

// historyRef is what a write notes on its item (ops.NoteHistory): one
// metadata history batch of a book, one operation journal row, or that some
// of a write's history could not be recorded (Incomplete).
type historyRef struct {
	Book   string `json:"book,omitempty"`
	Batch  string `json:"batch,omitempty"`
	Change string `json:"change,omitempty"`
	// Incomplete: a write of the item committed but part of its history
	// was not recorded. A marker naming only the rest could outlive an
	// overwrite or undo of the part it leaves out, so MarkRowApplied
	// records none.
	Incomplete bool `json:"incomplete,omitempty"`
}

func noteRef(ctx context.Context, r historyRef) {
	b, err := json.Marshal(r)
	if err != nil {
		// Three strings and a bool cannot fail to marshal; dropping the
		// reference would only make a later marker name less history.
		return
	}
	ops.NoteHistory(ctx, string(b))
}

// noteBatch notes a metadata history batch ModifyBook recorded.
func noteBatch(ctx context.Context, bookID, batchID string) {
	noteRef(ctx, historyRef{Book: bookID, Batch: batchID})
}

// noteChange notes an operation journal row journalRows recorded.
func noteChange(ctx context.Context, changeID string) {
	noteRef(ctx, historyRef{Change: changeID})
}

// noteIncomplete notes that a committed write's history was not all
// recorded (historyRef.Incomplete).
func noteIncomplete(ctx context.Context) {
	noteRef(ctx, historyRef{Incomplete: true})
}

// MarkRowApplied records the apply marker of row rowID (ops.Writer): the
// row, and every batch and journal row its Apply's writes noted on ctx's
// item. Preview, a Writer without a marker store, or an empty rowID records
// nothing. Gated like a write: a fenced run or a canceled ctx records
// nothing. So does an item one of whose writes could not record all of its
// history (noteIncomplete). Nothing is returned: the row's writes
// committed, so a marker that cannot be recorded is logged, and costs only
// the evidence (a resume skips the row as changed since plan; it never
// writes it twice).
func (w *Writer) MarkRowApplied(ctx context.Context, rowID string) {
	if w.cfg.Mode == ops.Preview || w.cfg.RowMarks == nil || rowID == "" {
		return
	}
	what := "apply marker of row " + rowID
	if err := w.check(ctx, what); err != nil {
		pkgLog.Warn("op %s: %s", logger.SanitizeLogValue(w.cfg.OpID), logger.SanitizeLogValue(err.Error()))
		return
	}
	m := database.RowMarkV3{RowID: rowID, MarkedAt: time.Now().UTC()}
	for _, raw := range ops.NotedHistory(ctx) {
		var r historyRef
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			// A marker that names less than the row wrote could outlive an
			// undo of the part it leaves out: record none.
			pkgLog.Warn("op %s: %s not written: unreadable history reference %q",
				logger.SanitizeLogValue(w.cfg.OpID), logger.SanitizeLogValue(what), logger.SanitizeLogValue(raw))
			return
		}
		switch {
		case r.Incomplete:
			pkgLog.Warn("op %s: %s not written: part of the row's history was not recorded (a resume will skip the row)",
				logger.SanitizeLogValue(w.cfg.OpID), logger.SanitizeLogValue(what))
			return
		case r.Change != "":
			m.Changes = append(m.Changes, r.Change)
		case r.Book != "" && r.Batch != "":
			m.BookBatches = append(m.BookBatches, database.RowMarkBatch{BookID: r.Book, BatchID: r.Batch})
		}
	}
	if err := w.cfg.RowMarks.PutRowMarkV3(w.cfg.OpID, m); err != nil {
		pkgLog.Warn("op %s: %s not written (the row's writes committed; a resume will skip it): %s",
			logger.SanitizeLogValue(w.cfg.OpID), logger.SanitizeLogValue(what), logger.SanitizeLogValue(err.Error()))
	}
}

// RowApplied reports whether some attempt of this run marked row rowID
// applied and everything the marker names still stands (ops.Writer):
//
//   - each journal row it names is still there, neither voided nor reverted
//     (journalRowsLive);
//   - each batch it names is in its book's metadata history and its run was
//     not reverted there (batchesUndone), and no batch was undone by
//     undo-last-apply (standsIn), both judged without the clock;
//   - each book_file a journal row it names wrote is still a file of that
//     book (filesStillOwned), judged without the clock;
//   - no write it names has been followed, on what it wrote, by a later
//     write recorded in EITHER store -- a metadata history row or an
//     operation journal row of the same book, of any operation and any
//     change type (standsIn, subjectOf), judged by the rows' stamps.
//
// A read is gated like a write, so a fenced or canceled run reads nothing
// and a resumed run that is only recognizing rows still renews its
// stand-down lease. One point read for the marker, one per journal row it
// names, then per book it names: one metadata history read, one journal
// read (bookJournal), and one book_file point read per file it names.
func (w *Writer) RowApplied(ctx context.Context, rowID string) (bool, error) {
	if err := w.checkAs(ctx, "apply marker of resumed row "+rowID, refusedRead); err != nil {
		return false, err
	}
	if w.cfg.Mode == ops.Preview || w.cfg.RowMarks == nil || rowID == "" {
		return false, nil
	}
	m, err := w.cfg.RowMarks.GetRowMarkV3(w.cfg.OpID, rowID)
	if err != nil {
		return false, fmt.Errorf("read the apply marker of row %s: %w", rowID, err)
	}
	if m == nil {
		return false, nil
	}
	named, ok, err := w.journalRowsLive(m.Changes)
	if err != nil || !ok {
		return false, err
	}
	books := map[string]*bookNamed{}
	book := func(id string) *bookNamed {
		if books[id] == nil {
			books[id] = &bookNamed{batches: map[string]bool{}}
		}
		return books[id]
	}
	for _, b := range m.BookBatches {
		book(b.BookID).batches[b.BatchID] = true
	}
	for _, c := range named {
		// A journal row with no book id has no book to look in: only
		// journalRowsLive's checks apply to it.
		if c.BookID != "" {
			book(c.BookID).changes = append(book(c.BookID).changes, c)
		}
	}
	if len(books) == 0 {
		return true, nil
	}
	if w.cfg.HistoryReader == nil || w.cfg.Changes == nil {
		// Either store can hold the later write: without both reads the
		// marker cannot be shown to stand.
		return false, nil
	}
	for bookID, n := range books {
		ok, err := w.bookStands(ctx, bookID, n)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

// bookNamed is what a marker names on one book: its batches there and its
// journal rows there.
type bookNamed struct {
	batches map[string]bool
	changes []*database.OperationChange
}

// journalRowsLive point-reads every journal row of ids (under this op id)
// and reports whether each is still there and neither voided nor reverted
// (Voided is checked on its own: a voided row normally has RevertedAt set
// too, but nothing older guarantees it). A row that cannot be found, or
// that does not decode, cannot be shown to stand (false, not an error).
func (w *Writer) journalRowsLive(ids []string) ([]*database.OperationChange, bool, error) {
	if len(ids) == 0 {
		return nil, true, nil
	}
	if w.cfg.Changes == nil {
		return nil, false, nil
	}
	out := make([]*database.OperationChange, 0, len(ids))
	for _, id := range ids {
		c, err := w.cfg.Changes.GetOperationChange(w.cfg.OpID, id)
		if undecodable(err) {
			pkgLog.Warn("op %s: journal row %s does not decode; it is not counted as standing: %s",
				logger.SanitizeLogValue(w.cfg.OpID), logger.SanitizeLogValue(id), logger.SanitizeLogValue(err.Error()))
			return nil, false, nil
		}
		if err != nil {
			return nil, false, fmt.Errorf("read journal row %s of op %s: %w", id, w.cfg.OpID, err)
		}
		if c == nil || c.Voided || c.RevertedAt != nil {
			return nil, false, nil
		}
		out = append(out, c)
	}
	return out, true, nil
}

// undecodable reports whether err is a JSON decode failure of a stored row
// (as opposed to a failed read): the row is there but cannot be judged.
func undecodable(err error) bool {
	var syntax *json.SyntaxError
	var typ *json.UnmarshalTypeError
	return errors.As(err, &syntax) || errors.As(err, &typ)
}

// bookStands reads one book's metadata history and operation journal and
// applies batchesUndone and standsIn to what the marker names there. A book
// whose history or journal has a row that does not decode cannot be shown
// to stand: the unreadable row could be the later write (fail closed, not
// an error).
func (w *Writer) bookStands(ctx context.Context, bookID string, n *bookNamed) (bool, error) {
	hist, bad, err := w.cfg.HistoryReader.ReadBookChangeHistory(bookID)
	if err != nil {
		return false, fmt.Errorf("read the metadata history of book %s: %w", bookID, err)
	}
	if bad > 0 {
		pkgLog.Warn("op %s: %d metadata history rows of book %s do not decode; what the marker names there is not counted as standing",
			logger.SanitizeLogValue(w.cfg.OpID), bad, logger.SanitizeLogValue(bookID))
		return false, nil
	}
	journal, err := w.bookJournal(ctx, bookID)
	if undecodable(err) {
		pkgLog.Warn("op %s: the operation journal of book %s has a row that does not decode; what the marker names there is not counted as standing: %s",
			logger.SanitizeLogValue(w.cfg.OpID), logger.SanitizeLogValue(bookID), logger.SanitizeLogValue(err.Error()))
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read the operation journal of book %s: %w", bookID, err)
	}
	if len(n.batches) > 0 && batchesUndone(hist, n.batches, w.cfg.OpID) {
		return false, nil
	}
	if ok, err := w.filesStillOwned(bookID, n.changes); err != nil || !ok {
		return false, err
	}
	return standsIn(hist, journal, n, BatchPrefix(w.cfg.OpID)), nil
}

// filesStillOwned reports whether every book_file a named journal row of
// this book wrote is still a file of this book. A book_file row is keyed by
// its book, and a reassignment (a merge, a regroup, a version split) moves
// it under another book's key -- journaled, when it is, on the TARGET book,
// whose journal this check never reads, and by several movers not at all.
// A file no longer under the book it was written on (moved, or deleted)
// means the write cannot be shown to stand, whatever either store says, so
// this is decided without the clock. Without a book_file store it fails
// closed. One point read per file.
func (w *Writer) filesStillOwned(bookID string, named []*database.OperationChange) (bool, error) {
	seen := map[string]bool{}
	for _, c := range named {
		s := subjectOf(c.FieldName, nil)
		if s.file == "" || s.file == everyFile || s.file == anything || seen[s.file] {
			continue
		}
		seen[s.file] = true
		if w.cfg.Files == nil {
			return false, nil
		}
		f, err := w.cfg.Files.GetBookFileByID(bookID, s.file)
		if err != nil {
			return false, fmt.Errorf("read book_file %s of book %s: %w", s.file, bookID, err)
		}
		if f == nil {
			pkgLog.Debug("op %s: book_file %s is no longer a file of book %s; the write the marker names there does not stand",
				logger.SanitizeLogValue(w.cfg.OpID), logger.SanitizeLogValue(s.file), logger.SanitizeLogValue(bookID))
			return false, nil
		}
	}
	return true, nil
}

// batchesUndone reports whether a wanted batch is missing from its book's
// history (no write row of it there) or its run was reverted on this book:
// an operation revert records its rows under the reverted op's id, so any
// row under this run's op id (opID) means it, whatever its timestamp says.
// undo-last-apply needs no check here: it records its undo rows in the
// batch itself, on the fields it puts back, and standsIn makes two rows of
// a wanted batch on one subject defeat each other whatever their stamps.
func batchesUndone(hist []database.MetadataChangeRecord, want map[string]bool, opID string) bool {
	found := map[string]bool{}
	for _, r := range hist {
		if opID != "" && r.BatchID == opID {
			return true
		}
		if want[r.BatchID] && historyWrite(r) {
			found[r.BatchID] = true
		}
	}
	return len(found) != len(want)
}

// historyWrite reports whether a metadata history row records a write to
// the library's rows. Left out: the undo markers undo-last-apply reads
// (apply_op_journaled and apply_incomplete, which every writer records
// under field "apply"), and two rows that only describe work on files
// outside the database (write_back: tags written to the audio files;
// cover_art: the previous cover archived).
func historyWrite(r database.MetadataChangeRecord) bool {
	switch r.Field {
	case "apply", "write_back", "cover_art":
		return false
	}
	return true
}

// write is one recorded write on a book, from either store, as standsIn
// compares them: what it wrote (subject, and the name it was recorded
// under), when it was stamped, and whether the marker names it.
type write struct {
	subj subject
	raw  string
	at   time.Time
	self string // identity: "h<index>" for a history row, "j<id>" for a journal row of this run
	// named: the marker names it. trusted: it is provably this run's own
	// write -- a journal row read under the run's op id, or a history row
	// whose batch id carries the run's batch prefix (BatchPrefix), which
	// no other writer mints.
	named, trusted bool
}

// standsIn reports whether no write it names on this book -- every write
// row of a wanted batch, every named journal row -- was followed on what it
// wrote (subject overlap) by ANOTHER write of either store stamped at or
// after it. A tie counts against the named write. Two rows of wanted
// batches on one subject under ONE name therefore defeat each other
// whatever their stamps: that is how an undo-last-apply (it records its
// undo row in the batch itself, under the field name of the row it puts
// back) is seen without trusting the clock. So does a later write of this
// same run under the same name (a row whose Apply wrote one field twice is
// not claimed), one an operation revert has since reverted, and one that
// put the named value back: the rule asks only whether the named write is
// still the newest, never whether a later one restored its value.
//
// One exemption: two named writes the run provably made itself (write
// trusted) that share a subject under DIFFERENT names do not defeat each
// other. The credit list and the author_id column are one subject (other
// writers record either change under either name), and an Apply that sets
// both records author_id in its batch and "authors" in the journal;
// without the exemption such a row could never be claimed. A foreign write
// is never trusted, so a note naming one cannot exempt it; a note naming
// another write of this same run can (ops.NoteHistory).
//
// The comparison is by timestamp across two stores, so it is only as good
// as the order of the stamps. The Writer stamps its rows after its write
// committed (a history row's ChangedAt in RecordDiff, a journal row's
// CreatedAt in CreateOperationChange, called after the write). Other
// writers do not all do so, and a foreign write F is missed whenever F
// commits after the named write N but F's row carries the earlier stamp:
//
//   - CreateOperationChange stamps CreatedAt before its own batch commits,
//     and stageSeriesObjectDrop stamps a series-object drop with the book
//     write's time before that batch commits: a window of the commit's
//     duration;
//   - a journal-first writer (internal/repairs.Writer, Step and the
//     book_file and credit methods) journals a change BEFORE making it, so
//     its stamp precedes its write by however long the write takes; and
//     when a resumed operation finds its earlier row it records nothing
//     new, so a write made today can carry a stamp from the first attempt.
//     That window has no bound.
//
// Closing it needs journal-first writers to re-stamp their row after the
// write, or a commit sequence compared instead of a clock; neither exists.
// The clock-free cases (batchesUndone, filesStillOwned, and two rows of a
// wanted batch on one subject under one name) do not have this window;
// every other comparison here does.
//
// Nor can it see a write recorded in NEITHER store: many book_file writers
// outside the Writer (the scanner, the AcoustID and tag backfills, the
// Deluge link backfill) record nothing, and seeing those needs a
// per-book_file history this store does not keep (todo.d,
// 2026-10-14-bookfile-change-history-for-fixer-resume).
func standsIn(hist []database.MetadataChangeRecord, journal []journalRow, n *bookNamed, batchPrefix string) bool {
	// files: every book_file id a recognised file name of this book's rows
	// (either store) names, so a journal row named by the bare file id
	// reads as a write to that whole file. Taken from the store, never from
	// what the marker names: a note on the item cannot change how a
	// foreign row is read.
	files := map[string]bool{}
	addFile := func(name string) {
		if s := subjectOf(name, nil); s.file != "" && s.file != everyFile && s.file != anything {
			files[s.file] = true
		}
	}
	for _, r := range hist {
		addFile(r.Field)
	}
	for _, r := range journal {
		addFile(r.field)
	}
	namedChange := make(map[string]bool, len(n.changes))
	for _, c := range n.changes {
		namedChange[c.ID] = true
	}
	var all, named []write
	for i, r := range hist {
		if !historyWrite(r) {
			continue
		}
		mine := n.batches[r.BatchID]
		wr := write{subj: subjectOf(r.Field, files), raw: r.Field, at: r.ChangedAt, self: fmt.Sprintf("h%d", i),
			named: mine, trusted: mine && batchPrefix != "" && strings.HasPrefix(r.BatchID, batchPrefix)}
		all = append(all, wr)
		if mine {
			named = append(named, wr)
		}
	}
	for _, r := range journal {
		wr := write{subj: subjectOf(r.field, files), raw: r.field, at: r.stamp()}
		if r.own {
			wr.self = "j" + r.id
			wr.named = namedChange[r.id]
			wr.trusted = wr.named
		}
		all = append(all, wr)
	}
	for _, c := range n.changes {
		// Read by id under the run's op id (journalRowsLive): trusted.
		named = append(named, write{subj: subjectOf(c.FieldName, nil), raw: c.FieldName, at: c.CreatedAt,
			self: "j" + c.ID, named: true, trusted: true})
	}
	for _, nw := range named {
		for _, r := range all {
			if r.self == nw.self || !overlap(nw.subj, r.subj) || r.at.Before(nw.at) {
				continue
			}
			if nw.trusted && r.trusted && r.raw != nw.raw {
				continue // the run's own writes to one subject under two names
			}
			return false
		}
	}
	return true
}

// subject is what one recorded write wrote, as both stores name it.
//
//   - file "": the book field part (a Book JSON name, or "authors" for the
//     credit list).
//   - file <id>: book_file <id>; part is one of its fields (BookFile JSON
//     name), or "" for the file row as a whole.
//   - file everyFile: every book_file of the book.
//   - file anything: a name this check does not know; it overlaps every
//     write on the book, so an unknown name fails closed.
type subject struct{ file, part string }

const (
	everyFile = "*"
	anything  = "?"
)

// bookFieldAliases maps names other writers record for a book field onto
// the name the Writer records (the Book JSON name), beyond
// database.HistoryFieldToJSON (author_name, series, series_position). Kept
// here, not in HistoryFieldToJSON: database.HistoryFieldName reverses that
// map, and series_name is not the history name of series_id.
//
// The author_id column and the credit list ("authors", the book_authors
// join) are ONE subject: writers record a credit-list rewrite as author_id
// or author_name (the author id repair, the duplicate merge, the path
// link, metafetch's apply history, a manual edit) and a column change as
// book_authors (the entities author ops), so a write under any of these
// names may have changed either.
var bookFieldAliases = map[string]string{
	"author":        "authors",   // journal rows of author merges and purges
	"author_id":     "authors",   // the column; also credit rewrites journaled under it
	"book_authors":  "authors",   // credit-list rewrites (author merge, relink, series dedup)
	"series_name":   "series_id", // a manual edit's series override (its lock key)
	"series_object": "series_id", // database.HistoryFieldSeriesObject
}

// knownBookFields: every name subjectOf reads as a book field.
var knownBookFields = func() map[string]bool {
	m := map[string]bool{"authors": true}
	for _, f := range database.TrackedBookFields() {
		m[f] = true
	}
	return m
}()

// subjectOf reads a metadata history Field or an operation journal
// FieldName as a subject. files, when set, are book_file ids a bare name
// may stand for (a repoint or a deletion journaled with FieldName = the
// file id).
func subjectOf(name string, files map[string]bool) subject {
	switch name {
	case "file_path":
		// The book's path: the organizer, the reconciler, fs-regroup and
		// a book field write all move the book's files with it.
		return subject{file: everyFile}
	case "track_number", "disc_number":
		// A manual track or disc edit recorded without its file id.
		return subject{file: everyFile}
	}
	if rest, ok := strings.CutPrefix(name, "book_file:"); ok {
		if id, part, ok := strings.Cut(rest, ":"); ok {
			return subject{file: id, part: part} // book_file:<id>:<field>, a manual file edit
		}
		return subject{file: rest} // book_file:<id>, a repoint, move, reassign, track change
	}
	if rest, ok := strings.CutPrefix(name, "file."); ok {
		if i := strings.LastIndexByte(rest, ':'); i > 0 {
			return subject{file: rest[i+1:], part: rest[:i]} // Writer.ModifyBookFile
		}
	}
	if files[name] {
		return subject{file: name}
	}
	if j, ok := database.HistoryFieldToJSON[name]; ok {
		name = j
	}
	if j, ok := bookFieldAliases[name]; ok {
		name = j
	}
	if knownBookFields[name] {
		return subject{part: name}
	}
	return subject{file: anything}
}

// overlap reports whether writes to a and b can write the same thing: an
// unknown name and anything, the same book field, every file and any file,
// a whole file and any field of it, or the same field of the same file.
func overlap(a, b subject) bool {
	switch {
	case a.file == anything || b.file == anything:
		return true
	case a.file == "" || b.file == "":
		return a.file == b.file && a.part == b.part
	case a.file == everyFile || b.file == everyFile:
		return true
	default:
		return a.file == b.file && (a.part == "" || b.part == "" || a.part == b.part)
	}
}
