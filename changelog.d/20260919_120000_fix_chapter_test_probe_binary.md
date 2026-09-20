### Fixed

- **A chapter-synthesis test failed on any machine with `mediainfo` installed.**
  `TestPersistChaptersForBook_MultiFileMP3s_SynthesizesFromTrackTags` pinned the
  expected end of the last chapter to the literal `9975.431111` — the sum of the
  six odyssey mp3 fixtures' durations *as reported by ffprobe*. But the code under
  test calls `audioutil.ProbeDurationSeconds`, which tries `mediainfo` first and
  only falls back to ffprobe. The two tools disagree per file (52 ms on four of the
  tracks, 94 ms on the other two, MediaInfoLib v26.05), so on a host with mediainfo
  the same six files sum to `9975.827` and the test failed by 0.396 s. No CI
  workflow installs ffmpeg or mediainfo, so the test skipped on every CI run and the
  divergence only ever appeared locally.

  The expected value is now derived by summing `ProbeDurationSeconds` over the same
  fixtures in the same order the production code accumulates them, and the tolerance
  **tightened** from `0.001` to `1e-6` — both sides now perform identical float64
  additions, so the window covers accumulation and nothing else. The assertion that
  the result must *not* land on the m4b container duration is unchanged and still
  holds under either prober. No production behaviour changed.

  The `DURATION-AUTHORITY` note on `synthesizeMultiFileChapters` said the fixture's
  legitimate durations "disagree by ~52ms", which future finished-detection work was
  told to size its tolerance against. That is now corrected: sum-of-tracks is not a
  single number, and the probe-binary spread alone is ~8x wider on this fixture.
