<!-- file: docs/continuation/2026-08-06-per-file-intro-continuation.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9b52e738-1c40-4af6-8d29-63105ea7f8b1 -->
<!-- last-edited: 2026-08-06 -->

# Continuation prompt — per-file intro transcription (tasks #21 → #24)

Paste the block under **PROMPT** into a fresh session. Everything above it is
context for a human deciding whether to.

---

## Where things stand

**Done and merged 2026-08-06:**

| PR | What |
|---|---|
| #2168 | Per-file intro transcription **storage** + disc-aware first-file sort |
| #2163 | Review-queue recommendations + durable human override |
| #2162 | First Aid tier-2 duration probe |
| #2161 | `dedupe-book-file-rows` per-row cost fix |
| #2166 | memdb warmup write-loss fix |
| #2159 / #2160 | All 5 Dependabot advisories |
| #2165 | Frontend dependency upgrade report |
| #2167 | ESLint 10 · TypeScript 6 · plugin-react 5 |

**Open:** #2169 (these TODO fragments).

**Blocked, documented, do not retry:** TypeScript 7 (`typescript-eslint` peers
`<6.1.0`; npm refuses; no stable compiler API until 7.1) and Vite 8 (crashed every
page here in June 2026 via a rolldown CJS/ESM interop bug; the fixing version was
never confirmed). Full reasoning in
`docs/2026-08-06-frontend-dependency-upgrade-report.md`.

## The idea being built

An audiobook opens with a spoken *"&lt;Title&gt; by &lt;Author&gt;, read by &lt;Narrator&gt;"*
announcement. That marks a book **start**; a file without one is a continuation.
It is **direct identity evidence**, where the shipped classifier has only runtime —
a proxy.

Storage is now per-`BookFile`, so the sequence across a book's files is finally
visible. This is what a real multi-file book looks like:

```
file 1: "This is a reading of Overlord, Book 7. This part includes the prologue and Chapter 1."
file 2: "This is a reading of Overlord Volume 7. This part includes Chapter 2."
file 3: "Hello... This is Overlord Volume 7, Chapter 3."
```

Per-file, that is proof of one book. Per-book it was invisible — which is also why
the credit-parse rate is only **45.8%** across 1,476 review-queue members: the op
sampled one arbitrary file per book.

## Measurements that should drive decisions (do not re-derive)

- **Library shape** (600-book random sample; 7.0 files/book vs library-wide 7.1, so
  representative): 72.7% single-file · 6.5% 2–5 · 6.8% 6–20 · **11.3% 21+** · 2.7%
  zero files. **89.7% of all 317,054 files sit in multi-file books.**
- **Review queue:** 356 pending holds, 1,476 member books. Under the shipped
  runtime rule: 137 ambiguous→separate, 24 ambiguous→combine, 121
  multidisc→combine, **3 multidisc→separate**, 71 insufficient-evidence.
- **Transcript coverage on queue members:** 45.8% credits parsed · 40.4%
  transcribed but no credits grammar · 13.8% no transcript.
- 🔴 **195 of those 204 "no transcript" books have ZERO `book_file` rows.** They are
  **unlinked**, not un-transcribed. There is no file to clip. **Relink first.**
- **Book-level transcription is saturated** — a full `only_missing` run over 221
  pages transcribed **0** books. No warm-up value remains.
- **The WAV clip cache is keyed by file path**, so clips already extracted survive
  the per-file move and ffmpeg is skipped on re-run.

## The rule that keeps being violated

**Absent evidence means "cannot verify", never a specific cause.** Four distinct
instances in one session:

| Absent value | Was read as | Cost |
|---|---|---|
| `DurationSec == 0` | "short" | series guard inert across 97.5% of the queue |
| a 404 response body | "zero files" | a confident, exactly-inverted measurement |
| `memPtr == nil` | "nothing to do" | writes silently dropped for the process lifetime |
| empty `intro_transcription` | "needs transcribing" | actually meant "has no file" |

