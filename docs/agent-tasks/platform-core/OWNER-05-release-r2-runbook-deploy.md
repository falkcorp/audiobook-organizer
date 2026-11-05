<!-- file: docs/agent-tasks/platform-core/OWNER-05-release-r2-runbook-deploy.md -->
<!-- version: 1.1.3 -->
<!-- guid: 671daa9f-45b1-4ffb-a1ce-a9b0fc7c953c -->
<!-- last-edited: 2026-11-05 -->

# OWNER-05 — Release R2: P12, the runbook's deploy steps 0–8, optional dry run and one-day smoke run, keep or roll back, then cut the R2 production release

> **NOT AGENT WORK.** This is the owner's R2 deploy. No agent deploys, runs a production read, takes or copies an
> archive, exports the keyring, renames env names on the owner's hosts, decides keep-or-rollback, cuts the release,
> or ticks this row. The operator runbook R2-01 commits is the procedure; this brief is the checklist 09 fixes.

| Field | Value |
|---|---|
| ID | OWNER-05 |
| Release | R2 (the one-way cutover) |
| Tier | **OWNER**: deploy, production reads, release |
| Wave | W43 |
| Depends on | R2-01 (the runbook and the R2 release note; it follows every flip, F8-04 and F9-03) |
| Unblocks | **R2-02** (ends the R2 freeze after the release) and every R3 task |
| Spec | 09 §9.4 R2 (Rollback, Owner), §9.3 item 2 (the optional dry run), §9.6 (what changes; last column feeds these steps), §9.7 P12; 05-config §2.4 (keyring export); 05-backup §5B.10 |

## Before you start

- D12 (a)–(d) are answered; what is left to you is whether to run the optional dry run and smoke run and, within the
  smoke day, whether to keep R2 or roll back (09 §9.4 R2 Owner).
- **Fixing forward is the default.** A rollback restore discards every write since the upgrade (imports, edits, phone
  progress) and **revives every credential revoked, user disabled or password changed on R2**; after a rollback,
  repeat those revocations (09 §9.2.1 "What a restore costs"; §9.6 row 25).
- **P12 again** (09 §9.7 P12): `df -B1 --output=avail <db-dir-parent>` vs `du -sb <db-dir>`. R2's first boot takes a
  checkpoint and rewrites ranges; Branch B (not enough room) means free space first, or the boot halts before
  writing.

## Owner steps: the deploy (09 §9.4 R2 Owner, steps 0–8, in this order)

0. **Merges and identity rows.** On the pre-freeze release, finish or undo every account merge in progress, and fix
   or delete each identity row the dry run reports as blocking (D13). A merge in progress or an unconvertible
   identity row refuses the upgrade (§9.6 row 24).
1. **Off-host archive.** Take an archive, verify it, and copy it off the host **together with `.encryption_key` and
   `<db parent>/config.yaml`**.
2. **Optional dry run** (09 §9.3 item 2), with the R2 binary on that archive, on the production host or any
   linux/amd64 machine:
   `audiobook-organizer storage migrate --dry-run --from-archive <archive>`: it reports per-step counts,
   quarantined rows and durations and writes nothing to the live store. To also boot the result:
   `storage restore-offline --archive <file> --into <dir>`, `<dir>` a new directory inside
   `.mediacore-checkpoints/scratch/`, booted in a container with `--network none` and
   `core.integrations.disabled=true`, reached through `docker exec`. The copy holds production data, plaintext
   secrets and session tokens: delete it afterwards (the checkpoint sweeper removes it 7 days after its mtime
   otherwise).
3. **Env renames.** Rename the env names that the host's own unit drop-ins, compose `.env` and plist set, per the
   rename table (OWNER-04's P4 list). A still-set retired protected or secret name refuses boot and names its
   successor.
4. **Deploy R2 as a tagged RC**: the newest `v<X.Y.Z>-rc.<N>` prerelease whose commit contains every R2 PR, R2-01
   included. Write the RC tag down; "Keep" cuts the release from it. Never deploy a local build or an untagged
   commit. Use `DEPLOY_READY_TIMEOUT` at the measured first-boot time the R2 release note gives (09 §9.3 "Boot
   time"). Expected version string: after the deploy, `audiobook-organizer --version` prints that RC tag's version
   (`v<X.Y.Z>-rc.<N>`); any other string means a different binary is running, so stop.
5. **Webhook receivers.** Switch each receiver to core's format (`X-Mediacore-Delivery`, the timestamped
   `X-Mediacore-Signature`; §9.6 row 5). The OIDC redirect URI is unchanged (row 4).
6. **Rules and dashboards.** Reload the Prometheus rules and Grafana dashboards R2 ships (§9.6 row 17).
7. **Keyring export.** After the first boot, `audiobook-organizer secrets export-keyring --out <file>` and keep the
   file at the off-host location with your backups (05-config §2.4; 05-backup §5B.10); health warns
   `secrets_keyring_not_exported` until then (05-config §2.4).
8. **Invites.** Re-send open invites (OWNER-04's P5 count; §9.6 row 9).

## Optional one-day smoke run (09 §9.4 R2 Owner, verbatim list)

Sign in to the web app; an ABS phone app keeps its session and syncs progress (P7); a library scan and a scheduled
task run once, with no catch-up storm; a webhook reaches its receiver; the Prometheus scrape answers and the alerts
evaluate; health shows no quarantined row.

## Keep or roll back

- **Keep:** cut the R2 production release from the RC tag you deployed:
  `gh workflow run release-prod.yml --ref <RC tag>` (`workflow_dispatch` accepts a tag ref, and the RC tag's workflow
  file is `main`'s). The release is then the deployed commit, and nothing merged after it during the freeze is
  included. Never dispatch with `--ref main`. Prepend R2-01's release note to the generated notes, never replacing
  them. The production tag this run creates names the same commit as the RC tag. It is R3's
  `UpgradeFloor.InstallFirst` (R3-02; 09 §9.4 R3). Take a new off-host archive after
  R2 (09 §9.6 row 19). R2-02 then ends the freeze.
- **Roll back:** `make rollback-restore` (09 §9.2.1, §9.4 R2 Rollback): the checkpoint, or the off-host archive with
  its state files, offline, plus the pre-freeze release, which the recipe checks against the source's levels.
  After an R2 hotfix: `make rollback-restore RELEASE=<pre-freeze tag>`. Then repeat every revocation made on R2.

## What to record, and where

One tracking-issue comment, no hosts, paths, secrets or values:

```
OWNER-05 recorded <date>
P12: Branch A | B; step 0: merges <n> finished/undone, blocking rows <n> fixed/deleted
dry run: done (<n> quarantined) | skipped; env names renamed <n>; webhooks switched <n>; keyring exported: yes
invites re-sent <n>; smoke run: done | skipped; deployed RC <rc tag>, version string matched: yes
decision: kept (R2 release <tag>, cut from <rc tag>) | rolled back (<reason>)
```

## Done when

R2 is deployed and kept with its production release cut, or rolled back with the reason recorded. The lead then runs
R2-02 and opens the R3 wave.
