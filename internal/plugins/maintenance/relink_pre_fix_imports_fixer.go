// file: internal/plugins/maintenance/relink_pre_fix_imports_fixer.go
// version: 1.0.1
// guid: a5cfbf11-cb27-4747-a0e3-d1536081ba52
// last-edited: 2026-10-11

package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
)

// relinkPreFixFixerID is the Repairs-lane id of the pre-fix import relink.
const relinkPreFixFixerID = "maintenance.relink-pre-fix-imports"

// Classes of a relink row. Every row carries one in Row.Class, so the plan
// counts each (PlanResult.ByClass) and the rows endpoint lists each; a row
// apply never writes carries the same string in Row.Skipped.
const (
	// pfrClassLink: the book has exactly one live (active or alt) version
	// no row names, carrying the row's torrent hash (both empty for a manual
	// import); apply adopts it through versions.CreateIngestVersion.
	pfrClassLink = "would_link"
	// pfrClassSameBook: the row's torrent is held by a live version of
	// this same book that another of its rows already names (the todo's
	// "linked elsewhere" multi-row case, decided by comparing the holder's
	// BookID with the book); apply links the row to that version.
	pfrClassSameBook = "same_book_version"
	// pfrClassLinked: the row already names a version of its own book.
	pfrClassLinked = "already_linked"
	// pfrClassHeldOther: a live version of ANOTHER book holds the row's
	// torrent. Refused: linking would move the one-to-one torrent index.
	pfrClassHeldOther = "held_by_other_book"
	// pfrClassForeign: the row names another book's version (a merge moved
	// the row and kept the absorbed book's VersionID). Reported, never
	// changed: the owner decides.
	pfrClassForeign = "foreign_version_id"
	// pfrClassDangling: the row names a version that does not exist.
	// Reported, never changed.
	pfrClassDangling = "dangling_version_id"
	// pfrClassNoCandidate: the book has versions, but none the row can be
	// linked to (every one is named by a row, or not live).
	pfrClassNoCandidate = "no_candidate"
	// pfrClassHashMismatch: the book has an unnamed live version, but it
	// carries a different torrent hash than the row (or the row carries one
	// and the version none). Not inferred: linking would make the row claim a
	// torrent it was not recorded from.
	pfrClassHashMismatch = "hash_mismatch"
	// pfrClassAmbiguous: more than one way to link the row (several
	// unnamed versions with its hash, another version of the book holding
	// the torrent, or several hashless rows for one hashless version).
	pfrClassAmbiguous = "ambiguous"
	// pfrClassPathNotAllowed: the row's path is outside the library root,
	// the import paths and the default prefixes, which
	// versions.CreateIngestVersion refuses (fileops.ValidateUserPath).
	pfrClassPathNotAllowed = "path_not_allowed"
	// pfrClassBookDeleted: the row's book is soft-deleted.
	pfrClassBookDeleted = "book_deleted"
	// pfrClassDeletedHolder: the row carries a torrent hash, its own book has
	// no version for it, and the torrent index names a version of a DELETED
	// book -- in practice a pre-fix import that a merge moved onto this book,
	// because a merge moves book_file rows but not versions. Reported, never
	// changed: linking the row to a version of a retired book, or moving the
	// version, is the owner's call.
	pfrClassDeletedHolder = "torrent_on_deleted_book"
	// pfrClassGone: the row no longer exists on the planned book (Replan
	// only: it was moved or the book was removed).
	pfrClassGone = "gone"
	// pfrClassError: a read failed; the row was not classified.
	pfrClassError = "error"
)

