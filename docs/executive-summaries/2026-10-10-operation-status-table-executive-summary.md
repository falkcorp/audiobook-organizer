<!-- file: docs/executive-summaries/2026-10-10-operation-status-table-executive-summary.md -->
<!-- version: 1.0.1 -->
<!-- guid: d11ae7cc-0628-4909-9393-c82bc835df5b -->
<!-- last-edited: 2026-10-10 -->

# Operations: one list of run statuses for the whole app

PR [#3920](https://github.com/falkcorp/audiobook-organizer/pull/3920) (branch `task/05-pr1`). Task brief:
[05-PR1](../proposals/2026-10-holistic/tasks/05/05-PR1.md).

## Executive Summary

- **One definition.** Every state a background operation can be in
  (queued, waiting, running, finished, failed, canceled, and the "interrupted"
  family) and what each one means is now written down in one place. Before,
  seven pieces of server code and three pieces of web code each kept their own
  list, and those lists already disagreed with each other.
- **The web page and the server can no longer drift.** The web app's list is
  produced automatically from the server's. If someone adds a state on the
  server and forgets to update the web app, the automated checks fail.
- **Nothing you can see changes.** No saved operation is rewritten, and every
  progress bar, scheduled task and Retry/Discard button behaves as before.
  This is groundwork: it removes a kind of mistake rather than fixing a
  visible one.

## Why it mattered

The app has hit the same mistake several times: a new "interrupted" state was
added in one place, and another list did not know it. When that happened, the
web page showed a finished operation as still running, the bell badge counted
it, or a scheduled task waited on it forever. With one list, adding a state
means changing one file, and the checks find any place that was missed.
