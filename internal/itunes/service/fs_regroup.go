// file: internal/itunes/service/fs_regroup.go
// version: 1.1.0
// guid: 3b8e1f04-7a2c-4d96-9e51-0c6f2a8d4b73
// last-edited: 2026-06-20

// Package service — filesystem shattered-book regrouping (tag-anchored).
//
// The filesystem scanner historically emitted ONE standalone book per single-file
// chapter subdir (`<Author>/<Book>/<Book> - N/<file>`), so a 47-chapter audiobook
// became 47 "books" that all share the same `album`/`artist` tags — the scanner read
// the album tag into Book.Title and the artist into the author, but GROUPED BY FOLDER
// instead of by album. That shattered ~1,075 real books into ~35,577 records and fed
// the exact-layer dedup-candidate explosion (chapters cross-paired by checkExactTitle).
//
// GroupShatteredBooks re-derives the real books WITHOUT the iTunes XML or fingerprinting:
// it groups single-file books by their shared **grandparent book-folder** and confirms
// cohesion via the tag-derived identity already in the DB — `ASIN` when present (the
// strongest key), else `(normalize(Title), AuthorID)`. The grandparent folder is the book
// boundary; tag agreement guards against merging genuinely-distinct same-folder records.
// Members are ordered by chapter number parsed from the chapter-dir name.
//
// This is a PURE function over DB-derived metadata: no I/O, fully unit-testable.

package itunesservice