// relinkPreFixFixer links the book_file rows imported before the
// IMPORT-ORPHANED-VERSIONS fix (2026-10-10) to the versions those imports
// recorded. Until then importer.ImportFile created the version BEFORE the
// book_file row, so the version found nothing to link: every import left an
// active version no row names and a row with no VersionID (and, until the hash
// backfill reached it, no FileHash), invisible to exact-duplicate matching
// and "unverifiable" to the torrent cleanup.
//
// PLAN (read-only; repairs.plan is the dry run). One bulk read of every
// book_file row finds the books worth looking at; each book is then
// classified on its own (versions, rows, torrent holder) on a bounded
// registry.RunItems pool. A book with no version, no row naming one and no
// unlinked row carrying a torrent hash is not listed: that is every scanned
// book, and none of them has anything to link. (A book with such a hashed row
// is listed even without a version: a merge moves rows but not versions, so a
// merged pre-fix import lands on a keeper that has none.)
// Every row of a listed book is classified into one of the pfrClass*
// outcomes, with its book, path, proposed version and reason.
//
// APPLY writes only would_link and same_book_version rows. Per row: the
// file's identity hash is computed first, outside every lock, when the row
// has none (filehash.BookFileHash, the algorithm the importer and the
// backfill-file-hashes job use: the whole file up to 100 MB, 10 MB from each
// end above it). Then, under the merge lock (a merge moving the row cannot
// interleave) and versions.LockTorrent(hash) (an import of the same torrent
// cannot interleave), the row is re-planned and refused as changed when its
// decision moved; the link is journaled (undo.ChangeTypeBookFileVersionLink,
// record-only) and written: would_link through versions.CreateIngestVersion
// in adopt-only mode (it never creates a version), same_book_version through
// a compare-and-set on the row. Nothing is ever deleted, and no version is
// created or changed.
//
// CONCURRENCY. Rows are book_file rows and BookIDs is the one book, so the
// engine's union-find runs every row of a book in one partition, in order: no
// two workers touch one book. Two books sharing a torrent are serialized by
// LockTorrent. The merge lock is process-wide, so the locked section is kept
// to reads and two point writes; the file read happens before it.
type relinkPreFixFixer struct{ p *Plugin }

func newRelinkPreFixFixer(p *Plugin) *relinkPreFixFixer { return &relinkPreFixFixer{p: p} }

var _ repairs.Fixer = (*relinkPreFixFixer)(nil)

// pfrLog logs through internal/logger, which sanitizes each line. The pass runs
// on user-controlled paths and torrent-derived IDs, so it never calls log/slog
// directly (internal/logger TestGuard_NoDirectSlogCalls).
var pfrLog = logger.New("maintenance")

func (f *relinkPreFixFixer) ID() string    { return relinkPreFixFixerID }
func (f *relinkPreFixFixer) Title() string { return "Link pre-fix imports to their versions" }
func (f *relinkPreFixFixer) Description() string {
	return "Files imported before 2026-10-10 were recorded with a version (where the copy came from and which " +
		"torrent it belongs to) that nothing points at, so they cannot be matched as exact duplicates or verified " +
		"by the torrent cleanup. Apply links each selected file to its own book's stranded version, and fills the " +
		"file's checksum when it has none. It never creates or deletes a version or a file record. Books without " +
		"any version are not listed."
}

// pfrStores returns the fixer's two store views.
func (f *relinkPreFixFixer) pfrStores() (OpsStore, versions.IngestStore, error) {
	store, vs := f.p.deps.OpsStore(), f.p.deps.IngestVersionStore()
	if store == nil || vs == nil {
		return nil, nil, fmt.Errorf("database not initialized")
	}
	return store, vs, nil
}

// pfrWorkers bounds the plan's per-book pool: each book is a handful of
// point reads, so this is I/O-bound, not CPU-bound.
func pfrWorkers() int { return titleRepairWorkers() }

// pfrDecision is one row's classification, carried from Replan to Apply
// in Row.Detail.
type pfrDecision struct {
	class  string
	reason string
	book   *database.Book
	file   database.BookFile
	// target is the version a link class links the row to.
	target *database.BookVersion
	// holder is the torrent's live holder, when the row carries a hash.
	holder   *database.BookVersion
	evidence []string
	// action is what the fingerprint hashes beside the row's own state:
	// "link:<version>" for both link classes (so a sibling's adoption, which
	// turns would_link into same_book_version, does not change it), the
	// class and its decisive ids otherwise.
	action string
}

