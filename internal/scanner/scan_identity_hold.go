// file: internal/scanner/scan_identity_hold.go
// version: 1.0.0
// guid: acb6d67e-dc3f-4d2f-adcb-ee17eacb8ca4
// last-edited: 2026-10-06
//
// Keeps a rescan from rewriting an existing book's identity, whatever the
// scanned value came from, and records what it would have written.

package scanner

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/personname"
)

// Why (2026-10-06): the nightly library.scan rewrote the title or author of
// 1,176 EXISTING books from their tags and file names ("183 of 301" became
// "of 301" on 299 Shadow's Edge fragments, "74" became "The Jasmine Throne").
// Each title or author change is a search-identity change, so the store
// dropped the book's cached metadata candidates in the same write, and the
// scanner recorded no history, so nothing showed it. The folder-parse hold
// (holdFolderFieldsForExisting, folderDerivedLocks) covered only values the
// folder parse supplied; a tag, the file-name fallback or the AI parse still
// got through.
//
// The rule now: a rescan, move or rename of an existing row keeps the row's
// title, author, series and series position -- and with them its work and
// its author credits -- whatever source the scanned value came from. A field
// the row does NOT have yet (an empty title, no author or the "Unknown
// Author" placeholder, no series, no position) is still filled, with a
// history row (recordScanHistory): filling a gap is not a rewrite. A row this
// same scan created (Book.createdRowID: the inline AI phase re-saves a new
// import) is a new import and takes every value. Where the scanned value
// differs from the held one it is recorded as a proposal
// (database.ScanIdentityProposal) for the reviewed fixer
// maintenance.scan-proposed-identity, so nothing the file says is lost.

// scannedIdentity is what the scan read for a book's identity, captured
// before any hold replaces it with the row's values.
type scannedIdentity struct {
	Title, Author, Series string
	Position              int
	positionFromTitle     bool
}

func scannedIdentityOf(b *Book) scannedIdentity {
	return scannedIdentity{Title: b.Title, Author: b.Author, Series: b.Series, Position: b.Position,
		positionFromTitle: b.positionFromTitle}
}

// identityGuard is what the merge-time hold (identityMergeLocks) needs about
// the row, read before ModifyBook so its callback does no IO.
type identityGuard struct {
	// placeholderAuthorID is the row's author id when that author is the
	// "Unknown Author" placeholder: a placeholder is a gap, and is filled.
	placeholderAuthorID *int
	scanned             scannedIdentity
}

// isPlaceholderAuthorName reports whether name is an "Unknown Author"
// placeholder (decorations stripped, as the scanner's other guards do).
func isPlaceholderAuthorName(name string) bool {
	return strings.TrimSpace(name) == "" || authorname.IsPlaceholder(personname.StripEditionSuffix(name))
}

// newIdentityGuard reads the row's author name once, outside the write.
func newIdentityGuard(row *database.Book, scanned scannedIdentity) identityGuard {
	g := identityGuard{scanned: scanned}
	if row == nil || row.AuthorID == nil || *row.AuthorID == 0 {
		return g
	}
	if st := getStore(); st != nil {
		if a, err := st.GetAuthorByID(*row.AuthorID); err == nil && a != nil && isPlaceholderAuthorName(a.Name) {
			id := *row.AuthorID
			g.placeholderAuthorID = &id
		}
	}
	return g
}

// holdIdentityForExisting replaces, on book, the scanned title, author,
// series and position with existing's stored values wherever existing has
// one, and returns what it held so saveBookToDatabase resolves (and creates)
// no author, series or work row from a value the row will not take. A new
// book (existing nil) or a row this scan created is left alone.
func holdIdentityForExisting(book *Book, existing *database.Book) folderHold {
	var h folderHold
	store := getStore()
	if store == nil || existing == nil || (book.createdRowID != "" && existing.ID == book.createdRowID) {
		return h
	}
	titleHeld := strings.TrimSpace(existing.Title) != ""
	if titleHeld {
		if book.positionFromTitle && !database.SameIdentityText(book.Title, existing.Title) {
			// The position was read off a title the row does not take.
			book.Position = 0
			book.positionFromTitle = false
		}
		book.Title = existing.Title
	}
	authorHeld := false
	if existing.AuthorID != nil && *existing.AuthorID != 0 {
		if a, err := store.GetAuthorByID(*existing.AuthorID); err != nil || a == nil || !isPlaceholderAuthorName(a.Name) {
			// An unreadable author is held: the row's id is kept, not
			// replaced on a guess.
			authorHeld = true
			h.author, h.authorID = true, existing.AuthorID
			if err == nil && a != nil {
				book.Author = a.Name
			}
		}
	}
	if existing.SeriesID != nil && *existing.SeriesID != 0 {
		h.series, h.seriesID = true, existing.SeriesID
		book.Series = ""
		if s, err := store.GetSeriesByID(*existing.SeriesID); err == nil && s != nil {
			book.Series = s.Name
		}
	}
	if existing.SeriesSequence != nil && *existing.SeriesSequence != 0 {
		book.Position = *existing.SeriesSequence
	}
	if titleHeld && authorHeld && existing.WorkID != nil {
		h.work, h.workID = true, existing.WorkID
	}
	return h
}

// mergeHolds combines the identity hold with the folder hold: a field
// either holds is held at the row's value.
func mergeHolds(a, b folderHold) folderHold {
	if b.author && !a.author {
		a.author, a.authorID = true, b.authorID
	}
	if b.series && !a.series {
		a.series, a.seriesID = true, b.seriesID
	}
	if b.work && !a.work {
		a.work, a.workID = true, b.workID
	}
	return a
}

