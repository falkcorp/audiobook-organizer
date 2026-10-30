### Fixed

- Ten operations accepted only `dry_run`. Eight silently dropped `dryRun`, so a
  run started as `{"dryRun":false}` previewed and wrote nothing while looking
  like a completed repair; the other two (`maintenance.rewrite-path-prefix`,
  `maintenance.repair-library-state`) refused the unknown key. They now accept both spellings, an omitted flag is still a
  preview, and a body that states the two with different values is refused with
  an error naming both. The ops are `dedup.series-dedup`,
  `maintenance.author-conjunction-repair`, `maintenance.author-duplicate-merge`,
  `maintenance.auto-match-transcribed`, `maintenance.intro-migrate-single-file`,
  `maintenance.repair-library-state`, `maintenance.repair-transcribe-status`,
  `maintenance.rewrite-path-prefix`, `maintenance.series-phantom-repair` and
  `maintenance.version-group-primary-repair`. `maintenance.duration-reextract`
  resolves its two spellings through the shared `opmode.ResolveDryRun` instead
  of its own inline copy.

### Added

- `TestGuard_DryRunParamsAcceptBothSpellings` (a source scan) and
  `TestOpsV3_DryRunFamilyDecodesBothSpellings` (over the live op registry) fail
  when an op declares only one spelling of the dry-run flag, or declares either
  as a plain `bool` whose Go zero value would read as a live run.
