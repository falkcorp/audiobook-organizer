// file: internal/dedup/exact_provenance.go
// version: 1.0.0
// guid: 22577d99-1104-4959-9603-0e04543d0eb1
// last-edited: 2026-09-27

// Package dedup — exact-rule provenance and the placeholder-title guard.
//
// # Why this file exists (2026-09-27)
//
// The /review Dupes lane showed "exact" pairs such as two books both titled
// "read by narrator" (one under .../Nickleby 02/, one under .../Marvel - New
// Avengers - Breakout pt 1/) and two both titled "Unknown Title" (.../The Long
// Earth/ and .../Viper's Kiss/). Clicking "Why?" said "No score breakdown
// recorded." Read-only checks against production showed:
//
//   - every one of these pairs came from checkExactTitle: both books hang off
//     ONE author row whose name is itself a placeholder ("read by narrator",
//     452 books; "Unknown Title", 309 books), and their titles are the same
//     placeholder, so the Levenshtein distance is 0;
//   - the file hashes differ, neither side has an ISBN/ASIN, and the runtimes
//     are unrelated (214 s vs 610 s; 72,067 s vs unknown), so no content rule
//     could have produced them;
//   - none of the 6,000 pending exact candidates sampled had a breakdown,
//     because upsertExactCandidate never wrote one — every exact emitter
//     called it with the same ("exact", 1.0) and no record of which rule
//     fired.
//
// Two fixes live here. The title-based rules refuse to pair on a placeholder
// title, or under an author that is itself a placeholder
// (titleEvidenceRefusal). And every exact candidate now carries a
// provenance-only breakdown naming its rule and the values that rule matched
// (exactEvidence -> ExactRuleBreakdown), which the review panel renders.
package dedup

import (
	"fmt"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
)

// Exact-layer rule names, stored in Signal.Rule on the SigExactRule signal.
// They are part of the wire format the review UI reads; do not rename.
const (
	ExactRuleFileHash          = "file_hash"
	ExactRuleISBNASIN          = "isbn_asin"
	ExactRuleMetadataHash      = "metadata_hash"
	ExactRuleTitleAuthor       = "title_author"
	ExactRuleDurationTitle     = "duration_title"
	ExactRuleOrganizeCollision = "organize_collision"
)

// exactEvidence is what an exact emitter hands upsertExactCandidate: the rule
// that fired, a human-readable account of what it matched, and the rule's raw
// measurement (Levenshtein distance, duration fraction, or 1 for identity).
type exactEvidence struct {
	rule   string
	detail string
	raw    float64
}

// ExactRuleBreakdown builds the provenance-only breakdown an exact candidate
// row carries: one non-scoring SigExactRule signal (Confidence 0, Score 0, no
// Band), so it records WHY the row exists without claiming a score it never
// had. Exported for emitters outside this package (the organize-collision
// hook in internal/server).
func ExactRuleBreakdown(aID, bID, rule, detail string, raw float64) *unified.UnifiedDedupScore {
	return &unified.UnifiedDedupScore{
		Pair: canonicalPairIDs(aID, bID),
		Signals: []unified.Signal{{
			Kind:       unified.SigExactRule,
			Rule:       rule,
			Raw:        raw,
			Confidence: 0,
			Evidence:   detail,
		}},
		Suppressors: []string{},
		Formula:     unified.FormulaVersion,
		ComputedAt:  time.Now().UTC(),
	}
}

// exactBreakdownFor is the breakdown upsertExactCandidate stores: the rule
// signal, plus the same_path signal when the pair resolves to one path.
func exactBreakdownFor(a, b *database.Book, ev exactEvidence) *unified.UnifiedDedupScore {
	sb := ExactRuleBreakdown(a.ID, b.ID, ev.rule, ev.detail, ev.raw)
	if SamePathPair(a, b) {
		sb.Signals = append(sb.Signals, samePathScoreBreakdown(a, b).Signals...)
	}
	return sb
}

// authorIsPlaceholder reports whether a RESOLVED author name is a placeholder.
// An empty name means the lookup found nothing (no author, a dangling ID, or a
// store error) — that is unknown, not a placeholder, and does not by itself
// block a pair; the placeholder check is about author rows whose name is the
// system's own "unknown" marker or a leaked placeholder title.
func authorIsPlaceholder(name string) bool {
	return strings.TrimSpace(name) != "" && authorname.IsPlaceholderAuthor(name)
}

// titleEvidenceRefusal reports why a TITLE-based rule (exact title, duration +
// title, metadata-fuzzy, embedding similarity) must not pair a and b, or ""
// when it may. A placeholder title carries no identity — every placeholder
// book "matches" every other one — and neither does a title shared under
// placeholder authors on both sides, where the title is the only evidence
// and the "same author" is a parsing accident.
//
// Content-based rules (identical file hash, ISBN/ASIN, metadata source record)
// never call this: their evidence is not the title.
func titleEvidenceRefusal(aTitle, bTitle, aAuthor, bAuthor string) string {
	switch {
	case authorname.IsPlaceholderTitle(aTitle):
		return fmt.Sprintf("placeholder title %q", strings.TrimSpace(aTitle))
	case authorname.IsPlaceholderTitle(bTitle):
		return fmt.Sprintf("placeholder title %q", strings.TrimSpace(bTitle))
	case authorIsPlaceholder(aAuthor) && authorIsPlaceholder(bAuthor):
		return fmt.Sprintf("placeholder author on both sides (%q, %q)", aAuthor, bAuthor)
	}
	return ""
}

