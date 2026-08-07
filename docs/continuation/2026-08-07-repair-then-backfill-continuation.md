<!-- file: docs/continuation/2026-08-07-repair-then-backfill-continuation.md -->
<!-- version: 1.0.0 -->
<!-- guid: e2b74f05-3c19-48a6-95d7-6a08f31be4c2 -->
<!-- last-edited: 2026-08-07 -->

# Continuation prompt — run the repair, then finish the per-file backfill

Paste the block under **PROMPT** into a fresh session. Everything above it is
context for a human deciding whether to.

---

## Where things stand

**Merged 2026-08-07:**

| PR | What |
|---|---|
| #2170 | Three-outcome intro classifier (`ClassifyIntro`) + reparse data-loss guard |
| #2171 | `maintenance.repair-transcribe-status` — fixes the July-1 status falsification |
| #2172 | `maintenance.intro-migrate-single-file` — tier 0 of the per-file backfill |

**Nothing has been applied to prod.** Both new ops default to `dry_run=true`.

## The two ops waiting to run

### 1. `maintenance.repair-transcribe-status` — run this FIRST

A day-long transcription-endpoint outage on **2026-07-01** tripped
`processTranscribePage`'s whole-batch error path, which marks **every** book in
a page `whisper_error`. Measured 2026-08-07:

- **76.7%** of a 300-book random sample carried `whisper_error`
- **229 of those 230 books had good transcript text**, dated **2026-06-27** —
  four days *before* the outage
- a 400-book sample put every failure in **17 timestamps, all on 2026-07-01**,
  every error a connection failure
- the most recent run (2026-08-06) had **25 errors out of 1,993** — Whisper is
  healthy

No transcript was damaged; `applyOutcome` refuses to overwrite good text with
nothing. **Only the status is wrong.** The library is in the state "everything
looks broken while everything is fine".

**Why first:** every "what still needs work" query — including the backfill's —
filters on status, and would currently over-count by roughly 4×.

### 2. `maintenance.intro-migrate-single-file` — tier 0, run second

Copies the book-level transcript onto the one `BookFile` row for the **33,780
single-file books (75.3% of the library)** at **zero GPU cost**.

## Measurements that should drive decisions (do not re-derive)

Full-library sweep 2026-08-07, all 44,875 books:

| shape | books | % | files |
|---|---|---|---|
| 0 `book_file` rows | 1,122 | 2.5% | 0 |
| 1 file (tier 0) | 33,780 | 75.3% | 33,780 |
| 2–5 files | 2,884 | 6.4% | 7,829 |
| 6–20 files | 2,775 | 6.2% | 34,601 |
| 21+ files | 4,314 | 9.6% | 228,681 |
| **total** | **44,875** | | **304,891** |

- **9.6% of books hold 75% of all files.** The long tail is the entire cost.
- Naive backfill ≈ 12–14 GPU-days. Tiered ≈ **1.4 days**.
- Parser corpus (987 distinct books): `written by` is the **most common** credit
  variant (24.1%) and **24.8% of stored titles** had a credit verb welded on.
- **1.4% of books (~644)** hold a parse their *current* transcript cannot
  regenerate — which is why reparse only ever upgrades, never clears.

## 🔴 Traps already paid for — do not rediscover these

1. **The prod API paginates on `offset`/`limit` and accepts-then-SILENTLY-IGNORES
   `?page=`.** A page-based sampler returns the same window every time. This
   produced one confidently wrong measurement already.
2. **`CheckpointFn` is NOT called in concurrent mode** (`run_items.go:54`).
   Tier 3 is both concurrent and resumable → it needs `OpFreshness.Stamp`.
3. **`internal/server` tests time out locally at 10 min** under parallel load
   (`setupTestServer` runs 21 Pebble migrations per test). It does this on
   unmodified `main` too — 600.71s vs 600.78s. Not a regression. CI passes.
4. **A latent data race exists in `UpsertBookToMemDB`** — it retains the caller's
   `*Book` across a deferred closure. Diagnosed, filed, NOT fixed:
   `todo.d/20260807_020500_memdb_warmup_caller_pointer_race.md`. It fires
   intermittently under `-race` on CI.
5. **Never commit the GPU host address or any fleet-internal IP.** Public repo;
   the pre-commit hook will reject tokens, but not IPs.

---

## PROMPT

> Continue the per-file audiobook identity signal. #2170 (classifier), #2171
> (status repair) and #2172 (tier-0 migration) are merged. Read
> `docs/continuation/2026-08-07-repair-then-backfill-continuation.md` first — it
> carries every measurement and the traps already paid for, so do not re-derive
> them.
>
> **Step 1 — run the status repair on prod.** Dispatch
> `maintenance.repair-transcribe-status` with `dry_run=true` first, show me the
> counts per bucket (`recomputed_ok`, `recomputed_unparsed`,
> `cleared_to_never_attempted`, `skip_genuine_failure_kept`,
> `skip_silence_sentinel`), and **stop for an explicit decision from me before
> applying**. Sanity-check the dry-run against the expectation that ~77% of the
> library is affected and that almost all of it should land in `recomputed_ok`
> or `recomputed_unparsed` — a large `cleared_to_never_attempted` count would
> mean the transcripts are not where we think they are, and that is a reason to
> stop and re-measure, not to proceed.
>
> **Step 2 — run tier 0** (`maintenance.intro-migrate-single-file`), same
> pattern: dry-run, show the per-reason counts, explicit gate before applying.
> Expect ~33,780 in `would_write` and ~1,122 in `skip_no_book_file_rows`. Verify
> afterwards that a sample of migrated books really have per-file
> `intro_transcription` set, by reading `GET /api/v1/audiobooks/{id}/files` —
> not by trusting the op's own counters.
>
> **Step 3 — build the multi-endpoint Whisper dispatcher.** `batch.go:51` reads
> a single `WhisperRemoteURL`; the U1 host is prepared (48 cores, 251 GB, **no
> GPU**, Python 3.14.3 + uv) but no worker is built. Benchmark int8
> faster-whisper against a real clip batch before promising any throughput — it
> is not a second GPU. 🔴 The dispatcher must distinguish "endpoint unreachable"
> from "this file failed to transcribe" and write per-file status ONLY for the
> second; collapsing those is exactly how a network outage got recorded 34,000
> times as a file problem. Point the slow node at tier 3, which has no deadline.
>
> **Step 4 — tiers 1/1b and 2/3**, then **#23** (wire the classifier into the
> regroup recommender, validating against the 356 measured holds) and **#24**
> (First Aid tier 2, enqueueing transcription rather than parking).
>
> Standing constraints: worktree per task, never edit the primary checkout; PR
> each one and give me the **full GitHub link on `main` after merge** — I am
> usually not on the Mac. Prod applies need a real gate, not a text reply.
> `review_apply_enabled` stays OFF. Public repo — never commit internal IPs.

## Also outstanding (separate from this chain)

- **memdb warmup data race** — diagnosed and filed, one-line fix + a regression
  test that must *force* the interleaving (a green local run proves nothing;
  the full package passed 0-races locally while CI caught it).
- **#4** multidisc canary — needs #23 plus an explicit gate to flip
  `review_apply_enabled`.
- **#6 follow-through** — apply the 434 confidently-linkable directory-shaped
  books. Relevant here: **1,122 books have zero `book_file` rows** and can never
  receive a per-file transcript until they are relinked.
- **Package upgrades** — fix the e2e suite first (`unknown parameter "_page"`,
  broken on `main`, gates nothing).
