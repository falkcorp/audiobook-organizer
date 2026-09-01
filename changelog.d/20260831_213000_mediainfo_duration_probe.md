### Changed

- **Duration probing now uses `mediainfo` first and falls back to `ffprobe`.** Every
  duration probe in the codebase goes through the single `audioutil.ProbeDurationSeconds`
  seam, so this switches the tool for all of them at once without changing what an answer
  means: ffprobe remains the source of truth and answers anything mediainfo cannot.

### Added

- `ABK_DISABLE_MEDIAINFO=1` forces the ffprobe-only path, with no rebuild. This changes
  the tool used on a hot path across the whole library, so reverting must not require a
  deploy.
- `audioutil.DurationProbeStats()` reports the mediainfo/ffprobe split. A silent fallback
  would be worse than a loud one: if mediainfo failed on every file we would run two
  subprocesses per file instead of one, be *slower* than before, and have no signal saying
  so. The first fallback also logs once with the concrete reason.
- `audioutil.DurationProbeAvailable()` — true when *either* prober is present.

### Fixed

- `TestProbeDurationSeconds_ExplicitFFprobePath` was silently defused by this change:
  mediainfo answers the real mp3 it generates, so the explicit ffprobe path it exists to
  verify was no longer being used. It now disables mediainfo so it tests its own name
  again.
