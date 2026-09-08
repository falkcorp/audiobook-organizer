### Fixed

- **The activity database was growing by gigabytes per scan, and the cause was
  not what the compression work assumed.** Measured on production 2026-09-07: a
  5.3 GB activity database whose `summary` column alone held 5.48 GB. The
  `details` column — the one zstd compression was added for — held 351 MB, about
  6% of the file. 208,103 `system` rows averaged 27 KB of summary, and the single
  largest was **9,558,930 bytes**.
- **Root cause: `ContractVerdict.Error()` was unbounded.** It formatted every
  iTunes safety-contract violation into one string, and a systematically bad
  write-back produces one violation per mhoh block. Reproduced at the production
  shape: **19.9 MB from a single call.** It now lists at most 20 violations and
  counts the rest, naming the failing guards; the full structured detail is still
  on `ContractVerdict.Results` for callers that need it.
- **Defence in depth: activity summaries are now clamped at 8 KiB on write.** A
  summary is a one-line headline, and the storage layer should not depend on
  every caller being well behaved. Truncation is marked and reports the original
  size, cuts on a rune boundary so the result is always valid UTF-8, and is
  idempotent — the cap covers the marker, so the several layers that clamp
  (`Record`, `recordBatch`, and a dual-write store writing to two backends) can
  never stack markers. The clamp is applied before the row's source key is
  derived, so both write paths agree on dedup keys.
