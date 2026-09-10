# Scope 14 — 26 items

## ITEM L10500 [tier C] section: Dedup (10)
primary_domain_guess: docs | all_domains_guess: docs

2. **PH-2 — run `maintenance.dedup-exact-triage` on prod + review populations; PH-2b
   per-population purge wave** (H1:916) — never blanket-purge; four residual
   populations (see `docs/dedup/STATUS.md`). **Apply path now exists** (T03-BUILD):
   `maintenance.dedup-exact-triage {"apply":true}` dismisses purgeable classes
   (stub/title_leak) via `UpdateCandidateStatus(id, "dismissed")` — dry-run
   (`apply=false`, the default) is unchanged report-only. Unblocks brief T03's
   sandbox purge wave.

## ITEM L10507 [tier C] section: Dedup (10)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

3. **REVIEW-band candidate producer for the review queue** (H1:35) — B2 fast-follow;
   no commit yet.

## ITEM L10509 [tier C] section: Dedup (10)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

4. **Flip `review_apply_enabled` ON in prod** — apply path merged (#1953) but default
   OFF (6f2f7ce0); gated human decision (see DECISIONS-PENDING).

## ITEM L10511 [tier C] section: Dedup (10)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

5. **C8 — auto-file issues per `not_dup` cluster** (H1:1332; INIT-10 T5) — deferred.

## ITEM L10521 [tier C] section: Dedup (10)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

7. **Async breakdown-refresh for bulk/cluster dismiss** (H1:1877) — per-pair synchronous
   refresh may need an async variant at scale (latency note).

## ITEM L10523 [tier C] section: Dedup (10)
primary_domain_guess: docs | all_domains_guess: docs

8. **Omnibus detection + dedup** — spec-only
   ([`docs/superpowers/specs/`](docs/superpowers/specs/) 2026-05-31); not started.

## ITEM L10525 [tier C] section: Dedup (10)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

9. **Regression tests for the 2 untested deluge hydrate sites** (H1:568) — optional.

## ITEM L10526 [tier C] section: Dedup (10)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

10. **Hide system-sourced tags from the Browse-by-Tag cloud** (H1:433) — UX preference,
    not a bug.

## ITEM L10531 [tier C] section: Identification / metadata (5)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

11. **AI-enrichment tier for the ambiguous regroup pile** (H1:35) — B2 fast-follow;
    blocked on local Ollama capacity.

## ITEM L10533 [tier C] section: Identification / metadata (5)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

12. **Cover recovery fast-follow** (H1:35) — B2 fast-follow.

## ITEM L10534 [tier C] section: Identification / metadata (5)
primary_domain_guess: docs | all_domains_guess: docs

13. **Community audiobook fingerprint index (INIT-8)** — spec-only
    ([spec](docs/specs/2026-07-10-community-fingerprint-index-design.md));
    STOP-FOR-HUMAN brainstorm/review session required.

## ITEM L10537 [tier C] section: Identification / metadata (5)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

14. **Description fetch campaign — ~29,083 books without descriptions** (H1:790).

## ITEM L10538 [tier C] section: Identification / metadata (5)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

15. **LLM/embeddings backend-mode toggle** (extracted from the archived 2026-07-02
    status doc) — config enum + FE selector (disable-all / OpenAI-only / local-only /
    OpenAI+local-fallback) + model-download prompt; local target qwen2.5:7b-instruct
    on the GPU box. Status unverified — check before building.

## ITEM L10572 [tier C] section: Pipeline (8)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

17. **iTunes path-heal residuals** (H1:899-906) — 3,720 ambiguous / 5,349 not-found /
    4,734 doubled-path records still unresolved.

## ITEM L10574 [tier C] section: Pipeline (8)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

18. **AP-1b — physically co-locate survivor's files after Combine** (H1:936) — inside
    RootDir only.

## ITEM L10576 [tier C] section: Pipeline (8)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

19. **AP-3 duration-reextract ~721-book tail** (H1:949) — re-enqueue apply
    (see pending-prod-actions).

## ITEM L10582 [tier C] section: Pipeline (8)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

21. **CONS-18 Part 2 — file-tag duration write-back** (H1:1019; spec 2026-06-19 DRAFT)
    — config-gated; deferred until dedup re-scope settles.

## ITEM L10586 [tier B] section: Pipeline (8)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

23. **Fingerprint UI verifications ×2** (H1:1383-1384) — [hold] verify the 14K
    false-positive purge is visible in dedup UI; book-sig coverage % renders.

## ITEM L10591 [tier C] section: Workflow / ops (4)
primary_domain_guess: docs | all_domains_guess: docs

24. **Workflow system WF-0/2/3/4/5 (INIT-6)** (H1:1128-1133;
    [plan](docs/plans/2026-07-10-workflow-system.md)) — STOP-FOR-HUMAN spec review;
    WF-6 closed NOT-DOING. Implementation plan (owner-approved 2026-07-18, PR #1935):
    [`docs/plans/2026-07-13-workflow-system-implementation-plan.md`](docs/plans/2026-07-13-workflow-system-implementation-plan.md)
    — grounds the spec against HEAD; recommends **build WF-2, defer WF-3/WF-4/WF-5**
    (INIT-1 T5+T6 shipped, so WF-3's headline use case exists without it; the spec's
    completeness gate is blind to the nested-config `label_refinement` family).

## ITEM L10598 [tier B] section: Workflow / ops (4)
primary_domain_guess: docs | all_domains_guess: docs

25. **PD-1 — subprocess isolation via parent-RPC bridge + MDA3 `Isolate:false` revert**
    (H1:1554-1561, 1435-1438; [spec](docs/specs/subprocess-isolation-rpc.md)) — [hold].

## ITEM L10603 [tier C] section: Workflow / ops (4)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

27. **Responses-API migration AI-RESP-A/B/E/F (INIT-7)** (H1:2596-2603) — [hold,
    do-not-start-without-greenlight]. AI-RESP-C/D closed.

## ITEM L10608 [tier C] section: Logging / verification / security-ops (5)
primary_domain_guess: docs | all_domains_guess: docs

28. **SLOG-PROD-VERIFY** (H1:2038; [runbook](docs/operations/slog-prod-verify.md)) —
    live prod smoke test of the op-activity chain.

## ITEM L10610 [tier C] section: Logging / verification / security-ops (5)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

29. **SLOG-W13 residual ~1,346 raw slog calls** (H1:2037) — remaining calls enumerated
    out-of-scope (no-ctx funcs, lifecycle, background); candidate to CLOSE with a
    scope note.

## ITEM L10613 [tier C] section: Logging / verification / security-ops (5)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

30. **SEC-AUDIT-11 — CodeQL bulk-dismissal rationales** (H1:2267) — GitHub-console
    action.

## ITEM L10615 [tier C] section: Logging / verification / security-ops (5)
primary_domain_guess: docs | all_domains_guess: docs

31. **PD-3 — post-deploy prod verification checklist** (H1:1568-1574;
    [checklist](docs/pd3-prod-verification.md)) — checklist exists, never filled in.

## ITEM L10617 [tier C] section: Logging / verification / security-ops (5)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

32. **I1 + I6 — prod pprof verification** (H1:1515, 1538) — measure chromem-lazy
    effect + heap re-audit; measurement only.

