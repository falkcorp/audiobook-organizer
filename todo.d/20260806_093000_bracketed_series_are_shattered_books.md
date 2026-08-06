<!-- file: todo.d/20260806_093000_bracketed_series_are_shattered_books.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9d3f621c-47ea-4b05-8c93-2f1a7de04b58 -->
<!-- last-edited: 2026-08-06 -->

- [ ] **~180 "bracketed series" are actually one shattered book each** — found by
  the `maintenance.series-denumber` dry run, 2026-08-06.

  The dry run flagged 198 series names carrying a bracketed number
  (`Dragon Born [04]`, `… Called Peace (12)`). Roughly 18 are genuine series
  positions. The rest are **one novel exploded into per-chunk series rows**:

  | Target base | Rows | Books | Reality |
  |---|---:|---:|---|
  | `Megan E. O'Keefe - Catalyst Gate` | 80 | 80 | one novel |
  | `Listening-to-ClassA-Threat-by-Dan-Sugralinov--Scribd` | 36 | 36 | scraped page titles |
  | `Listening-to-Arcane-Kingdom-Online-Dark-Magic-by-Jakob-Tanner--Scribd` | 27 | 27 | scraped page titles |
  | `The Light We Lost` | 25 | 25 | one novel |
  | `Arkady Martine - A Desolation Called Peace` | 12 | 12 | one novel |
  | `Dragon Born`, `Warbreaker`, `Guardian`, `Otherworld Academy`, … | ~18 | ~24 | **genuine** |

  🔴 **Do not resolve this by applying `applyMedium`.** That would manufacture an
  80-volume "Catalyst Gate" series out of a single book, and a 36-volume series
  out of a Scribd listing page. The denumber op deliberately holds them; the
  parser is behaving correctly, the *shape* is a lie.

  These belong to the **combine-into-one-book** track (The Successors class), not
  the series track: a bracketed `(47)` on a novel title is a disc/chunk marker
  that leaked into the series field. The `Listening-to-…--Scribd` rows are a
  distinct, narrower bug — a web scrape wrote page titles into series names, and
  those need their own cleanup rather than any kind of merge.

  Start from the report:
  `/var/lib/audiobook-organizer/series-denumber-2026-08-06.tsv`
  (`shape=bracketed`, group by `into_name`, anything with >3 rows is suspect).