func (d *pfrDecision) links() bool {
	return d.class == pfrClassLink || d.class == pfrClassSameBook
}

// Plan lists every book_file row of every live book that has a version or a
// row naming one, classified.
func (f *relinkPreFixFixer) Plan(ctx context.Context, _ json.RawMessage, rep registry.Reporter) ([]repairs.Row, error) {
	store, vs, err := f.pfrStores()
	if err != nil {
		return nil, err
	}
	cores, err := store.GetAllBookFilesCoreComplete()
	if err != nil {
		return nil, fmt.Errorf("list book files: %w", err)
	}
	named := map[string]bool{}
	// hashed: books with an unlinked row carrying a torrent hash. Such a book
	// is listed even with no version of its own: a merge moves rows, not
	// versions, so a merged pre-fix import sits on a keeper with no version.
	hashed := map[string]bool{}
	var books []string
	seen := map[string]bool{}
	for i := range cores {
		id := cores[i].BookID
		if id == "" {
			continue
		}
		if !seen[id] {
			seen[id] = true
			books = append(books, id)
		}
		if cores[i].VersionID != "" {
			named[id] = true
		} else if cores[i].DelugeHash != "" {
			hashed[id] = true
		}
	}
	sort.Strings(books)
	importPaths, err := vs.GetAllImportPaths()
	if err != nil {
		return nil, fmt.Errorf("list import paths: %w", err)
	}
	perBook := make([][]repairs.Row, len(books))
	var done atomic.Int64
	// Each worker writes only perBook[i] for its own book: one item per book,
	// so no two workers read-and-classify the same book.
	runErr := registry.RunItems(ctx, rep, indexesOf(len(books)), func(_ context.Context, i int) error {
		defer done.Add(1)
		id := books[i]
		if !named[id] && !hashed[id] {
			vers, verr := vs.GetBookVersionsByBookID(id)
			if verr == nil && len(vers) == 0 {
				return nil
			}
		}
		decs, cerr := classifyPreFixBook(vs, id, importPaths)
		if cerr != nil {
			perBook[i] = pfrErrorRows(id, cores, cerr)
			return nil
		}
		rows := make([]repairs.Row, 0, len(decs))
		for _, d := range decs {
			rows = append(rows, pfrRow(d))
		}
		perBook[i] = rows
		return nil
	}, registry.RunItemsOptions{
		Concurrency: pfrWorkers(),
		ErrMode:     registry.ErrModeCollect,
		// Label runs inside the workers: it reads only the atomic.
		Label: func(_, total int) string { return fmt.Sprintf("Pre-fix import relink %d/%d", done.Load(), total) },
	})
	if runErr != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var out []repairs.Row
	for _, rows := range perBook {
		out = append(out, rows...)
	}
	return out, nil
}

// pfrErrorRows reports every row of bookID as unclassified.
func pfrErrorRows(bookID string, cores []database.BookFileCore, cause error) []repairs.Row {
	var rows []repairs.Row
	for i := range cores {
		if cores[i].BookID != bookID {
			continue
		}
		d := &pfrDecision{class: pfrClassError, reason: cause.Error(),
			file:   database.BookFile{ID: cores[i].ID, BookID: bookID, FilePath: cores[i].FilePath},
			action: pfrClassError}
		rows = append(rows, pfrRow(d))
	}
	return rows
}

// Replan re-classifies the planned row's book now and returns the row.
func (f *relinkPreFixFixer) Replan(_ context.Context, _ json.RawMessage, planned repairs.Row, _ registry.Reporter) (repairs.Row, error) {
	_, vs, err := f.pfrStores()
	if err != nil {
		return repairs.Row{}, err
	}
	if len(planned.BookIDs) != 1 {
		return repairs.Row{}, fmt.Errorf("relink row %s: want one book, got %d", planned.RowID, len(planned.BookIDs))
	}
	bookID := planned.BookIDs[0]
	importPaths, err := vs.GetAllImportPaths()
	if err != nil {
		return repairs.Row{}, fmt.Errorf("list import paths: %w", err)
	}
	decs, err := classifyPreFixBook(vs, bookID, importPaths)
	if err != nil {
		return repairs.Row{}, fmt.Errorf("classify book %s: %w", bookID, err)
	}
	for _, d := range decs {
		if d.file.ID == planned.RowID {
			return pfrRow(d), nil
		}
	}
	d := &pfrDecision{class: pfrClassGone, reason: "the file record is no longer on this book",
		file: database.BookFile{ID: planned.RowID, BookID: bookID}, action: pfrClassGone}
	return pfrRow(d), nil
}

