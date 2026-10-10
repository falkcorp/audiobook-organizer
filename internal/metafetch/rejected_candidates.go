// file: internal/metafetch/rejected_candidates.go
// version: 1.0.0
// guid: 486771bb-091f-46bb-8846-ff740d2e8ab3
// last-edited: 2026-10-10

package metafetch

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The owner's candidate rejections (POST /metadata/batch-reject-candidates)
// are stored one key per rejected candidate:
//
//	rejected_candidate:{bookID}:{source}|{title}
//
// in the case the candidate carried when it was rejected. Every reader and
// writer builds and matches that key here, and nowhere else: until
// 2026-10-10 four sites built it by hand and two matched it with an exact
// lookup.
//
// Matching folds case (RejectionKey), the same way mergeCandidateRows
// dedups the cached row: two candidates the cache treats as one are one
// candidate to a rejection too, so a refetch that returns the rejected
// record with a different capitalization cannot slip past the owner's no.
// Stored keys are read as they are and folded on load, so every rejection
// written before the fold still matches. The key carries no ASIN: a
// rejection refuses the source+title, whichever of that source's records
// carries it.

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

// RejectedSet is one book's rejections in matching form (RejectionKey).
// nil holds none.
type RejectedSet map[string]bool

// Has reports whether the owner rejected source|title.
func (s RejectedSet) Has(source, title string) bool {
	return s[RejectionKey(source, title)]
}

// HasCandidate reports whether the owner rejected c.
func (s RejectedSet) HasCandidate(c *MetadataCandidate) bool {
	return c != nil && s.Has(c.Source, c.Title)
}

// LoadRejectedCandidates reads bookID's rejections. A failed scan is
// returned, never read as "nothing rejected": an apply path refuses the book
// on it (applygate.ReasonOwnerRejectionCheckFailed).
func LoadRejectedCandidates(r RejectedCandidateReader, bookID string) (RejectedSet, error) {
	if r == nil {
		return nil, nil
	}
	prefix := RejectedCandidateBookPrefix(bookID)
	pairs, err := r.ScanPrefix(prefix)
	if err != nil {
		return nil, fmt.Errorf("read owner rejections for book %s: %w", bookID, err)
	}
	if len(pairs) == 0 {
		return nil, nil
	}
	set := make(RejectedSet, len(pairs))
	for _, kv := range pairs {
		if suffix, ok := strings.CutPrefix(kv.Key, prefix); ok && suffix != "" {
			set[RejectionKeyOfStored(suffix)] = true
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
		if out[bookID] == nil {
			out[bookID] = RejectedSet{}
		}
		out[bookID][RejectionKeyOfStored(suffix)] = true
	}
	return out, nil
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
// It only re-orders. The raw row is read (not GetCachedCandidates, whose
// legacy filter would drop candidates if its answer were written back),
// nothing is deduplicated or capped, an undecodable candidate keeps its
// place after the ranked ones, and every other field of the row is written
// back as read. The row is written only when the order changed.
func (mfs *Service) RerankCachedCandidates(bookID string, rank func(MetadataCandidate) int) (RerankOutcome, error) {
	if mfs == nil || mfs.db == nil || rank == nil {
		return RerankNoRow, nil
	}
	defer mfs.lockRow(bookID)()
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
