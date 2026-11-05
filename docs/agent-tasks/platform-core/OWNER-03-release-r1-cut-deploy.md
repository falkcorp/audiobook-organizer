<!-- file: docs/agent-tasks/platform-core/OWNER-03-release-r1-cut-deploy.md -->
<!-- version: 1.0.0 -->
<!-- guid: dcd841c3-d463-456c-9860-5243f9a1dc22 -->
<!-- last-edited: 2026-11-05 -->

# OWNER-03 — Release R1: P12 disk headroom read, cut the R1 production release with its release-note lines, deploy, optional one-day smoke run

> **NOT AGENT WORK.** This is an owner checkpoint. No agent reads production, cuts the release, deploys, or ticks
> this row. The lead prepares the release-note lines (README "How to run a wave" step 5); the owner does the rest.

| Field | Value |
|---|---|
| ID | OWNER-03 |
| Release | R1 |
| Tier | **OWNER**: production read, release, deploy |
| Wave | W21 |
| Depends on | R1-01 … R1-30 (all merged) |
| Unblocks | **R1-31** (ends the R1 freeze after this release) and **every F2 task's merge**: R2's content lands dark while R1 is deployed (09 §9.3). F2 PRs may be opened earlier; only their merge waits. |
| Spec | 09 §9.4 R1 (Content: release note, Rollback, Owner), §9.7 P12, §9.2.1 (the R1 freeze), §9.3 |

## Why P12 first

The first R1 boot on a non-empty store takes the pre-migration checkpoint, a full copy of the database beside it,
kept 7 days (09 §9.2.3, §9.4 R1 Migrations). A disk without room halts the boot before it writes.

## Owner steps

1. **Check the content.** R1-01 … R1-30 are merged and R1's verification passed (09 §9.4 R1 Verification; plan
   §(b) R1 row), including `TestR0OpensR1Store`, both image-job runs and both `unit-lifecycle` cells.
2. **P12: disk headroom** (09 §9.7 P12), on the production host:

   ```bash
   df -B1 --output=avail <db-dir-parent>
   du -sb <db-dir>
   ```

   - **Branch A:** free space covers the checkpoint, the rewritten ranges and the compaction: deploy. (In R1 the
     checkpoint is the main cost; it is a full copy, so `avail` below the `du` figure is Branch B for certain.)
   - **Branch B:** free space first, or the boot halts before writing.
3. **Cut the R1 production release** on `main` (`release-prod.yml`, `workflow_dispatch`); the R1 freeze has kept
   `check-rc-ordinal` from promoting anything since R1-01, so cut it by hand. **Prepend** the R1 release-note lines,
   never replacing the generated notes. The lines of 09 §9.4 R1 "Content: release note", verbatim:

   > A restore now takes effect at the next restart, which the service performs itself; a container started without
   > a restart policy must be started again.
   >
   > In this release a factory reset or a database reset runs only from the command line, with the service stopped
   > (`audiobook-organizer storage reset`); the reset button in Settings returns with <R2 release>. Undoing a factory
   > reset restores the database but not the library files or Open Library data it deleted. A reset now leaves
   > `config.yaml` beside the database unchanged; delete it by hand for a full reset.
   >
   > The first start after this upgrade keeps a full copy of the database, including saved passwords and sign-in
   > tokens, beside it for 7 days; `audiobook-organizer storage prune-checkpoints` deletes it sooner.
   >
   > After an upgrade, open browser tabs offer to reload.
   >
   > `--version-json`, `--print-storage-levels` and `--print-upgrade-floor` print machine-readable build and store
   > facts.
   >
   > Paths of the form `/api/<name>/v<N>/…` are reserved for versioned APIs and are no longer redirected to
   > `/api/v1/`.
   >
   > Every response carries an `X-App-Build` header naming the build.

   Plus the HTTP-metrics lines of 09 §9.2.7 (the `http_route` value change among them) and 05-logging's log-file and
   level lines, which the lead copies from those sections. Replace `<R2 release>` with the planned R2 version.
4. **Deploy** (`make deploy`). The first boot writes core's `c/storage/` bookkeeping and, on a non-empty store,
   takes the checkpoint first. Watch it reach `"ready":true` on `/health`.
5. **Optional one-day smoke run** (09 §9.2.1; D11): sign in, a scan, a scheduled task, a phone-app sync, a reload
   prompt in an open tab after the deploy.
6. **Rollback if needed:** `make rollback` to R0 loses nothing (R0 ignores `c/storage/` rows,
   `TestR0OpensR1Store`); it is refused while a swap is in flight (09 §9.4 R1 Rollback).

## What to record, and where

One tracking-issue comment: `OWNER-03 recorded <date>: P12 Branch A | B (freed first); R1 release <tag>; deployed;
smoke run: done | skipped; kept | rolled back`. No sizes in absolute terms are needed beyond the branch; no hosts or
paths.

## Done when

The R1 release is cut and deployed (or rolled back with the reason) and the comment is recorded. The lead then runs
R1-31 (ends the R1 freeze) and lets the F2 merges proceed.
