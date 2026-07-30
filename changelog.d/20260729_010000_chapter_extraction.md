<!-- file: changelog.d/20260729_010000_chapter_extraction.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8b0f5f8b-4d3c-4b7a-9f7d-2b6a1e0c5d97 -->
<!-- last-edited: 2026-07-29 -->

### Added

#### Chapter extraction and multi-file timeline primitives (`internal/audioutil`)

Added the chapter model and extraction/synthesis primitives that ABS Sync API Phase 4
(`docs/specs/2026-07-29-abs-sync-api-design.md` §7) will persist and serve. This codebase
had no chapter concept at all before this change — `ffprobe` was used for duration only.

`internal/audioutil/chapters.go` adds `ProbeChapters`, which shells out to
`ffprobe -show_chapters -print_format json` and parses each chapter's `start_time`/
`end_time` string fields into a `Chapter{ID, StartSec, EndSec, Title}` (deliberately not
the sibling `start`/`end` integer + `time_base` pair, to preserve full float precision). A
file with no embedded chapters returns `(nil, nil)` — not an error.

`internal/audioutil/timeline.go` adds the pure, I/O-free timeline math a multi-file book
needs: `CumulativeOffsets` (running start offset per track), `SynthesizeChapters` (one
synthetic chapter per track, title falling back to filename when the track has no title
tag — mirroring real ABS behavior for multi-file books with no embedded chapters), and
`ShiftChapters` (rebases a track's own embedded chapters onto the whole-book timeline).

Verified against real Audiobookshelf 2.36.0 ground truth captured in
`testdata/abs-fixtures/README.md` (items 3-4) and against the committed Odyssey
(Butler/LibriVox) audio fixtures: the single-file m4b's 6 embedded chapters extract with
`StartSec == 0` and the last `EndSec` matching the file's total duration
(~9975.428s); the 6-track mp3 split's per-file durations reproduce the exact real ABS
`startOffset` sequence (`0, 1386.057143, 2788.702041, 4309.211429, ...`) via
`CumulativeOffsets`, confirmed with a sub-microsecond epsilon test.

Persistence and scanner wiring (a `Chapter` DB type, `process_file.go` integration) are
deliberately out of scope for this change to avoid colliding with parallel work on those
shared files; this PR delivers only the extraction/synthesis primitives and their tests.
