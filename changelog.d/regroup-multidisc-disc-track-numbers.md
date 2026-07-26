<!-- file: changelog.d/regroup-multidisc-disc-track-numbers.md -->
<!-- version: 1.0.0 -->
<!-- guid: c7f0a3d1-9b26-4e58-8a04-2f6b1d7e3c90 -->
<!-- last-edited: 2026-07-25 -->

### Fixed

#### Review-queue multidisc approve now writes correct disc/track numbers

Approving a "Multi-disc" regroup hold previously merged the folder's single-file books
into one book but wrote **no** `DiscNumber`/`TrackNumber` at all — so a merged audiobook
carried no play-order metadata, and the "Multi-disc" label was misleading for what were
often just sequentially-numbered chapters of one recording (e.g. `When We Were Sisters_1.mp3`
… `_6.mp3`).

The regroup classifier (`internal/itunes/service/fs_regroup_shape.go`,
`assignDiscTrack`) now derives per-file numbers from each member's real structure:

- Files in genuine `Disc N`/`CD N` subfolders (a real boxed set, e.g. Star Wars) get their
  true `DiscNumber` per file.
- Flat/chapter files on one disc get `DiscNumber = 0` (no disc concept — never fake disc
  numbers spread across chapters) and a contiguous `TrackNumber` in play order.

Numbers are threaded through the review payload (`regroupPayload.DiscNumbers`/`TrackNumbers`)
and written on approve (`internal/plugins/maintenance/regroup_apply.go`,
`applyDiscTrackNumbers`). The write is a targeted field-only update via `UpdateBookFile`
(fingerprint-preserving — never a full-record write-back) and is guarded group-level: if any
file already carries disc/track metadata, the whole set is left untouched ("unless the disc is
already set, then leave it"). By construction every `(disc, track)` pair in a group is unique,
so the file-order sort can never collide. Backward compatible: holds written before this
change (no arrays) merge exactly as before, just without numbering.