import (
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// FSBook is the slim DB-derived view the grouper reasons over.
type FSBook struct {
	ID          string
	Title       string // scanner set this from the file's `album` tag
	AuthorID    int    // 0 when unknown; same author => same id
	ASIN        string // AUDIOBOOK_ORGANIZER_ASIN / Book.ASIN; "" when absent
	FilePath    string
	DurationSec int
	FileCount   int  // BookFiles count; shattered chapters are single-file (0 or 1)
	IsPrimary   bool // non-primary version members are ignored
	EnrichScore int  // populated enrichment fields; the richest member becomes the survivor
}

// FSRegroupTarget is one real book recovered from a grandparent book-folder.
type FSRegroupTarget struct {
	BookFolder     string   // the grandparent dir all members live under
	Title          string   // consensus album/title
	AuthorID       int      // consensus author
	ASIN           string   // consensus ASIN ("" if none/mixed)
	Members        []FSBook // ordered by chapter number
	SurvivorID     string   // the member kept as the unified book (richest enrichment)
	Cohesive       bool     // every member agrees on identity
	DistinctTitles []string // populated when !Cohesive (review signal)
}

// chapterDirRe matches a chapter subdir basename like "Cage of Souls - 15":
// "<prefix> - <number>". The prefix is the real book title.
var chapterDirRe = regexp.MustCompile(`^(.*) - (\d+)$`)

// GroupStats reports why books were / were not grouped, so the record count can be
// reconciled against the on-disk inventory (no silent filtering).
type GroupStats struct {
	TotalBooks        int // all books seen
	NonPrimary        int // skipped: non-primary version member
	MultiFile         int // skipped: already a real multi-file book
	NotChapterPattern int // skipped: file not inside a "<prefix> - N" chapter dir
	ChapterCandidates int // single-file primary books inside a chapter dir
	SingletonGroups   int // (parent,prefix) groups with a single member (lone chapter)
	PrefixNotInParent int // groups skipped: prefix not a substring of the book folder name
	GroupedRecords    int // member records in accepted shattered books
	ShatteredBooks    int // accepted shattered books (== len(targets))
}

// normTitle lowercases and strips non-alphanumerics for substring/identity comparison.
func normTitle(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// chapterParts returns the parent dir, the book-title prefix, and the chapter number
// for a file inside a "<prefix> - N" chapter dir. ok=false when it isn't one.
func chapterParts(fp string) (parent, prefix string, num int, ok bool) {
	chapterDir := filepath.Dir(fp)
	m := chapterDirRe.FindStringSubmatch(filepath.Base(chapterDir))
	if m == nil {
		return "", "", 0, false
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return "", "", 0, false
	}
	return filepath.Dir(chapterDir), m[1], n, true
}

// GroupShatteredBooks groups shattered chapter books by (parent dir, chapter prefix).
//
// The true shattered-book signature is `<BookFolder>/<prefix> - N/<file>` where the book
// folder is NAMED AFTER the book — i.e. the chapter prefix is a substring of the parent
// folder name (`Cage of Souls - Cage of Souls/` holds `Cage of Souls - N/`). Requiring
// prefix ⊆ parent is high-precision: it catches Cage of Souls / Elantris but EXCLUDES
// flat dumps (`abooks/Throne of Jade 01/…`, parent "abooks" ⊉ "Throne of Jade") and series
// volumes stored as `Author/Series - N/singlefile` (parent = author ⊉ series) which must
// NOT be merged. The chapter prefix is used as the canonical title (Book.Title is often
// empty on these). Returns stats so the record count reconciles against the inventory.
func GroupShatteredBooks(books []FSBook) ([]FSRegroupTarget, GroupStats) {
	var st GroupStats
	st.TotalBooks = len(books)
	type key struct{ parent, prefix string }
	byKey := make(map[key][]FSBook)
	for _, b := range books {
		if !b.IsPrimary {
			st.NonPrimary++
			continue
		}
		if b.FileCount > 1 {
			st.MultiFile++
			continue
		}
		if b.FilePath == "" {
			st.NotChapterPattern++
			continue
		}
		parent, prefix, _, ok := chapterParts(b.FilePath)
		if !ok {
			st.NotChapterPattern++
			continue
		}
		st.ChapterCandidates++
		byKey[key{parent, prefix}] = append(byKey[key{parent, prefix}], b)
	}

	targets := make([]FSRegroupTarget, 0, len(byKey))
	for k, members := range byKey {
		if len(members) < 2 {
			st.SingletonGroups++
			continue
		}
		// Precision guard: the book folder must be named after the book.
		if !strings.Contains(normTitle(filepath.Base(k.parent)), normTitle(k.prefix)) {
			st.PrefixNotInParent++
			continue
		}
		sort.SliceStable(members, func(i, j int) bool {
			_, _, ni, _ := chapterParts(members[i].FilePath)
			_, _, nj, _ := chapterParts(members[j].FilePath)
			return ni < nj
		})

		authorVotes := make(map[int]int)
		asinVotes := make(map[string]int)
		for _, m := range members {
			authorVotes[m.AuthorID]++
			if m.ASIN != "" {
				asinVotes[m.ASIN]++
			}
		}
		author := topInt(authorVotes)
		asin, asinDominant := topASIN(asinVotes, len(members))

		survivor := ""
		bestEnrich := -1
		for _, m := range members {
			if m.EnrichScore > bestEnrich {
				bestEnrich = m.EnrichScore
				survivor = m.ID
			}
		}

		// Cohesive unless members disagree on a real (non-zero) author.
		distinctAuthors := 0
		for a := range authorVotes {
			if a != 0 {
				distinctAuthors++
			}
		}
		t := FSRegroupTarget{
			BookFolder: k.parent,
			Title:      k.prefix, // the chapter prefix IS the book title
			AuthorID:   author,
			Members:    members,
			SurvivorID: survivor,
			Cohesive:   distinctAuthors <= 1,
		}
		if asinDominant {
			t.ASIN = asin
		}
		st.ShatteredBooks++
		st.GroupedRecords += len(members)
		targets = append(targets, t)
	}
	sort.SliceStable(targets, func(i, j int) bool {
		if targets[i].BookFolder != targets[j].BookFolder {
			return targets[i].BookFolder < targets[j].BookFolder
		}
		return targets[i].Title < targets[j].Title
	})
	return targets, st
}

func topInt(votes map[int]int) int {
	bestKey, best := 0, -1
	for k, v := range votes {
		if v > best {
			bestKey, best = k, v
		}
	}
	return bestKey
}

// topASIN returns the dominant ASIN only if a clear majority of members carry it (guards
// against a stray mis-enriched chapter dictating identity).
func topASIN(votes map[string]int, total int) (string, bool) {
	bestKey, best := "", 0
	for k, v := range votes {
		if v > best {
			bestKey, best = k, v
		}
	}
	return bestKey, best*2 > total // > 50% of members agree
}
