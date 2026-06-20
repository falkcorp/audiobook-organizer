// file: internal/itunes/service/fs_regroup.go
// version: 1.0.0
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
}

// FSRegroupTarget is one real book recovered from a grandparent book-folder.
type FSRegroupTarget struct {
	BookFolder     string   // the grandparent dir all members live under
	Title          string   // consensus album/title
	AuthorID       int      // consensus author
	ASIN           string   // consensus ASIN ("" if none/mixed)
	Members        []FSBook // ordered by chapter number
	Cohesive       bool     // every member agrees on identity
	DistinctTitles []string // populated when !Cohesive (review signal)
}

// chapterDirRe matches a chapter subdir basename like "Cage of Souls - 15".
var chapterDirRe = regexp.MustCompile(`^(.*) - (\d+)$`)

// normTitle lowercases and strips non-alphanumerics for title cohesion comparison.
func normTitle(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// bookFolderOf returns the grandparent directory (the book folder) when the file sits
// inside a numbered chapter subdir (`<book>/<book> - N/<file>`); otherwise it returns
// the immediate parent. The grandparent is the shattered-book boundary.
func bookFolderOf(fp string) string {
	parent := filepath.Dir(fp)
	if chapterDirRe.MatchString(filepath.Base(parent)) {
		return filepath.Dir(parent)
	}
	return parent
}

// chapterNumOf extracts the chapter ordinal from the chapter-dir basename for ordering;
// returns a large fallback so unparseable members sort last but stably.
func chapterNumOf(fp string) int {
	if m := chapterDirRe.FindStringSubmatch(filepath.Base(filepath.Dir(fp))); m != nil {
		if n, err := strconv.Atoi(m[2]); err == nil {
			return n
		}
	}
	return 1 << 30
}

// GroupShatteredBooks groups single-file books by grandparent book-folder into recovered
// books. Only folders holding >=2 single-file primary books are returned (a lone single-file
// book under a folder is not "shattered"). Cohesion is reported, never silently merged across
// distinct identities.
func GroupShatteredBooks(books []FSBook) []FSRegroupTarget {
	byFolder := make(map[string][]FSBook)
	for _, b := range books {
		if b.FilePath == "" || !b.IsPrimary {
			continue
		}
		if b.FileCount > 1 {
			continue // already a real multi-file book
		}
		folder := bookFolderOf(b.FilePath)
		// require the file to actually be in a numbered chapter subdir — that is the
		// shattering fingerprint; a plain single-file book under its own folder is skipped.
		if !chapterDirRe.MatchString(filepath.Base(filepath.Dir(b.FilePath))) {
			continue
		}
		byFolder[folder] = append(byFolder[folder], b)
	}

	targets := make([]FSRegroupTarget, 0, len(byFolder))
	for folder, members := range byFolder {
		if len(members) < 2 {
			continue
		}
		sort.SliceStable(members, func(i, j int) bool {
			return chapterNumOf(members[i].FilePath) < chapterNumOf(members[j].FilePath)
		})

		// Consensus identity + cohesion.
		titleVotes := make(map[string]int)
		asinVotes := make(map[string]int)
		authorVotes := make(map[int]int)
		titleDisplay := make(map[string]string)
		for _, m := range members {
			nt := normTitle(m.Title)
			titleVotes[nt]++
			titleDisplay[nt] = m.Title
			authorVotes[m.AuthorID]++
			if m.ASIN != "" {
				asinVotes[m.ASIN]++
			}
		}
		title, _ := topString(titleVotes, titleDisplay)
		author := topInt(authorVotes)
		asin, asinDominant := topASIN(asinVotes, len(members))

		distinct := make([]string, 0, len(titleVotes))
		for nt := range titleVotes {
			distinct = append(distinct, titleDisplay[nt])
		}
		sort.Strings(distinct)
		cohesive := len(titleVotes) == 1 && len(authorVotes) == 1

		t := FSRegroupTarget{
			BookFolder: folder,
			Title:      title,
			AuthorID:   author,
			Members:    members,
			Cohesive:   cohesive,
		}
		if asinDominant {
			t.ASIN = asin
		}
		if !cohesive {
			t.DistinctTitles = distinct
		}
		targets = append(targets, t)
	}
	sort.SliceStable(targets, func(i, j int) bool { return targets[i].BookFolder < targets[j].BookFolder })
	return targets
}

func topString(votes map[string]int, display map[string]string) (string, int) {
	bestKey, best := "", -1
	for k, v := range votes {
		if v > best || (v == best && k < bestKey) {
			bestKey, best = k, v
		}
	}
	return display[bestKey], best
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
