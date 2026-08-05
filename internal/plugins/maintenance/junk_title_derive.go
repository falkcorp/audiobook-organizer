// file: internal/plugins/maintenance/junk_title_derive.go
// version: 1.0.0
// guid: 6fb9129d-4c59-4097-979e-6cfe61bc6894
// last-edited: 2026-08-04

package maintenance

import (
	"path/filepath"
	"sort"
	"strings"
)

// junkTitles are stored Book.Title values that are demonstrably NOT a book title.
// They come from two distinct accidents, both confirmed against production:
//
//   - "read by narrator" (1,595 books) — the on-disk convention is
//     "<Title> - <Author> - read by <narrator>.<ext>" and the importer kept the
//     trailing credit instead of the leading title. The organizer then *wrote*
//     folders named after it, so the directory is poisoned too.
//   - "intro" / "opening credits" / "big finish ident" (466 books) — track 1's
//     ID3 title tag was promoted to the book title on a multi-file book.
//
// Matched case-insensitively after trimming.
var junkTitles = map[string]struct{}{
	"read by narrator":  {},
	"intro":             {},
	"opening credits":   {},
	"big finish ident":  {},
	"opening credits 1": {},
}

// IsJunkTitle reports whether a stored title is one of the known bad values.
func IsJunkTitle(title string) bool {
	_, ok := junkTitles[strings.ToLower(strings.TrimSpace(title))]
	return ok
}

// audioExts are stripped from a filename before it is read as a title.
var audioExts = map[string]struct{}{
	".mp3": {}, ".m4a": {}, ".m4b": {}, ".mp4": {}, ".ogg": {},
	".opus": {}, ".flac": {}, ".wav": {}, ".aac": {}, ".wma": {},
}

// stripAudioExt removes a known audio extension, and nothing else — an unknown
// suffix is left alone rather than guessed at, so "Vol. 1.5" keeps its ".5".
func stripAudioExt(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if _, ok := audioExts[ext]; ok {
		return name[:len(name)-len(ext)]
	}
	return name
}

// titleFromFilename recovers the title from a file named by the
// "<Title> - <Author> - read by <narrator>" convention.
//
// 🔑 The author name is REQUIRED to strip safely, and that is the whole point.
// Naively dropping the last " - " segment after the credit would corrupt any
// title that legitimately contains one:
//
//	"Dark Gallifrey - The War Master Part 2 - read by narrator.mp3"
//	  naive → "Dark Gallifrey - The War Master"   ✗ eats real title text
//	  ours  → "Dark Gallifrey - The War Master Part 2"  ✓ (no author match, so
//	                                                       only the credit goes)
//
// When author is known and present, both it and the credit are removed:
//
//	"Nocturne - 3 - Umbra Mortem - JD Glasscock - read by narrator.m4b"
//	  → "Nocturne - 3 - Umbra Mortem"
//
// A filename carrying no credit at all is used whole.
func titleFromFilename(filename, author string) string {
	stem := strings.TrimSpace(stripAudioExt(filename))
	if stem == "" {
		return ""
	}

	lower := strings.ToLower(stem)
	idx := strings.LastIndex(lower, " - read by ")
	if idx < 0 {
		return stem // no credit suffix — the whole stem is the title
	}
	head := strings.TrimSpace(stem[:idx])

	// Remove a trailing " - <Author>" only when it really is the author.
	if a := strings.TrimSpace(author); a != "" {
		suffix := " - " + strings.ToLower(a)
		if strings.HasSuffix(strings.ToLower(head), suffix) {
			head = strings.TrimSpace(head[:len(head)-len(suffix)])
		}
	}
	return head
}

