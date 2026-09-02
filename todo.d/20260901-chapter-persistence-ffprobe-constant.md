## `TestPersistChaptersForBook_MultiFileMP3s_SynthesizesFromTrackTags` asserts on the ffprobe version, not the code

`internal/scanner/chapter_persistence_test.go:143-149` pins
`wantSumOfTracks = 9975.431111` to ±0.001 s. ffprobe 9.0.1 reports the six Odyssey
MP3 tracks summing to 9975.827 s, so the test fails identically on `main` (Go 1.26)
and on the Go 1.27 branch — it is not a toolchain regression. MP3 duration on these
fixtures is an estimate that drifts across ffmpeg releases, so the constant was written
against one specific ffprobe and the test is silently environment-pinned; if CI passes,
CI's ffprobe is the version the constant matches.

The property the test actually cares about is "the last chapter ends at the sum of
track durations, not at the container duration" (the container assertion at :151 already
uses a 0.01 s band). Derive the expected sum at test time by running the same ffprobe
over the six files and assert against that, rather than a literal. Widening the
tolerance would also pass but keeps the test pinned to a number nobody can re-derive.

Surfaced 2026-09-01 while verifying the Go 1.27 toolchain bump.

- [ ] Replace the literal with an ffprobe-derived expected sum; confirm which ffprobe CI installs
