<!-- file: docs/executive-summaries/2026-11-19-integrations-cutover-step-m6-executive-summary.md -->
<!-- version: 1.0.1 -->
<!-- guid: 7c0e5a39-b4d2-4f86-a1e3-92d57b6c08f4 -->
<!-- last-edited: 2026-11-19 -->

# The step that moves your connections, jobs and folders to the new storage is built, and switched off

**Date:** 19 November 2026. This is one piece of the move to the new platform
core. It changes nothing for a running install yet: the step is written and
tested but not registered to run at boot, which is a separate, later piece of
work.

## Executive Summary

- **What it will do.** On the first start after the upgrade, one step moves
  everything the old release kept in its settings blob and in its own rows over
  to where the new release looks for it: your metadata sources (Hardcover,
  Google Books and the rest), your AI endpoints, the Deluge connection, the
  webhook settings, your AI jobs, and the library folders and import paths.
  Each keeps its ID, so anything that points at one still finds it.
- **Nothing is lost or counted twice.** Every old row is either moved or set
  aside in a quarantine area with its original bytes; none is overwritten and
  none is deleted without a copy. If the computer stops half way, the next start
  carries on from the last finished chunk. Running the step a second time does
  nothing.
- **Passwords and keys stay out of the rows.** Secrets are carried as references
  to the secrets store, never as text in a row. A secret that could not be read
  during the earlier settings step keeps its connection, marked for you to
  re-enter the key; nothing is guessed. The webhook signing secret is the one
  deliberate exception, stored as the new system expects.
- **Things that are reported, not carried.** A few details have no home in the
  new storage: the Deluge background-service host and login, some AI job
  counters, and a webhook's "requires auth" flag. The step lists each of these
  in its report instead of dropping them quietly. A web address that does not
  parse is kept as stored and flagged as misconfigured.
- **A preview that cannot get in the way.** A separate preview function says,
  kind by kind, what would move and what would be set aside, and writes nothing
  at all, not even the bookkeeping a real run keeps. An earlier version ran the
  preview through the real machinery, which would have made the real start-up
  that followed skip every row.
- **Saved keys stay usable on the first start.** For each connection the step
  works out the address it will really use, so an address an administrator set in
  the server's environment (the Deluge one, for example) counts, and a saved key
  is not held back until someone re-saves the connection.
- **Settings the new storage refuses are set aside, not fatal.** If the new
  storage rejects a list of library folders, mappings or protected paths, the
  rows that fed that list are quarantined with their original bytes and the start
  carries on.
- **Why it matters.** This is a one-way change to the data of a library with a
  lot of history. The step was checked against a real-shaped sample of the old
  data (with invented names) and against a simulated power cut at every chunk.
