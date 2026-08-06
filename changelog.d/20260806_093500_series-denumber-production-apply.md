<!-- file: changelog.d/20260806_093500_series-denumber-production-apply.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2e7b0a49-6f13-4c85-9d02-b7513ac86f2e -->
<!-- last-edited: 2026-08-06 -->

### Changed

- **Applied `maintenance.series-denumber` (high tier) to production.** 25 fragmented
  series merged into 21 base series, 52 books given a real `series_position`, 11 base
  series created, 25 emptied series deleted, 0 failures. A follow-up dry run confirmed the
  high tier drained 25 → 0 with the medium (198) and low (466) tiers untouched. Rollback
  reports: `/var/lib/audiobook-organizer/series-denumber-{,APPLY-,VERIFY-}2026-08-06.tsv`.

  The dry run also established that the acceptance gate holds against real data: 689
  candidates (under the 982 ceiling), zero candidates whose base fails `IsJunkSeriesBase`,
  and `86—EIGHTY-SIX` — which exists in production in three spelling variants — absent
  from the candidate set entirely.

  The medium tier was deliberately NOT applied. See TODO: ~180 of its 198 rows are one
  shattered book each (80 rows for a single Megan E. O'Keefe novel, 63 more from a Scribd
  scrape writing page titles into series names), not series positions.
