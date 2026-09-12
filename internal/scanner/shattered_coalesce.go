// file: internal/scanner/shattered_coalesce.go
// version: 1.2.0
// guid: 9b4e2a17-6c08-4d35-8f91-3a7d05c2e6b4
// last-edited: 2026-09-12

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
	"context"
	"path/filepath"
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/chaptershape"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
)

// coalesceShatteredSiblings merges single-file books that are chapters of one
// shattered book (sibling "<prefix> - N" subdirs under a book-named folder) into
// ONE multi-file Book with SegmentFiles ordered by chapter number. All other
// books pass through untouched. Returns the input unchanged when nothing merges.
func coalesceShatteredSiblings(ctx context.Context, books []Book) []Book {
	type key struct{ parent, prefix string }
	groups := map[key][]int{}
	for i := range books {
		b := &books[i]
		if len(b.SegmentFiles) > 0 || b.FilePath == "" {
			continue // already multi-file, or no path to reason about
		}
		parent, prefix, _, ok := chaptershape.Parts(b.FilePath)
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
		if !chaptershape.PrefixInParent(k.parent, k.prefix) {
			continue
		}
		sort.SliceStable(idxs, func(a, b int) bool {
			_, _, na, _ := chaptershape.Parts(books[idxs[a]].FilePath)
			_, _, nb, _ := chaptershape.Parts(books[idxs[b]].FilePath)
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
		logging.Info(ctx, "scanner coalesced shattered siblings",
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