// placeholderTitleNote is appended to a CONTENT rule's evidence when either
// title is a placeholder, so the reviewer is told outright that the matching
// placeholder titles were not what paired the books.
func placeholderTitleNote(a, b *database.Book) string {
	var ph []string
	for _, t := range []string{a.Title, b.Title} {
		if authorname.IsPlaceholderTitle(t) {
			ph = append(ph, fmt.Sprintf("%q", strings.TrimSpace(t)))
		}
	}
	if len(ph) == 0 {
		return ""
	}
	return fmt.Sprintf(" Title %s is a placeholder and was NOT used as evidence.", strings.Join(ph, " / "))
}

// shortHash abbreviates a content hash for display; the full value stays
// greppable in the first 16 hex characters.
func shortHash(h string) string {
	if len(h) > 16 {
		return h[:16] + "…"
	}
	return h
}

// fileHashMatch says which hash two books were found to share.
type fileHashMatch struct {
	hash string
	// fileID is the book_file row whose hash matched, or "" for the book-level
	// file_hash.
	fileID string
}

func fileHashEvidence(a, b *database.Book, m fileHashMatch) exactEvidence {
	where := "book-level file hash"
	if m.fileID != "" {
		where = "file " + m.fileID
	}
	return exactEvidence{
		rule:   ExactRuleFileHash,
		raw:    1,
		detail: fmt.Sprintf("Identical file content: sha256 %s (%s).%s", shortHash(m.hash), where, placeholderTitleNote(a, b)),
	}
}

// isbnASINEvidence names every identifier the two books share.
func isbnASINEvidence(a, b *database.Book) exactEvidence {
	what := strings.Join(isbnASINEvidenceShared(a, b), ", ")
	if what == "" {
		what = "an identifier (values changed since the match)"
	}
	return exactEvidence{
		rule:   ExactRuleISBNASIN,
		raw:    1,
		detail: fmt.Sprintf("Shared %s.%s", what, placeholderTitleNote(a, b)),
	}
}

// isbnASINEvidenceShared lists the identifiers a and b share ("ASIN B0...",
// "ISBN-13 978...") after the same normalization identifiersConflict uses.
func isbnASINEvidenceShared(a, b *database.Book) []string {
	var shared []string
	add := func(label string, x, y *string, norm func(string) string) {
		if x == nil || y == nil {
			return
		}
		nx, ny := norm(*x), norm(*y)
		if nx != "" && nx == ny {
			shared = append(shared, label+" "+nx)
		}
	}
	add("ISBN-10", a.ISBN10, b.ISBN10, normalizeISBN)
	add("ISBN-13", a.ISBN13, b.ISBN13, normalizeISBN)
	add("ASIN", a.ASIN, b.ASIN, normalizeASIN)
	return shared
}

func metadataHashEvidence(a, b *database.Book) exactEvidence {
	h := ""
	if a.MetadataSourceHash != nil {
		h = *a.MetadataSourceHash
	}
	return exactEvidence{
		rule:   ExactRuleMetadataHash,
		raw:    1,
		detail: fmt.Sprintf("Metadata applied from the same external record (source hash %s).%s", shortHash(h), placeholderTitleNote(a, b)),
	}
}

func titleAuthorEvidence(a, b *database.Book, authorName string, dist int) exactEvidence {
	return exactEvidence{
		rule: ExactRuleTitleAuthor,
		raw:  float64(dist),
		detail: fmt.Sprintf("Same author %s and near-identical titles: %q vs %q (normalized; edit distance %d, limit 2).",
			authorLabel(a, authorName), normalizeTitle(a.Title), normalizeTitle(b.Title), dist),
	}
}

func durationTitleEvidence(a, b *database.Book, authorName string, aSec, bSec, pct float64, dist int) exactEvidence {
	return exactEvidence{
		rule: ExactRuleDurationTitle,
		raw:  pct,
		detail: fmt.Sprintf("Runtimes %.0fs vs %.0fs (%.2f%% apart, limit %.0f%%) and similar titles under the same author %s: %q vs %q (edit distance %d, limit %d).",
			aSec, bSec, pct*100, durationMatchTolerance*100, authorLabel(a, authorName),
			normalizeTitle(a.Title), normalizeTitle(b.Title), dist, durationLevenshteinMax),
	}
}

func authorLabel(b *database.Book, name string) string {
	id := ""
	if b.AuthorID != nil {
		id = fmt.Sprintf(" (author id %d)", *b.AuthorID)
	}
	if strings.TrimSpace(name) == "" {
		return "[unresolved]" + id
	}
	return fmt.Sprintf("%q%s", name, id)
}

// authorNameFor resolves a book's author name, "" when it has none or the
// lookup fails.
func (de *Engine) authorNameFor(b *database.Book) string {
	if b == nil || b.AuthorID == nil || de.bookStore == nil {
		return ""
	}
	a, err := de.bookStore.GetAuthorByID(*b.AuthorID)
	if err != nil || a == nil {
		return ""
	}
	return a.Name
}
