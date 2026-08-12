- [ ] **REVIEW-COMBINE-FIRST** Let the owner combine two books into one, or
      merge them as duplicates, **before** applying metadata — from the same
      surface where they are choosing the metadata. Requested 2026-08-11:
      *"a way to combine two books into one before I apply metadata, or merge
      them as duplicates before I apply my metadata."*

      **The ordering is the whole point.** Today the sequence is forced:
      metadata is applied to whatever book row happens to exist, and only later
      can rows be combined. When one logical book is split across several rows
      (extremely common in this library — see the 199 books exploded into 6,060
      single-file folders), the owner ends up applying the same metadata several
      times to fragments of one book, and the combine afterwards has to
      reconcile competing metadata that need never have diverged.

      Both actions already exist as separate UI flows:

      - **Combine into One Book** — `web/src/components/BatchToolbar.tsx:101`
        → `web/src/pages/Library.tsx:1256` → `POST /api/v1/audiobooks/combine`
        (`internal/server/wire_dedup_routes.go:75`). Hard-deletes the absorbed
        shells.
      - **Merge as Versions** — `web/src/pages/Library.tsx:1232`. Soft-deletes
        the losers and demotes them to non-primary.

      So the backend capability is there; what is missing is reaching it from
      the metadata chooser without losing the chooser's state.

      ⚠️ **Blocked-ish — read first.** Two live defects sit directly under this:

      1. **Version groups with two elected primaries.** Sampled on prod
         2026-08-11: 10 of 15 groups had two members both marked
         `is_primary_version=true`, so the "merged" book lists twice
         permanently. Suspected writers: `internal/merge/service.go:196-206`
         (reuses an existing group ID but only writes the flag on the books
         passed in, never demoting pre-existing members) and
         `internal/reconcile/reconcile.go:770-795`. Building combine-before-apply
         on top of a merge that can leave two primaries will produce more of
         them, faster. **Not verified** as the causal path for those rows.
      2. Applying metadata from the review screen did not reach the files at
         all until the fix on branch `fix/review-apply-writes-tags` — see the
         separate entry.

      Documentation check done 2026-08-11: the universal review queue
      (`docs/plans/2026-07-13-review-queue-and-regroup.md`, shipped July,
      `review_apply_enabled` defaults OFF) is **regroup-only**. It does not
      cover user-initiated combine, dedup review, or metadata review. The bulk
      metadata review plan
      (`docs/archive/superpowers/plans/2026-04-06-bulk-metadata-review.md`) and
      the dedup label review panel
      (`docs/archive/agent-tasks/dedup-ui/TASK-04-label-review-panel.md`) were
      both archived without shipping. The owner's instinct that these want one
      home is right; that home does not exist yet.
