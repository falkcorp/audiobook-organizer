### Added

- A test now checks the fingerprint reader against the fingerprint tool installed on the machine, using a generated test clip. Earlier tests used saved samples from one tool version. The new test runs the same steps the app uses and confirms the result matches the tool's own unpacked output frame for frame. It is skipped on machines without the tool.
- A written plan for re-checking the fingerprint match thresholds, which were set while fingerprints were being misread (`docs/audio-fingerprint/threshold-recalibration-plan.md`). It lists every affected threshold and how to measure each one on real data. It also records that stale entries from the old reading are still in the fingerprint search index and need clearing. No threshold was changed.
