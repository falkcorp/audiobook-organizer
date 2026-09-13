// file: internal/applygate/partial.go
// version: 1.2.0
// guid: b1e7d4a0-3c58-4f26-9a8d-0f6c2e5b7d19
// last-edited: 2026-09-13

package applygate

import (
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// ReasonPartialBook: the files hold one part of a book (or a span of several
// books) and the candidate is something else: the whole book, another part,
// or a single volume of a box set. An apply would give a part the whole
// book's title and runtime, and a rename would then collide with the other
// parts.
const ReasonPartialBook = "partial_book"

var (
	// partRe is a part marker: "Disc 2", "Part II", "Pt. 3", "(2 of 3)".
	partRe = regexp.MustCompile(`(?i)\b(?:disc|disk|cd|part|pt)\.?\s*(\d+|[ivx]+|one|two|three|four|five)\b|\(\s*(\d+)\s+of\s+\d+\s*\)`)
	// spanRe marks text covering several books.
	spanRe = regexp.MustCompile(`(?i)\b(?:omnibus|box(?:ed)?\s*set|boxset|publisher.?s\s+pack|complete\s+series|books\s*\d+\s*(?:-|–|to|thru|&|and)\s*\d+|vol(?:ume)?s\.?\s*\d+\s*(?:-|–|to|&|and)\s*\d+|vol(?:ume)?\.?\s*\d+\s*(?:&|and)\s*\d+)\b`)
	// fileExtRe is a file extension at the end of a path.
	fileExtRe = regexp.MustCompile(`\.[A-Za-z][A-Za-z0-9]{1,3}$`)
	// rangeTailRe follows a part marker that opens a range ("Disc 1-3",
	// "Parts 1 & 2"): a range means the files hold every part, not one.
	rangeTailRe = regexp.MustCompile(`(?i)^\s*(?:-|–|to|thru|through|&|and)\s*(?:\d+|[ivx]+)\b`)

	partWords = map[string]float64{"one": 1, "two": 2, "three": 3, "four": 4, "five": 5}
	partRoman = map[string]float64{"i": 1, "ii": 2, "iii": 3, "iv": 4, "v": 5, "vi": 6, "vii": 7, "viii": 8, "ix": 9, "x": 10}
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
// Skipped when the runtime check AGREES: files whose runtime matches the
// candidate's are the whole thing, whatever the folder says. It returns only
// block or neutral, never agree.
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
	if runtimeOutcome == OutcomeAgree {
		return r
	}
	bside := bookSide(book.Title, book.FilePath)
	cside := c.Title + " " + c.Subtitle
	bp, bok := partOf(bside)
	cp, cok := partOf(cside)
	if bok && (!cok || bp != cp) {
		r.Outcome, r.Reason = OutcomeBlock, ReasonPartialBook
		r.Detail = "the files are part " + ftoa(bp) + "; the candidate " + partText(cp, cok)
		return r
	}
	bs, cs := spanRe.FindString(bside), spanRe.FindString(cside)
	if (bs == "") != (cs == "") {
		r.Outcome, r.Reason = OutcomeBlock, ReasonPartialBook
		r.Detail = "only one side covers several books: files " + strconv.Quote(bs) + ", candidate " + strconv.Quote(cs)
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

func partText(p float64, ok bool) string {
	if !ok {
		return "is the whole book"
	}
	return "is part " + ftoa(p)
}

// partOf returns the part number of the first part marker in s. A marker
// that opens a range ("Disc 1-3") is not a part: those files hold them all.
func partOf(s string) (float64, bool) {
	for _, ix := range partRe.FindAllStringSubmatchIndex(s, -1) {
		if rangeTailRe.MatchString(s[ix[1]:]) {
			continue
		}
		var tok string
		if ix[2] >= 0 {
			tok = strings.ToLower(s[ix[2]:ix[3]])
		} else {
			tok = s[ix[4]:ix[5]]
		}
		if v, ok := partWords[tok]; ok {
			return v, true
		}
		if v, ok := partRoman[tok]; ok {
			return v, true
		}
		if v, err := strconv.ParseFloat(tok, 64); err == nil {
			return v, true
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
