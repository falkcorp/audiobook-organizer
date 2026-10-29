<!-- file: docs/executive-summaries/2026-10-29-hard-delete-owned-files-checks-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5714a21a-ac7a-4403-8b7e-2e454a5becea -->
<!-- last-edited: 2026-10-29 -->

# Deleting a book no longer moves a reader's progress away from a book that stays, and bulk deletes keep a version group's primary

## Executive Summary

Deleting a book that is a leftover copy first moves the listening progress of
anyone who was part-way through it onto the copy the player lists, and only then
removes the book. Several clean-up paths asked "does this book still own any
files?" in a way that could not see files the library had set aside for another
version of the book (called "retired" versions), and the check was made before
the step that locks out other library changes. So a book that still held such
files could have its readers' progress moved off it, and in the duplicate
clean-up its audio file removed, before the final delete refused and the book
stayed. This change makes every one of these paths ask the same question the
final delete asks, ask it again right before anything is moved, and remove the
audio only after the book record is gone. It also makes the bulk delete hand a
deleted primary's role on to another version, as the single delete already did.

## What changed

- **The duplicate clean-up and the iTunes regroup clean-up count set-aside
  files.** They used to read only the files a book plays; a book whose only files
  were set-aside ones looked empty, had its readers' progress moved to another
  copy, and was then kept anyway. They now keep such a book and its progress and
  say why in the report.
- **The check is repeated right before the progress is moved.** A set-aside
  version added to a book between the first check and the move is now caught
  before the move.
- **The duplicate clean-up removes the book record first and the audio file
  after.** If the delete is still refused, the audio is still there. Before, the
  audio was removed first.
- **The bulk delete shares the single delete's steps.** A deleted primary now
  hands its role on to another version of the book, so a group is no longer left
  without a primary after a bulk delete. The bulk delete also tells the
  notification plugins and refreshes the author and series lists, as the single
  delete does. Notification receivers will now see one "book deleted" message per
  book removed in bulk, which they did not get before.
- **The refusal message is accurate.** A book that holds only set-aside files is
  no longer told to move files that cannot be moved.

## Why it matters

The worst case before was a reader's progress moved to another copy and, in the
duplicate clean-up, the audio file of a book that was then kept deleted from disk.
Both needed a version to be set aside at the wrong moment, so they were rare, but
neither could be noticed afterwards.

## Not done

The bulk soft delete (move to the trash) still sends no "book deleted" message,
the bulk hard delete has no option to block the file's fingerprint as the single
delete has, and the bulk delete does not refresh the book list cache. Those are
separate gaps and were left alone.
