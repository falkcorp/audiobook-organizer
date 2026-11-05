<!-- file: docs/agent-tasks/platform-core/OWNER-06-release-r3-cut-deploy.md -->
<!-- version: 1.1.2 -->
<!-- guid: 87ccaa3f-5e58-4c9c-8d6f-888ce821b84e -->
<!-- last-edited: 2026-11-05 -->

# OWNER-06 — Release R3: confirm the keyring export and the off-host `.encryption_key` kept with every pre-R2 archive, cut the R3 production release, deploy, optional smoke run

> **NOT AGENT WORK.** This is an owner checkpoint. No agent touches the owner's archives or keys, cuts the release,
> deploys, or ticks this row.

| Field | Value |
|---|---|
| ID | OWNER-06 |
| Release | R3 (cleanup and the upgrade floor) |
| Tier | **OWNER**: confirmations, release, deploy |
| Wave | W47 |
| Depends on | R3-01, R3-02, R3-03, R3-04, R3-05 (all merged) |
| Unblocks | nothing in this plan (the last row) |
| Spec | 09 §9.4 R3 (Rollback, Owner), §9.2.2 (the R3 floor) |

## Why the confirmations come first

R3's first start runs books' start hook `legacy-file-cleanup`, which deletes `.encryption_key` (skipped, with a log
line and a retry at the next start, while `secrets_keyring_not_exported` is raised) and HEAD's `library.bleve`
directory. After that the **off-host `.encryption_key` is the only copy**, and restoring any pre-R2 backup needs it
and needs R2 installed first (09 §9.4 R3 Content and release note). R3 also adds the floor: a database that never ran
R2 stops the service at start with `install <R2 release> first` (exit 78).

## Owner steps

1. **Keyring exported**: health does not show `secrets_keyring_not_exported` (OWNER-05 step 7 done).
2. **`.encryption_key` kept off-host with every pre-R2 archive** you intend to keep; pre-R2 archives without it
   cannot be restored after R3. Delete pre-R2 archives you no longer want (09 §9.6 row 23).
3. **Cut the R3 production release** (`release-prod.yml`, `workflow_dispatch`) and **prepend** the R3 release note
   R3-04 committed (`docs/system/r3-release-note.md`, 09 §9.4 R3's lines verbatim), never replacing the generated
   notes (replace `<R2 release>` with R2's tag if R3-04 left the placeholder). Its core lines:

   > This release needs a database that ran <R2 release>. An older database stops the service at start with the
   > message `install <R2 release> first`: install that release, start it once, then upgrade.
   >
   > The first start of this release deletes the old settings key file `.encryption_key` and the old search index
   > directory. Keep your off-host copy of `.encryption_key` with any backup taken before <R2 release>: restoring
   > such a backup needs it, and needs <R2 release> installed first.

   followed by the macOS and Docker notes of 09 §9.2.2 and the removed `--db-type` and SQLite flags, which R3-04's
   file already carries.
4. **Deploy** (`make deploy`). R3 changes no data in the store (09 §9.4 R3 Migrations).
5. **Optional smoke run** (D11): the service starts, the log names the deleted `.encryption_key` as now only
   off-host, sign-in and a scan work.
6. **Rollback if needed:** `make rollback` to R2; R2 reads neither deleted file on a migrated store, so there is
   nothing to restore (09 §9.4 R3 Rollback).

## What to record, and where

One tracking-issue comment: `OWNER-06 recorded <date>: keyring exported yes; pre-R2 archives kept with
.encryption_key <n>; R3 release <tag>; deployed; smoke run: done | skipped; kept | rolled back`. No hosts, paths or
keys.

## Done when

R3 is released and deployed (or rolled back with the reason) and the comment is recorded. The workstream's releases
are then complete.
