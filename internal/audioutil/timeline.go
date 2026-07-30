// file: internal/audioutil/timeline.go
// version: 1.0.0
// guid: 4a87c956-6abc-4362-a3ac-578108f0387d
// last-edited: 2026-07-29

package audioutil

// Chapter is a single navigable chapter entry, matching the shape a real
// Audiobookshelf 2.36.0 server reports: {id, start, end, title} with start/end
// in float seconds. See docs/specs/2026-07-29-abs-sync-api-design.md §1 and
// testdata/abs-fixtures/README.md items 3-4 for the ground truth this mirrors.
type Chapter struct {
	ID       int
	StartSec float64
	EndSec   float64
	Title    string
}

// TrackInfo is the minimal per-file input SynthesizeChapters needs to build
// one synthetic chapter per audio file for a multi-file book.
type TrackInfo struct {
	// Title is the track's embedded title tag (e.g. "The Odyssey: Book 01").
	// Real ABS prefers this over the filename when present.
	Title string
	// Filename is the fallback chapter title used when Title is empty.
	Filename string
	// DurationSec is the track's duration in seconds, unrounded.
	DurationSec float64
}

// CumulativeOffsets returns the running start offset of each duration in
// durations: offsets[0] == 0, offsets[i] == sum(durations[0:i]). This is the
// same arithmetic ABS uses to compute multi-file startOffset values (see
// testdata/abs-fixtures/README.md item 3: "0, 1386.057143, 2788.702041, ...").
// No rounding is applied at any step.
func CumulativeOffsets(durations []float64) []float64 {
	if len(durations) == 0 {
		return nil
	}
	offsets := make([]float64, len(durations))
	var running float64
	for i, d := range durations {
		offsets[i] = running
		running += d
	}
	return offsets
}

// SynthesizeChapters builds one chapter per track with cumulative start/end
// offsets, mirroring real ABS behavior for a multi-file book with no embedded
// chapters (testdata/abs-fixtures/README.md item 4): chapter IDs start at 0,
// and each chapter's title is the track's Title if non-empty, else its
// Filename.
func SynthesizeChapters(tracks []TrackInfo) []Chapter {
	if len(tracks) == 0 {
		return nil
	}
	chapters := make([]Chapter, len(tracks))
	var start float64
	for i, t := range tracks {
		end := start + t.DurationSec
		title := t.Title
		if title == "" {
			title = t.Filename
		}
		chapters[i] = Chapter{ID: i, StartSec: start, EndSec: end, Title: title}
		start = end
	}
	return chapters
}

// ShiftChapters returns a copy of chs with every StartSec/EndSec shifted by
// offsetSec, leaving ID and Title untouched and the input slice unmodified.
// Used when a multi-file book's individual files each carry their own
// embedded chapters that must be rebased onto the whole-book timeline (e.g.
// track 3 starts at cumulative offset X, so its embedded chapters must be
// shifted by X before being merged into the book-level chapter list).
func ShiftChapters(chs []Chapter, offsetSec float64) []Chapter {
	if len(chs) == 0 {
		return nil
	}
	shifted := make([]Chapter, len(chs))
	for i, c := range chs {
		shifted[i] = Chapter{
			ID:       c.ID,
			StartSec: c.StartSec + offsetSec,
			EndSec:   c.EndSec + offsetSec,
			Title:    c.Title,
		}
	}
	return shifted
}
