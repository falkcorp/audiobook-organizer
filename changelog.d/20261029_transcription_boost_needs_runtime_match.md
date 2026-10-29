### Changed

- The "Transcription match" score boost (up to x2.0 on the title, x1.6 on the
  author, x1.4 on the narrator, stacking) now applies only when the candidate's
  runtime is within 10% of the book's complete local runtime. When the book's
  total runtime is unknown (including a partly measured one), the candidate has
  no runtime, or the two differ by more than 10%, the boost is withheld (x1.00)
  and `transcription_boosted` is false, so a withheld match no longer reads as a
  strong signal. The match search path, the pick-best path and the calibration
  sweeps share one gate. A transcription-matched Audible answer with no runtime
  gets the existing one-per-book Audnexus runtime lookup so the comparison can
  run, even when the book's own runtime is unknown.
- The score breakdown shows a "Transcription match" step at x1.00 with the
  reason ("the book's total runtime is unknown (only 1 of 2 files measured; file
  sizes 0 KB)", "the runtimes differ by 20%", ...) and the runtime step beside it.
- A candidate the audio intro matched but whose runtime did not confirm it now
  carries `transcription_runtime_check` with a verdict: `fragment` (the local
  runtime is under 50% of the candidate's), `runtime_mismatch`, or
  `runtime_unknown`. The review row shows it as a warning chip, the review list
  has a "Transcription needs runtime check" switch (`transcription_runtime_flagged`),
  and the apply gate refuses such a candidate with `transcription_runtime_unverified`
  (an owner review of the shown row can still apply it).
