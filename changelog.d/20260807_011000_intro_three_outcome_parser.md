<!-- file: changelog.d/20260807_011000_intro_three_outcome_parser.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2a6d94f8-5b31-4e07-9c82-6f10b3d7e548 -->
<!-- last-edited: 2026-08-07 -->

### Added

- **Three-outcome intro classifier** (`internal/transcribe/classify.go`).
  `ClassifyIntro` replaces the old credits-or-nothing parse with an explicit
  verdict — `credits` (a book-opening announcement, direct identity evidence),
  `chapter` (a structural marker, so the file is a continuation), `prose` (no
  announcement), or `unknown` (nothing interpretable). It reports a typed
  `IntroReason`, a confidence, and any announced chapter number, and takes an
  `IntroPosition` so a file's place in its book can shade the verdict.
- **Misfiled-book detection.** `IsLikelyMisfiled` separates "the parser was
  wrong" from "the parser was right and the FILE is in the wrong folder" — the
  case where a clip inside a *Girls with Rebel Souls* folder correctly announces
  *Meet Me in Paradise by Libby Hubscher*. The two need opposite fixes and were
  previously indistinguishable.
- **Golden corpus regression suite.** 188 real production transcripts
  (`internal/transcribe/testdata/intro_corpus.jsonl`), stratified by shape, plus
  invariant tests, a distribution canary, and a fuzz target.

### Fixed

- **Credit verbs no longer leak into parsed titles.** The parser split the title
  on the first standalone `by`, which lands *inside* `written by` and welded the
  verb onto the title (`"Awakened Essence 1 Written"`). Measured across 987
  sampled production books, **24.8% of stored titles carried a leaked verb**, and
  `written by` is the single most common credit variant in the library (24.1%) —
  it was not in the pattern list at all. The split now prefers the authorship-verb
  form, and `translated by` / `edited by` / `adapted by` are recognised.
- **Narrative prose is no longer parsed as credits.** Text merely containing the
  word `by` produced a title/author pair — one production clip yielded a ~900
  character "title" and an author of `"mirrored sunglasses. Fury didn't have
  time"`. Announcements must now clear plausibility gates (length, narration
  markers) that prose cannot.
- **Publisher jingles are `unknown`, not `prose`.** A clip that captured only
  `"This is Audible."` or a publisher tagline carries no information; reporting it
  as prose let downstream read it as weak continuation evidence.

### Changed

- **`reparseStoredIntros` only ever upgrades a stored parse — it never clears
  one.** The parsed fields are not always reproducible from the stored
  transcript: `applyOutcome` overwrites `IntroTranscription` unconditionally but
  writes parsed fields only when a title was extracted, so a later, worse
  transcription replaces good text while the good parse survives beside it.
  **1.4% of 987 sampled books (~644 library-wide)** are in that state — e.g. a
  book whose transcript is now `"This is Audible."` but which still holds
  `"Wind and Truth" / "Brandon Sanderson"`. A non-credits verdict now means "this
  text is not an announcement", never "the stored value is wrong"; bad values are
  neutralised by consumers gating on the classification rather than by erasing
  data. Regression-tested in `intro_reparse_guard_test.go`.
- **`ParseAudiobookIntro` now delegates to the classifier** and returns fields
  only for a `credits` verdict. Callers needing the three-way distinction must
  call `ClassifyIntro` — absent fields mean "not an announcement", which is not
  the same as "not a book start".
- **`[SILENCE]` has a single owner.** `transcribe.SilenceSentinel` is now the one
  declaration; `internal/plugins/maintenance` aliases it. Two packages disagreeing
  about the literal would have turned "known silent" into "unparsed prose".
