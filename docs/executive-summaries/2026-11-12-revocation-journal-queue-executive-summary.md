<!-- file: docs/executive-summaries/2026-11-12-revocation-journal-queue-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: b16666c5-1612-427e-8069-5808a92bac5e -->
<!-- last-edited: 2026-11-12 -->

# A lockout can no longer be lost while the server replays its revocation log

**Date:** 12 November 2026. Follow-up to the revocation log work merged as
PR 4622. That work is built but not yet switched on: nothing in the running
server uses it until a later change wires it into startup, and this fix lands
before that happens.

## Executive Summary

- When an administrator disables a user, deletes an account or revokes an API
  key, the server writes that action to a separate, tamper-evident log as well
  as to the database. If the database is later restored from an older backup,
  the server replays that log at startup so that people who were locked out
  stay locked out.
- A log entry written by a newer version of the server, of a kind this version
  does not understand, used to freeze the replay. The server held its place in
  the log just below that entry, so every later start replayed everything after
  it again, undoing an administrator's re-enable or a new password each time.
  Such an entry is now set aside in a durable waiting list, the replay carries
  on past it, and the first version that understands the entry applies it.
- When the replay would have locked out the last administrator, it keeps that
  account's access and restricts it instead. That restriction was written to
  the log in a way that moved the server's place in the log to the very end. A
  crash right after it left every entry the replay had not reached yet behind
  that place, so they were never applied. The restriction now runs only after
  the whole replay is done, and the entry that caused it is marked finished
  only after the restriction exists.
- Restores that could not write their own entry in the log shared a single
  identifier when the server copied audit records forward, so a second such
  restore skipped records it had never written. Each restore now gets its own
  identifier.
- The new crash tests stop the replay before each step of it, and on either
  side of the restriction, and check what the store holds before the next
  start touches it. Against the old order, eight of those stopping points
  failed.

## The replay froze on an entry from a newer version

**What it was.** The replay stopped at the first log entry whose kind it did
not recognise, and kept its place in the log just below it. Entries after it
were applied without moving that place.

**Why it mattered.** After a rollback to an older version, every start applied
the same later entries again, so an administrator's re-enable of a user or a
newly set password was undone each time. The log was also closed to new entries
for good.

**The fix.** Each entry it cannot read is first written to a waiting list. The
place in the log then moves past it like any other entry. A version that
understands the entry applies it once, only if the log still holds that exact
entry, and removes it from the list in the same step. The lock-down of sign-ins,
which the server does when it cannot be sure a lockout was applied, happens
once for each waiting entry. Entries that wait for
a log that cannot be trusted, or that the log no longer holds, stay on the
list and are reported at every start.

## A crash after the last-administrator restriction lost later lockouts

**What it was.** The restriction of a kept last administrator is itself a log
entry, and writing it moves the server's place in the log to the end of the log.
It ran as soon as the batch that kept the administrator had been saved, before
the rest of the log had been applied or set aside.

**Why it mattered.** A crash in that moment left every entry the replay had not
reached behind the saved place, so a user who had been disabled stayed active
and nothing would ever try again. A crash between the batch and the restriction
had the opposite effect: an administrator the log showed as demoted kept full
access, with nothing left to replay the restriction from.

**The fix.** The replay now records the administrators it kept and restricts
them only after every entry has been applied or set aside. A batch that kept an
administrator does not move the saved place, and does not remove its waiting-list
entry, until the restriction has been written. A crash before then simply makes
the next start repeat the batch, which is safe to repeat. If an administrator is
kept twice, the restriction is written once.

## Audit copies of two restores were treated as one

**What it was.** After a restore the server copies the previous store's audit
records forward. Each copy was labelled with the restore's number in the log,
and a restore whose log entry could not be written had number zero.

**Why it mattered.** Two such restores shared the label, so the second found the
first's records already "copied" and wrote none of its own.

**The fix.** A restore with no log entry now gets a random identifier that stays
the same if the work is repeated after a crash and is cleared when the work
finishes. The identifier is stored with the data, so a restore that swaps the
store out starts fresh.

## What to know

- None of this changes a running server today. The replay is wired in by a later
  change, which must treat any error from the replay as a reason not to serve.
- Entries left on the waiting list have no clearing command yet, and no health
  alert reports a non-empty list. Both are filed for the change that wires the
  replay in.
- When one log entry would lock out every administrator, the server keeps all the
  administrators of that role and restricts each of them. A first version of this
  fix did that one administrator at a time, so a crash after the first left the
  second with full access. That case needs only one log entry, not two, and the
  reviewers found it. All the kept administrators are now restricted in a single
  step. A kept administrator that a later entry deletes no longer stops the start.
- Two limits remain, both for the change that wires the replay in. A re-enable
  made by an administrator is not in the log, so an old entry applied by a newer
  version can undo it, however long the wait was. An entry that cannot be read at
  all stops every start until someone removes it, and nothing removes it yet.