// classifyPreFixBook reads one book, its versions and its rows, and decides
// every row. A read error fails the whole book (it is reported, not guessed).
func classifyPreFixBook(vs versions.IngestStore, bookID string, importPaths []database.ImportPath) ([]*pfrDecision, error) {
	book, err := vs.GetBookByID(bookID)
	if err != nil {
		return nil, fmt.Errorf("read book: %w", err)
	}
	files, err := vs.GetBookFiles(bookID)
	if err != nil {
		return nil, fmt.Errorf("read book files: %w", err)
	}
	if len(files) == 0 {
		return nil, nil
	}
	vers, err := vs.GetBookVersionsByBookID(bookID)
	if err != nil {
		return nil, fmt.Errorf("read versions: %w", err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].ID < files[j].ID })
	own := make(map[string]*database.BookVersion, len(vers))
	for i := range vers {
		own[vers[i].ID] = &vers[i]
	}
	referenced := map[string]string{} // version id -> the first row naming it
	for _, fl := range files {
		if fl.VersionID != "" {
			if _, ok := referenced[fl.VersionID]; !ok {
				referenced[fl.VersionID] = fl.ID
			}
		}
	}
	// peers counts this book's unlinked rows per torrent hash.
	peers := map[string]int{}
	for _, fl := range files {
		if fl.VersionID == "" {
			peers[fl.DelugeHash]++
		}
	}
	holders := map[string]*database.BookVersion{}
	holderOf := func(hash string) (*database.BookVersion, error) {
		if h, ok := holders[hash]; ok {
			return h, nil
		}
		h, herr := versions.LiveTorrentHolder(vs, hash)
		if herr != nil {
			return nil, herr
		}
		holders[hash] = h
		return h, nil
	}

	out := make([]*pfrDecision, 0, len(files))
	for _, fl := range files {
		d := &pfrDecision{book: book, file: fl}
		out = append(out, d)
		if book == nil || book.IsSoftDeleted() {
			d.class, d.action = pfrClassBookDeleted, pfrClassBookDeleted
			d.reason = "the book is deleted"
			continue
		}
		if fl.VersionID != "" {
			decidePreFixNamedRow(vs, d, own)
			continue
		}
		decidePreFixUnlinkedRow(vs, d, vers, referenced, peers[fl.DelugeHash], holderOf, importPaths)
	}
	return out, nil
}

// decidePreFixNamedRow classifies a row that already names a version.
func decidePreFixNamedRow(vs versions.IngestStore, d *pfrDecision, own map[string]*database.BookVersion) {
	vid := d.file.VersionID
	if v, ok := own[vid]; ok {
		d.class, d.action = pfrClassLinked, pfrClassLinked+":"+vid
		d.target = v
		d.reason = "the file is linked to version " + vid + " of its book"
		return
	}
	v, err := vs.GetBookVersion(vid)
	switch {
	case err != nil:
		d.class, d.action = pfrClassError, pfrClassError
		d.reason = fmt.Sprintf("read version %s: %v", vid, err)
	case v == nil:
		d.class, d.action = pfrClassDangling, pfrClassDangling+":"+vid
		d.reason = "the file names version " + vid + ", which does not exist; left for the owner"
	default:
		d.class, d.action = pfrClassForeign, pfrClassForeign+":"+vid+":"+v.BookID+":"+v.Status
		d.reason = "the file names version " + vid + " of another book (" + v.BookID +
			"), usually left behind when a merge moved the file; left for the owner"
		d.evidence = append(d.evidence, "version "+vid+" belongs to book "+v.BookID+", status "+v.Status)
		if v.TorrentHash != "" {
			d.evidence = append(d.evidence, "that version holds torrent "+v.TorrentHash)
		}
	}
}

