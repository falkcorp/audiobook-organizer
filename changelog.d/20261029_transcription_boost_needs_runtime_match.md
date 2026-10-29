### Changed

- The "Transcription match" score boost (up to x2.0 on the title, x1.6 on the
  author, x1.4 on the narrator, stacking) now applies only when the candidate's
  runtime is within 10% of the book's complete local runtime. When the book's
  total runtime is unknown (including a partly measured one), the candidate has
  no runtime, or the two differ by more than 10%, the boost is withheld (x1.00)
  and `transcription_boosted` is false, so a withheld match no longer reads as a
  strong signal. The match search path, the pick-best path and the calibration
  sweeps share one gate. A transcription-matched Audible answer
  with no runtime gets the existing one-per-book Audnexus runtime lookup so the
  comparison can run. That one lookup is made even when the book has no runtime
  information yet (nothing measured, no sizes): the candidate's runtime is what
  lets a measured or later-measured book be compared at all.
- The score breakdown shows a "Transcription match" step at x1.00 with the
  reason ("the book's total runtime is unknown (only 1 of 2 files measured; file
  sizes 0 KB)", "the runtimes differ by 20%", ...) and the runtime step beside it.
- A candidate the audio intro matched but whose runtime did not confirm it now
  carries `transcription_runtime_check` with a verdict: `fragment`,
  `runtime_mismatch`, or `runtime_unknown`. A partly measured book is called a
  fragment or a mismatch only on a bound its files prove (the most the file
  sizes could hold, or the measured part already running past the candidate);
  a bitrate guess never decides, and anything unproven stays `runtime_unknown`.
  The review row shows a warning chip, and the review list has a "Transcription
  needs runtime check" switch (`transcription_runtime_flagged`) that checks the
  candidate shown for each book. Both are computed LIVE from the book's files
  (the same verdict the apply gate gives), not read from the stamp the search
  left, so a row cached before this change shows its warning too and a row whose
  book has been measured since loses a stale one. "Transcription matched" does
  not count a row the live gate refuses. A flagged candidate is kept by the cover
  filter so review can see it.
- The apply gate checks the runtime LIVE, against the book's files as they are at
  apply time, wherever a transcription agreement would lower the score floor or
  count as confirmation: a candidate cached before this change (no stamp) or on a
  book whose files changed since is refused with `transcription_runtime_unverified`
  all the same, by the bulk apply, the upgrade path and the transcription
  auto-match. An owner review of the shown row can still apply it, but the select-all owner
  marker (a review of rows nobody looked at) cannot lift this reason. The reason does
  not count as "no usable candidate", so it never triggers the fallback-source
  fetch (a different source could not fix a runtime), and it ranks as refused when
  the merge ranker picks a candidate.
- The `auto-match-transcribed` op refuses such a book in both the search and the
  apply, with its own count in the summary ("runtime unverified"), and a dry run
  reports the same books. Auto-fetch (`FetchMetadataForBook`) no longer applies a
  candidate the audio intro alone identified when its runtime does not fit; it
  stays cached for review. The book's own title agreeing with the candidate
  excuses only an unknown runtime, never a proven fragment or runtime mismatch.
- The refusal detail now carries the verdict and why (for example
  "fragment: the files look like a fragment: ..."). A failure to read a book's
  files while loading its runtime is logged at warn level and treated as an
  unknown runtime, which refuses.
