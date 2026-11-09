<!-- file: docs/executive-summaries/2026-11-08-checkpoint-sweeper-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: e24cf04e-003b-49a3-b59f-da723d7c8df2 -->
<!-- last-edited: 2026-11-08 -->

# Upgrade safety copies of the database are now deleted after 7 days

**Date:** 8 November 2026

## Executive Summary

- Before an upgrade changes the library database, the server now saves a full
  copy of it so the upgrade can be undone. The first start of the next release
  makes one.
- That copy holds everything in the database, including saved passwords and
  sign-in tokens. Until now nothing deleted it.
- A cleanup job now runs once at every start, right after the server is ready,
  and then every hour. It deletes each copy 7 days after it stops being needed.
  The 7 days are fixed and no setting makes them longer.
- A copy from an upgrade that failed is kept while the server still depends on
  it to go back, and is deleted 7 days after that stops being true.
- The cleanup also checks that only the service account can read the copies.
  If any file or folder can be read by other accounts, it logs an error and
  names it. It never changes permissions itself.
- `audiobook-organizer storage prune-checkpoints` deletes the copies straight
  away. It works whether the server is running or stopped. It keeps a copy a
  failed upgrade still depends on unless you add `--include-open --confirm`.
- The admin status page data (`GET /api/v1/system/status`, signed in) now
  reports when a copy exists, when it will be deleted, and any copy with loose
  permissions. The public `/health` check reports none of this.

## Why it mattered

A copy of every password and token that stays on disk forever is a standing
risk. The copy sits next to the database with the same protection, but a
backup tool, a manual copy or a permissions mistake could expose it long after
anyone remembers it is there. Deleting it on a fixed schedule, and saying
plainly when one exists, keeps the undo safety net without leaving the secrets
behind.

## What to know

- After upgrading, a `.mediacore-checkpoints` folder appears next to the
  database for up to 7 days. That is expected.
- To remove it sooner, run `audiobook-organizer storage prune-checkpoints`.
