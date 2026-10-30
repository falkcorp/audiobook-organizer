<!-- file: docs/executive-summaries/2026-10-29-protected-path-symlink-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 33552e57-ccc8-45c9-95a3-eafe9d145b0e -->
<!-- last-edited: 2026-10-29 -->

# A file reached through a shortcut can no longer slip past the iTunes protection

## Executive Summary

The app has a standing rule that it never deletes or moves files that belong to
the iTunes library, the folders watched for imports, or the quarantine folder.
The check that enforces it compared the written-out location of a file with the
protected folders, and it did not follow shortcuts (symbolic links). A file
named through a shortcut that pointed into a protected folder looked like it was
somewhere harmless, so the purge that permanently removes trashed books and old
versions could have deleted the real iTunes file behind the shortcut. The check
now looks at both the location as written and the location the shortcut really
leads to, and it can only ever protect more than before.

## What changed

- **Shortcuts are followed.** A file, or a folder holding the file, that is a
  shortcut into the iTunes folder, an "iTunes Media" folder, an import folder or
  the quarantine folder is now treated as protected. So is a file under a
  protected folder that is itself set up as a shortcut, and a shortcut whose
  target file is missing.
- **Nothing that was protected stops being protected.** The written-out location
  is checked first, exactly as before, and a match there is final.
- **A path that cannot be followed is not waved through.** If the shortcuts in a
  path cannot be worked out (a shortcut that loops back on itself, or a folder
  the app is not allowed to read), the purge keeps the file and the book stays in
  the trash to be retried, and a looping shortcut gives up after a few passes
  with a message. Tag writes and previews treat such a path as protected.
- **One check, used everywhere it matters.** The permanent purge of books and of
  versions, the organizer (which decides between moving and copying a file), the
  metadata write-back and the revert path now share the same shortcut-aware
  check. The shortcut-following code that already protected the merge tools was
  moved to a shared place so there is a single copy of it.

## What to know

- A test written earlier showed the problem: a trashed version listing a file
  through a shortcut into the iTunes folder had that file removed. That scenario
  is now covered by tests for both the book purge and the version purge, and the
  tests fail if the shortcut-following part is removed.
- Not changed: the Deluge seeding-folder list, the book restore classification,
  and a few maintenance tools still compare written-out locations only. They are
  listed in the pull request description for a decision.
