// file: internal/applygate/partial.go
// version: 1.9.0
// guid: b1e7d4a0-3c58-4f26-9a8d-0f6c2e5b7d19
// last-edited: 2026-11-18

package applygate

import (
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/seqnum"
)

// ReasonPartialBook: the files hold one part of a book (or a span of several
// books) and the candidate is something else: the whole book, another part,
// or a single volume of a box set. An apply would give a part the whole
// book's title and runtime, and a rename would then collide with the other
// parts.
const ReasonPartialBook = "partial_book"

var (
	// spanRe marks text covering several books.
	spanRe = regexp.MustCompile(`(?i)\b(?:omnibus|box(?:ed)?\s*set|boxset|publisher.?s\s+pack|complete\s+series|books\s*\d+\s*(?:-|–|to|thru|&|and)\s*\d+|vol(?:ume)?s\.?\s*\d+\s*(?:-|–|to|&|and)\s*\d+|vol(?:ume)?\.?\s*\d+\s*(?:&|and)\s*\d+)\b`)
	// fileExtRe is a file extension at the end of a path.
	fileExtRe = regexp.MustCompile(`\.[A-Za-z][A-Za-z0-9]{1,3}$`)
)

// ClaimIndex records, for one batch, which books would take which candidate,
// so checkPartialBook can see a sibling folder holding another part of the
// same book. It is safe for concurrent Add and lookup.
//
// It covers only the books of the batch it was built from: a sibling outside
// the batch is not seen. A nil *ClaimIndex means "no batch data" and skips
// the sibling test (single-book callers such as metadata auto-upgrade).
type ClaimIndex struct {
	mu    sync.RWMutex
	byKey map[string][]claim
	// unreadable are books the index could not read, with whatever is still
	// known about them (folder, ASIN). A row that looks like one of them is
	// blocked for manual review; every other row proceeds.
	unreadable []unreadableClaim
}

type unreadableClaim struct {
	id, dir, asin string
}

type claim struct {
	bookID  string
	dir     string
	part    float64
	hasPart bool
}

// NewClaimIndex returns an empty index.
func NewClaimIndex() *ClaimIndex { return &ClaimIndex{byKey: map[string][]claim{}} }

// Add records that book would take candidate c. Nil arguments are ignored.
func (x *ClaimIndex) Add(book *database.Book, c *metafetch.MetadataCandidate) {
	if x == nil || book == nil || c == nil {
		return
	}
	k := ClaimKey(c)
	if k == "" {
		return
	}
	d := bookDir(book.FilePath)
	p, ok := partOf(bookSide(book.Title, book.FilePath))
	x.mu.Lock()
	x.byKey[k] = append(x.byKey[k], claim{bookID: book.ID, dir: d, part: p, hasPart: ok})
	x.mu.Unlock()
}

// Len is the number of claims recorded.
func (x *ClaimIndex) Len() int {
	if x == nil {
		return 0
	}
	x.mu.RLock()
	defer x.mu.RUnlock()
	n := 0
	for _, v := range x.byKey {
		n += len(v)
	}
	return n
}

func (x *ClaimIndex) get(k string) []claim {
	if x == nil || k == "" {
		return nil
	}
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.byKey[k]
}

// AddUnreadable records a book the index could not read. filePath and asin
// are whatever is still known about it ("" when nothing is). A book with
// neither is only counted: it blocks nothing.
func (x *ClaimIndex) AddUnreadable(id, filePath, asin string) {
	if x == nil {
		return
	}
	u := unreadableClaim{id: id, dir: bookDir(filePath), asin: strings.ToUpper(strings.TrimSpace(asin))}
	x.mu.Lock()
	x.unreadable = append(x.unreadable, u)
	x.mu.Unlock()
}

// Unreadable is the number of books the index could not read.
func (x *ClaimIndex) Unreadable() int {
	if x == nil {
		return 0
	}
	x.mu.RLock()
	defer x.mu.RUnlock()
	return len(x.unreadable)
}

