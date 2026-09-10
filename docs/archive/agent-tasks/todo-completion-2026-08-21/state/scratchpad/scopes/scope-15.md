# Scope 15 — 18 items

## ITEM L10632 [tier C] section: Infra (5)
primary_domain_guess: docs | all_domains_guess: docs

33. **REPO-SIZE-1 decision** ([plan](docs/plans/2026-07-10-repo-size-history-rewrite-plan.md),
    [package](docs/plans/2026-07-12-repo-size-targeted-purge-package.md)) —
    STOP-FOR-HUMAN; plan recommends Option (d) forward-only + GitHub Support gc.

## ITEM L10635 [tier C] section: Infra (5)
primary_domain_guess: docs | all_domains_guess: docs

34. **Execution-manifest human gates**
    ([manifest](docs/plans/2026-07-10-execution-manifest.md)) — the residual gated
    tasks: INIT-5 T2 spike sign-off, INIT-6 spec review, INIT-7 greenlight, INIT-8
    review, REPO-SIZE-1.

## ITEM L10660 [tier C] section: UX (4)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

36. **1.16 — resizable/sortable columns** (H1:2750) — remaining: dedup results,
    activity log, iTunes write-back preview, metadata review.

## ITEM L10662 [tier B] section: UX (4)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

37. **1.17 — product rename/branding sweep** (H1:2751) — blocked on name decision.

## ITEM L10663 [tier B] section: UX (4)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

38. **3.8 Plex-style media server API; 3.9 LLM series detection; 3.10 AI cover art**
    (H1:2772-2774) — all [hold].

## ITEM L10670 [tier C] section: Other / close-out (10)
primary_domain_guess: internal/audiobooks | all_domains_guess: internal/audiobooks;internal/database;internal/maintenance;internal/server;internal/server/;internal/server/handlers;internal/server/interfaces.go;docs

