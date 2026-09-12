<!-- file: docs/executive-summaries/2026-09-12-the-bookmarks-an-itunes-sync-reset-to-zero-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 032eec6c-a1fb-4902-b19a-6bf2807e3e7c -->
<!-- last-edited: 2026-09-12 -->

# The bookmarks an iTunes sync reset to zero

**Pull request:** https://github.com/falkcorp/audiobook-organizer/pull/3269

## Executive Summary

The app copies three listening details from iTunes onto each book: how many
times it has been played, when it was last played, and the **bookmark** — the
spot where you stopped listening, so the next session picks up where you left
off.

iTunes keeps its library in one of two file formats. One is a readable export
(XML). The other is the binary file iTunes itself works from (ITL). The app
accepts either one and decides which it is by looking at the start of the file.

The reader for the binary file never extracted a bookmark. Every track it
produced said "bookmark: 0". The sync treated that 0 as the real value and
wrote it over the bookmark the app already had. So when the library file the
sync was pointed at was the binary one, **every sync reset every stored
bookmark to the start of the book**. Nothing reported an error, because from
the sync's point of view nothing went wrong.

Play count and last-played were not affected. The binary reader does read
those two.

### What changed

Each file format now declares which of the three details it actually carries.
The readable export declares all three. The binary file declares play count and
last-played only. A library that declares nothing is treated as carrying
nothing, so a future reader that forgets to declare cannot overwrite anything.
The sync now writes a detail only when the source says it carries it.

This keeps one behaviour on purpose. If the readable export says a bookmark is
0, that is a real reset (you started the book over) and it is still applied.
Only a 0 the source never really knew about is ignored.

A book that is added for the first time from the binary file now gets an empty
bookmark, not a bookmark of 0. A later import from the readable export can then
fill it in.

New tests check four things. A sync from the binary file leaves an existing
book's bookmark and last-played alone, but still updates its play count. A
source that declares nothing changes nothing. A sync from the readable export
still writes all three, and a real 0 there still resets the bookmark. And a
book added from the binary file gets an empty bookmark.

### What this does not do

Bookmarks that earlier syncs already reset are still at 0. This change stops
further damage but does not bring them back. They could be restored from a
saved copy of the readable export taken before the resets, if one exists.
Whether and how to do that is the owner's decision, and nothing has been run.
