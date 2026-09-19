### Added

- The fingerprint package can now take acoustic fingerprints from the middle of a file instead of only its first two minutes, which for most Audible titles is the same publisher intro. A planner picks two-minute windows at 10%, 50% and 90% of files of ten minutes or more, one window at 50% for 150 s to 10 min, and one window covering the whole file for anything shorter. Each window records the exact tool versions and decode chain that produced it, and a comparison function scores two books' windows while allowing for a few seconds of shift, ignores the shared intro, and refuses to compare prints made with different tools. Nothing calls this yet; the storage and the backfill job come in later changes.

### Fixed

- Removed a broken internal path that fingerprinted later parts of a file whenever fpcalc was installed: it read fpcalc's number list as text and failed every time, and it ignored ffmpeg errors. Those segments are now cut through the new window pipeline. No current feature used the segments it produced.
- The check for whether a torrent's audio is already in the library now fingerprints only the start of its first file, which is all it compares. It used to also probe the file's length and try to fingerprint six more five-minute stretches, then discard them.
