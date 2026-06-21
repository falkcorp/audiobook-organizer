// file: internal/scanner/shattered_coalesce.go
// version: 1.0.0
// guid: 9b4e2a17-6c08-4d35-8f91-3a7d05c2e6b4
// last-edited: 2026-06-21

// Package scanner — scan-time prevention of the "shattered book" defect.
//
// The scanner groups files PER LEAF DIRECTORY (groupFilesIntoBooks runs once per
// dir), so a book laid out as one chapter per subdir —
// `<Book>/<Book> - 1/f`, `<Book>/<Book> - 2/f`, … — produces one standalone
// single-file Book per chapter. That shattered ~1,075 real books into ~35,577
// records and fed the 380K dedup-candidate explosion. The data was healed by
// maintenance.fs-regroup-xml; coalesceShatteredSiblings is the scan-time analogue
// that stops the defect from being re-persisted on the next scan.
//
// It is PATH-BASED (no tag I/O — album/track tags aren't read until ProcessBooks)
// and reuses the heal's exact precision guard: group single-file books by
// (grandparent dir, chapter prefix) where the chapter dir matches "<prefix> - N",
// accepting a group ONLY when the prefix is a substring of the parent folder name
// (`Cage of Souls - Cage of Souls/Cage of Souls - N/`). That excludes flat dumps
// (`abooks/Throne of Jade 01/…`) and series volumes (`Author/Series - N/file`),
// both of which must NOT be merged — validated in production by the heal.
//
// Gated by config.AppConfig.CoalesceShatteredSiblings (default OFF).

package scanner

import (
	"log/slog"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// shatterChapterDirRe matches a chapter subdir basename "<prefix> - <number>".
// Mirrors itunesservice.chapterDirRe.
var shatterChapterDirRe = regexp.MustCompile(`^(.*) - (\d+)$`)

// shatterChapterParts returns the grandparent dir, book-title prefix, and chapter
// number for a file inside a "<prefix> - N" chapter dir. ok=false otherwise.
func shatterChapterParts(fp string) (parent, prefix string, num int, ok bool) {
	chapterDir := filepath.Dir(fp)
	m := shatterChapterDirRe.FindStringSubmatch(filepath.Base(chapterDir))
	if m == nil || strings.TrimSpace(m[1]) == "" {
		return "", "", 0, false
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return "", "", 0, false
	}
	return filepath.Dir(chapterDir), strings.TrimSpace(m[1]), n, true
}

// normShatterPrefix lowercases and strips non-alphanumerics — matches
// itunesservice.normTitle so the prefix⊆parent guard behaves identically to the
// production-validated heal (distinct from this package's space-joined
// normForCompare).
func normShatterPrefix(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// coalesceShatteredSiblings merges single-file books that are chapters of one
// shattered book (sibling "<prefix> - N" subdirs under a book-named folder) into
// ONE multi-file Book with SegmentFiles ordered by chapter number. All other
// books pass through untouched. Returns the input unchanged when nothing merges.
func coalesceShatteredSiblings(books []Book) []Book {
	type key struct{ parent, prefix string }
	groups := map[key][]int{}
	for i := range books {
		b := &books[i]
		if len(b.SegmentFiles) > 0 || b.FilePath == "" {
			continue // already multi-file, or no path to reason about
		}
		parent, prefix, _, ok := shatterChapterParts(b.FilePath)
		if !ok {
			continue
		}
		k := key{parent, prefix}
		groups[k] = append(groups[k], i)
	}
	if len(groups) == 0 {
		return books
	}

	merged := make([]bool, len(books))
	coalesced := map[int]Book{} // first-member index → coalesced book
	for k, idxs := range groups {
		if len(idxs) < 2 {
			continue // a lone chapter is not a shattered book
		}
		// Precision guard: the book folder must be named after the book.
		if !strings.Contains(normShatterPrefix(filepath.Base(k.parent)), normShatterPrefix(k.prefix)) {
			continue
		}
		sort.SliceStable(idxs, func(a, b int) bool {
			_, _, na, _ := shatterChapterParts(books[idxs[a]].FilePath)
			_, _, nb, _ := shatterChapterParts(books[idxs[b]].FilePath)
			return na < nb
		})
		segs := make([]string, len(idxs))
		for j, idx := range idxs {
			segs[j] = books[idx].FilePath
			merged[idx] = true
		}
		coalesced[idxs[0]] = Book{
			FilePath:     segs[0],
			Format:       strings.ToLower(filepath.Ext(segs[0])),
			SegmentFiles: segs,
		}
		slog.Info("scanner coalesced shattered siblings",
			"book", k.prefix, "chapters", len(segs), "dir", k.parent)
	}
	if len(coalesced) == 0 {
		return books
	}

	out := make([]Book, 0, len(books))
	for i := range books {
		if cb, ok := coalesced[i]; ok {
			out = append(out, cb)
			continue
		}
		if merged[i] {
			continue
		}
		out = append(out, books[i])
	}
	return out
}