// unreadableLookAlike returns an unreadable book, other than bookID, whose
// folder is related to dir (relatedDirs) or whose ASIN equals asin.
func (x *ClaimIndex) unreadableLookAlike(bookID, dir, asin string) (string, bool) {
	if x == nil {
		return "", false
	}
	a := strings.ToUpper(strings.TrimSpace(asin))
	x.mu.RLock()
	defer x.mu.RUnlock()
	for _, u := range x.unreadable {
		if u.id == bookID {
			continue
		}
		if relatedDirs(u.dir, dir) || (u.asin != "" && u.asin == a) {
			return u.id, true
		}
	}
	return "", false
}

// ClaimKey identifies "the same candidate": the ASIN when there is one, else
// the normalized title plus the sorted author surnames. "" = no key.
func ClaimKey(c *metafetch.MetadataCandidate) string {
	if a := strings.TrimSpace(c.ASIN); a != "" {
		return "asin:" + strings.ToUpper(a)
	}
	t := normText(c.Title)
	if t == "" {
		return ""
	}
	s := surnames(c.Author)
	sort.Strings(s)
	return "ta:" + t + "|" + strings.Join(s, " ")
}

// checkPartialBook blocks when (a) the book's title or folder has a part
// marker the candidate does not share, (b) exactly one side is a multi-book
// span (omnibus, box set, "Books 1-3"), or (c) a book of the same batch in the
// same folder, a nested folder or a sibling folder takes the same candidate
// and carries a part number the candidate lacks.
//
// Rule (a), the part marker, runs whatever the runtime says: parts of one book
// can be within 5% of each other's length, so a matching runtime does not say
// which part the files are. The part the files are is read from the book's own
// text first (its title, its subtitle, the single file's own name) and from the
// folder's label only when that text states none: the folder is where a stale
// marker lives, and a book that says its own part has said which one it is.
// With a runtime that agrees and a candidate that names no part, the folder's
// label is not read at all. The own text outranks the folder only under the
// same marker word ("Part" over "Part"); a disc or CD in the own text and a part
// in the folder are two levels and both must be the candidate's part. Rules (b) and (c) are skipped when the runtime
// AGREES: files whose runtime matches the candidate's are the whole thing,
// whatever the folder says. It returns only block or neutral, never agree.
//
// Not a block: two books taking one candidate with no part marker. On the
// 2026-09-13 preview that caught 3 of the 35 flagged rows and newly blocked
// 308 others, nearly all one audiobook imported twice; that is dedup's job,
// and the rename preflight already refuses two books renamed onto one target.
func checkPartialBook(book *database.Book, c *metafetch.MetadataCandidate, runtimeOutcome string, claims *ClaimIndex) CheckResult {
	r := CheckResult{Name: "partial_book", Outcome: OutcomeNeutral}
	d := bookDir(book.FilePath)
	// A book the index could not read may be exactly the sibling part this
	// row collides with. Its look-alikes (related folder, same ASIN) go to
	// manual review, whatever the runtime says; every other row proceeds.
	if u, ok := claims.unreadableLookAlike(book.ID, d, c.ASIN); ok {
		r.Outcome, r.Reason = OutcomeBlock, ReasonPartialBook
		r.Detail = "sibling " + u + " unreadable; manual review"
		return r
	}
	// The stored subtitle is part of the book's own text: "Part 2" there is as
	// much the files' part marker as one in the title.
	bTitle := strings.TrimSpace(book.Title + " " + derefStr(book.Subtitle))
	bside := bookSide(bTitle, book.FilePath)
	cside := c.Title + " " + c.Subtitle
	_, bok := partOf(bside)
	cp, cok := partOf(cside)
	// The part the files are: the book's own text decides (title, subtitle, the
	// single file's own name), because a folder's label is where a stale marker
	// lives ("Disc 2" over files merged since, "Part 1" over a re-split). The
	// folder's part counts only when the own text states none, and a candidate
	// that names no part, with a runtime that agrees, does not read the folder
	// at all: those files are the whole thing whatever the folder says.
	//
	// The own text beats the folder only when both use the SAME marker word
	// ("Part 2" over "Part 1": one level, and the folder's label is stale). A
	// disc, a CD and a part are different levels ("Hush Disc 1" in the folder
	// "Hush Part 2" is disc 1 OF part 2: neither contradicts the other), so
	// when the words differ BOTH must be the candidate's part.
	mp, mm, mok := partWithMarker(bookOwnSide(bTitle, book.FilePath))
	fp, fm, fok := partWithMarker(dirBase(d))
	if !mok && (cok || runtimeOutcome != OutcomeAgree) {
		mp, mm, mok = fp, fm, fok
		fok = false // the folder is the one level read
	}
	if mok && (!cok || mp != cp) {
		r.Outcome, r.Reason = OutcomeBlock, ReasonPartialBook
		r.Detail = "the files are part " + ftoa(mp) + "; the candidate " + partText(cp, cok)
		return r
	}
	if mok && fok && fm != mm && fp != cp {
		r.Outcome, r.Reason = OutcomeBlock, ReasonPartialBook
		r.Detail = "the folder says " + fm + " " + ftoa(fp) + " and the files " + mm + " " + ftoa(mp) + ", two levels; the candidate " + partText(cp, cok)
		return r
	}
	// The candidate is one part of a book and nothing on the book's side, the
	// folder included, says the files are a part: the files are the whole book,
	// or a part the candidate does not name. (A volume number the candidate
	// alone states is a different matter: that is a series position, and the
	// sequence leg checks it.)
	if cok && !bok {
		r.Outcome, r.Reason = OutcomeBlock, ReasonPartialBook
		r.Detail = "the candidate is part " + ftoa(cp) + "; nothing says the files are a part"
		return r
	}
	// A runtime that agrees settles the spans and the siblings (the files are
	// the whole thing whatever the folder says), but not which part: two
	// parts of one book can be within 5% of each other's length.
	if runtimeOutcome == OutcomeAgree {
		return r
	}
	// The spans themselves are compared. The RANGES (the books covered) must
	// be equal: a range on one side only, or two different ranges ("Books 1-3"
	// and "Books 1-5", a list with a tail), is a book covering other books than
	// the candidate does. Equal ranges with different edition wording ("Books
	// 1-3" and "Omnibus: Books 1-3") are the same books under another name, so
	// this hard refusal does not apply: the collected-edition rule
	// (collectedRuntimeRefusal), which an owner review can lift, decides. With
	// no range on either side the edition wording is the span, and it must be
	// equal (omnibus against a box set, wording on one side only).
	bw, br := spanParts(bside)
	cw, cr := spanParts(cside)
	if br != cr || (br == "" && bw != cw) {
		r.Outcome, r.Reason = OutcomeBlock, ReasonPartialBook
		bs, cs := joinSpan(bw, br), joinSpan(cw, cr)
		if bs == "" || cs == "" {
			r.Detail = "only one side covers several books: files " + strconv.Quote(bs) + ", candidate " + strconv.Quote(cs)
		} else {
			r.Detail = "the two sides cover different books: files " + strconv.Quote(bs) + ", candidate " + strconv.Quote(cs)
		}
		return r
	}
	for _, s := range claims.get(ClaimKey(c)) {
		if s.bookID == book.ID || !relatedDirs(s.dir, d) || !s.hasPart {
			continue
		}
		if !cok || s.part != cp {
			r.Outcome, r.Reason = OutcomeBlock, ReasonPartialBook
			r.Detail = "book " + s.bookID + " in " + strconv.Quote(dirBase(s.dir)) + " is part " + ftoa(s.part) + " and takes the same candidate, which " + partText(cp, cok)
			return r
		}
	}
	return r
}