---

## PROMPT

> Continue the per-file audiobook identity signal. Storage and the disc-aware
> first-file sort are merged (#2168). Read
> `docs/continuation/2026-08-06-per-file-intro-continuation.md` and the two
> `todo.d/20260806_2200*` fragments first — they carry every measurement, so do not
> re-derive them.
>
> Work these in order, one PR each, worktree per task, verifying with real output
> before claiming anything passes:
>
> **#21 — three-outcome parser.** `ParseAudiobookIntro`
> (`internal/transcribe/parse.go:70`) splits on the first standalone `"by"` and
> returns credits-or-nothing. Confirmed production false positives: a book in a
> *Girls with Rebel Souls* folder parsed as `"Meet Me in Paradise / Libby
> Hubscher"`, and prose *"...he wasn't mildly amused by Memphis fortunes"* parsed as
> TITLE/AUTHOR. Make it distinguish **book-opening credits** / **chapter
> announcement** (*"This part includes Chapter 2"*, *"Welcome to X Audio Books"*) /
> **prose**. Use **track position** as the new discriminator — a genuine opening is
> at track 1, prose-containing-"by" occurs anywhere. Absent transcript must yield
> "cannot verify", never "continuation".
>
> **#22 — tiered backfill.** Tier 0: single-file books migrate by copy, zero GPU
> (~32,600 books) — but verify against `intro_transcribe.go`'s "second audio file"
> retry, which breaks the copy assumption for some books. Tier 1: assembled
> multi-file books, probe the first 3 files only. Tier 1b: **escalate to the full
> set if all 3 carry credits** — that is what makes the cheap tier safe rather than
> merely cheap. Tier 2: bookless/shattered/queue members get every file. Tier 3: a
> lazy, interruptible, checkpointed full sweep so every file eventually has a
> transcript, yielding to every tier above it. Keep it a bounded worker pool per
> CLAUDE.md; the item count goes ~7× when iterating files instead of books, so
> re-check page sizing and the 5-minute `ProgressTimeout`.
>
> **#23 — wire into the classifier.** The credits signal should outrank runtime
> where both exist: every member has credits → `separate`; file 1 credits + rest
> continuations → `combine`; disagreement → review; no transcript → fall back to
> the runtime rule. Extend `RecommendationEvidence` with the transcript counts.
> **Validate by diffing against the 356 holds already measured under the runtime
> rule** and inspecting every disagreement by hand before shipping.
>
> **#24 — wire into First Aid.** Credits belong in **tier 2**, beside the duration
> probe — two independent signals whose agreement is what makes a verdict
> trustworthy. Let the verdict pick the fixer, and when a transcript is missing,
> **enqueue** the transcription op rather than parking (TODO.md notes that
> `waiting_deps` parking waits but never enqueues the producer).
>
> Standing constraints: worktree per task, never edit the primary checkout; PR each
> one and give me the **full GitHub link on `main` after merge** — I am usually not
> on the Mac. `review_apply_enabled` stays OFF; prod applies need an explicit gate.
> This is a **public repo** — never commit internal IPs or tokens; the pre-commit
> hook will reject them.

## Also outstanding (separate from this chain)

- **#4** multidisc canary — snapshot captured (132 holds, 4,146 members, zero
  unreadable); needs #23 plus an explicit human gate to flip `review_apply_enabled`.
- **#6 follow-through** — apply the 434 confidently-linkable directory-shaped books.
- **Package upgrades** — zustand, jsdom, Testing Library are ~1 day combined and
  independent. React 19 → react-router 8 next. MUI 5→6→7→9 is ~1 week and ~75% of
  the total. **Fix the e2e suite first** (`unknown parameter "_page"`, broken on
  `main`, gates nothing) — it is the exact gap that let Vite 8 merge green in June.
- **#7, #8, #10–#18** — see `TODO.md`.
