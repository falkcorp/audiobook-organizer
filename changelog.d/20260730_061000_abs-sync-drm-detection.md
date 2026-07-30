<!-- file: changelog.d/20260730_061000_abs-sync-drm-detection.md -->
<!-- version: 1.0.0 -->
<!-- guid: 21aa1272-250b-446d-bf74-ceb98e762cfb -->
<!-- last-edited: 2026-07-30 -->

### Added

- **DRM detection for Audible AAX/AAXC (abs-sync Phase 4, first step).** Added
  `audioutil.DetectDRM`, a cheap extension-based check flagging `.aax`/`.aaxc` files as
  DRM-protected-and-unplayable, with a documented reason string. Detection only, not yet wired into
  the scanner or surfaced to users -- that's a follow-up task once a schema-owning task decides
  where the "unplayable" flag lives.
