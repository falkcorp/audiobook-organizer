- [ ] **MAINT-RESUME-DROP** Give the five maintenance jobs that declare
      `CanResume() == true` a `ResumePolicy` that actually resumes, or drop the
      `CanResume()` claim. `bulk-deluge-import`, `cleanup-empty-folders`,
      `refetch-missing-authors`, `repair-missing-files` and `scan-composer-tags`
      all return `maintenance.DefaultPolicy()`, whose `ResumePolicy` is
      `opsregistry.ResumeDrop`, while their `CanResume()` returns true. Until
      PR #2784 the contradiction was masked: `server.resumeLegacyOp`'s `default:`
      branch re-enqueued them off the v1 row and gated on `CanResume()`, so the
      declared policy never had to be right. #2784 retired the v1 minter and
      deleted that branch, so these five now do not resume at all. Verified by
      structure, not by name: `CanResume()==true` holds for nine jobs, and the
      four that resume correctly (`backfill-file-hashes`,
      `recompute-book-aggregates`, `retention-and-hygiene` via `RestartPolicy`,
      `bulk-fetch-metadata` via `RequeuePolicy`) are exactly the four not listed
      here.

      **The blocker recorded in `internal/maintenance/jobs/policy_declaration_test.go`
      is now stale.** It says these cannot take `ResumeRequeue` because
      `server.resumeV2Op` re-enqueues with nil params, under which `DryRun`
      resolves to false and a preview runs for real. But `resumeV2Op` has exactly
      one caller, fed only from `store.GetInterruptedOperations()` — v1 rows —
      and it dispatches only when `opRegistry.Def(op.Type)` resolves. v1
      maintenance rows are typed `maintenance:<job>` (colon) while v2 defs are
      `maintenance.<job>` (dot), and `RegisterOp` rejects ids containing `:`, so
      that lookup can never succeed for a maintenance row. With the v1 minter
      retired, no new v1 maintenance rows exist at all. The path that would have
      dropped params is unreachable; the registry's own
      `resumeAfterStartup`/`resumeRequeue` preserves `Params`, which
      `TestResume_PreservesParamsAcrossRestartAndRequeue` pins. So `dry_run`
      survives a resume today and the stated reason not to fix this no longer
      holds — re-read that test's comment before acting on it.

      Note `cleanup-empty-folders` deletes directories from disk, so this is an
      owner decision on resume semantics rather than a mechanical change. Raised
      by the post-merge review of PR #2784.