// decidePreFixUnlinkedRow classifies a row that names no version.
func decidePreFixUnlinkedRow(vs versions.IngestStore, d *pfrDecision, vers []database.BookVersion, referenced map[string]string,
	peers int, holderOf func(string) (*database.BookVersion, error), importPaths []database.ImportPath) {
	fl := d.file
	hash := fl.DelugeHash
	set := func(class, action, reason string) {
		d.class, d.action, d.reason = class, action, reason
	}
	if abs, err := filepath.Abs(fl.FilePath); err != nil || fl.FilePath == "" || !fileops.IsAllowedPath(abs, importPaths) {
		set(pfrClassPathNotAllowed, pfrClassPathNotAllowed,
			"the file's path is outside the library root, the import paths and the allowed prefixes; linking reads the file there")
		return
	}
	var stranded, otherHash []*database.BookVersion
	for i := range vers {
		v := &vers[i]
		if !pfrAdoptable(v) {
			continue
		}
		if _, named := referenced[v.ID]; named {
			continue
		}
		if v.TorrentHash == hash {
			stranded = append(stranded, v)
		} else {
			otherHash = append(otherHash, v)
		}
	}
	var holder *database.BookVersion
	if hash != "" {
		h, err := holderOf(hash)
		if err != nil {
			set(pfrClassError, pfrClassError, fmt.Sprintf("read the torrent index for %s: %v", hash, err))
			return
		}
		holder = h
		d.holder = h
		if h == nil {
			d.evidence = append(d.evidence, "torrent "+hash+": no live version holds it in the torrent index")
		} else {
			d.evidence = append(d.evidence, "torrent "+hash+" is held by version "+h.ID+" of book "+h.BookID)
		}
	}
	if holder != nil && holder.BookID != fl.BookID {
		set(pfrClassHeldOther, pfrClassHeldOther+":"+holder.ID,
			"torrent "+hash+" is held by version "+holder.ID+" of another book ("+holder.BookID+
				"); linking would take it from that book")
		return
	}
	switch {
	case len(stranded) > 1:
		ids := make([]string, len(stranded))
		for i, v := range stranded {
			ids[i] = v.ID
		}
		set(pfrClassAmbiguous, pfrClassAmbiguous+":"+strings.Join(ids, ","),
			fmt.Sprintf("the book has %d unlinked live versions with this file's torrent hash (%s); which one is the file's is a guess",
				len(stranded), strings.Join(ids, ", ")))
	case len(stranded) == 1:
		v := stranded[0]
		switch {
		case holder != nil && holder.ID != v.ID:
			set(pfrClassAmbiguous, pfrClassAmbiguous+":"+v.ID+":"+holder.ID,
				"the book's unlinked version "+v.ID+" carries the torrent, but its version "+holder.ID+" holds it in the torrent index")
		case hash == "" && peers > 1:
			set(pfrClassAmbiguous, pfrClassAmbiguous+":"+v.ID+":hashless",
				fmt.Sprintf("%d files of this book carry no torrent hash and the book has one unlinked version without one (%s); "+
					"which file that import recorded is a guess", peers, v.ID))
		default:
			d.target = v
			d.class, d.action = pfrClassLink, pfrLinkAction(v)
			d.reason = "the book's one unlinked " + v.Status + " version " + v.ID + " carries this file's torrent hash" +
				pfrHashWords(hash) + "; an import before 2026-10-10 recorded it without linking the file"
		}
	case holder != nil && pfrAdoptable(holder):
		// The holder is a version of this book (the other-book case returned
		// above) and is not unlinked (it would be in stranded), so another row
		// of the book names it: the todo's same-book "linked elsewhere" case.
		d.target = holder
		d.class, d.action = pfrClassSameBook, pfrLinkAction(holder)
		d.reason = "torrent " + hash + " is held by version " + holder.ID + " of this same book, already linked to file " +
			referenced[holder.ID] + "; this file carries the same torrent, so it is linked to that version too"
	case holder != nil:
		set(pfrClassNoCandidate, pfrClassNoCandidate+":"+holder.ID+":"+holder.Status,
			"torrent "+hash+" is held by version "+holder.ID+" of this book in status "+holder.Status+", which is not linked to files")
	case hash != "" && holder == nil && pfrDeletedHolder(vs, d, hash):
		// Classified (or failed) inside pfrDeletedHolder.
	case len(otherHash) > 0:
		hashes := make([]string, len(otherHash))
		for i, v := range otherHash {
			hashes[i] = v.ID + "=" + pfrHashLabel(v.TorrentHash)
		}
		set(pfrClassHashMismatch, pfrClassHashMismatch+":"+strings.Join(hashes, ","),
			"the book's unlinked versions carry a different torrent hash than the file ("+pfrHashLabel(hash)+" vs "+
				strings.Join(hashes, ", ")+"); not inferred")
	default:
		set(pfrClassNoCandidate, pfrClassNoCandidate,
			"every live version of the book is already linked to a file, or none is live")
	}
}