// identityMergeLocks returns locked plus the lock key of every identity field
// cur (the row at write time) already holds, and that set alone as held. The
// rescan overlay (applyScannerFields) skips locked keys, so it fills an
// identity gap and never replaces a value. It runs inside ModifyBook, so it
// reads only cur and g. It is the write-time half of the hold: the raced-row
// and late hash-duplicate paths reach the merge without passing
// holdIdentityForExisting.
func identityMergeLocks(locked map[string]bool, cur *database.Book, g identityGuard) (map[string]bool, map[string]bool) {
	held := map[string]bool{}
	if strings.TrimSpace(cur.Title) != "" {
		held[database.FieldKeyTitle] = true
	}
	if cur.AuthorID != nil && *cur.AuthorID != 0 &&
		(g.placeholderAuthorID == nil || *g.placeholderAuthorID != *cur.AuthorID) {
		held[database.FieldKeyAuthorName] = true
	}
	if cur.SeriesID != nil && *cur.SeriesID != 0 {
		held[database.FieldKeySeriesName] = true
	}
	if cur.SeriesSequence != nil && *cur.SeriesSequence != 0 {
		held[database.FieldKeySeriesPosition] = true
	}
	// A position read off a title the row does not take is not the row's.
	if held[database.FieldKeyTitle] && g.scanned.positionFromTitle && !database.SameIdentityText(g.scanned.Title, cur.Title) {
		held[database.FieldKeySeriesPosition] = true
	}
	out := make(map[string]bool, len(locked)+len(held))
	for k, v := range locked {
		out[k] = v
	}
	for k := range held {
		out[k] = true
	}
	return out, held
}

// scanIdentityChanges lists each held field whose scanned value differs from
// the value written: the proposal. A field the user locked is not proposed
// (it is never rewritten); an empty scanned value proposes nothing.
func scanIdentityChanges(written *database.Book, s scannedIdentity, held, userLocked map[string]bool) map[string]database.ScanIdentityChange {
	out := map[string]database.ScanIdentityChange{}
	st := getStore()
	add := func(key, field, from, to string) {
		if !held[key] || userLocked[key] || strings.TrimSpace(to) == "" || database.SameIdentityText(from, to) {
			return
		}
		out[field] = database.ScanIdentityChange{From: from, To: strings.TrimSpace(to)}
	}
	add(database.FieldKeyTitle, database.ScanProposalTitle, written.Title, s.Title)
	if held[database.FieldKeyAuthorName] && strings.TrimSpace(s.Author) != "" && st != nil && written.AuthorID != nil {
		from := "#" + strconv.Itoa(*written.AuthorID)
		if a, err := st.GetAuthorByID(*written.AuthorID); err == nil && a != nil {
			from = a.Name
		}
		add(database.FieldKeyAuthorName, database.ScanProposalAuthor, from, s.Author)
	}
	if held[database.FieldKeySeriesName] && strings.TrimSpace(s.Series) != "" && st != nil && written.SeriesID != nil {
		from := "#" + strconv.Itoa(*written.SeriesID)
		if sr, err := st.GetSeriesByID(*written.SeriesID); err == nil && sr != nil {
			from = sr.Name
		}
		add(database.FieldKeySeriesName, database.ScanProposalSeries, from, s.Series)
	}
	if s.Position > 0 {
		from := ""
		if written.SeriesSequence != nil && *written.SeriesSequence != 0 {
			from = strconv.Itoa(*written.SeriesSequence)
		}
		add(database.FieldKeySeriesPosition, database.ScanProposalSeriesPosition, from, strconv.Itoa(s.Position))
	}
	return out
}

// persistScanIdentityProposal writes bookID's proposal, rewrites it only when
// it changed, and clears it once the file agrees with the row again. A
// failure is logged and never fails the scan: the row was already held.
func persistScanIdentityProposal(bookID, path string, changes map[string]database.ScanIdentityChange) {
	st := getStore()
	if st == nil || bookID == "" {
		return
	}
	key := database.ScanIdentityProposalKey(bookID)
	raw, err := st.GetRaw(key)
	if err != nil {
		defaultLog.Warn("scan identity proposal for %s unreadable: %v", bookID, err)
		raw = nil
	}
	var prev *database.ScanIdentityProposal
	if len(raw) > 0 {
		var p database.ScanIdentityProposal
		if json.Unmarshal(raw, &p) == nil {
			prev = &p
		}
	}
	if len(changes) == 0 {
		if len(raw) > 0 {
			if derr := st.DeleteRaw(key); derr != nil {
				defaultLog.Warn("clearing scan identity proposal for %s: %v", bookID, derr)
			}
		}
		return
	}
	if prev != nil && prev.SameFields(changes) && prev.FilePath == path {
		return
	}
	data, err := json.Marshal(database.ScanIdentityProposal{BookID: bookID, FilePath: path,
		ObservedAt: time.Now().UTC(), Fields: changes})
	if err != nil {
		defaultLog.Warn("encoding scan identity proposal for %s: %v", bookID, err)
		return
	}
	if err := st.SetRaw(key, data); err != nil {
		defaultLog.Warn("recording scan identity proposal for %s: %v", bookID, err)
		return
	}
	scanIdentityProposalsRecorded.Add(1)
	defaultLog.Info("rescan of %s held its identity; the file reads differently: %s",
		bookID, describeScanIdentityChanges(changes))
}

func describeScanIdentityChanges(changes map[string]database.ScanIdentityChange) string {
	var parts []string
	for _, k := range []string{database.ScanProposalTitle, database.ScanProposalAuthor, database.ScanProposalSeries,
		database.ScanProposalSeriesPosition} {
		if c, ok := changes[k]; ok {
			parts = append(parts, k+" "+strconv.Quote(c.From)+" -> "+strconv.Quote(c.To))
		}
	}
	return strings.Join(parts, ", ")
}
