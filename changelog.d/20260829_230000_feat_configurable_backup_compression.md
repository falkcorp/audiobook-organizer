### Added

- Backup archives can now be compressed with **zstd** instead of gzip, or left
  uncompressed, via the new `backup_compression` setting (`gzip`, `zstd`,
  `none`) and `backup_compression_level`. gzip remains the default, so existing
  installations are unaffected until they opt in.

### Fixed

- An unset backup compression level no longer means `gzip.NoCompression`. It
  previously would have: level 0 is "store" in the standard library, so a
  configuration that omitted the level would have written large archives with no
  compression at all, with no error and no log line. Zero now means "this
  codec's default".
- Backup listing, retention and the auto-backup freshness check recognise every
  archive format rather than only `.tar.gz`. Recognising just one format would
  have made archives in any other format invisible to retention, so nothing
  would ever have been pruned.
- Restoring an archive now identifies its format from the archive's own magic
  bytes instead of the current configuration or the filename, so archives
  written under an earlier setting -- or renamed -- still restore.