func pfrLinkAction(v *database.BookVersion) string {
	return "link:" + v.ID + ":" + v.BookID + ":" + v.Status + ":" + v.TorrentHash
}

// pfrAdoptable: the statuses a file is linked to (the ones
// versions.CreateIngestVersion adopts).
func pfrAdoptable(v *database.BookVersion) bool {
	return v.Status == database.BookVersionStatusActive || v.Status == database.BookVersionStatusAlt
}

func pfrHashLabel(h string) string {
	if h == "" {
		return "(none)"
	}
	return h
}

func pfrHashWords(h string) string {
	if h == "" {
		return " (none: a manual import)"
	}
	return " (" + h + ")"
}

// pfrRow builds the plan row for d.
func pfrRow(d *pfrDecision) repairs.Row {
	fl := d.file
	r := repairs.Row{RowID: fl.ID, BookIDs: []string{fl.BookID}, Class: d.class, Reason: d.reason,
		Risk: repairs.RiskLow, Evidence: d.evidence, Detail: d}
	if d.book != nil {
		r.Title = d.book.Title
		if d.book.Author != nil {
			r.Author = d.book.Author.Name
		}
	}
	fileHash := "present"
	if fl.FileHash == "" {
		fileHash = "missing"
	}
	r.Current = map[string]string{
		"file":       fl.FilePath,
		"version_id": pfrHashLabel(fl.VersionID),
		"torrent":    pfrHashLabel(fl.DelugeHash),
		"file_hash":  fileHash,
	}
	if d.links() {
		r.Proposed = map[string]string{"version_id": d.target.ID, "version_status": d.target.Status,
			"version_source": d.target.Source}
		if fl.FileHash == "" {
			r.Proposed["file_hash"] = "filled"
		}
		if d.class == pfrClassSameBook {
			r.Risk = repairs.RiskReview
		}
	} else {
		r.Skipped, r.SkipReason = d.class, d.reason
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{fl.ID, fl.BookID, fl.VersionID, fl.DelugeHash, fl.FilePath, d.action}, "\n")))
	r.Fingerprint = hex.EncodeToString(sum[:])[:32]
	return r
}

