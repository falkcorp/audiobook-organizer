### Testing

- [ ] **Five goroutine bodies converted to `wg.Go` in #2992 have ZERO test
      coverage, and two of them are maintenance job entry points.** Measured at
      block level from the raw coverage profile (not enclosing-function
      percentage), execution count 0:

  | site | function |
  | --- | --- |
  | `internal/plugins/acoustid/fingerprint_rescan.go:175` | `runFingerprintRescan` |
  | `internal/plugins/maintenance/extract_wav_clips.go:109` | `runExtractWAVClips` |
  | `internal/maintenance/jobs/repair_missing_files.go:176` | `(*repairMissingFilesJob).Run` |
  | `internal/maintenance/jobs/scan_composer_tags.go:160` | `(*scanComposerTagsJob).Run` |
  | `internal/metafetch/openlibrary.go:131` | `(*OpenLibraryService).Import` |

  There is no middle group — every block in a non-zero-coverage function did
  execute. The gap predates the conversion.

  **The two `Run` methods are the ones to care about.** They are maintenance job
  *entry points* that are entirely untested while their own helpers are partly
  covered (`rmfr_buildFilenameIndex` 92.9%, `rmfr_repairOne` 48.0%). Testing the
  helpers and not the entry point means nothing exercises the wiring that decides
  whether those helpers are called at all — the shape that let
  `FilterBooksNeedingOrganization` return a confident success while organizing
  zero books.

- [ ] **The `wg.Go` parameter-capture sites rest on review, not on tests.**
      Mutation-testing #2992 ran three mutants; two were killed, and the survivor
      is the one that attacks the only semantics the PR actually changed: moving
      the hoisted locals *out* of the loop so every goroutine shares them (the
      pre-Go-1.22 aliasing bug). **The suite stayed green and `-race` did not
      fire.**

  Reviewed by hand and the conversions are correct — at
  `extract_wav_clips.go` the captured `bookID` is a per-iteration range variable
  and `src`/`cacheKey`/`bookFileID`/`dest` are all declared with `:=` inside the
  loop body, so each iteration has its own; `intro_transcribe.go` and
  `dispatcher.go` hoist explicitly.

  **But note what that correctness depends on: `go 1.26` in `go.mod`.**
  Per-iteration loop variables are Go 1.22+ semantics. Under an older directive
  the same source is a data race. A test that pins this would fail loudly if the
  directive were ever lowered; today nothing would notice.

  Cheapest fix: one table test per shape that starts N goroutines over a loop and
  asserts every distinct input was observed exactly once. That kills the aliasing
  mutant and documents the dependency.
