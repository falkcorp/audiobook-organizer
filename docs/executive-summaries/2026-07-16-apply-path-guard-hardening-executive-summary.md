<!-- file: docs/executive-summaries/2026-07-16-apply-path-guard-hardening-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2a8f1e64-5c37-4b90-a1d6-9e30f7b5c8a4 -->
<!-- last-edited: 2026-07-16 -->

# Executive Summary: Closing Two Quiet Failure Gaps in Cleanup Actions

## What changed

When the app tidies up the library — merging duplicate iTunes entries, or combining
several entries into one — it sometimes deletes an entry or writes new information. We
found two places where, if a small internal read or write failed at the wrong moment,
the app carried on as if nothing had gone wrong.

- **Deleting a "now-empty" entry.** Before removing an entry the iTunes cleanup checks
  that it truly has no files and no leftover iTunes links. If that check itself failed to
  read — a momentary storage hiccup — the app treated the missing answer as "nothing
  there" and deleted the entry anyway. It could have removed an entry that still had
  audio or links attached. The check now treats an unreadable answer as "don't delete,"
  so it errs on the side of keeping things.
- **Losing a chosen author.** When you combine entries and type in the correct author,
  that choice is saved. If saving it failed, the app said "combined" anyway and the
  author you picked quietly vanished. It now records a warning when that save fails, so
  the loss is visible instead of silent.

## Why it mattered

Both are quiet-failure paths: no error was shown, so a rare storage or write hiccup could
have deleted an entry that wasn't really empty, or discarded a metadata choice, with
nothing in the interface to signal it. Neither erases audio from disk. The fixes make the
delete refuse to run when it can't confirm the entry is safe to remove, and make the
dropped-author case log a warning instead of passing silently. The delete-guard fix ships
with a test that simulates the failed check and confirms the entry is kept.