// Apply links one fresh would_link or same_book_version row (see the type
// comment for the order of the hash, the locks and the write).
func (f *relinkPreFixFixer) Apply(ctx context.Context, w *repairs.Writer, fresh repairs.Row) error {
	store, vs, err := f.pfrStores()
	if err != nil {
		return err
	}
	d, ok := fresh.Detail.(*pfrDecision)
	if !ok || d == nil || !d.links() {
		return fmt.Errorf("%w: row %s is not a link", repairs.ErrChangedSincePlan, fresh.RowID)
	}
	bookID, fileID := d.file.BookID, d.file.ID

	// The identity hash, read before any lock: a bounded file read that must
	// not hold the process-wide merge lock. A failure leaves the hash to the
	// backfill-file-hashes job; the link does not depend on it.
	hash := ""
	if d.file.FileHash == "" {
		hash = pfrIdentityHash(vs, d.file)
	}

	// The process-wide merge lock first, then the torrent's: a merge moving
	// the row between books, or an import of the same torrent, cannot run
	// between the re-check below and the write. Nothing that holds a torrent
	// lock takes the merge lock (only the importer takes torrent locks), so
	// this order cannot deadlock.
	if err := w.LockWaiting(ctx, "the merge lock", merge.LockMergeRMW, merge.UnlockMergeRMW); err != nil {
		return err
	}
	defer merge.UnlockMergeRMW()
	unlockTorrent := versions.LockTorrent(d.file.DelugeHash)
	defer unlockTorrent()

	locked, err := f.Replan(ctx, nil, fresh, nil)
	if err != nil {
		return err
	}
	if locked.Fingerprint != fresh.Fingerprint || !locked.Applicable() {
		why := locked.Reason
		if !locked.Applicable() {
			why = locked.SkipReason
		}
		return fmt.Errorf("%w: under the merge and torrent locks: %s", repairs.ErrChangedSincePlan, why)
	}
	ld, ok := locked.Detail.(*pfrDecision)
	if !ok || ld == nil || !ld.links() {
		return fmt.Errorf("%w: row %s is not a link under the locks", repairs.ErrChangedSincePlan, fileID)
	}
	if err := w.Beat("version link of file " + fileID); err != nil {
		return err
	}
	target := ld.target
	entry := repairs.UndoEntry{ChangeType: undo.ChangeTypeBookFileVersionLink, Field: "book_file:" + fileID, Old: "", New: target.ID}
	return w.JournalStep(bookID, entry, func() error {
		if ld.class == pfrClassSameBook {
			return pfrSameBook(store, ld, hash)
		}
		return pfrAdopt(store, vs, ld, hash)
	})
}

// errPreFixRowMoved: the row's compare-and-set found it changed.
var errPreFixRowMoved = errors.New("the file record changed before the link was written")

// pfrAdopt links the row to its stranded version through
// versions.CreateIngestVersion in adopt-only mode. The hash computed before
// the locks is stored first (a compare-and-set on an unchanged, still
// hashless row), so CreateIngestVersion, which trusts a row that carries one,
// does not read the file again under the merge lock. Returns an error only
// when the link was not written.
func pfrAdopt(store OpsStore, vs versions.IngestStore, d *pfrDecision, hash string) error {
	fl := d.file
	if hash != "" {
		if _, err := store.ModifyBookFile(fl.BookID, fl.ID, func(r *database.BookFile) error {
			if r.FileHash != "" || r.FilePath != fl.FilePath || r.VersionID != "" {
				return database.ErrSkipBookFileWrite
			}
			r.FileHash = hash
			return nil
		}); err != nil && !errors.Is(err, database.ErrBookFileDurabilityUnknown) {
			// Not fatal: the link below hashes the file itself when the row
			// still has none.
			pfrLog.Warn("relink-pre-fix-imports: identity hash not stored before the link for book %s (book_file %s): %s",
				logger.SanitizeLogValue(fl.BookID), logger.SanitizeLogValue(fl.ID), logger.SanitizeLogValue(err.Error()))
		}
	}
	ver, linked, err := versions.CreateIngestVersion(vs, versions.IngestVersionParams{
		BookID: fl.BookID, BookFileID: fl.ID, FilePath: fl.FilePath, Format: fl.Format,
		TorrentHash: fl.DelugeHash, AdoptVersionID: d.target.ID,
	})
	switch {
	case err != nil:
		return fmt.Errorf("adopt version %s for file %s: %w", d.target.ID, fl.ID, err)
	case ver == nil || ver.ID != d.target.ID:
		got := "none"
		if ver != nil {
			got = ver.ID
		}
		return fmt.Errorf("%w: file %s is linked to version %s, not %s", repairs.ErrChangedSincePlan, fl.ID, got, d.target.ID)
	case !linked:
		return fmt.Errorf("adopt version %s for file %s: the link write failed (logged by versions)", d.target.ID, fl.ID)
	}
	return nil
}

