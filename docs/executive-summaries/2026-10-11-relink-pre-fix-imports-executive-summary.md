<!-- file: docs/executive-summaries/2026-10-11-relink-pre-fix-imports-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: c8b260fc-3025-4657-b8a0-573cbea538a5 -->
<!-- last-edited: 2026-10-11 -->

# Repair: link files imported before the 10-10 fix to their versions

Branch `feat/relink-pre-fix-imports`.

## Executive Summary

- **What was left behind.** Until 2026-10-10, every import recorded a
  "version" of the book (where the copy came from, and which torrent it
  belongs to) but never attached the file to it. The 10-10 fix stopped new
  cases; it did not repair the old ones.
- **What attaching a file changes.** Two things the owner can see:
  - **Swapping versions.** A version swap moves the files attached to each
    version. A file attached to nothing is moved along with either version;
    once attached, it moves only with its own.
  - **Purging a version now deletes its file.** Once a file is attached,
    trashing that version and then purging it ("purge now", or the automatic
    cleanup 14 days after it was trashed) **deletes the file from disk**. For
    these files that is usually a file in an import folder or a Deluge
    download folder. Before this repair, purging one of these versions deleted
    no audio. Every file imported since 10-10 already works this way.
- **A new repair does the attaching, and the owner reviews it first.** "Link
  pre-fix imports to their versions" is in the Repairs tab of the review page.
  Running a trial changes nothing. It lists every file of every book that has
  a version or a file pointing at one, plus every file that carries a torrent
  but points at no version, even on a book with no version (an import a merge
  moved). Each row shows the book, the file's location, the version it would
  be attached to and why, sorted into groups with a count for each. Only rows
  the owner selects and applies are written.
- **Only two kinds of file are ever attached.** The first is a file whose book
  has exactly one version not attached to anything, carrying the same torrent
  as the file (or no torrent, for a manual import). The second is a file of
  the same torrent as another file of the same book that is already attached
  to that book's version; it joins that version.
- **Nearly every row will be the first kind, with no torrent.** Imports before
  10-10 did not record torrents, so the match for almost every file is "this
  book has one unattached version and one unattached file". To make that
  checkable, each row shows whether the version and the file agree: created at
  the same time, the same format, the file still in an import folder (not
  moved into the library), and the file not marked missing. A row where any of
  these disagree is marked **review**, with the reason, and needs a look
  before it is applied.
- **Everything else is listed and left alone.** That covers files whose
  torrent belongs to a different book, files that point at another book's
  version (usually left behind by a merge), files that point at a version
  that no longer exists, torrent files whose version stayed behind on a book
  that was merged away (a merge moves files but not versions), books with
  more than one possible match, files with no torrent on a book whose version
  has one, files outside the allowed folders, and deleted books. Each is a
  decision for the owner, not a guess.
- **What it writes, and what it refuses.** It never creates, deletes or
  changes a version, and never creates or deletes a file record. On a file
  record it sets only the version link, and the checksum when there is none.
  Just before writing, it checks again that nothing changed since the trial,
  while holding the locks that keep a merge or an import of the same torrent
  from running at the same moment. The write itself changes only those fields
  on the record as it is at that moment, and is refused if the record's
  version link, location or torrent changed, or the version was trashed, in
  the meantime. Anything else someone changes on the same file at the same
  moment (a track number, a missing mark) is kept, not undone. The same
  protection now covers the importer's own linking step. Running the repair
  again finds nothing new to link.
- **Missing checksums are filled in on the way.** A linked file with no
  checksum gets one (the same calculation the importer uses), read before any
  lock is taken. If the file cannot be read, it is still linked, and the
  existing "Backfill File Hashes" job fills the checksum later.
- **One limit.** The Operations page "undo" does not unlink these files. Each
  link is recorded in the operation's history, but reversing it would only
  bring back the broken state the repair fixed.
- **Not run in production yet.** It needs the owner's review of the trial
  list in the Repairs tab, then an apply of the chosen rows.
