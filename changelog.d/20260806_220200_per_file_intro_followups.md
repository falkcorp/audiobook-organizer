<!-- file: changelog.d/20260806_220200_per_file_intro_followups.md -->
<!-- version: 1.0.0 -->
<!-- guid: c40a7e15-8362-4b9d-a17e-539f2c68b0da -->
<!-- last-edited: 2026-08-06 -->

### Added

- **Two TODO fragments recording the per-file intro-transcription initiative** —
  the follow-on work after the storage move and disc-aware sort fix landed.

  The first captures the design and every measurement behind it: why per-book
  storage made "12 files that are one book" indistinguishable from "12 files that
  are 12 books", the confirmed parser false positives, the four-tier backfill that
  turns ~284,000 files of GPU work into roughly 43,000 decision-critical ones, and
  the ordering constraint that relinking must precede transcription (195 of 204
  "untranscribed" review-queue members turned out to have no file at all).

  The second records U1 (`unimatrixone`) as a prepared but unbuilt second Whisper
  worker — 48 cores, 251 GB, **CPU-only**, Tdarr idle with an empty queue, uv and
  pip installed. It states plainly that CPU int8 is not a second GPU and must be
  benchmarked rather than assumed, and points it at the deadline-free tier where
  "slower" costs nothing.