// pfrSameBook links the row to the version of its own book that holds its
// torrent, in one compare-and-set: the row must still name no version, still
// belong to the book and still carry the planned torrent hash and path. The
// identity hash computed before the locks is stored in the same write when
// the row still has none.
func pfrSameBook(store OpsStore, d *pfrDecision, hash string) error {
	fl := d.file
	moved := false
	row, err := store.ModifyBookFile(fl.BookID, fl.ID, func(r *database.BookFile) error {
		if r.VersionID != "" || r.DelugeHash != fl.DelugeHash || r.FilePath != fl.FilePath {
			moved = true
			return database.ErrSkipBookFileWrite
		}
		r.VersionID = d.target.ID
		if r.FileHash == "" && hash != "" {
			r.FileHash = hash
		}
		return nil
	})
	if err != nil && !errors.Is(err, database.ErrBookFileDurabilityUnknown) {
		return fmt.Errorf("link file %s to version %s: %w", fl.ID, d.target.ID, err)
	}
	if row == nil && err == nil {
		return fmt.Errorf("%w: file %s is gone", repairs.ErrChangedSincePlan, fl.ID)
	}
	if moved {
		return fmt.Errorf("%w: %w", repairs.ErrChangedSincePlan, errPreFixRowMoved)
	}
	return nil
}

// pfrIdentityHash computes the row's identity hash (filehash.BookFileHash)
// on the allow-listed path, or "" when the file cannot be read.
func pfrIdentityHash(vs versions.IngestStore, fl database.BookFile) string {
	path, err := fileops.ValidateUserPath(vs, fl.FilePath)
	if err != nil {
		return ""
	}
	h, err := filehash.BookFileHash(path)
	if err != nil {
		pfrLog.Warn("relink-pre-fix-imports: identity hash failed for %s (book %s, book_file %s); left for backfill-file-hashes: %s",
			logger.SanitizeLogValue(fl.FilePath), logger.SanitizeLogValue(fl.BookID), logger.SanitizeLogValue(fl.ID),
			logger.SanitizeLogValue(err.Error()))
		return ""
	}
	return h
}

// pfrDeletedHolder classifies d as pfrClassDeletedHolder when the torrent
// index names a version of a deleted book other than d's (LiveTorrentHolder
// reports no holder for it), and reports whether it classified d. A read
// error classifies d as pfrClassError.
func pfrDeletedHolder(vs versions.IngestStore, d *pfrDecision, hash string) bool {
	v, err := vs.GetBookVersionByTorrentHash(hash)
	if err != nil {
		d.class, d.action = pfrClassError, pfrClassError
		d.reason = fmt.Sprintf("read the torrent index for %s: %v", hash, err)
		return true
	}
	if v == nil || v.BookID == d.file.BookID {
		return false
	}
	b, err := vs.GetBookByID(v.BookID)
	if err != nil {
		d.class, d.action = pfrClassError, pfrClassError
		d.reason = fmt.Sprintf("read book %s: %v", v.BookID, err)
		return true
	}
	if b != nil && !b.IsSoftDeleted() {
		return false
	}
	merged := ""
	if b != nil && b.MergedIntoBookID != nil {
		merged = *b.MergedIntoBookID
	}
	d.class, d.action = pfrClassDeletedHolder, pfrClassDeletedHolder+":"+v.ID+":"+v.BookID+":"+merged
	d.reason = "torrent " + hash + " belongs to version " + v.ID + " of deleted book " + v.BookID +
		"; this book has no version for it (a merge moves files, not versions); left for the owner"
	d.evidence = append(d.evidence, "version "+v.ID+" (status "+v.Status+") is on deleted book "+v.BookID)
	if merged != "" {
		d.evidence = append(d.evidence, "book "+v.BookID+" was merged into "+merged)
	}
	return true
}
