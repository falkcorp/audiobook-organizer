<!-- file: docs/agent-tasks/platform-core/OWNER-20-release-s7-soak-row.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5912e5df-b83a-4e91-bac2-8eb92baedb4e -->
<!-- last-edited: 2026-11-04 -->

# OWNER-20 — Release S7: deploy to the reference host, 14-day soak, stage-record ticks, soak row

> **NOT AGENT WORK.** No agent may complete, simulate or shortcut any step of this checkpoint: no agent runs
> `scripts/soak_record.py`, deploys to the reference host, reads production, edits `ci/soak.txt`,
> `ci/soak_health_allow.txt` or `ci/prev_ref.txt`, reads the shared release workflow, or merges a PR that changes
> those files (09 §9.2.1 "Soaks and owner checkpoints"). An agent may only remind the owner that this checkpoint
> blocks W17.

| Field | Value |
|---|---|
| ID | OWNER-20 |
| Tier | OWNER — no agent may complete it |
| Wave | W16 (closes release `S7`) |
| Depends on | PC-16-06 (the S7 flip, and every earlier W16 PR, merged and deployed) |
| Blocks | every W17 merge (PC-17-01 … PC-17-05): their `Stage-Release: S8 …` trailers need a current `S7` row; PC-17-02 also needs the raw-asset reading below |
| Duration | 14 days of soak from the deploy that carries PC-16-06 (plan §(b) decision 2) |
| Spec | 09 §9.2.1 "Soaks and owner checkpoints"; 09 §9.3 boot-time budget and "Staging"; 09 §9.7 (P10); 09 S7 "Done when"; 09 S8 "Preconditions" |
| Digest | `docs/plans/platform-core/digests/S6-S7.md` §8; `docs/plans/platform-core/digests/S8-S10.md` §8 (S8 precondition) |

## Owner steps

1. **Deploy** the `main` build that contains PC-16-06 to the reference host with the committed `deploy` recipe. The
   recipe calls `scripts/soak_record.py --deployed` after its ready check; if a deploy was done by hand, run that
   command from the operator's checkout over ssh (`DEPLOY_HOST`, `DEPLOY_BIN`, `DEPLOY_STATE_DIR`).
2. **P10 after each deploy** (09 §9.7): read `<bin>.prev --version-json` and `<bin> --version-json` on the
   reference host and commit the resolved `PREV_REF` as `ci/prev_ref.txt` in a bookkeeping-only PR (the `commit`
   with a trailing `+dirty` stripped, else the `g<hex>` part of a describe-form `version`, else a manual ref; never
   `version=dev`).
3. **Staging checks** on a scrubbed, production-size copy (09 §9.3; `scripts/staging/provision.sh`): the boot-budget
   A/B with `scripts/boot_budget.py --prev <S6 bin> --cur <S7 bin> --db-copy <dir> --runs 5`; confirm every S7 boot of
   the copy carries `core.integrations.disabled=true` in the operator layer and that the S6 `prev` booted on the
   scrubbed copy attempts no outbound connection; record results in the stage record.
4. **Credentialed smoke scrape** of `/metrics` on the reference host (any non-confined credential, as at HEAD): 200,
   non-empty family list containing `process_resident_memory_bytes` and `go_goroutines` (05-metrics §6).
5. **Tick the stage record** (`docs/plans/platform-core/stage-ledger/S7.md`) against 09 S7 "Done when", item by item:
   `plugin.DownloadClient` deleted with `internal/plugin`; one production `download.Client` implementor; no package
   throttle/quota map; `test ! -e core/integrations/indexer && test ! -e core/arrfacade && test ! -e core/acquire`;
   webhook golden byte-equal to S6 for a `legacy_headers` instance; search fixture set equal to S6's; the private-IP
   legacy-backed instance and the non-admin refusals; `ErrFieldNotStorable` 422; the series golden adds only
   `<namespace>_ai_*`; the smoke scrape; `test_staging_scrub.py` S7 cases; the four R-test rows; the boot budget.
   Record the generated counts (aliases, importers) beside 09's informative ones.
6. **Release notes** of the S7 production release carry rows 7 and 8 of 09 §9.2.1 verbatim (prepended to the
   generated notes): "Webhooks queued at rollback are delivered after the upgrade, or dropped after 24 h." and
   "Integrations created on this release do nothing after a rollback; events during the rollback are not delivered
   to them."
7. **Before S8 (09 S8 "Preconditions"):** read `reusable-release.yml` at the SHA in `.github/ghcommon-ref.txt` and
   record in the stage record whether releases publish the raw asset `audiobook-organizer-<os>-<arch>` that the
   updater fetches (05-updater §5U.2 marks it UNKNOWN). If they do not, a manual apply through the core updater
   reports no update, as at HEAD; PC-17-02 must know which branch holds.
8. **Soak row.** Once the deploy that carries PC-16-06 has served 14 days with every pass criterion met (zero
   `restore_*` and boot errors since `<since>`, every owner at level 0 in both `--print-storage-levels` and
   `<db>.storage-levels`, one health read with no Critical component Down and no token outside
   `ci/soak_health_allow.txt`), run from the operator's checkout:

   ```bash
   scripts/soak_record.py --release S7
   ```

   It writes `.soak/S7.json` and prints the row only when every criterion passes. Add the printed row to
   `ci/soak.txt` in a bookkeeping-only PR (that file alone, or with the other two bookkeeping files) and merge it.
   If a health token from the `core.integrations.<capability>.<instance>` components is not yet in
   `ci/soak_health_allow.txt`, add it in that owner-edited file first.

## Done when

- `ci/soak.txt` holds a current `S7` row written by `scripts/soak_record.py --release S7` (its landing commit is the
  current one for `S7`).
- The stage record ticks the stage's done-check list (step 5) and records the raw-asset reading (step 7).
- P10 ran after each deploy and `ci/prev_ref.txt` was updated.

## Planner notes

None.
