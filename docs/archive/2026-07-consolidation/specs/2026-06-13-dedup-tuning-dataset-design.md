<!-- file: docs/specs/2026-06-13-dedup-tuning-dataset-design.md -->
<!-- version: 1.1.0 -->
<!-- guid: 2b9f4c6a-1d83-4e57-9a02-7c5b8e3d1f64 -->
<!-- last-edited: 2026-06-13 -->

# Dedup Tuning Dataset & Feedback Loop — Sub-project #1 Design

## Where this fits (the larger vision, decomposed)

The goal is a **self-improving dedup system**: continuously look at matches, learn
what a good dedup match looks like, auto-file bugs for the nonsense it finds, and
feed corrections back into scoring. That is multiple subsystems, sequenced:

1. **Labeled match dataset (THIS spec)** — capture every match + the reasoning +
   the context a judge needs + the human/auto verdict. The substrate everything
   else learns from.
2. **Deterministic bug-catchers** — cheap rules (duration-ratio, folder
   ancestor/part-of) that catch obvious false positives, suppress them, and
   auto-file dedup bug issues. Built from the same features as #1.
3. **AI-review loop (later)** — feed match JSON to an LLM-as-judge that scores
   good/bad, explains why, files bugs, proposes scoring tweaks. Uses the existing
   OpenAI batch infrastructure.
4. **Fine-tuning (later, gated on data)** — train a model on the labeled dataset
   once enough labels accrue. **Not viable yet** (see Experiment 0).
5. **Feedback into scoring** — adjust signal weights / band thresholds from what
   is learned.
6. **Vision model (deferred, probably never needed)** — "see" the rendered
   comparison. Reserved for what structured data genuinely can't express; the
   same-folder/part-of bug is encoded losslessly in JSON, so vision is the most
   expensive possible way to learn it.
7. **Community-owned audiobook fingerprint index (separate future track)** — a
   public, Git-repo-backed "AcoustID for audiobooks" seeded by the verified
   records this loop produces. Captured in `TODO.md` ("Open Audiobook
   Acoustic-Fingerprint Index"); needs its own brainstorming → spec session. It
   **supersedes** the earlier "submit to AcoustID.org" idea: their model is
   song/recording-based and fits audiobooks poorly, so we build a better index
   we control rather than feeding theirs. The labeled dataset in THIS spec is the
   upstream source of truth that loop will export from.

This spec covers **#1 + #2 only**. #3–#7 are named here for context and get their
own spec/plan when reached.

## Experiment 0 — measured, not assumed

Run `tools/cmd/dedup-dataset-audit` against a copy of the prod PebbleDB
(2026-06-13, full set). Results:

- **18,075 candidates**, **9,842 distinct books**.
- **Bands: 100% empty.** Every candidate has a nil unified `Band` /
  `ScoreBreakdown`. Layers: `exact` 85.9%, `acoustid` 10.8%, `embedding` 1.7%,
  `llm` 1.5%. (This is why the UI shows Certain/High/Medium/Review all = 0.)
- **recording_id oracle coverage: 0 / 9,842 books (0.0%).** The AcoustID online
  lookup has never persisted a MusicBrainz recording_id. → **No auto-label pair
  oracle exists today.**
- **iTunes PID: 2,931 / 9,842 books (29.8%)** carry a live PID — but the map is
  one-PID→one-book, so it is a per-book *attribute*, not a pair link.
- **Deterministic catchers:** duration known on both sides for 5,135 pairs; of
  those **1,023 (19.9%) are part-vs-whole (<0.5 duration ratio)**, and **163
  scored `exact`/high** — confirmed false positives. Plain shared-parent-folder
  caught only 14 (the real bug is parent/child folder nesting, not a shared
  parent).

### What Experiment 0 forces

1. **No fine-tuning yet.** Zero auto-labels and only 81 human-resolved candidates
   (77 merged + 4 dismissed). Bootstrap labels from deterministic rules + an
   LLM-as-judge + human review; revisit fine-tuning once labels accrue.
2. **The builder computes its own features.** Since `ScoreBreakdown` is empty on
   100% of rows, the dataset builder derives features directly from books/files
   (durations, folder relationship, recording_id, PID, paths) — it does **not**
   depend on a populated breakdown. It still snapshots whatever breakdown exists
   (forward-compatible for when unified scoring is run).
3. **The real near-term oracle is our own whole-book signature, not
   acoustid.org.** `internal/fingerprint/book_signature.go` already decodes each
   file's chromaprint into `[]uint32` and concatenates a book's parts into one
   whole-book signature (with downsampling + a compare function). Two books whose
   whole-book signatures match are the same content — an **internal, dependency-
   free pair oracle** we fully control, unlike acoustid.org lookup (which returns
   0% because their DB has no audiobook recordings). This is the "combine the
   parts into the full fingerprint" idea — it is already built, just not wired as
   a dedup signal. M1's feature builder computes the whole-book signature (and
   keeps per-part signatures) per book; a new catcher compares them.
