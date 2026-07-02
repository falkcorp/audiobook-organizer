// file: internal/dedup/dataset/builder.go
// version: 1.2.1
// guid: 4a91c7e0-6d83-4b25-9f10-2c5a8e7d4b31
// last-edited: 2026-07-01

// Package dataset builds labeled dedup examples and runs deterministic catchers
// over them. Pure: a store interface in, a database.LabeledExample out, no
// side effects. This is the audit CLI's per-pair logic promoted to a reusable,
// unit-tested package (spec C1/C2).
package dataset

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math/bits"
	"path/filepath"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
)

// sigMatchThreshold is the BookSignatureSimilarity score at/above which two
// whole-book signatures are treated as a content match (spec: 0.95).
const sigMatchThreshold = 0.95

// sigContainmentThreshold is the similarity required, over a resampled
// candidate window (see signatureContainment), to declare offset/subsequence
// containment. Set slightly below sigMatchThreshold to tolerate the extra
// quantization noise introduced by nearest-neighbor resampling.
const sigContainmentThreshold = 0.90

// containmentFractions are the candidate window-length fractions of the full
// fingerprint.BookSignatureFixedLength array tried when searching for offset/
// subsequence containment (e.g. one book being an excerpt, or the first half,
// of another). Both BookSigV1 signatures are always down-sampled to the same
// fixed length, so containment cannot be found by a literal sub-slice
// comparison; instead a candidate window of the longer book's "timeline" is
// resampled back up to the fixed length and compared to the shorter book.
var containmentFractions = []int{2, 3, 4, 6, 8}

// containmentOffsetSteps bounds how many candidate start offsets are tried per
// window length, keeping the search O(len(containmentFractions) *
// containmentOffsetSteps) resamples-and-compares (a few dozen) instead of
// scanning every one of the up to 4096 possible offsets.
const containmentOffsetSteps = 8

// BuilderStore is the narrow store surface BuildExample needs.
type BuilderStore interface {
	GetBook(id string) (*database.Book, error)
	GetBookFiles(id string) ([]database.BookFile, error)
}

// BuildExample loads both books and computes every feature for the candidate pair.
// It is pure: it reads from the store and returns a LabeledExample with no side
// effects. Label fields are left empty — callers should run Classify to populate
// them.
func BuildExample(store BuilderStore, cand database.DedupCandidate) (database.LabeledExample, error) {
	ex := database.LabeledExample{
		CandidateID: cand.ID,
		EntityAID:   cand.EntityAID,
		EntityBID:   cand.EntityBID,
		Layer:       cand.Layer,
		Band:        cand.Band,
		Similarity:  cand.Similarity,
	}

	// Populate Score and ScoreBreakdown snapshot from the candidate's unified
	// score when present. On current production data these are nil (Experiment 0
	// found 100% empty) — this is forward-correctness for rows produced by the
	// T015/T016 unified pipeline. A nil ScoreBreakdown leaves Score at 0 and
	// ex.ScoreBreakdown nil, which is the safe/zero value.
	if cand.ScoreBreakdown != nil {
		ex.Score = cand.ScoreBreakdown.Score
		if raw, err := json.Marshal(cand.ScoreBreakdown); err == nil {
			ex.ScoreBreakdown = raw
		}
		// On marshal error: Score is still set; ScoreBreakdown is left nil.
		// BuildExample never fails due to a snapshot marshal error.
	}

	a, aFiles, err := loadSide(store, cand.EntityAID)
	if err != nil {
		return ex, err
	}
	b, bFiles, err := loadSide(store, cand.EntityBID)
	if err != nil {
		return ex, err
	}
	ex.A = buildFeatures(a, aFiles)
	ex.B = buildFeatures(b, bFiles)

	ex.DurationRatio = durationRatio(ex.A.TotalDurationSec, ex.B.TotalDurationSec)
	ex.FolderRelation = folderRelation(ex.A.PrimaryPath, ex.B.PrimaryPath)
	ex.SharesRecordingID = sharesAny(ex.A.RecordingIDs, ex.B.RecordingIDs)
	ex.SignatureRelation = signatureRelation(a, b)
	return ex, nil
}

func loadSide(store BuilderStore, id string) (*database.Book, []database.BookFile, error) {
	bk, err := store.GetBook(id)
	if err != nil {
		return nil, nil, err
	}
	files, err := store.GetBookFiles(id)
	if err != nil {
		return bk, nil, err
	}
	return bk, files, nil
}

