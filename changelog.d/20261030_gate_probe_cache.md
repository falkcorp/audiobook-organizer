### Fixed

- A full-library per-file intro run no longer probes every Whisper worker's health once per batch of 8 files. The first gate of a dispatch reuses a clean health answer for up to 30 seconds. Any failure marked against a worker discards its cached answer immediately, workers that are unreachable, stuck, unable to start their decoder or missing a mount are never remembered, and the re-checks made after a wait or a cooldown still probe.
