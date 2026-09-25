// file: internal/dedup/chapter_sibling.go
// version: 1.2.0
// guid: c1d4e7a2-9b35-4f80-8e16-2a7c0d5b9f43
// last-edited: 2026-09-25

// Package dedup — same-physical-book detection for emit-time suppression.
//
// The exact-layer emitters (checkExactTitle, checkDurationMatch) historically
// cross-paired the chapters of ONE multi-file audiobook against each other,
// because every chapter shares the same album-derived Title + author. That fed
// the 380K candidate explosion (.claude/notes/shattered-books-inventory.md).
//
// The existing same_dir_multi_file guard (filepath.Dir(a) == filepath.Dir(b))
// only catches chapters stored as multiple FILES in ONE folder. It MISSES the
// "shattered" layout where each chapter is its own subdir —
// `<Book>/<Book> - 1/f`, `<Book>/<Book> - 2/f` — whose parent dirs DIFFER. That
// is exactly why purge-stale only cleared ~8% of the backlog. chapterSiblings
// closes that gap: two paths are chapter siblings when both live in a
// `<prefix> - N` chapter dir, sharing the same grandparent AND the same prefix.

package dedup

import (
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
)

// chapterDirNameRe matches a chapter subdir basename like "Cage of Souls - 15":
// "<prefix> - <number>". Mirrors itunesservice.chapterDirRe (kept local to avoid
// a dedup→itunes/service import edge purely for a regex).
var chapterDirNameRe = regexp.MustCompile(`^(.*) - \d+$`)

// chapterDirParts returns the grandparent dir and the book-title prefix for a
// file inside a "<prefix> - N" chapter dir. ok=false when it isn't one.
func chapterDirParts(fp string) (grandparent, prefix string, ok bool) {
	dir := filepath.Dir(fp)
	m := chapterDirNameRe.FindStringSubmatch(filepath.Base(dir))
	if m == nil || strings.TrimSpace(m[1]) == "" {
		return "", "", false
	}
	return filepath.Dir(dir), strings.TrimSpace(m[1]), true
}

// chapterSiblings reports whether two file paths are chapter subdirs of the SAME
// shattered book: both live in a "<prefix> - N" dir, under the same grandparent,
// with the same prefix. Two genuinely-distinct books in sibling dirs (different
// prefixes, e.g. "Book A - 1" vs "Book B - 2") are NOT siblings and pass through.
func chapterSiblings(aPath, bPath string) bool {
	if aPath == "" || bPath == "" {
		return false
	}
	ag, ap, aok := chapterDirParts(aPath)
	bg, bp, bok := chapterDirParts(bPath)
	return aok && bok && ag == bg && strings.EqualFold(ap, bp)
}

// sameMultiFileBook reports whether two books are parts of ONE physical
// multi-file audiobook — either chapters in the same folder (same parent dir) or
// chapters shattered across `<prefix> - N` sibling subdirs. Such pairs must
// never be emitted as duplicate candidates.
//
// Two book rows at the SAME path are not two chapters of one book: a chapter
// book is a single file, and two chapters never share one file. They are the
// "two book rows at one path" duplicate (CHAPTER-SUBFOLDER-NN-ROWS, 2026-09-25:
// a regrouped `<Book>/<Book> - NN/32.m4b` book sat at the same book folder as
// the real one; ABS listed both and this guard kept dedup from pairing them,
// because filepath.Dir of two equal paths is equal). They are left for the
// other guards and the scorer to judge.
func sameMultiFileBook(a, b *database.Book) bool {
	if a.FilePath == "" || b.FilePath == "" {
		return false
	}
	if SamePathPair(a, b) {
		return false
	}
	if filepath.Dir(a.FilePath) == filepath.Dir(b.FilePath) {
		return true
	}
	return chapterSiblings(a.FilePath, b.FilePath)
}

// SamePathPair reports whether a and b are two distinct book rows that
// resolve to the SAME file-system path once cleaned (CHAPTER-SUBFOLDER-NN-ROWS,
// 2026-09-25). sameMultiFileBook stopped suppressing such pairs on purpose —
// they are a real duplicate shape, not chapters of one book — which means
// they now reach every automated scoring/merge pass just like any other
// candidate pair.
//
// Owner decision (2026-09-25): a same-path pair goes to the REVIEW QUEUE
// ONLY. No automated path may merge, link, or resolve it — only a human
// looking at the pair may decide. Every automated merge/auto-resolve/
// auto-link/bulk-merge site must call this and skip the pair when it
// reports true; every candidate-emitting site should tag the pair (e.g.
// "same_path" in its reason/evidence) so the review UI can explain why the
// pair is flagged. Emitting the candidate itself is intentional and must
// NOT be skipped — only automated resolution of it.
func SamePathPair(a, b *database.Book) bool {
	if a == nil || b == nil {
		return false
	}
	if a.FilePath == "" || b.FilePath == "" {
		return false
	}
	if a.ID != "" && b.ID != "" && a.ID == b.ID {
		return false
	}
	return filepath.Clean(a.FilePath) == filepath.Clean(b.FilePath)
}

// samePathEvidence is the human-readable string every same_path signal /
// candidate note carries, so grepping the review UI or logs for one string
// finds every emitter that tagged this pair shape.
const samePathEvidence = "same_path: two book rows resolve to the same cleaned file path — review queue only, no automated merge"

// samePathScoreBreakdown builds a minimal UnifiedDedupScore carrying only the
// non-scoring SigSamePath signal, for candidate-emitting call sites (like
// upsertExactCandidate) that don't otherwise run the pair through
// unified.ComposeScore. It never awards a Band — Confidence 0 and no
// configured boost score to 0, same as an empty signal set — so this is
// purely a UI/audit tag, not a scoring shortcut.
func samePathScoreBreakdown(a, b *database.Book) *unified.UnifiedDedupScore {
	return &unified.UnifiedDedupScore{
		Pair: canonicalPairIDs(a.ID, b.ID),
		Signals: []unified.Signal{{
			Kind:       unified.SigSamePath,
			Raw:        1,
			Confidence: 0,
			Evidence:   samePathEvidence,
		}},
		Formula:    unified.FormulaVersion,
		ComputedAt: time.Now().UTC(),
	}
}
