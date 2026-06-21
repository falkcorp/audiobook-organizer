// file: internal/dedup/chapter_sibling.go
// version: 1.0.0
// guid: c1d4e7a2-9b35-4f80-8e16-2a7c0d5b9f43
// last-edited: 2026-06-21

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

	"github.com/falkcorp/audiobook-organizer/internal/database"
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
func sameMultiFileBook(a, b *database.Book) bool {
	if a.FilePath == "" || b.FilePath == "" {
		return false
	}
	if filepath.Dir(a.FilePath) == filepath.Dir(b.FilePath) {
		return true
	}
	return chapterSiblings(a.FilePath, b.FilePath)
}
