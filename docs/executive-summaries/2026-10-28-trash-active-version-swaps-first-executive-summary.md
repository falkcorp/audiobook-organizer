<!-- file: docs/executive-summaries/2026-10-28-trash-active-version-swaps-first-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: c4e8a2d6-3f17-4b59-8d0a-9e1b6f7c2a35 -->
<!-- last-edited: 2026-10-28 -->

# Moving a book's current version to the trash no longer takes the book's audio with it

## Executive Summary

A book can keep several versions of itself, for example two recordings of the
same story, and one of them is the version the book plays. If you moved that
playing version to the trash, the book kept pointing at its audio files. Two
weeks later the nightly clean-up deleted those files, and the book was left with
nothing to play. The clean-up has since learned to refuse that deletion. This
change stops the situation from arising in the first place: moving the playing
version to the trash now first switches the book to another version, and only
then trashes the old one.

## What changed

- **The book switches to another version first.** The newest other version that
  has its audio files in its own storage folder and is not in the trash becomes
  the one the book plays. Its
  files are moved up to the book's folder, the old version's files are moved into
  the old version's own storage folder, and then the old version goes to the
  trash. When the clean-up later removes the old version, it removes only its own
  files.
- **If there is nothing to switch to, nothing happens.** When the playing version
  is the book's only version, or every other version is already in the trash, is
  being purged, or has no files, the request is refused with a message that says
  why, and the version stays exactly as it was. Add or restore another version,
  then try again.
- **If the switch fails, the old version is not trashed.** If the files cannot be
  moved, the book keeps the version it had: anything already moved is moved back
  and the versions get their old status again, so the book is never left without a
  playing version. If the files did move and the app then fails to record that, the
  request reports the error instead of going on to the trash.
- **The switch checks where the files are before it moves anything.** It only
  takes a version whose files are in that version's own storage folder inside the
  book's folder, and it refuses, saying why, when the book's own file path is
  empty or out of date, when a file is a shortcut (symbolic link), or when two
  versions point at the very same file. An earlier form of this change trusted
  those records. In two tested cases that meant either losing the only copy of a
  book's audio or leaving a book stuck half-switched, so every later attempt to
  trash a version of it failed.
- **The clean-up also refuses to delete when it cannot find what the book plays.**
  If none of the files the book is recorded as playing is actually on disk, or the
  book's path is a folder with no file in it, the clean-up cannot tell what is in
  use, so it leaves the trashed version alone instead of guessing. This now has its
  own line in the clean-up report, with the book's path to fix, instead of being
  described as "files missing, or a drive that is down".
- **A missing file stops the switch.** If a version's audio file is not where its
  record says it is, the switch refuses and says which file is missing. An earlier
  form took a stray file with the same name found nearby to be that version's audio
  and moved it. The switch also refuses to move a file onto a name that is already
  taken, and checks the storage folders themselves (a shortcut or a plain file in
  their place, or one pointing outside the book's folder) before it moves anything.
- **Undoing a failed switch never overwrites anything.** It puts back exactly the
  files that this attempt moved, and if one of their old names has been taken in
  the meantime it reports that rather than replacing what is there.
- **Trying again after a half-finished switch does not make it worse.** A version
  that is stuck in the middle of a switch cannot be trashed or switched again, and
  the error says plainly that the book needs repair and that deleting again will
  not help.
- **Versions set aside by the library are never switched in.** A "retired"
  version holds files that belong to another book. The switch refuses to touch
  one, in either direction, and the check is in place before anything is written.
- **Two version operations on one book cannot interleave.** Trashing a version
  now waits for any running merge to finish, and says to try again in a moment if
  one is still running.

- **Books stored as a folder of several files are not switched yet.** For those
  the app cannot yet tell where the other version's files belong, so it refuses
  and says so instead of moving files into the wrong folder.

## Why it matters

This was a path to silently losing the audio of a book. Before this change, the
safety net was the clean-up refusing to delete a file the book still plays. That
net is still there, but a book whose current version was in the trash kept
showing the trashed version's files, and any other way of removing them could
still lose them.

## Not done

A book left stuck in the middle of a switch (it has no playing version, and the
library may pick up a second one) is not listed anywhere yet, and there is no
automatic repair; that is tracked as a follow-up (TODO-SWAP-STUCK-RECOVERY). It
needs a failure after the files have moved, such as the database refusing a write,
and the change now refuses the cases that used to cause it.

Today nothing in the app puts another version's files into that version's own
storage folder, so for most books the switch will answer "no other version can take
over" until a follow-up teaches it to adopt files from elsewhere safely. That is
deliberate: refusing leaves everything as it was, while the earlier behaviour could
lose audio. The same follow-up covers books stored as a folder of several files.

The Versions panel in the book details offers a trash button only for versions
the book is not playing, so this change is reached through the API and any other
client, not through a new button. Showing the new refusal message in that panel,
and offering the button for the playing version, are left for a separate change.
