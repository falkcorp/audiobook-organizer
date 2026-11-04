<!-- file: docs/agent-tasks/platform-core/OWNER-18-release-floor-soak.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6355914e-a70c-4961-bcdf-d5e5420a2294 -->
<!-- last-edited: 2026-11-04 -->

# OWNER-18 — Release floor: deploy to the reference host, 14-day soak, stage-record ticks, soak row

> ## ⛔ NOT AGENT WORK
> This is an owner checkpoint. No agent may perform any step below: no agent reads the production store's sidecars,
> installs a stepping-stone release, deploys to the reference host, runs `scripts/soak_record.py`, edits `ci/soak.txt`,
> `ci/soak_health_allow.txt` or `ci/prev_ref.txt`, ticks the stage record, or merges a PR that changes those files
> (09 §9.2.1). An agent asked to "do OWNER-18" stops and hands this brief to the owner.

**ID:** OWNER-18 · **Wave:** W14 (release `floor`, 05-tasks T8) · **Tier:** OWNER · **Duration:** at least 14 days of soak
**Depends on:** PC-14-01 merged.
**Unblocks:** W15 (S6). `floor` is always S6's predecessor: no `S6 1` PR and no S6 flip merges until this row is current
(09 §9.2.1; `check_soak.py` checks (a) and (b)).
**Owning spec:** 09 §9.2.2 "Floor release and stepping-stone release"; 09 §9.2.1 "Soaks and owner checkpoints"; 09 §9.3
boot-time budget and "Staging"; 09 §9.7 (P10, P13); 05-tasks §5T.6.2 "the floor release"; 02 N14; digest
`docs/plans/platform-core/digests/S5.md` §6, §8.

## Owner steps

1. **At the PR's merge:** confirm the stepping-stone tag PC-14-01 names is the newest production tag that existed when it
   merged, and that `TestStepSteppingStoneValid` was green on the merged commit (digest S5 §8).
2. **Release notes** for the `floor` production release, prepended to the generated body: name the stepping-stone
   release; state that stores that have not applied books' steps up to `LegacyCap` must install it first; tell macOS
   users to `launchctl bootout` the job and Docker users to read the last line of `docker logs` and stop the container
   (09 §9.2.2). Confirm the binary's last log line on refusal reads `install <release> first`.
3. **P13 before the deploy** (09 §9.7): `sudo -n cat <db>.storage-applied` on the reference host beside
   `<new-bin> --print-upgrade-floor`.
   - Branch A, the store meets the floor: deploy.
   - Branch B, below: install and run the stepping-stone release first, let it apply its steps, re-run P13, then deploy.
   The S0+ updater and the `deploy` recipe refuse the same way (`stepping_stone_required`); do not override them.
4. **Boot-budget A/B** on the staging copy (09 §9.3) and append the result to the stage record.
5. **Deploy** the floor binary to the reference host; run `scripts/soak_record.py --deployed` after each deploy.
6. **P10 after each deploy**; update `ci/prev_ref.txt` in a bookkeeping-only PR.
7. **Watch the soak:** health stays `ok` except allow-listed tokens; no unit exit other than 0 or 75 (an exit 78 means a
   floor refusal: stop and investigate, never restart in a loop); every owner at level 0.
8. **Tick the stage record's done-check list** for T8 (no `registry.` import in ABO, `TestUpgradeFloor_RefusedBelowLegacyCap`,
   `TestStepSteppingStoneValid`, `TestRestoreBelowUpgradeFloorRefusedAtStaging`, the `compat-rehearsal` result of PC-14-01).
9. **Write the row:** once the floor deploy has served 14 days, run `scripts/soak_record.py --release floor` from the
   operator's checkout and merge the bookkeeping-only PR adding the row to `ci/soak.txt`.

## Done when

- `ci/soak.txt` holds a current `floor` row written by `scripts/soak_record.py --release floor`;
- the stage record ticks the stage's done-check list and records P13's branch;
- P10 ran after each deploy and `ci/prev_ref.txt` is updated.
