// file: internal/audioutil/timeline.go
// version: 1.0.1
// guid: 4a87c956-6abc-4362-a3ac-578108f0387d
// last-edited: 2026-10-12

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
