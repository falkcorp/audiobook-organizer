// file: internal/metafetch/rejected_candidates.go
// version: 1.5.2
// guid: 486771bb-091f-46bb-8846-ff740d2e8ab3
// last-edited: 2026-10-20

package metafetch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// The owner's candidate rejections (POST /metadata/batch-reject-candidates)
// are stored one key per rejected source+title:
//
//	rejected_candidate:{bookID}:{source}|{title}
//
// in the case the candidate carried when it was rejected. Every reader and
// writer builds and matches that key here, and nowhere else: until
// 2026-10-10 four sites built it by hand and two matched it with an exact
// lookup.
//
// The key's VALUE says which of that source+title's records were rejected
// (RejectionValue). Since 2026-10-10 it lists each rejected record's
// identifiers as one group, {"records":[["asin:b00x...","isbn:978..."]]}:
// the record's ASIN and every ISBN it carries (as ISBN-13), so rejecting the
// abridged record does not also refuse the unabridged record with the same
// title. A value with no identifier (every key written before then holds
// "1") rejects every record of that source+title, as it always did.
// Matching (Rejection.Matches) uses the identifiers when both the rejection
// and the candidate carry one of the same kind, and falls back to
// source+title otherwise. So once a rejection names an ISBN, a record of the
// same source+title with no ASIN and a different ISBN is a different edition
// and is not refused.
//
// Identifiers are book-wide, across every source (owner decision
// 2026-10-10: ASIN and ISBN rejections apply across all sources): a rejected
// ASIN names one Audible product, which Audible and Audnexus ("Audnexus
// (Audible)") both serve, and a rejected ISBN names one edition whichever
// provider reports it. An Audible rejection stores its record's ISBN too, so
// a Google Books or Hardcover record carrying that audiobook ISBN is
// refused as well. So an identifier-precise rejection refuses any
// provider's record for that book carrying one of its identifiers, whatever
// the record's source or title (RejectedSet.Matches). That index is a
// positive match only: the source+title fallback above stays inside the
// rejection's own source+title, so an ASIN rejection under Audible does not
// refuse an unrelated record of another provider that shares none of its
// identifiers. A value with no identifier (every legacy "1") stays
// title-wide under its own source and joins no index. ISBNs are compared as
// ISBN-13 (an ISBN-10 converted, an "ISBN"/"ISBN-13:" label dropped), so
// one provider's ISBN-10 meets another's ISBN-13.
//
// Undo (RemoveRejection) removes the undone record's rejection and nothing
// else: under the record's own source+title, the records it is; under any
// other key of the book, only records with its ASIN (the same product
// rejected through another source, as Audnexus serves Audible's). A record
// rejected under another source that only shares an ISBN with it is a
// separate rejection the owner made and stays, and so does the refusal of
// that ISBN: the undone record is then still refused if it carries it
// (POST /metadata/unreject-candidate reports still_rejected).
//
// Matching folds case (RejectionKey, and the identifiers), the same way
// mergeCandidateRows dedups the cached row: two candidates the cache treats
// as one are one candidate to a rejection too, so a refetch that returns the
// rejected record with a different capitalization cannot slip past the
// owner's no. Stored keys are read as they are and folded on load, so every
// rejection written before the fold still matches.

// RejectedCandidatePrefix is the keyspace of the owner's rejections.
const RejectedCandidatePrefix = "rejected_candidate:"

// RejectedCandidateReader reads the owner's rejections: a prefix scan of
// the raw keyspace (database.RawKVStore has it).
type RejectedCandidateReader interface {
	ScanPrefix(prefix string) ([]database.KVPair, error)
}

// RejectedCandidateBookPrefix is the prefix of bookID's rejection keys.
func RejectedCandidateBookPrefix(bookID string) string {
	return RejectedCandidatePrefix + bookID + ":"
}

// RejectedCandidateStoreKey is the key a rejection of source|title for
// bookID is stored under, in the candidate's own case.
func RejectedCandidateStoreKey(bookID, source, title string) string {
	return RejectedCandidateBookPrefix(bookID) + source + "|" + title
}

