<!-- file: todo.d/20260806_220000_per_file_intro_identity_signal.md -->
<!-- version: 1.1.0 -->
<!-- guid: 3a7c2e94-5b18-4d60-9f27-c8140b6e3d52 -->
<!-- last-edited: 2026-08-07 -->

- [ ] **Per-file intro transcription as the primary book-identity signal** — owner
  design 2026-08-06. Storage and the first-file sort fix are **DONE** (PRs #2168);
  the parser, the tiered backfill, and the wiring are open.

  **The idea.** An audiobook opens with a spoken *"&lt;Title&gt; by &lt;Author&gt;, read by
  &lt;Narrator&gt;"* announcement. That announcement marks a book **start**. A file
  without one is a continuation. That is direct identity evidence, where the
  current classifier only has runtime — a proxy.

  **Why it needed per-file storage.** Transcripts lived on `Book`, so only ONE
  file's opening was ever captured and "12 files that are one book" was
  indistinguishable from "12 files that are 12 books". Measured on prod, one
  folder's files read:

  ```
  file 1: "This is a reading of Overlord, Book 7. This part includes the prologue and Chapter 1."
  file 2: "This is a reading of Overlord Volume 7. This part includes Chapter 2."
  file 3: "Hello... This is Overlord Volume 7, Chapter 3."
  ```

  Per-file that sequence is proof of continuation; per-book it is invisible. It
  also explains the measured **45.8%** credit-parse rate across 1,476 review-queue
  members — the op sampled one arbitrary file per book.

  ### Remaining work

  - [x] **Three-outcome parser.** ✅ DONE 2026-08-07 — `ClassifyIntro`
        (`internal/transcribe/classify.go`) returns credits / chapter / prose /
        unknown with a typed reason, confidence, and chapter number. **Position
        is a weight, never a veto**: credits at ordinal >0 IS the shattered-book
        signal, so vetoing it would hide the very finding this was built to
        surface. Both confirmed prod false positives are covered — the *Girls
        with Rebel Souls* case is reclassified as **misfiled** rather than
        mis-parsed (`IsLikelyMisfiled`: the announcement was read correctly, the
        FILE is in the wrong folder), and prose-containing-"by" now fails
        plausibility gates (case-sensitive prose markers, so "Meet **Me** in
        Paradise" survives while "...and **he** wasn't amused" does not).
        The corpus surfaced a larger defect than either: **24.8% of stored
        titles carried a leaked credit verb** ("Awakened Essence 1 Written")
        because the split landed *inside* `written by` — the library's most
        common credit variant (24.1%), absent from the pattern list entirely.
        Backed by a 188-transcript production corpus
        (`internal/transcribe/testdata/intro_corpus.jsonl`), invariant tests, a
        distribution canary, and a fuzz target (165k execs clean).
        🔴 `reparseStoredIntros` now **only upgrades, never clears**: 1.4% of
        987 sampled books (~644 library-wide) hold a parse their *current*
        transcript cannot regenerate, because `applyOutcome` overwrites the
        transcript unconditionally but the parsed fields only on success.
  - [ ] **Tiered backfill.** Naive "every file" is ~284,000 files ≈ 12–14 days of
        GPU. Tiers: **0** single-file books migrate by copy (zero GPU, ~32,600
        books); **1** assembled multi-file books probe the first 3 files only;
        **1b** escalate to the full set if all 3 carry credits — which is what
        makes the cheap tier *safe*, since it cannot silently be wrong; **2**
        bookless/shattered/queue members get every file; **3** a lazy, indefinite
        full sweep so every file eventually has a transcript.
  - [ ] **Wire into the regroup classifier**, outranking runtime where both exist.
        Validate by diffing against the 356 holds already measured under the
        runtime rule.
  - [ ] **Wire into First Aid** as a tier-2 signal beside the duration probe, and
        let the verdict pick the fixer.

  ### Measured facts worth keeping

  - 72.7% of books are single-file; 11.3% have 21+ files and hold most of the
    317,054 rows. The signal is precisely targeted at the fraction that is
    actually ambiguous.
  - **195 of 204** "untranscribed" review-queue members have ZERO `book_file`
    rows — unlinked, not un-transcribed. **Relink before transcribing** or they
    need a second pass. [[first-aid-library-validate-repair]]'s probe already
    found 434 of 1,019 directory-shaped books confidently linkable.
  - The WAV clip cache is keyed by **file path**, so clips already extracted
    survive the per-file move and ffmpeg is skipped on re-run.
  - Book-level transcription is already **saturated** — a full `only_missing` run
    over 221 pages transcribed 0 books. There is no warm-up value left; the
    per-file pass is the entire remaining work.

  🔴 **Absent transcript means "cannot verify", never "continuation".** This
  codebase has now been bitten by absent-value-read-as-evidence four separate
  ways: `DurationSec == 0` read as "short" (disabled the series guard across 97.5%
  of the queue), a 404 body read as "zero files", `memPtr == nil` read as "nothing
  to do" (silently dropped writes for the process lifetime), and an empty
  `intro_transcription` read as "needs transcribing" when it meant "has no file".
