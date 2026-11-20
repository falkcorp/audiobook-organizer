<!-- file: docs/executive-summaries/2026-11-19-rollback-restores-env-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 49fce5a5-0434-4ddf-987d-b3cc9ed091bd -->
<!-- last-edited: 2026-11-19 -->

# Going back to the old version now brings its settings back too

**Date:** 19 November 2026. The rollback commands now restore the old
version's settings along with the old program, and the deploy command refuses
to ship a setup that could not be rolled back.

## Executive Summary

- When a new release goes wrong, the rollback command puts the previous program
  back. Until now it put back only the program. The settings the service runs
  with (who may connect, how it signs people in, the owner's email) stayed as
  the new release left them. Once a release renames its settings, that is the
  old program started with settings it does not understand: it can fall back
  to letting anyone on the network in, and it loses its sign-in setup.
- Each deploy now keeps the previous settings beside the previous program,
  labelled with a fingerprint of that program, so a rollback finds the settings
  that belong to the exact program it is putting back (never just the most
  recent ones). Several sets are kept, and the set for the last version before a
  big storage or settings change is never pushed out by a run of small fixes. If
  the kept settings are missing or do not belong to the program, the rollback
  refuses before it stops anything, says why and lists what is kept, instead of
  doing half the job. A deploy that fails half way and is run again can no
  longer file the new settings under the old program.
- Today's server has no kept copy of anything: the deploy replaces the program
  in place and keeps no old one, so a rollback would have had nothing to go
  back to. The deploy command now checks, before it uploads anything, that it
  would keep one, and says what to change if it would not. It also warns when
  the deploy settings file still uses a name the new release no longer reads.
- Two one-time steps are needed from the owner before this takes effect, in this
  order: first install the rollback helpers on the server with one command
  (`make install-rollback-helpers`, which shows what it will do before it does
  it and checks the permissions file before putting it in place), then update the
  local deploy file from the committed example. Until then a rollback refuses
  rather than swap the program alone, and the deploy asks for a deliberate
  override, which it will not accept for the release that changes the settings
  names.
- Nothing about day-to-day use changes.
