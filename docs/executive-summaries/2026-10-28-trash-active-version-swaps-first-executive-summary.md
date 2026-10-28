<!-- file: docs/executive-summaries/2026-10-28-trash-active-version-swaps-first-executive-summary.md -->
<!-- version: 1.0.0 -->
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
  has its audio files and is not in the trash becomes the one the book plays. Its
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
  moved, the book keeps the version it had. When nothing has been moved yet, the
  book is put back as it was, so it is never left without a playing version.
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

The Versions panel in the book details offers a trash button only for versions
the book is not playing, so this change is reached through the API and any other
client, not through a new button. Showing the new refusal message in that panel,
and offering the button for the playing version, are left for a separate change.