// buildFeatures computes the per-book feature snapshot from a book and its files.
// Note: BookFeatures.Author is left empty — BuilderStore provides only Book and
// BookFile records; author name resolution would require a separate store method.
func buildFeatures(bk *database.Book, files []database.BookFile) database.BookFeatures {
	f := database.BookFeatures{
		FileCount:  len(files),
		FilesExist: len(files) > 0,
	}
	if bk != nil {
		f.Title = bk.Title
		f.WholeBookSigPresent = bk.BookSigV1 != nil && *bk.BookSigV1 != ""
		if bk.CoverURL != nil && *bk.CoverURL != "" {
			f.HasCover = true
		}
		// Book-level size as a baseline; the per-file max below can exceed it.
		if bk.FileSize != nil && *bk.FileSize > f.FileSizeBytes {
			f.FileSizeBytes = *bk.FileSize
		}
	}
	var total float64
	for i := range files {
		fl := &files[i]
		if f.PrimaryPath == "" && fl.FilePath != "" {
			f.PrimaryPath = fl.FilePath
		}
		// Largest file size across the book's files — the signal for "has real
		// audio" vs a stub. A genuine unscanned copy keeps a large size here.
		if fl.FileSize > f.FileSizeBytes {
			f.FileSizeBytes = fl.FileSize
		}
		// Prefer fpcalc-measured duration; fall back to container duration (int seconds).
		if fl.AcoustIDFingerprintDurationSec > 0 {
			total += fl.AcoustIDFingerprintDurationSec
		} else if fl.Duration > 0 {
			total += float64(fl.Duration)
		}
		if fl.AcoustIDOnlineRecordingID != "" {
			f.RecordingIDs = append(f.RecordingIDs, fl.AcoustIDOnlineRecordingID)
		}
		if fl.ITunesPersistentID != "" {
			f.ITunesPIDPresent = true
		}
	}
	f.TotalDurationSec = total
	return f
}

// durationRatio returns min/max of the two durations, or 0 if either is ≤0.
func durationRatio(a, b float64) float64 {
	if a <= 0 || b <= 0 {
		return 0
	}
	lo, hi := a, b
	if lo > hi {
		lo, hi = hi, lo
	}
	return lo / hi
}

// folderRelation classifies how two primary file paths sit relative to each other.
// Returns one of: unrelated, same_dir, a_ancestor_of_b, b_ancestor_of_a, sibling_parts.
func folderRelation(a, b string) string {
	if a == "" || b == "" {
		return "unrelated"
	}
	da, db := filepath.Dir(a), filepath.Dir(b)
	if da == db {
		return "same_dir"
	}
	if isAncestor(da, db) {
		return "a_ancestor_of_b"
	}
	if isAncestor(db, da) {
		return "b_ancestor_of_a"
	}
	// Two directories that share a common grandparent AND look like numbered
	// parts of the same work (e.g. "Part 1" / "Part 2") are sibling_parts.
	// A bare shared grandparent is not enough — that would also match
	// unrelated sibling directories like /lib/AuthorOne vs /lib/AuthorTwo or
	// /lib/Series vs /lib/Series-Extra, which must stay "unrelated".
	if filepath.Dir(da) == filepath.Dir(db) && partStem(filepath.Base(da)) == partStem(filepath.Base(db)) {
		return "sibling_parts"
	}
	return "unrelated"
}

// isAncestor reports whether anc is a strict path ancestor of desc.
func isAncestor(anc, desc string) bool {
	anc = strings.TrimRight(anc, "/")
	return anc != "" && strings.HasPrefix(desc, anc+"/")
}

// partStem strips a trailing part number (and its separator) from a directory
// name, e.g. "Part 1" -> "Part", "Part 2" -> "Part". Names without a trailing
// digit are returned unchanged, so unrelated names never collide.
func partStem(name string) string {
	stripped := strings.TrimRight(name, "0123456789")
	if stripped == name {
		return name
	}
	return strings.TrimRight(stripped, " -_")
}

// sharesAny reports whether the two recording-ID slices share any element.
func sharesAny(a, b []string) bool {
	set := make(map[string]struct{}, len(a))
	for _, x := range a {
		set[x] = struct{}{}
	}
	for _, y := range b {
		if _, ok := set[y]; ok {
			return true
		}
	}
	return false
}

