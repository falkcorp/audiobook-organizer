- [ ] **MEDIAINFO-EXTENSION-SET** `mediainfo.Extract` accepts only `audioext.DefaultSet()` while the
      scanner admits the configured `SupportedExtensions`; decide whether mediainfo should honour the
      configured set. Until then a row at a configured extension outside the default set (for example
      `.mp4`) can never be measured: the unknownRows backfill and the folder-books holds decline it
      (`skipped_not_audio`) instead of probing it.
