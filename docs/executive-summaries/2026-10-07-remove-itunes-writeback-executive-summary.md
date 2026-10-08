<!-- file: docs/executive-summaries/2026-10-07-remove-itunes-writeback-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6d2a8f13-4c7e-4b91-a5d0-e83f1c9b2a74 -->
<!-- last-edited: 2026-10-07 -->

# The organizer no longer writes to iTunes (2026-10-07)

## What you decided

"Drop itunes writeback. We will do import only and then not care."

## What changed

- The organizer still **reads** your iTunes library: import, sync from iTunes,
  play positions and finished books coming across, and the iTunes ids it keeps
  on each book all work as before.
- It never **writes** your iTunes library any more. Every path that could
  change the `.itl` file is gone: the background queue that pushed edits,
  rebuild, relocate, cleanup of merged tracks, upload, restore and the
  "Write Back to iTunes" and "Force Sync to iTunes" buttons.
- Editing, merging, organizing or deleting a book now changes only the
  organizer's own data and files. Writing tags into the audio files themselves
  is a separate feature and is unchanged.
- The leftover queue entries from the old write-back are deleted once when the
  server starts.
- The iTunes library file setting stays, but only for the PID check and the
  download button. Nothing writes to that file.

## Why it matters

Write-back had been failing quietly for weeks, and any write to that file lands
in the library iTunes actually opens. With no writer left, nothing can damage
the iTunes library by mistake.

## What is left

- The host's `ITUNES_WRITE*` lines in `local.conf` can be removed after the
  deploy.
- A few follow-ups are listed in
  `docs/plans/2026-10-07-remove-itunes-writeback.md` (final inventory).