4. **Duration is a cheap interim proxy that the signature subsumes.** Part-vs-
   whole is intrinsic to signature length/containment (a part's signature is a
   subsequence of the whole; the whole's length encodes total runtime). So we do
   **not** build a dedicated duration-extraction backfill — duration stays only
   as the ~28%-coverage fallback until signature coverage is high.

### M0 — the screenshot garbage is already-purgeable legacy data (do this first)

The "exact 100%" part-vs-part rows in the bug screenshot (e.g. "The Crafter's
Defense" matched against its own parts) are **stale legacy candidates**, not a
live scoring bug. Before the whole-file fingerprint cutover (~2026-05-12) the
engine compared *per-segment* acoustid hashes, so any two files sharing even one
segment were flagged 100% identical — ~12,320 false `exact`-layer rows.

A dedicated op already exists for them: **`dedup.purge-legacy-fp-candidates`**
(shipped as F5-T015, `internal/plugins/dedup/purge_legacy_fp.go`; dry-run
default, `{"apply":true}` to commit, idempotency flag `dedup_fp_purge_v1_done`,
excludes the `acoustid` layer). **The 2026-06-13 audit proves it was never
applied on prod:** the by-status histogram shows 99.6% `pending` and **zero
`stale-fp`** rows. Running it (dry-run → `apply`) clears most of the screenshot
garbage immediately, with no new code and independent of M1–M4. M0 in the build
sequence is just "run the op that's already there."

## Storage decision

SQLite was deliberately removed (fable5 T022) and cannot be re-enabled; do not
reintroduce it. Use:

- **System of record: a new PebbleDB keyspace** `dedup:label:<candidateID16hex>`
  → JSON `LabeledExample`. Mutable (humans re-label), queryable (serves the
  review view), co-located with the rest of state, no new dependency.
- **Export artifact: JSONL on disk** via an endpoint/job
  (`GET /api/v1/dedup/dataset/export.jsonl`). Immutable, append-only, and the
  exact format OpenAI fine-tuning ingests; trivially `scp`-able for offline
  analysis.

## Data model — `LabeledExample`

One row per candidate pair, written/updated at capture time.

```
LabeledExample {
  candidate_id       int64
  entity_a_id        string   // book IDs
  entity_b_id        string

  // --- the match as scored ---
  layer              string   // exact|acoustid|embedding|llm
  band               string   // may be "" until unified scoring is run
  score              float64  // 0 if no breakdown
  score_breakdown    json     // UnifiedDedupScore snapshot (may be empty today)
  similarity         *float64

  // --- computed features (builder-derived; the judge's real evidence) ---
  a / b each: {
    title, author, primary_path string
    total_duration_sec  float64
    file_count          int
    has_cover           bool
    files_exist         bool      // every referenced path resolves on disk
    recording_ids       []string  // MusicBrainz online recording_ids (often empty)
    itunes_pid_present  bool
    part_of_m           *int      // parsed N-of-M if a single part
    whole_book_sig_present bool    // book_signature.go produced a signature
  }
  duration_ratio       float64    // min/max of the two totals (0 if unknown)
  folder_relation      enum       // unrelated | same_dir | a_ancestor_of_b | b_ancestor_of_a | sibling_parts
  shares_recording_id  bool
  signature_relation   enum       // unknown | match | disjoint | a_contains_b | b_contains_a (whole-book sig)

  // --- the label ---
  label                enum       // true_dup | not_dup | unsure
  label_source         enum       // rule | itunes_attr | human | llm_judge
  label_reason         string     // e.g. "duration ratio 0.02 — part vs whole"
  decided_at           timestamp
  formula_version      string
}
```

## Components

### C1. Feature builder (`internal/dedup/dataset`)
Pure function `BuildExample(store, candidate) LabeledExample` that loads both
books' files once and computes every feature above. This is the audit CLI's
per-pair logic, promoted to a reusable, unit-tested package. One clear input
(candidate + store), one clear output (example); no side effects.

### C2. Deterministic catchers (`internal/dedup/dataset/rules.go`)
Pure predicates over a `LabeledExample`, each returning (fires?, reason):
- `wholeBookSignatureMatch` (the strong oracle): both books have a whole-book
  signature (from `book_signature.go`) and they match within tolerance →
  `true_dup` (`label_source = rule`, high confidence). Disjoint signatures with
  comparable runtime → `not_dup`. This is the auto-**positive** oracle the
  recording_id approach failed to provide.
- `partVsWhole`: signature-containment (one side's whole-book signature aligns as
  an offset subsequence of the other) OR, as fallback, `duration_ratio < 0.5`
  with both durations known → `not_dup` ("a part matched against the whole
  book"). NOTE: `book_signature.go` provides the signature primitives, but its
  `BookSignatureSimilarityMasked` is *same-coordinate* masking; true offset/
  subsequence alignment (the part sits at an unknown position in the whole) is
  M1 implementation work, not a solved primitive. Until it lands, the
  duration-ratio fallback carries this rule.
- `folderAncestorOrSibling`: one path is an ancestor of the other, or they are
  `sibling_parts` of one multi-file book → `not_dup`.
- `missingFile`: either side's `files_exist == false` → flag (don't merge a
  candidate whose file is gone — the "we have to actually have the file" point).
Each fired rule produces an auto label (`label_source = rule`) and, for rules
that contradict a high scorer band, an auto-filed bug issue (C5).

### C3. Store (`internal/database`, Pebble keyspace `dedup:label:`)
`UpsertLabeledExample`, `GetLabeledExample`, `ListLabeledExamples(filter)`,
`CountLabeledExamples(filter)`. Filter supports band, label, label_source,
folder_relation, signature_relation, duration-ratio buckets, agreement-with-
score — the axes the review view stratifies on.

### C4. Population
- **Backfill job** (`dedup.dataset-backfill` UOS op): iterate all candidates,
  `BuildExample`, run catchers, write/auto-label. Dry-run default; `apply`
  commits. Reuses the audit CLI's traversal.
- **Live capture**: `MergeDedupCandidate` / `DismissDedupCandidate` /
  Keep-A/Keep-B write a `human`-labeled example at decision time (score as it
  was), and suppress at the same point if a catcher fires.

### C5. Auto bug-filing
When a catcher fires on a candidate the scorer ranked high, file a deduplicated
dedup-bug issue (grouped by rule kind) capturing the example JSON. Wire to the
existing issue/burndown path; dedupe by `(rule, formula_version)` so we don't
file 1,000 identical issues — file one per class with a count and examples.

### C6. Curated review view (frontend)
A stratified queue that surfaces the off-diagonal cells (high score × likely
not-dup; low score × likely dup) so humans label *failures* fast, not 18k easy
agreements. Built on `ListLabeledExamples` filters. Each row: the rich card +
the computed features + one-click `true_dup` / `not_dup` / `unsure` that writes
a `human` label.

### C7. JSONL export
`GET /api/v1/dedup/dataset/export.jsonl[?label=&source=]` streams examples as
JSONL for offline analysis / future fine-tuning.

## Build sequence (milestones)

0. **M0 — run the existing legacy-FP purge** (no new code). `dedup.purge-legacy-
   fp-candidates` dry-run → review count → `apply`. Clears the ~12K stale
   `exact`-layer false positives that dominate the screenshot. Do this first; it
   is independent of all code below and immediately quiets the worst garbage.
1. **M1 — features + catchers + store** (C1, C2, C3) with unit tests. Promote
   the audit logic; wire the whole-book signature (`book_signature.go`) into the
   feature builder; prove the catchers reproduce Experiment 0's counts.
2. **M2 — backfill op + live capture** (C4). Run dry-run on prod, compare to
   Experiment 0, then `apply` to populate the dataset and suppress remaining
   part-vs-whole / folder false positives that M0 didn't cover.
3. **M3 — auto bug-filing** (C5).
4. **M4 — review view** (C6) + **JSONL export** (C7).

LLM-as-judge (#3) and fine-tuning (#4) are **separate later specs**, unblocked
once M1–M4 have produced labels. The whole-book signature oracle (C2) is the
internal auto-positive source; acoustid.org lookup/submission is **not** a
dependency (superseded by the community-index track, vision item #7).

## Testing

- C1/C2 unit tests with synthetic books/files covering: whole-book signature
  match (auto-positive), signature containment (part-vs-whole), signature
  disjoint, nested folder, sibling parts, missing file, no-signature fallback to
  duration ratio, no-duration.
- C3 store round-trip + filter tests (Pebble temp dir, the existing pattern).
- C4 backfill dry-run asserts counts match the audit on a fixture set.
- Handler tests for export + the live-capture write on merge/dismiss.
- M0 acceptance: after `dedup.purge-legacy-fp-candidates --apply`, the by-status
  histogram shows the legacy `exact` rows moved `pending → stale-fp` and the
  Crafter's-Defense part-vs-part rows no longer surface in the dedup UI.
- M2 acceptance: any residual part-vs-whole pairs are present as `not_dup`
  examples with `label_source = rule`.

## Rollback

All additive: a new keyspace, a new package, new opt-in op, new endpoints. The
backfill is dry-run by default and the catchers can be feature-flagged off. No
change to existing candidate storage or the merge engine.

## Resolved decisions (were open questions)

1. **AcoustID online lookup: dropped.** Its recording_id coverage is 0% and the
   DB has no audiobook recordings, so it cannot be the oracle. The internal
   whole-book signature (`book_signature.go`) is the auto-positive oracle
   instead. Contributing verified records goes to the community-index track
   (vision #7), not acoustid.org.
2. **Auto-bug-filing target: the existing burndown/issue system**, deduped by
   rule class (`(rule, formula_version)`). One issue per class with a count +
   example payloads, not 1,000 duplicates.
3. **No duration-extraction backfill.** Part-vs-whole is intrinsic to signature
   containment; duration stays a ~28%-coverage fallback only. Effort goes into
   whole-book signature coverage, not duration extraction.
