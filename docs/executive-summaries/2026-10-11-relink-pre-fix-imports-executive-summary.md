<!-- file: docs/executive-summaries/2026-10-11-relink-pre-fix-imports-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: c8b260fc-3025-4657-b8a0-573cbea538a5 -->
<!-- last-edited: 2026-10-11 -->

# Repair: link files imported before the 10-10 fix to their versions

Branch `feat/relink-pre-fix-imports`.

## Executive Summary

- **What was left behind.** Until 2026-10-10, every import recorded a
  "version" of the book (where the copy came from, and which torrent it
  belongs to) but never attached the file to it. Each such file is still
  invisible to exact-duplicate matching and shows as "cannot be verified" in
  the torrent cleanup. The 10-10 fix stopped new cases; it did not repair the
  old ones.
- **A new repair does that, and the owner reviews it first.** "Link pre-fix
  imports to their versions" is in the Repairs tab of the review page. Running
  a trial changes nothing. It lists every file of every book that has a
  version, with the book, the file's location, the version it would be linked
  to and why, sorted into groups with a count for each. Only rows the owner
  selects and applies are written.
- **Only two kinds of file are ever linked.** The first is a file whose book
  has exactly one version not attached to anything, carrying the same torrent
  as the file (or no torrent, for a manual import). The second is a file of
  the same torrent as another file of the same book that is already attached
  to that book's version; it joins that version.
- **Everything else is listed and left alone.** That covers files whose
  torrent belongs to a different book, files that point at another book's
  version (usually left behind by a merge), files that point at a version
  that no longer exists, torrent files whose version stayed behind on a book
  that was merged away (a merge moves files but not versions), books with
  more than one possible match, files with
  no torrent on a book whose version has one, files outside the allowed
  folders, and deleted books. Each is a decision for the owner, not a guess.
- **It cannot create or lose anything.** It never creates, changes or deletes
  a version, and never deletes a file record. Before writing each file it
  checks again that nothing changed since the trial, while holding the locks
  that keep a merge or an import of the same torrent from running at the same
  moment. A file that changed is refused, not overwritten. Running it again
  finds nothing new to link.
- **Missing checksums are filled in on the way.** A linked file with no
  checksum gets one (the same calculation the importer uses). If the file
  cannot be read, it is still linked, and the existing "Backfill File
  Hashes" job fills the checksum later.
- **One limit.** The Operations page "undo" does not unlink these files. Each
  link is recorded in the operation's history, but reversing it would only
  bring back the broken state the repair fixed.
- **Not run in production yet.** It needs the owner's review of the trial
  list in the Repairs tab, then an apply of the chosen rows.