// majorityDirOf returns the directory holding the most of these files. Books
// whose files span directories are common enough that picking the first would
// be arbitrary.
func majorityDirOf(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	counts := map[string]int{}
	for _, p := range paths {
		if d := filepath.Dir(p); d != "" && d != "." {
			counts[d]++
		}
	}
	best, bestN := "", -1
	keys := make([]string, 0, len(counts))
	for d := range counts {
		keys = append(keys, d)
	}
	sort.Strings(keys) // deterministic when two dirs tie
	for _, d := range keys {
		if counts[d] > bestN {
			best, bestN = d, counts[d]
		}
	}
	return best
}

// DeriveJunkTitleReplacement works out the real title for a book whose stored
// title is junk, and reports which evidence it used.
//
// Candidates are tried in order of trustworthiness for the shape observed:
//
//  1. Multi-file book → the deepest folder segment. These are the ID3-residue
//     cases, and the organizer already named the folder correctly:
//     ".../Big Finish Productions/Dark Gallifrey/Dark Gallifrey - The War Master Part 2"
//  2. The filename, parsed for the "read by" convention. This is the only
//     evidence for the single-file cases, whose folder was named from the same
//     bad title and is therefore useless.
//  3. Walking up the directory, first segment that is not itself junk — a last
//     resort for ".../Kaiju Task/read by narrator/..." shapes.
//
// Returns ok=false when nothing trustworthy is found; the caller must then leave
// the book alone rather than invent a title.
func DeriveJunkTitleReplacement(storedTitle, author string, filePaths []string) (title, method string, ok bool) {
	stored := strings.TrimSpace(storedTitle)

	// corroborated: some path element already matches the stored title, which
	// means the stored value is what the library actually says. Walking further
	// up would then "repair" a correct title into its parent folder — how an
	// early version of this turned "Some Book" into its author's name.
	corroborated := false

	accept := func(cand, how string) (string, string, bool) {
		c := strings.TrimSpace(cand)
		// Never swap one junk title for another, and never write an empty or
		// single-character title.
		if len(c) < 2 || IsJunkTitle(c) {
			return "", "", false
		}
		if strings.EqualFold(c, stored) {
			corroborated = true
			return "", "", false
		}
		return c, how, true
	}

	dir := majorityDirOf(filePaths)

	// 1. Multi-file: trust the folder.
	if len(filePaths) >= 2 && dir != "" {
		if t, m, good := accept(filepath.Base(dir), "folder"); good {
			return t, m, true
		}
	}

	// 2. The filename and its "read by" convention.
	if len(filePaths) > 0 {
		if t, m, good := accept(titleFromFilename(filepath.Base(filePaths[0]), author), "filename"); good {
			return t, m, true
		}
	}

	// 3. Walk up for the first non-junk segment — but only a BOUNDED distance,
	//    and only if nothing so far corroborated the stored title.
	//
	//    🔴 Unbounded, this is actively destructive: it climbs past the book, past
	//    the author, and happily returns a library root. It produced "lib" from
	//    "/lib/A/intro.mp3" and "Author" from "/lib/Author/Some Book/Some Book.m4b"
	//    before this bound existed. The real shape needing rescue is only ever one
	//    level up — ".../Kaiju Task/read by narrator/<file>" — so one level is all
	//    it gets.
	//    The hop is also only taken to ESCAPE A POISONED FOLDER. If the folder was
	//    rejected for any other reason — too short, corroborating the stored title —
	//    the layout is not the shape this rescue understands, and climbing anyway
	//    just returns whatever happens to sit above it (that is how "/lib/A/intro.mp3"
	//    yielded "lib").
	const maxAncestorHops = 1
	if !corroborated && dir != "" && IsJunkTitle(filepath.Base(dir)) {
		d := dir
		for hop := 0; hop <= maxAncestorHops; hop++ {
			if d == "" || d == "/" || d == "." {
				break
			}
			if t, m, good := accept(filepath.Base(d), "ancestor-folder"); good {
				return t, m, true
			}
			if corroborated {
				break
			}
			d = filepath.Dir(d)
		}
	}

	return "", "", false
}
