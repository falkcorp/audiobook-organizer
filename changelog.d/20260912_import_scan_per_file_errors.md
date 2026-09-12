### Fixed

- **Import-path scans surface per-file failures again.** A file the scan could
  not read (or whose book row could not be saved) was logged as free text
  through a stdout-only logger, so the operation log never saw it and Settings
  -> Paths "View Errors" had nothing to show. The scanner now counts every
  failed file and writes the first 25 to the operation log at warn with
  `file_path` / `stage` / `reason` as structured attrs, plus one summary line
  with the total (`files_failed`, `files_listed`, `files_omitted`); the final
  progress message says how many files failed and `library.scan` persists the
  count and sample as its result. The Paths tab now polls the real operation
  (it used to run a 3-second fake progress timer) and reads the failures back
  off its log into "View Errors". The poll ends instead of spinning: a 404 (the
  operation was discarded or purged) stops it with "the scan operation no
  longer exists", ten failed reads in a row stop it with an error, and an
  `interrupted_*` scan shows as interrupted rather than failed, still polled
  only for `interrupted_quiesced`, which resumes in place when the scan
  stand-down lifts.
