// file: internal/scanner/multifile_detector.go
// version: 1.1.1
// guid: 7a3e4c8b-1d2f-4a5b-9c6d-8e0f1a2b3c4d
// last-edited: 2026-07-12

// Package scanner — multi-file audiobook detection.
//
// MAYDEPLOY-G1: when a folder contains N≥3 audio files matching a sequential
// naming pattern AND their album / album_artist tags agree across files
// (quorum ≥ 75% of files with a non-empty value), the whole folder is one
// audiobook with N BookFiles — not N separate Books.
//
// This file provides the pure detection helper. The scanner's tag-reading
// code populates MultiFileInfo for each candidate file and calls
// DetectMultiFileGroup; the scanner is responsible for the file IO.

package scanner

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// MultiFileInfo carries the per-file information the detector needs.
// All fields except Path are best-effort — empties are tolerated.
type MultiFileInfo struct {
	Path        string // absolute or relative file path
	Album       string // tag: album   (TALB / ©alb / ALBUM)
	AlbumArtist string // tag: album_artist (TPE2 / aART / ALBUMARTIST)
	TrackNum    int    // tag: track number, 0 = unknown
	TotalTracks int    // tag: track total, 0 = unknown
}

// detectedNum is a per-file detection result used internally.
type detectedNum struct {
	idx    int // index back into the input slice
	number int // detected sequential number (1-based), 0 = none
	total  int // detected total (from "N of M" or tag), 0 = unknown
}

// MultiFileDetectionConfig tunes the detector. Defaults via DefaultMultiFileConfig().
type MultiFileDetectionConfig struct {
	MinFiles      int     // minimum files in folder to consider; default 3
	TagQuorum     float64 // fraction of files needed for tag agreement; default 0.75
	PatternQuorum float64 // fraction of files needed to yield a sequential number; default 0.75
	DensityRatio  float64 // (#detected) / (max-min+1); default 0.75
}

// DefaultMultiFileConfig returns the standard thresholds.
func DefaultMultiFileConfig() MultiFileDetectionConfig {
	return MultiFileDetectionConfig{
		MinFiles:      3,
		TagQuorum:     0.75,
		PatternQuorum: 0.75,
		DensityRatio:  0.75,
	}
}

// Compiled sequential-number patterns, applied in priority order on the
// filename stem. Each regex must capture the sequential number in group 1,
// optionally the total in group 2.
var multiFileNumPatterns = []*regexp.Regexp{
	// "Chapter 01", "chapter_05"
	regexp.MustCompile(`(?i)\bchapter[\s_\-]+(\d{1,4})\b`),
	// "Part 1 of 8" / "Part 1"
	regexp.MustCompile(`(?i)\bpart[\s_\-]+(\d{1,4})(?:[\s_\-]+of[\s_\-]+(\d{1,4}))?\b`),
	// "Track 01"
	regexp.MustCompile(`(?i)\btrack[\s_\-]+(\d{1,4})\b`),
	// "Disc 01" / "CD 01"
	regexp.MustCompile(`(?i)\b(?:disc|cd)[\s_\-]+(\d{1,4})\b`),
	// "(76 of 85)"
	regexp.MustCompile(`\((\d{1,4})\s*of\s*(\d{1,4})\)`),
	// "(76/85)" or "(76_85)" or "(76-85)"
	regexp.MustCompile(`\((\d{1,4})[\s_\-\/](\d{1,4})\)`),
	// trailing " - 1_85" / " - 1/85" / "_1_85" near end of stem
	regexp.MustCompile(`[\s_\-](\d{1,4})[\s_\-\/](\d{1,4})$`),
	// "01 of 85"
	regexp.MustCompile(`(?i)\b(\d{1,4})\s+of\s+(\d{1,4})\b`),
	// leading "01 - ", "002. ", "1_"
	regexp.MustCompile(`^(\d{1,4})[\s_\-\.\:]`),
	// bare "01"
	regexp.MustCompile(`^(\d{1,4})$`),
}

// extractSeqNumber returns (number, total) extracted from a filename stem.
// number == 0 means no sequential number found.
func extractSeqNumber(stem string) (number int, total int) {
	for _, re := range multiFileNumPatterns {
		m := re.FindStringSubmatch(stem)
		if m == nil {
			continue
		}
		number = atoiSafe(m[1])
		if number <= 0 {
			continue
		}
		if len(m) > 2 {
			total = atoiSafe(m[2])
		}
		return number, total
	}
	return 0, 0
}

func atoiSafe(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
		if n > 1_000_000 {
			return 0
		}
	}
	return n
}

// normalizeTagValue lowercases, strips diacritic-irrelevant whitespace and
// collapses spaces for case/punctuation-insensitive comparison.
func normalizeTagValue(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	// Collapse any run of whitespace/underscore/dash to a single space.
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '_' || r == '-' {
			if !prevSpace {
				b.WriteRune(' ')
			}
			prevSpace = true
		} else {
			b.WriteRune(r)
			prevSpace = false
		}
	}
	return strings.TrimSpace(b.String())
}

