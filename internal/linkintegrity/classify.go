// file: internal/linkintegrity/classify.go
// version: 1.1.0
// guid: 7a3e15c8-4b92-4d07-a6f3-1c85920be47d
// last-edited: 2026-08-06

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

	// ProbesOK / ProbesFailed are set only by ClassifyDirProbed and record how
	// much of the folder was actually MEASURED. They are reported separately
	// from AudioCount on purpose: "4 files, 4 measured" and "4 files, 1
	// measured" can produce the same verdict from very different evidence, and
	// an operator reading the queue needs to see which one they are looking at.
	ProbesOK     int
	ProbesFailed int
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

// ── tier-2: classification with PROBED durations ─────────────────────────────
//
// ClassifyDir's series guard is inert without durations, so a whole-library
// tier-1 scan (one DB read + one os.Stat per book, no budget for subprocesses)
// can only ever route a multi-file folder to review. Tier 2 revisits that much
// smaller flagged set and spends an ffprobe per file to supply the missing
// signal. ClassifyDirProbed is the seam between the two: same classifier, better
// inputs.

// ProbedDuration is one audio file's probe OUTCOME — deliberately not a bare
// int.
//
// 🔴 THIS TYPE EXISTS TO KEEP "COULD NOT MEASURE" FROM BECOMING "MEASURED
// ZERO". A failed probe expressed as 0 seconds does not read as missing data
// downstream; it reads as a very short file, which the series guard interprets
// as "a chapter, therefore safe to merge into one book". That exact substitution
// — DurationSec==0 taken as evidence rather than as its absence — disabled the
// regroup series guard across 97.5% of the review queue and came within one
// apply of merging 41 of 43 distinct novels. Carrying the OK flag alongside the
// number makes the failure impossible to express.
type ProbedDuration struct {
	// Name is the audio file's basename, for reporting.
	Name string
	// Sec is the measured runtime in seconds. Meaningful ONLY when OK is true.
	Sec int
	// OK is true only when the file was genuinely measured. Construct it as
	// (err == nil && secs > 0): ffprobe can exit 0 having reported nothing
	// usable for a truncated or header-only container, and audioutil's
	// ProbeDurationSeconds deliberately does not validate that itself ("callers
	// apply their own validity rules"). A zero from a successful exit is still
	// an absence of evidence.
	OK bool
}

// ClassifyDirProbed classifies a directory using per-file probe outcomes.
//
// It feeds ClassifyDir ONLY the durations that were actually measured — a failed
// probe contributes nothing at all rather than a zero — and then applies a
// coverage guard on top of the result.
//
// WHY THE COVERAGE GUARD IS NEEDED ON TOP OF THE EXCLUSION. Excluding failures
// already fixes the dangerous direction in most shapes: with 4 files sharing one
// stem where only one measured 6,000s, the guard sees 1-of-1 long and fires.
// But the reverse partial is still unsafe — one file measured at chapter length
// and three unknown yields "0 of 1 are long", which passes the guard and would
// auto-link a folder we have barely looked at. So when ANY probe failed, a
// multi-file folder cannot be called one book no matter how the measured subset
// reads. Absent evidence means "cannot verify", never "confirmed".
//
// The single-audio-file case is exempt because its verdict never depended on
// duration: one file in a folder is one book whether or not it could be probed.
func ClassifyDirProbed(names []string, subdirCount int, probes []ProbedDuration) DirVerdict {
	known := make([]int, 0, len(probes))
	failed := 0
	for _, p := range probes {
		// Re-check Sec > 0 rather than trusting OK alone: this is the one place
		// the invariant is enforced, and a caller that sets OK from err==nil
		// only must not be able to smuggle a zero past it.
		if p.OK && p.Sec > 0 {
			known = append(known, p.Sec)
			continue
		}
		failed++
	}

	v := ClassifyDir(names, subdirCount, known)
	v.ProbesOK = len(known)
	v.ProbesFailed = failed

	if failed > 0 && v.OneBook && v.AudioCount > 1 {
		v.OneBook = false
		v.Reason = fmt.Sprintf(
			"%d audio files share one title and the %d that could be measured are chapter-length, "+
				"but %d file(s) could not be probed — cannot rule out a series on partial evidence",
			v.AudioCount, len(known), failed)
	}
	return v
}

// BookLengthSec mirrors fs_regroup_shape.go's bookLengthSec: the runtime above
// which a single file is judged to be a whole book rather than a chapter. 90
// minutes sits in a wide empty band — a 90-minute chapter is vanishingly rare
// and a novel shorter than that would not be split across files.
const BookLengthSec = 90 * 60

// DirNameOf returns the folder name a human would recognise for a path.
func DirNameOf(p string) string { return filepath.Base(strings.TrimRight(p, "/")) }
