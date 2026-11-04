<!-- file: docs/agent-tasks/platform-core/OWNER-12-release-s4r2-soak.md -->
<!-- version: 1.0.0 -->
<!-- guid: e34f351c-12ca-4e4f-93f5-7cca8bf84619 -->
<!-- last-edited: 2026-11-04 -->

# OWNER-12 — Release S4r2: deploy to the reference host, 14-day soak, stage-record ticks, soak row

> ## ⛔ NOT AGENT WORK
> This is an owner checkpoint. No agent may perform any step below: no agent deploys, logs in with a live ABS client on
> the reference host, runs `scripts/soak_record.py`, edits `ci/soak.txt`, `ci/soak_health_allow.txt` or
> `ci/prev_ref.txt`, or merges a PR that changes them (09 §9.2.1). An agent asked to "do OWNER-12" stops and hands this
> brief to the owner.

**ID:** OWNER-12 · **Wave:** W11 (S4 r2, the flip) · **Tier:** OWNER · **Duration:** at least 14 days of soak
**Depends on:** PC-11-01, PC-11-02, PC-11-03, PC-11-04 (all merged).
**Unblocks:** W12 (S5 r1); contract C4's window starts with S4 r2's ship date (OWNER-24).
**Owning spec:** 09 S4 (Releases r2, Rollback, Done when); 09 §9.2.1 "Soaks and owner checkpoints", rows 3, 4, 11;
09 §9.3; 09 §9.7 (P7, P10); digest `docs/plans/platform-core/digests/S4.md` "Owner checkpoints".

## Owner steps

1. **Release notes** for the S4 r2 production release, prepended to the generated body: 09 §9.2.1 rows 3, 4 and 11
   (texts in the S4 digest "Rollback costs"); "With authentication off, credential and backup routes now refuse
   requests; turn authentication on to use them." (09 §9.2.6); "A missing or wrong confirmation now answers 422
   instead of 400." (reset paths); the `auth_disabled_remote` note; row 12's authentication 401/403 metric line.
2. **Boot-budget A/B** on the staging copy for `S4r2` (09 §9.3); the first r2 boot appends a `gap` line to the
   revocation journal (r1 listed no `journal` duty), which is expected.
3. **Deploy** the build carrying r2's last PR (PC-11-03) to the reference host; `scripts/soak_record.py --deployed`
   after each deploy.
4. **P7 branch B live ABS-client check** (09 §9.7): if P7 found the ABS surface on with sessions, log in with a live ABS
   client on the reference host after each deploy of S4 and after a rollback of one; record pass/fail only.
5. **Audit check:** `GET /api/core/v1/audit?kind=auth.login` shows the owner's own login after the deploy (09 S4 Done
   when).
6. **P10 after each deploy**; update `ci/prev_ref.txt` in a bookkeeping-only PR.
7. **Watch the soak:** health stays `ok` except allow-listed tokens; `auth_disabled_remote` on an auth-off install is
   expected and is shown with a Settings banner.
8. **Tick the stage record's done-check list** (09 S4 Done when, r2 items, including the R-test identity cells and
   cells a, b, c and e from `compat-rehearsal` on the flip PR).
9. **Write the row:** after 14 days on the deploy carrying r2's last PR, run `scripts/soak_record.py --release S4r2`
   from the operator's checkout and merge the bookkeeping-only PR adding the row to `ci/soak.txt`.

## Done when

- `ci/soak.txt` holds a current `S4r2` row written by `scripts/soak_record.py --release S4r2`;
- the stage record ticks the stage's done-check list (and the P7 branch-B result when P7 found ABS in use);
- P10 ran after each deploy and `ci/prev_ref.txt` is updated.
