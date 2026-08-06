// file: internal/linkintegrity/classify.go
// version: 1.0.0
// guid: 7a3e15c8-4b92-4d07-a6f3-1c85920be47d
// last-edited: 2026-08-05

package linkintegrity

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// ── directory-shape classification ───────────────────────────────────────────
//
// When a book's FilePath resolves to a DIRECTORY, the audio lives inside it and
// we must decide whether those files are ONE book (link them all to this book)
// or SEVERAL (hold for a human). Getting this wrong in the permissive direction
// merges distinct novels into a single row, and the merge path hard-deletes
// absorbed rows — so this classifier is deliberately biased toward "review".
//
// 🔴 WHY AUTO-LINKING EVERYTHING IS NOT AN OPTION. Real folders measured on
// 2026-08-05, all of which look identical to a naive "link the folder's files"
// rule:
//
//	Emberverse/Book 10 - The Given Sacrifice   23 files `_01_`…`_23_`   → ONE book
//	Faith Hunter (Jane Yellowrock)/Jane Yellowrock
//	                                           11 files, but they are
//	                                           `01 Skinwalker part 1`,
//	                                           `02 Blood Cross Part 1`, …
//	                                                                    → MANY books
//	Doctor Who/Spin-offs/Stageplays            5 files, 5 different plays
//	                                                                    → MANY books
//
// The discriminator is the same one fs_regroup_shape.go already relies on:
// distinct title stems. Files that are chapters of one book share a stem after
// ordinals are stripped; files that are different works do not.
//
// Owner decision D1 (2026-08-05): auto-link ONLY the unambiguous; everything
// else becomes a review hold.

var (
	audioExtRe = regexp.MustCompile(`(?i)\.(m4b|mp3|m4a|mp4|ogg|flac|aac|wma|opus|wav)$`)
	// leadOrdinalRe strips "01 ", "1. ", "03 - ", "1_" from the front of a name.
	leadOrdinalRe = regexp.MustCompile(`^\s*\d{1,4}\s*[-._)\]]*\s*`)
	// trailOrdinalRe strips a trailing ordinal, including a bare "_04_" form.
	trailOrdinalRe = regexp.MustCompile(`[\s._-]*\d{1,4}[\s._-]*$`)
	// partMarkerRe matches an explicit "one work split into N" marker: "Part 1",
	// "pt 2", "Part 1of2", "Disc 3", "CD 02". These are POSITIVE evidence of a
	// single book, and are the shape a stem comparison alone can miss.
	partMarkerRe = regexp.MustCompile(`(?i)\b(?:p(?:ar)?t|disc|disk|cd)\.?\s*_?\s*\d{1,3}(?:\s*of\s*\d{1,3})?`)
)

// IsAudioFile reports whether a filename carries a known audio extension.
func IsAudioFile(name string) bool { return audioExtRe.MatchString(name) }

// TitleStem reduces a filename to its comparable identity: extension gone,
// leading and trailing ordinals gone, part/disc markers gone, then lowercased
// with non-alphanumerics removed.
//
// Stripping the part marker BEFORE comparing is what makes
// "The Hatching-Part01" and "The Hatching-Part02" collapse to one stem — they
// are one book — while "Skinwalker part 1" and "Blood Cross Part 1" stay
// distinct, because their remaining text differs.
func TitleStem(name string) string {
	s := audioExtRe.ReplaceAllString(name, "")
	s = partMarkerRe.ReplaceAllString(s, " ")
	s = leadOrdinalRe.ReplaceAllString(s, "")
	s = trailOrdinalRe.ReplaceAllString(s, "")
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// DirVerdict is the outcome of classifying a directory's contents.
type DirVerdict struct {
	// OneBook is true only when the folder's audio is confidently ONE book.
	OneBook bool
	// Reason names the evidence, never just the verdict — a queue of rows all
	// saying "ambiguous" is unworkable, which is the failure this replaces.
	Reason string
	// DistinctStems is the evidence count behind the call.
	DistinctStems int
	// AudioCount is how many audio files the folder held.
	AudioCount int
}

// ClassifyDir decides whether a directory's audio files are one book.
//
// names is the folder's immediate entries (files and subdirs, basenames only);
// subdirCount is how many of them are directories. durationsSec is the per-audio
// -file runtime in the same order as the audio files appear after filtering,
// or nil when unknown — it is the series-vs-chapters discriminator that
// filenames cannot provide, and its ABSENCE forces a review verdict for any
// multi-file folder rather than a guess.
func ClassifyDir(names []string, subdirCount int, durationsSec []int) DirVerdict {
	audio := make([]string, 0, len(names))
	for _, n := range names {
		if IsAudioFile(n) {
			audio = append(audio, n)
		}
	}
	v := DirVerdict{AudioCount: len(audio)}

	switch {
	case len(audio) == 0 && subdirCount > 0:
		v.Reason = "no audio directly in the folder, but it has subdirectories — needs a recursive scan"
		return v
	case len(audio) == 0:
		v.Reason = "folder contains no audio files"
		return v
	case len(audio) == 1:
		v.OneBook = true
		v.Reason = "exactly one audio file in the folder"
		v.DistinctStems = 1
		return v
	}

	stems := map[string]struct{}{}
	for _, n := range audio {
		if s := TitleStem(n); s != "" {
			stems[s] = struct{}{}
		}
	}
	v.DistinctStems = len(stems)

	// A folder whose files carry MANY distinct titles is a collection of works,
	// not one book. This is the Jane Yellowrock / Doctor Who shape.
	if len(stems) > 1 {
		v.Reason = fmt.Sprintf("%d audio files carry %d distinct titles — likely separate works, not one book",
			len(audio), len(stems))
		return v
	}

	// One shared stem. Before calling it one book, apply the series guard: if
	// most members are individually long enough to BE books, this is a series
	// whose volumes happen to share a name prefix (the "Super Sales on Super
	// Heroes 1..5" shape that stems alone cannot separate).
	if len(durationsSec) == 0 {
		v.Reason = fmt.Sprintf("%d audio files share one title, but no durations are known — cannot rule out a series",
			len(audio))
		return v
	}
	long := 0
	for _, d := range durationsSec {
		if d >= BookLengthSec {
			long++
		}
	}
	if long*2 > len(durationsSec) {
		v.Reason = fmt.Sprintf("%d audio files share one title but %d of them are over %d minutes — these are whole books, not chapters",
			len(audio), long, BookLengthSec/60)
		return v
	}

	v.OneBook = true
	v.Reason = fmt.Sprintf("%d audio files share one title and are chapter-length — one book", len(audio))
	return v
}

// BookLengthSec mirrors fs_regroup_shape.go's bookLengthSec: the runtime above
// which a single file is judged to be a whole book rather than a chapter. 90
// minutes sits in a wide empty band — a 90-minute chapter is vanishingly rare
// and a novel shorter than that would not be split across files.
const BookLengthSec = 90 * 60

// DirNameOf returns the folder name a human would recognise for a path.
func DirNameOf(p string) string { return filepath.Base(strings.TrimRight(p, "/")) }