// spanWordingRe is the span wording that is not a range: the edition kinds.
var spanWordingRe = regexp.MustCompile(`(?i)\b(omnibus|box(?:ed)?\s*set|boxset|publisher.?s\s+pack|complete\s+series)\b`)

// spanParts reads what s covers when it covers several books: the edition
// wording class (omnibus, box set, publisher's pack, complete series; "boxed
// set" and "boxset" are one class) and the range, read by the shared range
// reader (seqnum.ParseRange) in every spelling it accepts, so "Book 1-3",
// "Books 1 to 3", "Vols 1–3" and "Books 1, 2 & 3" give one range and "Books
// 1-3 & 5" another. Any other span wording spanRe knows that the range reader
// does not is part of the range, lowercased with its spacing collapsed. Both
// are "" for a text that covers one book.
func spanParts(s string) (wording, rng string) {
	if m := spanWordingRe.FindString(s); m != "" {
		wording = strings.ToLower(m)
		switch {
		case strings.Contains(wording, "box"):
			wording = "box set"
		case strings.Contains(wording, "pack"):
			wording = "publisher's pack"
		case strings.Contains(wording, "complete"):
			wording = "complete series"
		}
	}
	var parts []string
	if n, ok := seqnum.ParseRange(s); ok {
		parts = append(parts, "books "+n.Text)
	} else {
		for _, m := range spanRe.FindAllString(s, -1) {
			if !spanWordingRe.MatchString(m) {
				parts = append(parts, strings.Join(strings.Fields(strings.ToLower(m)), " "))
			}
		}
	}
	sort.Strings(parts)
	return wording, strings.Join(parts, "; ")
}

