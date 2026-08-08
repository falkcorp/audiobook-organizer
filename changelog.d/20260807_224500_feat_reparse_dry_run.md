<!-- file: changelog.d/20260807_224500_feat_reparse_dry_run.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4e7a9c12-5b3d-4f68-9a21-8c6d0e4f7b35 -->
<!-- last-edited: 2026-08-07 -->

### Changed

- **`maintenance.transcribe-book-intros` with `reparse_only=true` now DEFAULTS
  to `dry_run=true`.** This is a behaviour change: a reparse-only dispatch with
  no explicit `dry_run` no longer writes anything — it runs the identical
  classification+comparison logic, reports "Dry run — would update N of M
  transcribed", and skips every `UpdateBook` call. Pass `dry_run=false` to
  apply. Motivated by the 2026-08-07 reparse of 12,990 books being dispatched
  with no preview (acceptable only because reparse is upgrade-only). The new
  `dry_run` param is currently honoured by `reparse_only` runs only; the
  full-transcribe path ignores it.
