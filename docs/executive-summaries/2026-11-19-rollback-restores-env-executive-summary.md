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
  written in one step so the two cannot get out of step. A rollback puts both
  back. If the kept settings are missing, or belong to a different program, the
  rollback refuses before it stops anything, and says why, instead of doing half
  the job.
- Today's server has no kept copy of anything: the deploy replaces the program
  in place and keeps no old one, so a rollback would have had nothing to go
  back to. The deploy command now checks, before it uploads anything, that it
  would keep one, and says what to change if it would not. It also warns when
  the deploy settings file still uses a name the new release no longer reads.
- Two one-time steps are needed from the owner before this takes effect: update
  the local deploy file from the committed example, and install the rollback
  helpers on the server. Neither is a command to paste; the notes list the
  files. Until then a rollback refuses rather than swap the program alone, and
  the deploy asks for a deliberate override.
- Nothing about day-to-day use changes.
