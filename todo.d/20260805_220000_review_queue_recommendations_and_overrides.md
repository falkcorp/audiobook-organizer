<!-- file: todo.d/20260805_220000_review_queue_recommendations_and_overrides.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4e91b7c2-06d8-4a35-9f17-b3820e5cd641 -->
<!-- last-edited: 2026-08-05 -->

- [ ] **Give review holds a real recommendation, and let the human override it**
  — owner items 1 and 2 (2026-08-05). Two halves of one change; building the
  recommendation as a string first and bolting override on later means rewriting
  the first half.

  **The problem.** `proposedAction` is one generic string on **762 of 777** holds
  ("review: flat folder shares a title but ordering is unclear") and
  `survivorTitle` is frequently wrong. A queue where every row says the same
  thing is a queue nobody can work.

  **Recommendation.** Add structured fields to `regroupPayload`:
  `recommendedAction` (`combine` / `separate` / `duplicate-of` /
  `insufficient-evidence`), `recommendationReason`, and
  `recommendationEvidence` — the numbers that produced it (member durations,
  distinct stem count, part/disc marker count, folder shape). The evidence field
  is what makes the queue workable; a reason alone is just a nicer generic string.

  **Override.** `ApproveReviewItem` takes an optional body `{action: "..."}`
  defaulting to `recommendedAction`, and dispatch keys off the CHOSEN action.
  Today `approveOne` (`internal/server/handlers/review/handler.go`) dispatches on
  `item.Kind`, so this is the structural change that makes override possible.
  Keep the four `Kind` strings unchanged — they are load-bearing and the frontend
  maps them verbatim.

  `separate` needs no apply handler: every member is already its own book, so
  "separate into N" is a status transition, and `UpsertReviewItem`'s dedup-key
  idempotency keeps it decided across re-scans.

  **Also fix `deriveSurvivorTitle`**, which reads the folder name only and so
  returns author names ("C. T. Phipps"), "Volume 1", and wrong volume numbers
  ("…Vol. 01" on a folder whose files say Vol. 9). `folderNamedAfterBook` and
  `dominantPrefix` are already computed a few lines above — use the folder name
  when the former is true, the dominant member title when it is not, and emit
  empty rather than a wrong title when neither is trustworthy.

  🔴 **Sequencing.** The decisive signal is member `DurationSec`, which was ZERO
  for 97.5% of the queue because those books had no `book_file` rows. Do this
  AFTER [[relink-unlinked-books]] and a regroup re-run, or the recommendations
  are computed on blank evidence — the same failure that let 41 of 43 "confident"
  candidates propose merging distinct novels.