// signatureRelation reports the whole-book-signature relationship between two books.
// Uses fingerprint.BookSignatureSimilarity with a 0.95 threshold for "match".
// Returns one of:
//   - match: whole-signature similarity >= sigMatchThreshold.
//   - a_contains_b: b's signature closely matches a resampled contiguous
//     window of a's signature (b looks like an excerpt/partial re-record of a).
//   - b_contains_a: the reverse of a_contains_b.
//   - disjoint: neither a match nor a containment relationship was found.
//   - unknown: either signature is absent or the comparator errors (e.g.
//     corrupt/short base64).
func signatureRelation(a, b *database.Book) string {
	if a == nil || b == nil {
		return "unknown"
	}
	if a.BookSigV1 == nil || *a.BookSigV1 == "" || b.BookSigV1 == nil || *b.BookSigV1 == "" {
		return "unknown"
	}
	sim, err := fingerprint.BookSignatureSimilarity(*a.BookSigV1, *b.BookSigV1)
	if err != nil {
		return "unknown"
	}
	if sim >= sigMatchThreshold {
		return "match"
	}
	aWords, errA := decodeBookSigWords(*a.BookSigV1)
	bWords, errB := decodeBookSigWords(*b.BookSigV1)
	if errA == nil && errB == nil {
		if rel, ok := signatureContainment(aWords, bWords); ok {
			return rel
		}
	}
	return "disjoint"
}

// decodeBookSigWords decodes a base64-encoded BookSigV1 string into its
// little-endian []uint32 words. Mirrors the (unexported) decoding used by
// fingerprint.BookSignatureSimilarity; duplicated here because that package
// exposes no public decode helper and containment search needs the raw words,
// not just a similarity score.
func decodeBookSigWords(encoded string) ([]uint32, error) {
	b, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	if len(b)%4 != 0 {
		return nil, errSigNotWordAligned
	}
	words := make([]uint32, len(b)/4)
	for i := range words {
		words[i] = binary.LittleEndian.Uint32(b[i*4:])
	}
	return words, nil
}

// errSigNotWordAligned is returned by decodeBookSigWords when the decoded
// byte length is not a multiple of 4.
var errSigNotWordAligned = errors.New("decoded signature length is not word-aligned")

// signatureContainment searches for evidence that one of a/b's signature is a
// contiguous, possibly-resampled sub-sequence of the other's. Both signatures
// are always fingerprint.BookSignatureFixedLength words long regardless of the
// underlying audio duration, so "b is the first half of a" cannot be found by
// literal sub-slicing; instead candidate windows of the longer book's
// down-sampled timeline are resampled back up to the fixed length and
// compared to the other signature. Returns ("a_contains_b", true),
// ("b_contains_a", true), or ("", false) if no window search matched.
func signatureContainment(a, b []uint32) (string, bool) {
	if len(a) == 0 || len(a) != len(b) {
		return "", false
	}
	if rel, ok := containmentDirection(a, b, "a_contains_b"); ok {
		return rel, true
	}
	if rel, ok := containmentDirection(b, a, "b_contains_a"); ok {
		return rel, true
	}
	return "", false
}

// containmentDirection checks whether some resampled window of long matches
// short closely enough (>= sigContainmentThreshold) to report relation.
func containmentDirection(long, short []uint32, relation string) (string, bool) {
	n := len(long)
	for _, frac := range containmentFractions {
		winLen := n / frac
		if winLen < 8 {
			continue // too short a window to be a meaningful excerpt
		}
		maxStart := n - winLen
		if maxStart <= 0 {
			continue
		}
		for _, start := range containmentGridStarts(maxStart) {
			window := resampleNearest(long, start, winLen, len(short))
			if hammingSimilarity(window, short) >= sigContainmentThreshold {
				return relation, true
			}
		}
	}
	return "", false
}

// containmentGridStarts returns a bounded grid of candidate start offsets in
// [0, maxStart], spaced so at most containmentOffsetSteps+1 offsets are
// tried per window length.
func containmentGridStarts(maxStart int) []int {
	if maxStart <= 0 {
		return []int{0}
	}
	step := maxStart / containmentOffsetSteps
	if step == 0 {
		step = 1
	}
	starts := make([]int, 0, containmentOffsetSteps+1)
	for start := 0; start <= maxStart; start += step {
		starts = append(starts, start)
	}
	return starts
}

// resampleNearest nearest-neighbor resamples src[start:start+length] up (or
// down) to targetLen words, so a sub-window of one signature can be compared
// directly against another signature's full fixed-length representation.
func resampleNearest(src []uint32, start, length, targetLen int) []uint32 {
	out := make([]uint32, targetLen)
	for i := 0; i < targetLen; i++ {
		idx := start + i*length/targetLen
		if idx >= start+length {
			idx = start + length - 1
		}
		out[i] = src[idx]
	}
	return out
}

// hammingSimilarity scores two equal-length uint32 slices the same way
// fingerprint.BookSignatureSimilarity scores two full signatures: 1.0 minus
// the fraction of differing bits.
func hammingSimilarity(a, b []uint32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var diff int
	for i := range a {
		diff += bits.OnesCount32(a[i] ^ b[i])
	}
	return 1.0 - float64(diff)/float64(len(a)*32)
}