// RejectionKey is the matching form of a rejection: source|title with case
// folded (see the file comment).
func RejectionKey(source, title string) string {
	return strings.ToLower(source + "|" + title)
}

// RejectionKeyOfStored is the matching form of a stored key's source|title
// suffix (the part after RejectedCandidateBookPrefix).
func RejectionKeyOfStored(suffix string) string {
	return strings.ToLower(suffix)
}

// ParseRejectedCandidateKey splits a stored rejection key into its book id
// and its source|title suffix. ok is false for a key outside the keyspace
// or with no suffix.
func ParseRejectedCandidateKey(key string) (bookID, suffix string, ok bool) {
	rest, found := strings.CutPrefix(key, RejectedCandidatePrefix)
	if !found {
		return "", "", false
	}
	bookID, suffix, found = strings.Cut(rest, ":")
	if !found || bookID == "" || suffix == "" {
		return "", "", false
	}
	return bookID, suffix, true
}

// Rejection is the owner's rejection of one source+title for one book.
type Rejection struct {
	// Any: every record of the source+title is rejected (a value with no
	// identifier: a legacy key, or a record that carried none).
	Any bool
	// IDs: every rejected record's identifiers, typed and case-folded
	// ("asin:b00x...", "isbn:978..."): the union of Records, which is what
	// matching reads. Only read when Any is false.
	IDs map[string]bool
	// Records: each rejected record's identifiers as one group, sorted, so
	// Undo can take one record out whole (RemoveRejection).
	Records [][]string
}

// Matches reports whether the rejection refuses a record carrying ids
// (CandidateIdentifiers). It falls back to source+title -- refusing the
// record -- when the rejection is Any, when the record carries no
// identifier, or when the two share no identifier kind (an ASIN rejection
// against an ISBN-only record): with nothing to tell the records apart,
// the owner's no stands.
func (r *Rejection) Matches(ids []string) bool {
	if r == nil {
		return false
	}
	if r.Any || len(r.IDs) == 0 || len(ids) == 0 {
		return true
	}
	shared := false
	for _, id := range ids {
		if r.IDs[id] {
			return true
		}
		if r.hasKind(identifierKind(id)) {
			shared = true
		}
	}
	return !shared
}

func (r *Rejection) hasKind(kind string) bool {
	for id := range r.IDs {
		if identifierKind(id) == kind {
			return true
		}
	}
	return false
}

// merge adds records (each a record's identifiers) to r, once each; every
// widens r to every record of the title.
func (r *Rejection) merge(every bool, records [][]string) {
	if every {
		r.Any = true
	}
	for _, rec := range records {
		rec = sortedUnique(rec)
		if len(rec) == 0 || r.hasRecord(rec) {
			continue
		}
		r.Records = append(r.Records, rec)
	}
	r.reindex()
}

func (r *Rejection) hasRecord(rec []string) bool {
	for _, have := range r.Records {
		if slices.Equal(have, rec) {
			return true
		}
	}
	return false
}

// dropRecords removes the records drop selects and reports whether any was
// removed.
func (r *Rejection) dropRecords(drop func(rec []string) bool) bool {
	kept := r.Records[:0:0]
	for _, rec := range r.Records {
		if !drop(rec) {
			kept = append(kept, rec)
		}
	}
	removed := len(kept) != len(r.Records)
	r.Records = kept
	r.reindex()
	return removed
}

// reindex rebuilds IDs from Records.
func (r *Rejection) reindex() {
	r.IDs = nil
	for _, rec := range r.Records {
		for _, id := range rec {
			if r.IDs == nil {
				r.IDs = map[string]bool{}
			}
			r.IDs[id] = true
		}
	}
}

func sortedUnique(ids []string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return slices.Compact(out)
}

func identifierKind(id string) string {
	kind, _, _ := strings.Cut(id, ":")
	return kind
}

