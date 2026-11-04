<!-- file: docs/agent-tasks/platform-core/OWNER-11-release-s4r1-soak.md -->
<!-- version: 1.0.0 -->
<!-- guid: cbf86121-8034-4deb-bf2b-8592af3596fa -->
<!-- last-edited: 2026-11-04 -->

# OWNER-11 — Release S4r1: deploy to the reference host, 14-day soak, stage-record ticks, soak row

> ## ⛔ NOT AGENT WORK
> This is an owner checkpoint. No agent may perform any step below: no agent deploys, reads production, runs
> `scripts/soak_record.py`, edits `ci/soak.txt`, `ci/soak_health_allow.txt` or `ci/prev_ref.txt`, commits the P1
> branch-B list, or merges a PR that changes them (09 §9.2.1). An agent asked to "do OWNER-11" stops and hands this
> brief to the owner.

**ID:** OWNER-11 · **Wave:** W10 (S4 r1, dark) · **Tier:** OWNER · **Duration:** at least 14 days of soak
**Depends on:** PC-10-01, PC-10-02, PC-10-03, PC-10-04, PC-10-05, PC-10-06 (all merged).
**Unblocks:** W11 (PC-11-01 and the S4 r2 flip).
**Owning spec:** 09 S4 (Releases r1; Done when: r1's soak row and decode census); 09 §9.2.1 "Soaks and owner
checkpoints"; 09 §9.3; 09 §9.7 (P1, P2, P3, P5, P7, P10); digest `docs/plans/platform-core/digests/S4.md` "Owner
checkpoints".

## Owner steps

1. **Production reads before r2 can start** (09 §9.7; counts only, never values, never `sess:` contents), on
   production or a restored copy:
   - **P1** db-census per identity family, plus the **P4d decode census** that the r1 build runs at P4d step 1c and
     logs (every `u:`, `apikey:`, `role:` row read through books' legacy stores; undecodable rows counted and listed);
   - **P2** custom (non-seed) roles: each is mapped through 05-rbac §6.1; record the count;
   - **P3** API keys with empty scopes: record the count (read as `LegacyFullScope`, kept as written);
   - **P5** outstanding invite TTLs and external API callers;
   - **P7** ABS surface in use (sets whether OWNER-12's live ABS-client check is needed).
2. **Commit the P1 branch-B list:** the rows the owner leaves restricted on purpose, with reasons, in the S4 r1 stage
   record (`docs/plans/platform-core/stage-ledger/S4.md`). r2 may start only when the decode census lists **no**
   undecodable `u:`, `apikey:` or `role:` row outside that list (09 S4 Releases; A-9).
3. **Boot-budget A/B** on the staging copy for `S4r1` (09 §9.3); record both values.
4. **Deploy** the build carrying r1's last PR to the reference host; `scripts/soak_record.py --deployed` after each
   deploy. Confirm on the reference host what `TestDarkRelease_HEADSeedWritersKeepRunning` asserts: every restart
   writes a new bootstrap token file; core writes no identity row.
5. **P10 after each deploy**; update `ci/prev_ref.txt` in a bookkeeping-only PR.
6. **Mock-conversion timing** (04 open item 1): record the decision in the stage record.
7. **Tick the stage record's done-check list** for r1 (09 S4 Done when, r1 items).
8. **Write the row:** after 14 days on the deploy carrying r1's last PR, run `scripts/soak_record.py --release S4r1`
   from the operator's checkout; merge the bookkeeping-only PR adding the row to `ci/soak.txt`.

## Done when

- `ci/soak.txt` holds a current `S4r1` row written by `scripts/soak_record.py --release S4r1`;
- the stage record ticks the stage's done-check list, holds the P1 branch-B list, the P2 mapping count and the P3
  empty-scope key count, and the decode census lists no undecodable row outside the branch-B list;
- P10 ran after each deploy and `ci/prev_ref.txt` is updated.

## Notes

S4 r1 is never a `shipped_stage:` value and has no flip PR; its first PR (PC-10-01) set `release: S4r1`. Rollback row 3
(`ABO_CORE_AUTH_MODE`) applies from r1; its release-note text is in the S4 digest.
