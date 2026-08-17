<!-- file: docs/plans/2026-08-17-split-scan-ai-phase.md -->
<!-- version: 1.2.0 -->
<!-- guid: 3f0c9a71-6d24-4e18-b5aa-71c2e0d4f9b3 -->
<!-- last-edited: 2026-08-17 -->

# Split the scan's AI-parsing phase out of the metadata pass

**Status:** plan, not started. Requires approval before any code change.
**Origin:** measured while investigating why `library.scan` leaves 21-minute
uncheckpointed gaps (#2518 follow-up).

## The measurement that motivates this

Sampled the running production scan `01M070AVW1XHMH3C03CDRY5J4W` every 10s via
`GET /api/v1/operations/v2/<id>`, classifying each sample by the shape of
`progress_message`. Observed window 13.1 minutes, 65 samples, 2026-08-17
14:18–14:32 UTC:

| phase | wall-clock | share |
|---|---:|---:|
| `ai-parse` (`AI parsing batch N/M`) | 8.8 min | **66.9%** |
| `metadata` (`Processed: N/M books`) | 4.3 min | 33.1% |

Throughput over the same window: 723 books in 13.1 min = **0.92 books/s**.
Excluding time attributed to `ai-parse`: **2.78 books/s**, a 3.0× difference.

**Re-measured over a doubled window** (24.0 min, 121 samples, 14:18–14:42 UTC),
which is the check the first table asked for:

| phase | wall-clock | share |
|---|---:|---:|
| `ai-parse` | 16.7 min | **69.4%** |
| `metadata` | 7.4 min | 30.6% |

The split is stable — 66.9% at n=65 and 69.4% at n=121 — so the AI phase
dominating is a property of the workload, not an artifact of when sampling
started.

> ⚠️ **Scope of these numbers.** Still one folder of one scan, 24 minutes of a
> multi-hour run. Enough to establish that the AI phase dominates and that the
> figure is stable; **not** enough to quote a whole-scan speedup, because folder
> composition varies and the share of books needing AI parsing varies with it.
> Re-measure across a folder boundary before using 3.0× in planning.

Instantaneous rates within the window differ by more than an order of magnitude
(4.7 books/s during a metadata stretch, 0.28 books/s across a stretch containing
an AI block), so a single average of the scan is not a meaningful figure.

## What the code actually does today

`ProcessBooksParallel` (`internal/scanner/scanner.go`) already has two phases,
but they are nested **inside each 500-book chunk**:

1. A parallel per-book metadata pass at `runtime.NumCPU()` workers, which
   accumulates `aiCandidates` — books whose filename-derived metadata is weak.
2. A **sequential** AI batch phase over that chunk's candidates
   (`scanner.go:1200-1265`): batches of 20, `2s` delay between batches, `30s`
   timeout per batch.

`processBookChunks` (`internal/scanner/service.go:521`) then checkpoints at the
chunk boundary — *after* both phases.

## Why splitting is worth doing

1. **The checkpoint cannot see the AI phase.** It runs before the chunk-end
   checkpoint, so up to ~25 batches × 30s ≈ 12 minutes of AI work per chunk is
   uncheckpointed and repeated on restart. This is a direct answer to the
   uncheckpointed-strike gaps, and a better one than checkpointing the walk.
2. **Neither phase saturates its resource.** Metadata is disk+CPU bound and
   parallel; AI is network bound and deliberately serialized. Interleaving them
   idles the CPU pool during every AI block and idles the backend during every
   metadata stretch. The 0.92 vs 2.78 books/s gap above is that idling.
3. **Failure isolation, already paid for once.** `scanner.go:1205` records that
   on 2026-08-16 an OpenAI `credit_balance_exhausted` made all 77 batches fail
   after retries — ~25 minutes of guaranteed-useless work, silent, which tripped
   the watchdog's `ProgressTimeout` and killed the scan, discarding a completed
   3,917-file walk. A separate pass degrades itself instead of the scan.

## 🔴 The hazard that makes this a plan and not a patch

Today AI results are merged into the in-memory `Book` **before**
`saveBookToDatabase`. Split the phases and the AI pass necessarily runs after
those rows are persisted, so it must **update** them.

A bare whole-row `UpdateBook` is this repository's dominant data-loss shape — it
is what wiped `AcoustIDFingerprint` and `Author` in previous incidents, and what
`preserveExistingFields` exists to mitigate. **The AI pass must write a narrow,
field-level update of only the fields it derived, never a row write.** This is
the bulk of the work and the whole reason for the review gate.

Second hazard: a scan currently clobbers applied metadata for not-yet-processed
books. Moving AI writes later widens the window in which a book row is
half-populated. The narrow update must therefore also respect whatever
field-level provenance the review/apply path sets, or an AI guess can overwrite
a human-approved value.

## 🟢 Do this first: the AI phase is throttled for a backend we do not use

`GET /api/v1/ai/backends/status` on production, 2026-08-17:

```json
{ "llm_mode": "local",
  "local_base_url": "http://<llm-host>:11434/v1",
  "local_reachable": true,
  "llm_model": { "name": "qwen2.5:7b-instruct", "pulled": true } }
```

The batch loop is **strictly sequential with a hardcoded `2s` sleep between
batches** (`scanner.go:1202`). That shape is a rate-limit courtesy for a *hosted,
quota-metered* API. Production runs a local Ollama on the GPU host, which has no
quota and no per-account rate limit.

Measured cost of the throttle: 13 batches took ~4m32s ≈ 21s/batch, of which 2s
is the deliberate sleep — roughly **10% of AI time spent sleeping**, and AI time
is 67% of the scan.

**Removing the sleep is separable from the risky part.** It does not move when
writes happen, so it carries none of the whole-row `UpdateBook` hazard above, and
it can ship on its own.

Suggested first PR, independent of everything else in this document:

1. Make the inter-batch delay conditional on the backend being remote (0 for
   local), rather than an unconditional constant. Worth ~10% of AI time.
2. Re-measure the phase split and compare against the table above.

That is the whole list. **A worker pool is deliberately NOT on it** — see below.

## 🔴 RETRACTED: do not add concurrency to the AI batch loop

An earlier revision of this document recommended giving the batch loop a bounded
worker pool of 2–4. **That recommendation was wrong and is withdrawn.** It rested
on this probe, run against the LLM host while the production scan was running:

| shape | wall-clock | speedup vs serial |
|---|---:|---:|
| serial, n=3 | 0.81s (0.27s each) | 1.00× |
| concurrent, n=3 | 0.44s | 1.86× |
| concurrent, n=6 | 0.67s | 2.43× |

The probe used **8-token completions of a trivial prompt at ~0.27s each**, while a
real batch is 20 filenames taking ~21s. Those sit at opposite ends of the
prefill/decode curve, so what the probe actually measured was **request-latency
headroom, not compute headroom.** The document flagged that caveat and then made
the recommendation anyway.

Direct measurement of the GPU settles it. Sampled every 5s for 25s while the scan
was in its AI phase:

```
util 92–93%   temp 95–97 C   clock 1860 MHz (max 2130)   vram 6.7/8.0 GB
```

At 92–93% utilization the device is **saturated**. Client-side concurrency cannot
create throughput that is not there; it would only deepen the queue.

### The GPU was hardware-throttling the whole time

From the card's own telemetry, 2026-08-17:

```
GPU Current Temp        : 97 C
GPU Shutdown Temp       : 95 C      <-- running ABOVE its own shutdown spec
GPU Slowdown Temp       : 92 C
GPU Max Operating Temp  : 88 C
GPU Target Temperature  : 83 C

HW Slowdown             : Active
  HW Thermal Slowdown   : Active
HW Thermal Slowdown ctr : 8,737,239,236 us   (~2h 25m cumulative)
```

Clocks were pinned at 1860 MHz against a 2130 MHz maximum — about **87% of rated
clock**, held down by hardware thermal slowdown, not by anything in this codebase.

The scan was cancelled on operator instruction. Within 70s of the load stopping:
97 C → 61 C, utilization 93% → 7%, and the latched HW Thermal Slowdown cleared.
A 36 C drop that fast means the cooler does move heat — this is sustained
100%-duty inference exceeding what that cooling can hold, not a failed fan.
(`nvidia-smi` reports `[Unknown Error]` for fan speed on this card, which remains
unexplained and is worth a physical check.)

### What this does to the 69.4% figure

The phase-split measurement above stands — AI parsing really did consume 69.4% of
scan wall-clock. But its **cause is not purely architectural.** An unknown share
of it is the card running at 87% clock under thermal slowdown. Any re-measurement
of the split, and any before/after comparison for the sleep removal, must record
GPU temperature and clock alongside it, or it will attribute a thermal effect to
a code change.

**Before optimising this phase in software, fix the thermals.** Recovering
1860 → 2130 MHz is ~14.5% on the phase that is ~69% of the scan, for zero code
risk, and it is the only change here that makes the hardware safer rather than
working it harder.

## Proposed shape

Two options, in preference order.

### Option A — second pass within `library.scan` (preferred)

Run metadata for the whole folder first, then a single AI pass over the
accumulated candidates for that folder, with its own watermark.

- Candidates are re-read from the DB by ID, not carried in memory, so the pass
  is resumable and does not hold a folder's worth of books live.
- Checkpoint per AI batch (20 books) rather than per 500-book chunk.
- Preserves one operation, one progress bar, one cancel.

### Option B — separate `metadata.ai-parse-backfill` op

Scan never calls the AI parser; a distinct op sweeps books flagged as
weak-metadata.

- Cleanest isolation and independently schedulable; an AI outage cannot touch
  scanning at all.
- Costs a new op, a durable "needs AI parse" flag, and a longer window where
  freshly scanned books show filename-derived metadata.

## Ordered steps (Option A)

1. Add a field-level update path for AI-derived fields; prove with a test that
   it leaves `AcoustIDFingerprint`, `Author`, and any human-applied field
   untouched. **This lands first and alone.**
2. Extract the AI batch loop out of `ProcessBooksParallel` into its own
   function taking book IDs.
3. Have the metadata pass record candidate IDs instead of parsing inline.
4. Call the new pass once per folder, after `processBookChunks`.
5. Give it `ResumeFrom` / `CheckpointStateFn` per batch, matching the pattern in
   `maintenance.chapters-backfill`.
6. Re-measure the phase split on a real scan and compare against the table above.

## Test strategy

- Field-level update: mutation-test that removing the field allowlist causes a
  preservation test to fail. A green preservation test is worthless until it has
  been seen to fail.
- Resume: reuse the `chapters-backfill` shape — assert the completed prefix is
  skipped, and that the checkpoint carries every parameter, not just the index.
- Backend-down: assert a permanently-failing AI backend degrades the AI pass and
  leaves the metadata pass's results committed, reproducing the 2026-08-16 shape.
- Conformance: the split must leave book rows byte-identical to the unsplit path
  for a fixture library where AI is disabled.

## Rollback

Steps 2–5 are behind the existing `aiEnabled` check. Rollback is reverting the
call site so the AI loop runs inline again; step 1's narrow update is additive
and can stay. No schema change, no data migration.

## Open questions

- ~~Is the AI backend local or hosted?~~ **Answered: local Ollama at
  `<llm-host>:11434`.** See the throttle section above — this makes the
  2s inter-batch sleep unjustified and promotes it to the first PR.
- ~~Does the Ollama host serve concurrent completion requests faster than
  serially?~~ **Answered, and it closes the question rather than opening a PR:**
  the GPU sits at 92–93% utilization under the existing serial load, so there is
  no compute headroom for a client-side pool to claim. See the retraction section.
- **New, and it belongs to the hardware, not this codebase:** why does the card
  reach 97 C — above its own 95 C shutdown spec — under sustained inference, and
  why does `nvidia-smi` report `[Unknown Error]` for fan speed? Until that is
  answered, any AI-phase timing measured on this host is confounded by thermal
  throttling.
- Does anything downstream depend on AI-derived fields being present at the
  moment `saveBookToDatabase` returns?
