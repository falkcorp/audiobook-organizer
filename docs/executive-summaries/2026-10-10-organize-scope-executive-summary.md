<!-- file: docs/executive-summaries/2026-10-10-organize-scope-executive-summary.md -->
<!-- version: 1.1.1 -->
<!-- guid: 377482e4-3d85-400d-a2d5-8a4b524c8bea -->
<!-- last-edited: 2026-10-10 -->

# Organize: a run limited to some books or one folder now stays inside that limit

Branch `fix/organize-folder-scope`.

## Executive Summary

- **A folder-limited organize used to organize everything.** The organize
  operation accepted a "folder" setting and then ignored it, so asking it to
  tidy one folder acted on every book in the library: books not yet in the
  library folder were copied or linked into it, and books already there were
  renamed to their proper place. Nothing in the app's own screens sends a
  folder today, but the operation could be started that way directly.
- **Two more ways it could spread past its limit were found and fixed.** An
  organize of a few chosen books with "fetch metadata first" turned on
  reloaded and organized every book after the fetch step. And an explicit
  empty list of books meant "all books" instead of "no books".
- **A bad folder setting now stops the run instead of widening it.** A folder
  that is blank, is not a full path, or steps up out of itself makes the run
  fail before it reads or touches anything.
- **A book is only organized when every file it would touch is in the
  folder.** A book can carry an out-of-date record of where its file is, and
  organize sometimes works from that record. A book whose files are in the
  folder but whose old record points somewhere else is now left alone and
  reported, instead of having the file at the old location copied or renamed.
  The same goes for a book with files both inside and outside the folder, and
  for a book that a library scan moves out of the folder while the run is
  under way: each book is checked again right before it is organized.
- **The limit picks which books, not where they go.** Organized books still
  land in the library folder as usual, and tidying may remove folders left
  empty above the chosen folder, never above the library folder itself.

## Why it mattered

Organize moves and renames real audiobook files. A run that touches more than
it was asked to is the kind of mistake that changes the library without anyone
noticing until files are not where they used to be. Each case above now has a
test that failed before the fix.
