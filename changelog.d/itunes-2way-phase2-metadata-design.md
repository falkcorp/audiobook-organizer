<!-- file: changelog.d/itunes-2way-phase2-metadata-design.md -->
<!-- version: 1.0.0 -->
<!-- guid: a3cc156e-ef4b-42ad-8c33-cf9be7a27d59 -->
<!-- last-edited: 2026-07-25 -->

### Added

#### iTunes 2-way sync Phase 2 design: bidirectional metadata sync (docs)

`docs/specs/2026-07-25-itunes-2way-sync-phase2-metadata-design.md` — the design for
expanding the sync cycle from location-only to all AO-owned audiobook metadata
(title/author/series-as-album/genre/narrator), plus a bidirectional iTunes→AO read-back
watcher so AO stays authoritative without destroying iTunes-side edits. Owner decisions
resolved: Album=Book.Title, Name=BookFile.Title→Book.Title, play-state (bookmark / play
count / rating / dates) never written, 10-minute settle window, persistent background
watcher, and a mandatory SHA-attribution safeguard so the watcher never mistakes AO's own
writes for iTunes edits. Design-only; no code path built yet.