// majorityValue returns the (value, count) of the most common non-empty
// entry. If no non-empty entries exist, returns ("", 0).
func majorityValue(values []string) (string, int) {
	counts := make(map[string]int)
	for _, v := range values {
		v = normalizeTagValue(v)
		if v == "" {
			continue
		}
		counts[v]++
	}
	best := ""
	bestN := 0
	for k, n := range counts {
		if n > bestN {
			best = k
			bestN = n
		}
	}
	return best, bestN
}

// DetectMultiFileGroup decides whether the given audio files in a single
// directory should be treated as ONE multi-file audiobook. When isMultiFile
// is true, sortedFiles contains the input files ordered by detected track
// number (files without a number are appended at the end in their original
// order).
//
// Detection rules (all must hold):
//   - len(files) >= cfg.MinFiles
//   - sequential numbers extractable from ≥ cfg.PatternQuorum fraction of files
//   - detected numbers are dense in [min..max]:
//     #detected / (max-min+1) >= cfg.DensityRatio
//   - ≥ cfg.TagQuorum fraction of files share the same non-empty
//     normalized album OR album_artist; OR all files have empty album AND
//     album_artist (AP-5: untagged tracks are grouped by sequential name alone)
//
// The detector does NO file IO; the caller pre-extracts tag metadata.
func DetectMultiFileGroup(files []MultiFileInfo, cfg MultiFileDetectionConfig) (bool, []MultiFileInfo) {
	if cfg.MinFiles <= 0 {
		cfg.MinFiles = 3
	}
	if cfg.TagQuorum <= 0 {
		cfg.TagQuorum = 0.75
	}
	if cfg.PatternQuorum <= 0 {
		cfg.PatternQuorum = 0.75
	}
	if cfg.DensityRatio <= 0 {
		cfg.DensityRatio = 0.75
	}

	n := len(files)
	if n < cfg.MinFiles {
		return false, files
	}

	// Step 1: extract a sequential number per file.
	detections := make([]detectedNum, n)
	for i, f := range files {
		stem := strings.TrimSuffix(filepath.Base(f.Path), filepath.Ext(f.Path))
		num, tot := extractSeqNumber(stem)
		if num == 0 && f.TrackNum > 0 {
			// Fall back to the audio tag's track number if the stem doesn't
			// reveal a sequence number — many ripped audiobooks have clean
			// titles but proper track tags.
			num = f.TrackNum
			tot = f.TotalTracks
		}
		detections[i] = detectedNum{idx: i, number: num, total: tot}
	}

	// Step 2: pattern quorum.
	numbered := 0
	for _, d := range detections {
		if d.number > 0 {
			numbered++
		}
	}
	if float64(numbered)/float64(n) < cfg.PatternQuorum {
		return false, files
	}

	// Step 3: density check on detected numbers.
	min, max := 0, 0
	first := true
	for _, d := range detections {
		if d.number <= 0 {
			continue
		}
		if first {
			min, max = d.number, d.number
			first = false
		} else {
			if d.number < min {
				min = d.number
			}
			if d.number > max {
				max = d.number
			}
		}
	}
	span := max - min + 1
	if span <= 0 {
		return false, files
	}
	// Reject if the span is wildly larger than the file count — e.g. files
	// numbered 1, 2 and 500 are not a sequence.
	if float64(numbered)/float64(span) < cfg.DensityRatio {
		return false, files
	}
	// Numbers should also not all collide on one value.
	seen := make(map[int]bool, numbered)
	for _, d := range detections {
		if d.number > 0 {
			seen[d.number] = true
		}
	}
	if len(seen) < int(float64(numbered)*0.5) {
		return false, files
	}

	// Step 4: tag agreement.
	albums := make([]string, n)
	albumArtists := make([]string, n)
	for i, f := range files {
		albums[i] = f.Album
		albumArtists[i] = f.AlbumArtist
	}
	_, albumCount := majorityValue(albums)
	_, artistCount := majorityValue(albumArtists)
	required := int(float64(n)*cfg.TagQuorum + 0.5)
	if required < 1 {
		required = 1
	}
	// AP-5: when ALL files lack album and album_artist tags the silence is
	// uniform, not a sign of tag conflict. Sequential filenames alone are
	// sufficient evidence — the scanner will use the folder name as the title.
	allTagsAbsent := albumCount == 0 && artistCount == 0
	tagAgrees := albumCount >= required || artistCount >= required || allTagsAbsent
	if !tagAgrees {
		return false, files
	}

	// Positive — sort by detected number, then by original index for stable
	// placement of un-numbered files at the end.
	sorted := make([]MultiFileInfo, n)
	copy(sorted, files)
	sort.SliceStable(sorted, func(i, j int) bool {
		ni, nj := detections[indexOf(files, sorted[i].Path)].number,
			detections[indexOf(files, sorted[j].Path)].number
		switch {
		case ni > 0 && nj > 0:
			return ni < nj
		case ni > 0 && nj == 0:
			return true
		case ni == 0 && nj > 0:
			return false
		default:
			return false
		}
	})
	return true, sorted
}

// indexOf returns the index of a file with the given path in files,
// or -1 if not found. Linear scan — N is small (single folder).
func indexOf(files []MultiFileInfo, path string) int {
	for i, f := range files {
		if f.Path == path {
			return i
		}
	}
	return -1
}
