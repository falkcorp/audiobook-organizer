<!-- file: docs/executive-summaries/2026-10-30-copy-organize-sync-identity-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 30f80eea-6fe1-4f60-9778-e0a71a2b4faf -->
<!-- last-edited: 2026-10-30 -->

# Re-organizing a book no longer loses the listener's place

## Executive Summary

When the organiser re-files a book by making a fresh copy, the listening apps
were being shown the new copy as a brand new book. Anyone partway through it
lost their place: the book dropped off Continue Listening and the new copy
started at zero, while the old place stayed attached to a copy nobody could see.
This change moves the book's identity, each listener's place and their book
status onto the new copy at the moment it takes over.

## What changed

- **The place moves with the book.** The identity the apps remember, every
  listener's position, and their status for the book now follow the book onto
  the new copy, and nothing is left behind on the hidden original.
- **Downloaded files keep working.** A file already downloaded to a phone is
  matched to the new copy's matching file, not left pointing at a file the new
  copy does not have.
- **Books imported from iTunes are covered.** Their places were saved without
  the apps' identity, so they were not carried before; they are now.
- **Books nobody has listened to are untouched.** Nothing is invented for them.
- **A failure never undoes the re-organize.** If the move cannot finish, the
  re-organize still stands, the place stays together on the original so nothing
  is lost, a repair record is kept so it is finished later, and the operation
  log says so.

## Why it mattered

Losing a listener's place is lost work for them, and until now it could happen
silently on any book that was re-filed by copy.

## What is still open

A copy made while an app is open can briefly be given its own identity before
the move runs; the move replaces it. Choosing a different copy as the playing
version afterwards is a separate step that does not move places, and is not
changed here.
