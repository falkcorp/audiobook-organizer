<!-- file: docs/executive-summaries/2026-09-09-the-server-that-forgot-where-it-lived-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4e18a5d7-92c3-4b60-8f1a-d735c096be24 -->
<!-- last-edited: 2026-09-09 -->

# The server that forgot where it lived

**Pull requests:** [#3168](https://github.com/falkcorp/audiobook-organizer/pull/3168),
[#3169](https://github.com/falkcorp/audiobook-organizer/pull/3169)

## Executive Summary

- The app's database was moved to a new, faster, better-protected drive. The move
  itself worked. **The app then refused to start**, reporting that its database was in
  a folder that no longer existed.
- It was not lost. It had **already opened the database in its new home, successfully**,
  and was reading from it. What it could not cope with was its own leftover note about
  where that database used to be.
- Here is the odd part. The app stores its settings *inside its own database*. One of
  those settings is "where the database is". So on start-up it opened the new database,
  read the old address out of it, believed the old address over the instruction it had
  just been given, and then refused to run because the old address pointed at nothing.
- **The instruction to use the new location was being thrown away every single time.**
  Whoever ran the app could tell it where the database was — on the command line, or
  through the server's own configuration — and the app would quietly ignore both and
  use the address saved months earlier. Changing where the database lived required
  editing the database you were trying to move.

## Why this mattered more than a failed start-up

- A start-up failure is loud, and loud problems get fixed. The version of this that
  stays quiet is the dangerous one: had the old folder still existed, the app would
  have started normally and simply used the **wrong database** — an old copy, or an
  empty one — while every instruction on screen said otherwise. Nobody would have had
  any reason to look.
- That quiet version was in fact already happening, one level down. When the server was
  brought back up on a temporary workaround, its logs showed it running **split across
  two drives**: the book database on the new fast drive, but the **search index** and
  the emergency access token still being written to the old location. The search index
  it created there was brand new and empty, so **search results were wrong** — the real
  index had been moved with everything else and was sitting unused.
- So the fix does more than let the server start. It reunites the search index with the
  database it describes.

## What changed

- **The app now does what it is told.** A database location given on the command line,
  or set in the server's configuration, takes precedence over the one saved inside the
  database. The saved value is still used when nobody specifies anything, which is the
  normal case for ordinary installations.
- **Error messages now say what actually went wrong.** Every failure to inspect the
  database folder was previously reported as "does not exist" — so a permissions
  problem, a folder that was really a file, and a genuinely missing folder all produced
  the same misleading sentence. The first hour of this outage was spent chasing a
  permissions theory that the message made impossible to confirm or rule out.
- **The database location is now a setting on the Settings page**, alongside the
  activity-log location that was already there. When the server's own configuration is
  pinning the location, the field is shown as read-only **and names what is pinning
  it** — the specific setting or the specific command-line option. A greyed-out box
  with no explanation sends people to change the wrong thing.
- The setting carries a plain warning that changing it **does not move any data**, and
  that the old database is never deleted — so a wrong entry is recoverable by putting
  the old path back rather than being a disaster.

## Status

- Production is running normally. It was restored the same afternoon on a temporary
  workaround that made the old folder exist again; that workaround is removed once the
  corrected version is deployed, and the file containing it carries the exact test for
  whether it is still needed.
- No data was lost at any point. The database was never actually written to the wrong
  place — only described as being there.
