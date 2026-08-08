<!-- file: todo.d/20260807_203000_all_ops_must_support_dry_run.md -->
<!-- version: 1.0.0 -->
<!-- guid: a7f28c15-9364-4e0b-b8d2-46c1e0937fa5 -->
<!-- last-edited: 2026-08-07 -->

- [ ] **Require every operation to support `dry_run`, and enforce it at the
      registry rather than by convention.** Any op that mutates state must be
      runnable in a mode that computes and reports exactly what it WOULD do,
      writing nothing — so it can be tested independently and reviewed before it
      touches prod.

      **Motivating case (2026-08-07).** Three maintenance ops were run in one
      session and they did not agree with each other:

        maintenance.repair-transcribe-status      dry_run, defaults TRUE
        maintenance.intro-migrate-single-file     dry_run, defaults TRUE
        maintenance.transcribe-book-intros        NO dry_run at all

      The first two could be previewed, reconciled bucket-by-bucket against the
      full book count, and gated on real numbers. The third — a reparse that
      rewrites parsed title/author/narrator across the library — had no preview
      mode whatsoever: dispatching it IS applying it. The only reason that was
      acceptable was an unrelated internal guard (reparse only ever upgrades),
      which is luck, not design.

      **What "supported" should mean** — a bare `dry_run` bool is not enough:

      - **Declared, not optional.** Put it on `OperationDef` (e.g.
        `SupportsDryRun bool`, or better, make the param struct embed a shared
        `DryRunParams`). An op declaring `CapLibraryWrite` without dry-run
        support should fail registration, so the gap is caught at startup rather
        than discovered while someone is deciding whether to hit apply.
      - **Default TRUE for destructive ops.** Both ops that had it defaulted to
        dry-run; that is the right default and should not be per-author choice.
      - **Report per-reason counts that RECONCILE.** The value of the two
        previewable ops was that every item landed in exactly one labelled
        bucket and the buckets summed to the population — 11,315 + 19,505 + 0 +
        12,587 + 1,463 + 7 = 44,877 exactly. "would change 30,820" with no
        account of the rest is the shape of report that hides a bug. Consider a
        shared result type so this is structural rather than remembered.
      - **Same code path.** The dry run must execute the identical decision
        logic and diverge only at the write, or it is testing something other
        than what will run. Both existing ops do this correctly (classify, then
        branch on `dryRun` immediately before the store call) — that pattern is
        the one to generalise.

      Related: the write-set/scheduler-conflict work
      (`OperationDef.Writes []Resource`). Both are the same idea — an op should
      DECLARE what it does, and the system should enforce it, instead of every
      author re-deciding by hand.