40. **4.8 — Store ISP sweep** (H1:2787) — **RE-SCOPED 2026-07-18; the "~38-file + 18
    noop" count below was pre-reorg and is WRONG.** Re-audit found `database.Store` is a
    field/param in **~151 prod + 35 test files** (a package reorg since the April plan
    split `internal/server` into `internal/audiobooks|metafetch|merge|organizer|
    maintenance/jobs|server/handlers/*`, obsoleting the file lists in
    `docs/archive/superpowers/plans/2026-04-17-store-iface-sweep.md` — whose COMPLETE
    stamp reflected a deliberate "diminishing returns on the hubs" stop that STILL holds
    post-reorg). **Decision 2026-07-18: do the DI-seams + shallow-consumer subset only**
    (narrow the 8 `internal/server/handlers/*/interfaces.go` + `internal/server/
    interfaces.go`, plus genuinely-shallow post-April consumers; leave hubs/bootstrap/
    wiring/decorators wide with justification comments) — NOT the full 151-file sweep.
    Type-only change (no runtime/data impact); existing `mocks.Store` already satisfies
    every sub-interface so no wave triggers a mockery regen. Old sweep tooling
    (`scripts/{check_store_noops,narrow_struct_services,apply_narrowing}.py`) survives but
    its hardcoded file lists must be regenerated. ~~Not started~~ — **PARTIALLY DONE
    2026-08-17 (PR #2534): the `internal/maintenance/jobs` consumer is narrowed.**
    `MaintenanceJob.Run` now takes a 12-sub-interface `maintenance.JobStore` (187 methods)
    instead of `database.Store` (40 sub-interfaces, 398 methods) — a 53.0% cut across all
    37 jobs, with zero `Run` body changes and 8 job files dropping their
    `internal/database` import entirely. The union was measured from call sites (a lower
    bound) and then PROVEN by the type checker: the build only passes if every job and
    every helper it reaches fits inside the 12. Also confirmed the note above: no mockery
    regen was triggered, and exactly **1** test fake needed editing.
    ⚠️ Correcting an overclaim carried in earlier notes: **this does NOT delete
    `database.MockStore`** — it is imported far beyond this package and satisfies
    `JobStore` too, so the 14 job tests that build one still compile unchanged.
    **Still not started:** the 8 `internal/server/handlers/*/interfaces.go` +
    `internal/server/interfaces.go` DI seams, which remain the bulk of this item.

## ITEM L10706 [tier C] section: Other / close-out (10)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

42. **2026-05-01 re-audit block close-out pass** (H1:3137-3177) — TEST-2, DEP-1a-e,
    DEAD-1, CTX-4, LOG-5, R-9, R-10 mostly stale: DEP-1 0 non-test hits, DEP-1e moot
    (post-SQLite removal), PERF-1 OBSOLETE as scoped (Jul-16 truncation fix made
    whole-library ops deliberately unbounded). Needs a checkbox-level close-out.

## ITEM L10710 [tier C] section: Other / close-out (10)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

43. **WaitForWarmup hazard note** (H1:3118) — latent create-then-read-memdb test
    hazard; document or fix.

## ITEM L10712 [tier C] section: Other / close-out (10)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

44. **GFO-4 — graceful-file-ops sub-op phase tracking** — last open graceful-file-ops
    item.

## ITEM L10714 [tier C] section: Other / close-out (10)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

45. **Performance items #1/#2/#6** (2026-04-14 set) — still open.

## ITEM L10727 [tier B] section: Other / close-out (10)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

47. **Library centralization backlog** — needs a brainstorming session; future work.

## ITEM L10728 [tier C] section: Other / close-out (10)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

48. **Transcription quality filter** — ~40% of transcripts low-quality/unparsed; list
    endpoint omits transcription fields.

## ITEM L10730 [tier C] section: Other / close-out (10)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

49. **iTunes heal Layer-6 re-trigger** (H1:897) — re-run after path-heal residuals
    shrink (see pending-prod-actions).

## ITEM L10750 [tier C] section: Dedup + review consolidation (3) — 2026-07-18 owner request
primary_domain_guess: internal/dedup | all_domains_guess: internal/dedup;internal/fingerprint;internal/plugins/acoustid

50. **Fingerprint-confirmed dedup + shattered-book reassembly against the original
    source** (GROUNDED 2026-07-19 via read-only prod verification). Two related tests,
    added as signals on existing candidates — not a new pipeline:
    - **(a) Acoustic confirm** — where both sides of a candidate pair are fingerprinted,
      use `WholeFileSimilarity` closeness as a *confirming* signal to auto-promote the
      "same file, one extra character" title-leak near-dupes to auto-merge; distinct
      pairs fall back to today's scoring. Per-file acoustic signals already feed scoring
      (`exact_acoustid`/`lsh_acoustid`); this extends them + strengthens the
      `auto_resolve` gate (behind the existing `AutoResolveEnabled` kill-switch).
    - **(b) Shattered-book reassembly** — for a book split into many fragments (author-
      first shards of a multi-author anthology), match the fragments' per-file
      fingerprint **set** against the assembled ORIGINAL source folder (set containment
      `fragments ⊆ source_folder`) via the existing `fpidx` LSH index → the source
      folder whose file-set contains them identifies the true whole book. Metadata
      (album/iTunes-XML/PID/version-group) is the primary regroup key; the fingerprint-
      set match is the safety confirmation that makes the auto-regroup safe.
    - **Design constraints (owner, 2026-07-19):** dedup AGAINST the original source as the
      identity reference, but keep the organized (primary) copy canonical; reflink new
      files on import. **NEVER mutate the active iTunes tree** — read-only at most (see
      [[feedback_itunes_active_library_hands_off]]).
    - **VERIFIED (prod, read-only, 2026-07-19):** file-level raw-fingerprint coverage is
      **94%** (296,010 / 315,013 files; zero-duration count == 0, so the old Seg0
      over-count worry is moot — the "~65%" figure was stale/pair-level, NOT a current
      file-level blocker). **PREREQUISITE / the one real gap:** the assembled source-
      download root is NOT a configured scan path, so its folders are on disk but not in
      the DB (title search for a known source book = 0 hits). **Step 1 = scan + fpcalc-
      fingerprint the source root as a read-only REFERENCE corpus** (cheap — reflinks;
      distinct root from iTunes so the guardrail holds) and index into `fpidx`; only then
      does (b) have ground truth to match against. See
      [[project_dedup_assembled_source_ground_truth]].
    - Cross-ref: `internal/dedup/engine.go`, `internal/dedup/unified/auto_resolve.go`,
      `internal/dedup/split_book_detector.go`, `internal/fingerprint/`,
      `internal/plugins/acoustid/`.

## ITEM L10783 [tier C] section: Dedup + review consolidation (3) — 2026-07-18 owner request
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

51. **Overhaul the review interface ("make it not suck")** — the review page UX is a
    pain point. Needs a concrete redesign spec: read-only audit of the current review
    page (what it shows today, interaction friction, per-hold actions) → propose
    redesign. Ties to the review-queue track (A1/A2/B1 shipped; B2 apply path merged
    #1953, default OFF — see [[project_review_queue_regroup]]). Prereq for item 52.

## ITEM L10788 [tier C] section: Dedup + review consolidation (3) — 2026-07-18 owner request
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

52. **Consolidate the dedup page into the review page** — slim the dedup page down to
    run-control only (start/stop dedup runs + run status/progress); move ALL candidate
    and result display + review actions into the review page so there is one place to
    review everything. Depends on item 51 (the review UI must be good enough to absorb
    the dedup results first). Investigate current dedup-page vs review-page component
    boundaries before committing to a plan.

## ITEM L10831 [tier C] section: Remaining — execution state (briefs)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **T03** — **sandbox** purge wave: `maintenance.dedup-exact-triage {"apply":true}` (dismiss
      ~7,878 purgeable, op merged in #2008) → purge-stale → full-scan → measure vs 9,074
      baseline. Needs sandbox redeploy with current main first. NOT yet run **on the sandbox**
      — note prod (T04) went ahead and ran, so this is now a validation-parity gap, not a
      blocker.

## ITEM L10849 [tier C] section: Remaining — execution state (briefs)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **T13** — docs truth-up with measured sandbox/prod numbers (dedup/STATUS.md, pending-prod-actions.md, exec summary) — in progress

