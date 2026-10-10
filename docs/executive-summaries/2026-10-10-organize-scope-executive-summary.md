<!-- file: docs/executive-summaries/2026-10-10-organize-scope-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 377482e4-3d85-400d-a2d5-8a4b524c8bea -->
<!-- last-edited: 2026-10-10 -->

# Organize: a run limited to some books or one folder now stays inside that limit

Branch `fix/organize-folder-scope`.

## Executive Summary

- **A folder-limited organize used to organize everything.** The organize
  operation accepted a "folder" setting and then ignored it, so asking it to
  tidy one folder moved and renamed files across the whole library. Nothing in
  the app's own screens sends a folder today, but the operation could be
  started that way directly.
- **Two more ways it could spread past its limit were found and fixed.** An
  organize of a few chosen books with "fetch metadata first" turned on
  reloaded and organized every book after the fetch step. And an explicit
  empty list of books meant "all books" instead of "no books".
- **A bad folder setting now stops the run instead of widening it.** A blank,
  relative or `..` folder makes the run fail before it reads or touches
  anything. A book with files both inside and outside the folder is left alone
  and reported, because organizing it would move the files outside the folder.

## Why it mattered

Organize moves and renames real audiobook files. A run that touches more than
it was asked to is the kind of mistake that changes the library without anyone
noticing until files are not where they used to be. Each case above now has a
test that failed before the fix.