func joinSpan(wording, rng string) string {
	switch {
	case wording == "":
		return rng
	case rng == "":
		return wording
	}
	return wording + "; " + rng
}

func partText(p float64, ok bool) string {
	if !ok {
		return "is the whole book"
	}
	return "is part " + ftoa(p)
}

// partWithMarker is partOf with the marker word the part was written with
// ("part", "disc", "cd", "of": seqnum.Stated.Marker).
func partWithMarker(s string) (float64, string, bool) {
	for _, st := range seqnum.ReadStated(s) {
		if st.Kind == seqnum.KindPart {
			return st.Number.Value, st.Marker, true
		}
	}
	return 0, "", false
}

// partOf returns the part number of the first part marker in s, read by the
// one sequence number reader (seqnum.ReadStated): "Disc 2", "Part II", "Pt. 3",
// "(2 of 3)", "Part 1 of 3", the second part. A marker that opens a range
// ("Disc 1-3") is not a part: those files hold them all.
func partOf(s string) (float64, bool) {
	for _, st := range seqnum.ReadStated(s) {
		if st.Kind == seqnum.KindPart {
			return st.Number.Value, true
		}
	}
	return 0, false
}

// bookSide is the text a book's part marker can sit in: its title, its
// folder name and, for a single-file book, the file name without extension
// ("Lonesome Dove Part 1.m4b").
func bookSide(title, filePath string) string {
	d := bookDir(filePath)
	s := title + " | " + dirBase(d)
	if p := strings.TrimSpace(filePath); p != "" && p != d {
		s += " | " + fileExtRe.ReplaceAllString(filepath.Base(p), "")
	}
	return s
}

// bookOwnSide is bookSide without the folder name: the book's title and, for a
// single-file book, the file name.
func bookOwnSide(title, filePath string) string {
	d := bookDir(filePath)
	s := title
	if p := strings.TrimSpace(filePath); p != "" && p != d {
		s += " | " + fileExtRe.ReplaceAllString(filepath.Base(p), "")
	}
	return s
}

// bookDir is the book's folder: the path itself for a folder book, its
// parent for a single-file book.
func bookDir(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if fileExtRe.MatchString(filepath.Base(p)) {
		return filepath.Dir(p)
	}
	return p
}

func dirBase(d string) string {
	if d == "" {
		return ""
	}
	return filepath.Base(d)
}

// relatedDirs: the same folder, one inside the other, or sibling folders.
func relatedDirs(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/") || filepath.Dir(a) == filepath.Dir(b)
}