// normalizeIdentifier keeps an identifier's letters and digits, lower-cased:
// "978-0-00-000000-2" and "9780000000002" are one ISBN, "B00ABC" and
// "b00abc" one ASIN.
func normalizeIdentifier(v string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(v) {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// stripISBNLabel drops a leading "ISBN" label from a normalised ISBN
// ("ISBN 0306406152", "ISBN-13: 978...", "ISBN-10: 0-306-..."), so a
// labelled value meets the bare one. The "10"/"13" after the label is
// dropped only when what follows is then exactly an ISBN-10 or ISBN-13
// long: "ISBN 1012345678" is an ISBN-10 that starts with 10.
func stripISBNLabel(n string) string {
	rest, ok := strings.CutPrefix(n, "isbn")
	if !ok {
		return n
	}
	if (len(rest) == 12 || len(rest) == 15) && (strings.HasPrefix(rest, "10") || strings.HasPrefix(rest, "13")) {
		return rest[2:]
	}
	return rest
}

// canonicalISBN is an ISBN in matching form: an "ISBN" label dropped
// (stripISBNLabel) and an ISBN-10 converted to its ISBN-13 ("0-00-000001-X"
// and "9780000000011" are one edition), anything else as
// normalizeIdentifier leaves it. One provider reporting only the ISBN-10
// and another only the ISBN-13 must still meet (the cross-source index,
// RejectedSet).
func canonicalISBN(v string) string {
	n := stripISBNLabel(normalizeIdentifier(v))
	if len(n) != 10 {
		return n
	}
	for i := range 9 {
		if n[i] < '0' || n[i] > '9' {
			return n
		}
	}
	if last := n[9]; (last < '0' || last > '9') && last != 'x' {
		return n
	}
	core := "978" + n[:9]
	sum := 0
	for i := 0; i < len(core); i++ {
		d := int(core[i] - '0')
		if i%2 == 1 {
			d *= 3
		}
		sum += d
	}
	return core + strconv.Itoa((10-sum%10)%10)
}

// canonicalIdentifier is a stored or built typed identifier ("asin:...",
// "isbn:...") in matching form.
func canonicalIdentifier(kind, val string) string {
	if kind == "isbn" {
		return "isbn:" + canonicalISBN(val)
	}
	return kind + ":" + normalizeIdentifier(val)
}

// CandidateIdentifiers is a record's identifiers in matching form: its
// ASIN, and every ISBN it carries as ISBN-13 (canonicalISBN), once each.
// It is also what rejecting the record stores (owner decision 2026-10-10:
// ASIN and ISBN rejections apply across all sources, so an Audible
// rejection names its audiobook ISBN too).
func CandidateIdentifiers(asin, isbn, isbn10, isbn13 string) []string {
	var ids []string
	if a := normalizeIdentifier(asin); a != "" {
		ids = append(ids, "asin:"+a)
	}
	seen := map[string]bool{}
	for _, v := range []string{isbn13, isbn, isbn10} {
		if n := canonicalISBN(v); n != "" && !seen[n] {
			seen[n] = true
			ids = append(ids, "isbn:"+n)
		}
	}
	return ids
}

// CandidateRejectionIdentifiers is what rejecting c stores: every
// identifier c carries (CandidateIdentifiers).
func CandidateRejectionIdentifiers(c *MetadataCandidate) []string {
	if c == nil {
		return nil
	}
	return candidateIdentifiers(c)
}

func candidateIdentifiers(c *MetadataCandidate) []string {
	return CandidateIdentifiers(c.ASIN, c.ISBN, c.ISBN10, c.ISBN13)
}

// asinOf is the "asin:" identifier among ids, or "".
func asinOf(ids []string) string {
	for _, id := range ids {
		if identifierKind(id) == "asin" {
			return id
		}
	}
	return ""
}

type rejectionValue struct {
	Records [][]string `json:"records"`
}

// parseRejectionValue reads a stored value: its records' identifiers, or
// every=true for a value naming none (every legacy "1", and anything that
// does not parse: the safe reading is the title-wide one).
func parseRejectionValue(v []byte) (every bool, records [][]string) {
	var rv rejectionValue
	if err := json.Unmarshal(v, &rv); err != nil {
		return true, nil
	}
	for _, rec := range rv.Records {
		var ids []string
		for _, id := range rec {
			if kind, val, ok := strings.Cut(strings.ToLower(id), ":"); ok && val != "" {
				if c := canonicalIdentifier(kind, val); !strings.HasSuffix(c, ":") {
					ids = append(ids, c)
				}
			}
		}
		if len(ids) > 0 {
			records = append(records, ids)
		}
	}
	if len(records) == 0 {
		return true, nil
	}
	return false, records
}

// parsedRejection is a stored value as a Rejection.
func parsedRejection(v []byte) *Rejection {
	r := &Rejection{}
	r.merge(parseRejectionValue(v))
	return r
}

func encodeRejection(r *Rejection) []byte {
	if r == nil || r.Any || len(r.Records) == 0 {
		return []byte("1")
	}
	records := make([][]string, 0, len(r.Records))
	for _, rec := range r.Records {
		records = append(records, sortedUnique(rec))
	}
	slices.SortFunc(records, func(a, b []string) int { return slices.Compare(a, b) })
	out, _ := json.Marshal(rejectionValue{Records: records})
	return out
}

// RejectionValue is the value to store when the owner rejects a record
// carrying rejectIDs (CandidateRejectionIdentifiers) under a key whose
// current value is existing (nil: no key yet). A key that already rejects
// every record stays that way; otherwise the record joins the key's records,
// and a record with no identifier widens the key to every record.
func RejectionValue(existing []byte, rejectIDs []string) []byte {
	r := &Rejection{}
	if existing != nil {
		r.merge(parseRejectionValue(existing))
	}
	if len(rejectIDs) == 0 {
		r.Any = true
	}
	r.merge(false, [][]string{rejectIDs})
	return encodeRejection(r)
}

// UnrejectValue is what un-rejecting a record carrying candIDs
// (CandidateIdentifiers) leaves of its own source+title key, whose value is
// existing: the value to write back, or remove=true to delete the key. Every
// record of the key sharing an identifier with candIDs leaves it, and when
// the rest would still match the record by the source+title fallback (a key
// rejecting every record, a record with no identifier, or no shared
// identifier kind) the whole key goes, so the key no longer refuses it.
func UnrejectValue(existing []byte, candIDs []string) (value []byte, remove bool) {
	r := parsedRejection(existing)
	r.dropRecords(func(rec []string) bool {
		for _, id := range rec {
			if slices.Contains(candIDs, id) {
				return true
			}
		}
		return false
	})
	if r.Any || len(r.Records) == 0 || r.Matches(candIDs) {
		return nil, true
	}
	return encodeRejection(r), false
}

// RejectedSet is one book's rejections: by matching key (RejectionKey),
// and the identifiers of every identifier-precise rejection book-wide,
// across sources (see the file comment). The zero value holds none.
type RejectedSet struct {
	byKey map[string]*Rejection
	ids   map[string]bool
}

func (s *RejectedSet) add(suffix string, value []byte) {
	if s.byKey == nil {
		s.byKey = map[string]*Rejection{}
	}
	key := RejectionKeyOfStored(suffix)
	r := s.byKey[key]
	if r == nil {
		r = &Rejection{}
		s.byKey[key] = r
	}
	every, records := parseRejectionValue(value)
	r.merge(every, records)
	if every {
		// A value naming no identifier (a legacy "1") is title-wide under
		// its own source and nothing more: it joins no book-wide index.
		return
	}
	if s.ids == nil {
		s.ids = map[string]bool{}
	}
	for _, rec := range records {
		for _, id := range rec {
			s.ids[id] = true
		}
	}
}

// Empty reports whether the set holds no rejection.
func (s RejectedSet) Empty() bool {
	return len(s.byKey) == 0
}

// Matches reports whether the owner rejected the source+title record
// carrying ids (CandidateIdentifiers): one of ids is an identifier the
// owner rejected for this book under any source or title, or the
// source+title's own rejection refuses it (Rejection.Matches).
func (s RejectedSet) Matches(source, title string, ids []string) bool {
	for _, id := range ids {
		if s.ids[id] {
			return true
		}
	}
	return s.byKey[RejectionKey(source, title)].Matches(ids)
}

// Has reports whether the owner rejected a record of source|title that
// carries no identifier: any rejection of that source+title.
func (s RejectedSet) Has(source, title string) bool {
	return s.Matches(source, title, nil)
}

// HasCandidate reports whether the owner rejected c.
func (s RejectedSet) HasCandidate(c *MetadataCandidate) bool {
	return c != nil && s.Matches(c.Source, c.Title, candidateIdentifiers(c))
}

// HasMetadata reports whether the owner rejected source's record m: the
// auto-fetch's pick (FetchMetadataForBook), which works on provider records
// before they become candidates. source is the provider's Name(), the same
// string a candidate from it carries.
func (s RejectedSet) HasMetadata(source string, m *metadata.BookMetadata) bool {
	return m != nil && s.Matches(source, m.Title, CandidateIdentifiers(m.ASIN, m.ISBN, m.ISBN10, m.ISBN13))
}

// LoadRejectedCandidates reads bookID's rejections. A failed scan is
// returned, never read as "nothing rejected": an apply path refuses the book
// on it (applygate.ReasonOwnerRejectionCheckFailed).
func LoadRejectedCandidates(r RejectedCandidateReader, bookID string) (RejectedSet, error) {
	if r == nil {
		return RejectedSet{}, nil
	}
	prefix := RejectedCandidateBookPrefix(bookID)
	pairs, err := r.ScanPrefix(prefix)
	if err != nil {
		return RejectedSet{}, fmt.Errorf("read owner rejections for book %s: %w", bookID, err)
	}
	var set RejectedSet
	for _, kv := range pairs {
		if suffix, ok := strings.CutPrefix(kv.Key, prefix); ok && suffix != "" {
			set.add(suffix, kv.Value)
		}
	}
	return set, nil
}

// LoadAllRejectedCandidates reads every book's rejections in one prefix
// scan, by book id: for a reader that needs many books' rejections at once
// (the review list's page) instead of one scan per book. The keyspace holds
// one key per rejection the owner made by hand, so it stays small.
func LoadAllRejectedCandidates(r RejectedCandidateReader) (map[string]RejectedSet, error) {
	if r == nil {
		return nil, nil
	}
	pairs, err := r.ScanPrefix(RejectedCandidatePrefix)
	if err != nil {
		return nil, fmt.Errorf("read owner rejections: %w", err)
	}
	out := make(map[string]RejectedSet)
	for _, kv := range pairs {
		bookID, suffix, ok := ParseRejectedCandidateKey(kv.Key)
		if !ok {
			continue
		}
		set := out[bookID]
		set.add(suffix, kv.Value)
		out[bookID] = set
	}
	return out, nil
}

// RejectionStore writes the owner's rejections (database.Store has it).
//
// ModifyBook is there for its lock, not to write the book: a rejection is
// written holding the book's write stripe (underBookWriteLock), the stripe
// every candidate apply re-reads the rejections under just before its
// commit (Service.ApplyMetadataCandidateWithOptions, the auto-fetch's
// commitApply guard, and Service.CommitCandidateApply, the bulk fetch's
// commit). A rejection and an apply of the same book are
// therefore ordered: either the rejection is in before the apply's check,
// and the apply is refused, or the apply committed first.
type RejectionStore interface {
	RejectedCandidateReader
	GetRaw(key string) ([]byte, error)
	SetRaw(key string, value []byte) error
	DeleteRaw(key string) error
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
}

// underBookWriteLock runs fn holding bookID's write stripe (a ModifyBook
// that writes nothing: database.ErrSkipBookWrite). fn does only raw-key
// reads and writes, nothing slow, and no book write (the stripe rules in
// database/pebble_store_book_lock.go). Lock order: the book stripe, then
// rejectionWriteMu inside fn; nothing takes them the other way round. A
// book with no row runs fn without the stripe: no apply can commit to it.
// A failed read of the row is returned and fn does not run.
func underBookWriteLock(store RejectionStore, bookID string, fn func() error) error {
	called := false
	var inner error
	_, err := store.ModifyBook(bookID, func(*database.Book) error {
		called = true
		inner = fn()
		return database.ErrSkipBookWrite
	})
	if err != nil {
		return fmt.Errorf("hold the write lock of book %s: %w", bookID, err)
	}
	if !called {
		return fn()
	}
	return inner
}

// rejectionWriteMu serializes the read-modify-write of a rejection key's
// value, so two rejections of records sharing a source+title cannot drop
// one another's identifier. Rejections are the owner's manual actions; one
// lock for all of them costs nothing.
var rejectionWriteMu sync.Mutex

// RecordRejection stores the owner's rejection of c for bookID: c's
// identifiers join the key's value (RejectionValue). It holds the book's
// write stripe while it writes (RejectionStore).
func RecordRejection(store RejectionStore, bookID string, c *MetadataCandidate) error {
	if c == nil {
		return nil
	}
	return underBookWriteLock(store, bookID, func() error { return recordRejection(store, bookID, c) })
}

func recordRejection(store RejectionStore, bookID string, c *MetadataCandidate) error {
	rejectionWriteMu.Lock()
	defer rejectionWriteMu.Unlock()
	key := RejectedCandidateStoreKey(bookID, c.Source, c.Title)
	existing, err := store.GetRaw(key)
	if err != nil {
		return fmt.Errorf("read rejection %s: %w", key, err)
	}
	if err := store.SetRaw(key, RejectionValue(existing, CandidateRejectionIdentifiers(c))); err != nil {
		return fmt.Errorf("write rejection %s: %w", key, err)
	}
	return nil
}

// RemoveRejection un-rejects c for bookID: it removes c's own rejection and
// nothing else (see the file comment). Under c's own source+title, in
// whatever case the key was written, the records c is leave the key
// (UnrejectValue), so that key no longer refuses c. Under every other key of
// the book, the records with c's ASIN leave it, ISBNs and all: the same
// product rejected through another source (an Audible rejection seen on the
// Audnexus row). A record of another key that only shares an ISBN with c is
// a separate rejection and stays, so c can still be refused afterwards when
// it carries that ISBN; the caller re-reads to tell (the un-reject handler's
// still_rejected). A key left with no record is deleted, never rewritten:
// an empty value would read as "every record of the title". A key that
// names no identifier (a legacy "1") of another source+title is left alone.
// changed reports whether any key was rewritten or deleted. It holds the
// book's write stripe while it writes (RejectionStore).
func RemoveRejection(store RejectionStore, bookID string, c *MetadataCandidate) (changed bool, err error) {
	if c == nil {
		return false, nil
	}
	err = underBookWriteLock(store, bookID, func() error {
		var rErr error
		changed, rErr = removeRejection(store, bookID, c)
		return rErr
	})
	return changed, err
}

func removeRejection(store RejectionStore, bookID string, c *MetadataCandidate) (changed bool, err error) {
	rejectionWriteMu.Lock()
	defer rejectionWriteMu.Unlock()
	prefix := RejectedCandidateBookPrefix(bookID)
	pairs, err := store.ScanPrefix(prefix)
	if err != nil {
		return false, fmt.Errorf("read rejections for book %s: %w", bookID, err)
	}
	want := RejectionKey(c.Source, c.Title)
	ids := candidateIdentifiers(c)
	asin := asinOf(ids)
	for _, kv := range pairs {
		suffix, ok := strings.CutPrefix(kv.Key, prefix)
		if !ok {
			continue
		}
		var value []byte
		var remove bool
		if RejectionKeyOfStored(suffix) == want {
			value, remove = UnrejectValue(kv.Value, ids)
		} else if value, remove, ok = dropProduct(kv.Value, asin); !ok {
			continue
		}
		switch {
		case remove:
			if err := store.DeleteRaw(kv.Key); err != nil {
				return changed, fmt.Errorf("delete rejection %s: %w", kv.Key, err)
			}
		case bytes.Equal(value, encodeRejection(parsedRejection(kv.Value))):
			// The key rejects other records of the title only: c was not
			// among them, so nothing changes.
			continue
		default:
			if err := store.SetRaw(kv.Key, value); err != nil {
				return changed, fmt.Errorf("rewrite rejection %s: %w", kv.Key, err)
			}
		}
		changed = true
	}
	return changed, nil
}

// dropProduct takes the records naming asin (one Audible product) out of
// another source+title's stored value existing: the value to write back, or
// remove=true when no record is left (the key is deleted; re-encoding an
// empty set would widen it to every record of its title). touched is false
// when asin is "" (c has no ASIN, so no other key holds its product), when
// no record names it, or when the value names no identifier at all (a
// legacy "1"). A record that only shares an ISBN with c is not c's and
// stays.
func dropProduct(existing []byte, asin string) (value []byte, remove, touched bool) {
	if asin == "" {
		return nil, false, false
	}
	r := parsedRejection(existing)
	if r.Any {
		return nil, false, false
	}
	if !r.dropRecords(func(rec []string) bool { return slices.Contains(rec, asin) }) {
		return nil, false, false
	}
	if len(r.Records) == 0 {
		return nil, true, true
	}
	return encodeRejection(r), false, true
}

// RerankOutcome is what RerankCachedCandidates did to one row.
type RerankOutcome int

const (
	// RerankNoRow: the book has no cache row (or it holds no candidates).
	RerankNoRow RerankOutcome = iota
	// RerankUnchanged: the row was already in rank order; nothing written.
	RerankUnchanged
	// RerankReordered: the row's candidates were re-ordered and written.
	RerankReordered
)

// RerankCachedCandidates re-orders bookID's cached candidates by rank (lower
// first, then score; metabatch.MergeRanker's tiers), under the row's lock, so
// a candidate the owner has just rejected leaves slot 0 -- the candidate the
// review lane shows and every bulk apply reads -- and one just un-rejected
// takes its place back.
//
// buildRank is called under the row lock, so it reads the owner's
// rejections there: two re-ranks of one book (a reject and an un-reject in
// quick succession) cannot leave the row in the order of the older
// rejection set, since whichever takes the lock last reads the newest set.
// A buildRank error is returned and nothing is written.
//
// It only re-orders. The raw row is read (not GetCachedCandidates, whose
// legacy filter would drop candidates if its answer were written back),
// nothing is deduplicated or capped, an undecodable candidate keeps its
// place after the ranked ones, and every other field of the row is written
// back as read. The row is written only when the order changed.
func (mfs *Service) RerankCachedCandidates(bookID string, buildRank func() (func(MetadataCandidate) int, error)) (RerankOutcome, error) {
	if mfs == nil || mfs.db == nil || buildRank == nil {
		return RerankNoRow, nil
	}
	defer mfs.lockRow(bookID)()
	rank, err := buildRank()
	if err != nil {
		return RerankUnchanged, fmt.Errorf("build the ranking for %s: %w", bookID, err)
	}
	if rank == nil {
		return RerankNoRow, nil
	}
	entry, err := mfs.db.GetMetadataCache(bookID)
	if err != nil {
		return RerankNoRow, fmt.Errorf("read cache row for %s: %w", bookID, err)
	}
	if entry == nil || len(entry.Candidates) == 0 {
		return RerankNoRow, nil
	}
	ranked := rankCandidateRows(entry.Candidates, rank)
	if len(ranked) != len(entry.Candidates) {
		// rankCandidateRows keeps every row; this is the guard that a
		// re-order can never be what deletes a candidate.
		return RerankUnchanged, fmt.Errorf("re-order of %s changed the candidate count (%d -> %d); not written",
			bookID, len(entry.Candidates), len(ranked))
	}
	if sameRowOrder(entry.Candidates, ranked) {
		return RerankUnchanged, nil
	}
	entry.Candidates = ranked
	if err := mfs.db.PutMetadataCache(entry); err != nil {
		return RerankUnchanged, fmt.Errorf("write re-ordered cache row for %s: %w", bookID, err)
	}
	return RerankReordered, nil
}

// sameRowOrder reports whether a and b hold the same rows in the same order.
func sameRowOrder(a, b []json.RawMessage) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if string(a[i]) != string(b[i]) {
			return false
		}
	}
	return true
}
