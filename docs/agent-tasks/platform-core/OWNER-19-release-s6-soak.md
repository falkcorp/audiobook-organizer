<!-- file: docs/agent-tasks/platform-core/OWNER-19-release-s6-soak.md -->
<!-- version: 1.0.0 -->
<!-- guid: dc2a7e64-edaa-46a3-bedd-27fbc7410490 -->
<!-- last-edited: 2026-11-04 -->

# OWNER-19 — Release S6: deploy to the reference host, 14-day soak, stage-record ticks, soak row

> ## ⛔ NOT AGENT WORK
> This is an owner checkpoint. No agent may perform any step below: no agent deploys to the reference host, logs in with
> a live ABS client there, reads production config, runs `scripts/soak_record.py`, edits `ci/soak.txt`,
> `ci/soak_health_allow.txt` or `ci/prev_ref.txt`, ticks the stage record, or merges a PR that changes those files
> (09 §9.2.1). An agent asked to "do OWNER-19" stops and hands this brief to the owner.

**ID:** OWNER-19 · **Wave:** W15 (S6, the flip) · **Tier:** OWNER · **Duration:** at least 14 days of soak
**Depends on:** PC-15-04 merged (and with it PC-15-01 … PC-15-03).
**Unblocks:** W16 (S7). S7's live PRs merge only once the post-flip `S6` row exists (09 §9.2.1; `check_soak.py` checks
(a) and (b)).
**Owning spec:** 09 S6 (Rollback, Done when); 09 §9.2.1 "Soaks and owner checkpoints"; 09 §9.3 boot-time budget and
"Staging"; 09 §9.7 (P7, P9, P10); digest `docs/plans/platform-core/digests/S6-S7.md` §6, §8.

## Owner steps

1. **Release notes** for the `S6` production release, prepended to the generated body: S6 has no rollback-cost row
   (binary swap; outbox rows are left unprocessed by the older build). Add the line PC-15-04's PR description proposes
   for any stream-A frame type the flip dropped (for example `scan.auto_triggered`), if you approved dropping it.
2. **Boot-budget A/B** on the staging copy (09 §9.3) and append the result to the stage record.
3. **Deploy** the build carrying the flip (PC-15-04) to the reference host; `scripts/soak_record.py --deployed` after each
   deploy.
4. **Check after deploy:** the Activity page shows live progress; the Library patches changed rows without a reload; the
   shutdown banner appears on a restart; `GET /api/core/v1/events` streams for an authenticated session.
5. **P7 branch B live ABS-client check** (09 §9.7): if P7 found the ABS surface on with sessions, log in and sync progress
   with a real ABS client after each deploy of S6, and again after a rollback of it; record pass/fail only.
6. **P9 webhooks census before S7** (09 §9.7): `curl … /api/v1/config | jq '.plugins.webhook // empty | keys'` (counts
   and keys only, never values). Branch A, none: S7 serves no webhook instance. Branch B, some: one legacy-backed
   `notify/webhook-<n>` instance per URL with `legacy_headers`; record the count in the stage record for W16.
7. **P10 after each deploy**; update `ci/prev_ref.txt` in a bookkeeping-only PR.
8. **Watch the soak:** health stays `ok` except allow-listed tokens (a `core.outbox` Degraded `relay_disabled` is not on
   the list unless you set the dial on purpose); no unit exit other than 0 or 75; every owner at level 0.
9. **Tick the stage record's done-check list** (09 S6 Done when: the `text/event-stream` grep, `TestProjectionTopicsExistAtStage`,
   legacy-stream goldens per event type, `system.status` frames byte-shape-equal to S5's, R-test cells a, b and c from
   PC-15-04's `compat-rehearsal`, the outbox suite and the crash-matrix extension).
10. **Write the row:** once the deploy carrying the flip has served 14 days, run `scripts/soak_record.py --release S6`
    from the operator's checkout and merge the bookkeeping-only PR adding the row to `ci/soak.txt`.

## Done when

- `ci/soak.txt` holds a current `S6` row written by `scripts/soak_record.py --release S6`;
- the stage record ticks the stage's done-check list, with the P7 branch-B result (when P7 found ABS in use) and the P9
  census;
- P10 ran after each deploy and `ci/prev_ref.txt` is updated.
