<!-- file: changelog.d/20260806_160000_review_override_persistence.md -->
<!-- version: 1.0.0 -->
<!-- guid: c47f8e21-9b53-4d06-8a1f-5e2c7b0d9a36 -->
<!-- last-edited: 2026-08-06 -->

### Changed

- **A reviewer's disagreement with the classifier is now written down, and it is what
  actually runs later.** Approving a review hold with an explicit action recorded the
  status and nothing else: the chosen action lived nowhere. That mattered because with
  `review_apply_enabled` off — which is how production runs — approving executes
  nothing, and the work is carried out later by the replay pass. Replay re-derived the
  action from the hold's payload, so a hold the classifier wanted *combined* that a
  human had approved as *keep separate* would come back as a merge, hard-deleting the
  rows for books that person had explicitly said to keep apart.

  The stopgap was to refuse those overrides with a 409. Safe, but it meant that in the
  configuration production actually uses, a reviewer could not register a disagreement
  at all — which is the entire feature.

  Review items now carry the chosen action, written in the same atomic batch as the
  status change, and replay reads it first. The 409 is gone. Two tests hold the line
  in both directions: a `combine` hold approved as `separate` must come out of replay
  *unmerged*, and a `separate` hold approved as `combine` must come out *merged* —
  either one alone would pass on a replay that had simply stopped working.

  The write is deliberately narrow. It re-reads the record inside the store rather
  than writing back the caller's copy, touches only the status, the timestamp and the
  chosen action, and has no way to *clear* the chosen action — so the later
  approved→applied transition cannot erase the decision it is in the middle of
  carrying out. Holds decided before this change carry no action and fall back to the
  payload, which for a pre-recommendation hold means "not enough evidence": they keep
  working, and keep failing closed.
