# Scope 06 — 20 items

## ITEM L3790 [tier C] section: 3. Real authors, genuinely duplicated
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] Normalize whitespace (and probably case) in author lookup/creation, so a
      `Raymond  L.  Weil` can never be minted alongside `Raymond L. Weil`.
      ⚠️ Check `util.NormalizeAuthor` first — it is already used for the series
      name index (`pebble_store_series.go`), so the helper may exist and simply
      not be applied on the author path.

## ITEM L3795 [tier C] section: 3. Real authors, genuinely duplicated
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] Merge the type-3 real-author duplicates. The existing
      `maintenance.author-*` ops already know how to relink via the join slice —
      see `author_conjunction_repair.go`'s `mergeAuthorInto`, which handles the
      BookAuthor rewrite and the AuthorID hydration correctly.

## ITEM L3799 [tier B] section: 3. Real authors, genuinely duplicated
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] Decide what to DO with types 1 and 2 rather than merging them. Merging 25
      `Cthulhu Armageddon (Unabridged)` rows into one still leaves a book title
      masquerading as an author. These need the books re-parsed, or the rows
      retired and the books re-attributed — a different operation from dedupe.

## ITEM L3803 [tier C] section: 3. Real authors, genuinely duplicated
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] 🚨 Do NOT write a single op that treats all three kinds the same. Type 3
      wants a merge; types 1 and 2 want the author link removed entirely. An op
      that merges everything would consolidate the junk and make it look
      intentional — the laundering failure mode recorded in
      `feedback_stripping_without_corroboration_is_laundering`.

**Counts to re-measure before acting** — these are from the 2026-08-14 07:50
snapshot of a 9,320-row author table, taken via `/api/v1/authors` paged by
`limit`/`offset` (note: `page` is not a parameter this endpoint accepts).

## ITEM L3884 [tier C] section: Likely cause
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] Decide the single meaning of a nil `IsPrimaryVersion` and apply it in both
      places. `Default: true` is already the storage answer, so the post-filter
      is the side that should change — but confirm before flipping, because
      22,552 books currently answer to `false` library-wide and some of those
      may be nil-flagged.

## ITEM L3889 [tier C] section: Likely cause
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] Add a conformance test in the shape used by #2406/#2410/#2411: one
      fixture containing a nil-flag book, an explicit-true book and an
      explicit-false book; assert the library path and the author path classify
      all three identically. A fixture without a nil-flag row cannot catch this.

## ITEM L3893 [tier B] section: Likely cause
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] Decide whether the author listing SHOULD expose non-primary books at all.
      Today it cannot, which is defensible for a listing, but it means the UI
      has no way to show a book on the author page it is genuinely attached to.

⚠️ **This is why the 46627 merge could not be verified** — see the handoff. Every
available instrument for "which books does author X have" is either
primary-only or disagrees about nil, so `0 non-primary books for 43791` is not
evidence the merge failed. Do not read it as such.

## ITEM L3910 [tier C] section: PERF-4 is still live: iTunes search calls `SearchBooks(search, 0, 0)`
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] First MEASURE, don't assume: confirm what `SearchBooks(q, 0, 0)` returns
      today (the store may have changed limit-0 semantics since June). A
      bogus-value + known-good-value probe against a seeded store settles it in
      one test.

## ITEM L3914 [tier C] section: PERF-4 is still live: iTunes search calls `SearchBooks(search, 0, 0)`
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] If it returns nothing: the iTunes search surface has been silently empty
      — fix with a bounded call (or route through Bleve IDs + iTunes filter,
      as the audit suggested), and add the value-asserting test that would
      have caught a filter answering nothing.

## ITEM L3918 [tier C] section: PERF-4 is still live: iTunes search calls `SearchBooks(search, 0, 0)`
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] If it returns everything: that is the opposite failure (unbounded
      materialization on a handler path) and wants a limit anyway.

## ITEM L3921 [tier C] section: PERF-4 is still live: iTunes search calls `SearchBooks(search, 0, 0)`
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Legacy operation rows never leave "pending" — the ops UI shows
      finished jobs as running for hours.** Twice on 2026-08-14 this misled
      the operator: the composer scan showed progress 0 while 3h into real
      work, and the E02 chapters dry-run showed as an active 1.5-hour task
      when it had finished at 17:57 with logged results. A
      `GET /api/v1/operations?limit=20` dump shows EVERY maintenance-job row
      of the day stuck at status "pending" — including `fix-file-modes` and
      `normalize-primary-flags`, which completed with journaled summaries.
      The v2 registry rows complete correctly; it is the LEGACY op row
      (`maintenance:<job>` type, created for jobs dispatched via
      `maintenance.job`) whose status/progress is write-only after creation.
      Fix: on v2 op completion, propagate terminal status (+ final
      progress/message) onto the paired LegacyOpID row; backfill-repair the
      day's stuck rows; and the ops UI should render v2 state when a legacy
      row has a live v2 twin. Note the C510 opstate sweep treats unknown
      statuses as KEEP — stuck-pending rows also pin their opstate blobs
      forever, so this defect quietly defeats that retention too.

## ITEM L3939 [tier C] section: PERF-4 is still live: iTunes search calls `SearchBooks(search, 0, 0)`
primary_domain_guess: internal/maintenance | all_domains_guess: internal/maintenance;internal/operations;internal/server;internal/server/...

