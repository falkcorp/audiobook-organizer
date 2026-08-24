### Added

- The library scan now reports its skip rate. `shouldSkipFile` previously
  returned a bare boolean with no counter, log or metric, so a scan that skipped
  every file and a scan that skipped none produced byte-identical logs. Each
  decision is now classified and counted, and the completion summary reports how
  many files were skipped plus which re-read reason dominated: cache-miss,
  changed, forced-rescan, stat-error, or cache-disabled. The five reasons are
  counted separately because they call for different fixes -- "changed" is the
  scan working as designed, while "cache-miss" is the population that gets
  re-read on every tick forever.