- [ ] **Check GitHub CI on the merge commit.** Merged on an explicit instruction
      to skip the CI *wait*, so no GitHub result was read. Local verification was
      complete and green: `go build ./...` exit 0, `go vet ./internal/server/...`
      exit 0, `gofmt` clean, and `go test ./internal/server/...
      ./internal/maintenance/... ./internal/operations/... -short -race -count=1`
      → **exit 0, 19/19 packages ok, zero failures** (`internal/server` 898s).
      Plus four independent mutations each killing a distinct test. Only the
      GitHub-side result (lint, frontend, changelog-check) is unread.

## ITEM L3948 [tier C] section: PERF-4 is still live: iTunes search calls `SearchBooks(search, 0, 0)`
primary_domain_guess: internal/database | all_domains_guess: internal/database

- [ ] **`opstate:<id>:params` keys are never swept.** `runMaintenanceJob` now
      persists a small params blob (~90 bytes) per maintenance run so a restart
      can resume faithfully. `DeleteOperationState` clears both `opstate:<id>`
      and `opstate:<id>:params`, but only two of the 34 jobs
      (`recompute-book-aggregates`, `backfill-file-hashes`) call
      `operations.ClearState` on clean completion — the other 32 leave the key
      behind forever. There is no retention sweep for the `opstate:` prefix
      (grep confirms the only writers/deleters are in
      `internal/database/pebble_store_operations.go`). Growth is small but
      unbounded; either add an `opstate:` sweep to `retention-and-hygiene` or
      have `maintenance.job`'s Run clear params when the job finishes.

## ITEM L3960 [tier C] section: PERF-4 is still live: iTunes search calls `SearchBooks(search, 0, 0)`
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Verify the dry-run default on prod after deploy.** `GET
      /api/v1/maintenance/jobs` publishes `default_params`; POST a job that
      advertises `dry_run:true` with no body and confirm the run reports a
      preview rather than applying. Safest probe: `scan-composer-tags` (scan
      only). Do **not** probe with `cleanup-series` or `cleanup-empty-folders`.

## ITEM L3966 [tier C] section: PERF-4 is still live: iTunes search calls `SearchBooks(search, 0, 0)`
primary_domain_guess: internal/dedup | all_domains_guess: internal/dedup

- [ ] **`dedup.series-dedup` still has no dry-run parameter at all.**
      `internal/dedup/series_dedup.go:266` `DedupSeries` applies on every
      invocation, and its merge loop reassigns books via the *listing* getter
      `GetBooksBySeriesIDCore` (which filters trashed and non-primary rows)
      before calling `DeleteSeries` unconditionally — the mechanism that strands
      books on a deleted series ID. It has never run in production (0 of 10,161
      operations), so there is no existing damage; it is a latent hazard only.
      Give it a dry-run parameter and switch it to the all-versions getter
      before anything wires it to a trigger.

## ITEM L3976 [tier C] section: PERF-4 is still live: iTunes search calls `SearchBooks(search, 0, 0)`
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Consider making the resume path's fallback observable.** When no saved
      params exist, `resumeLegacyOp` now logs at info and resumes with the
      advertised default. Once the pre-change operations have aged out, that log
      line firing at all means something failed to save — worth a metric rather
      than a log grep.

## ITEM L3982 [tier C] section: PERF-4 is still live: iTunes search calls `SearchBooks(search, 0, 0)`
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Metadata matcher: shift-click range selection.** Owner request
      (2026-08-14): clicking one row then shift-clicking another should select
      every row between them, like a file manager. Track the anchor index of
      the last plain click; on shift-click select the inclusive range
      (respecting current sort/filter order). Frontend-only.

## ITEM L3988 [tier C] section: PERF-4 is still live: iTunes search calls `SearchBooks(search, 0, 0)`
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Metadata matcher: "skip all" / hide-multiples control.** Owner request
      (2026-08-14): multi-match groups need a way to be hidden in bulk —
      a "skip all" that stashes them for a later pass — so a triage session
      can clear the unambiguous rows without wading through the multiples
      every time. Persist the skip set (per user or per session) so hidden
      groups come back on demand, not on reload.

## ITEM L3995 [tier C] section: PERF-4 is still live: iTunes search calls `SearchBooks(search, 0, 0)`
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Metadata matcher: apply falsely reports "signed out — no changes were
      made" after a long write.** Owner observed (2026-08-14): with write-to-
      files enabled, a multi-file apply blocks past the auth/session timeout;
      the UI then reports a sign-out AND claims no changes were made — but
      the writes had clearly happened. Two defects: (1) the result message is
      dishonest — never claim "no changes" from a timeout, report "connection
      lost, operation may still be running" and re-query; (2) the root fix is
      the background-job dispatch already filed in
      `20260814-matcher-writeback-background-job.md` — an op id returned
      immediately makes the timeout impossible and the ops screen owns
      progress/results.

## ITEM L4007 [tier C] section: PERF-4 is still live: iTunes search calls `SearchBooks(search, 0, 0)`
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Metadata matcher: multi-file write-to-files must dispatch as a
      background operation.** Owner request (2026-08-14): with "write to
      files" enabled and more than 1 file affected, the apply currently
      blocks the UI until every file is rewritten — at the measured
      ~35 s/file for a full tag rewrite that is minutes-to-forever from the
      user's chair. Route the >1-file case through the operations system
      (`maintenance.bulk-write-back` already exists, takes explicit
      book_ids, and shows in the ops UI) and return immediately with the
      op id; single-file applies can stay synchronous. Note bulk-write-back
      is serial ~35 s/book — the E08 prerequisites fragment
      (diff-skip + in-op parallelism) applies here too.

