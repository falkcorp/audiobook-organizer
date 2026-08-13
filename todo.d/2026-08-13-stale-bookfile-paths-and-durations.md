## Library data integrity — surfaced by the chapters-backfill cohort run

Measured 2026-08-13 against the 77-book `job` test cohort on production. These are
**pre-existing defects the backfill exposed**, not regressions from it.

- [ ] **`BookFile.FilePath` rows point at files that do not exist — 16,130 books
      library-wide, 33.7% of all single-file books.** ⚠️ The cohort figure this
      was first written from (14 of 58, 24%) understated it; a whole-library dry
      run put the real number at `probe-failed=16130`, and an independent `test -e`
      sweep over a 400-book random sample agreed at 88/295 = 29.8% (which is what
      rules out ffprobe concurrency exhaustion — `test -e` has no subprocess to
      exhaust). Of 88 sampled missing rows, **86 (97.7%) have a `Book.FilePath`
      that IS a regular file on disk**; only 2 are genuinely gone. So this is
      recoverable, not data loss.
      **MITIGATED, NOT FIXED (2026-08-13, PR #2372):** `maintenance.chapters-backfill`
      now falls back to `Book.FilePath` when the `BookFile` path does not resolve,
      recovering ~16k books. That is a workaround inside ONE op — the stale rows
      are still stale, and every other consumer that resolves a file by stored
      path still degrades silently on them. The row repair itself is still open.
      The op probed
      `.../Timothy Zahn/The Icarus Job/The Icarus Job/The Icarus Job - Timothy Zahn - read by narrator.m4b`;
      the file actually lives at `.../Unknown Author/The Icarus Job/`. Eight of
      the fourteen are filed under `Cliff Kurt`, **who is the narrator, not the
      author** — the real files are under `PZG/`. The signature is a path
      recomputed from edited metadata without the file ever being moved (or a
      re-organize that never wrote back the `BookFile` row). Any op that resolves
      a file by stored path silently degrades on these. Full list:
      `probe-failed=15` in op `01KZXSZM5K6DA7QP21DPRAR17C`.
- [ ] **`Book.FilePath` and `BookFile.FilePath` disagree for the same book.** For
      `The Icarus Job` the book row points into the iTunes tree while the book-file
      row points at a nonexistent path under the organized tree. Any consumer that
      picks the "wrong" one gets a different answer. Decide which is authoritative
      and make the other derive from it.
- [ ] **`Book.FilePath` is NOT unique — 1,264 values are shared by more than one
      book row (4,353 of 63,870 rows, 6.8%).** This bounds how far the #2372
      fallback can safely be reused: anything that resolves a book to a file via
      `Book.FilePath` can land on a row belonging to a different book. It happens
      not to bite the chapters backfill (0 of the 88 sampled recoverable rows are
      among the 4,353), but that is a property of today's data, not a guarantee —
      **re-run the collision count before extending the fallback to any op that
      WRITES a book row**, since chapters go to their own `chapters:<bookID>`
      keyspace and a book-row write would not be so contained. Likely the same
      root cause as the duplicate-book-rows item below.
- [ ] **Stored `duration` is short of the real container by 119–186s on 7 cohort
      books.** Confirmed by ffprobe: `Mushoku Tensei … Vol. 03` stores 33582s while
      both physical copies measure 33767.759s. The chapter timelines written by the
      backfill are correct; the duration field is stale. Related:
      `project_duration_filesize_aggregation` (snapshots, not sums).
- [ ] **Multi-file chapter synthesis produces a timeline that stops short.** One of
      the two `Genesis` rows (1,189 files) serves 1,189 chapters ending at 32,636s
      against a 258,256s duration; its twin ends correctly at 258,256s. The mapper's
      per-file synthesis is picking up wrong or missing per-file durations.
- [ ] **Duplicate book rows per title under different author folders** (`Deadly Jobs`
      ×3, `The Icarus Job` ×3, every `Mushoku Tensei` volume ×2 as `PZG` and
      `Unknown Author`). Worth checking as a *source* for exact-pending dedup
      regrowing to 5,947 by 2026-08-12 — that note says it needs a source fix
      rather than another drain. Pointer only; not chased here.

## Follow-up on the op itself

- [ ] `registry.RunItems` label re-render (fixed 2026-08-13) changed shared
      infrastructure used by every op. Progress labels for other ops now advance
      one item later than before — verify none of them assumed the old timing.
- [ ] One unreproduced failure of `internal/plugins/maintenance` was observed on
      2026-08-13 during mutation testing; 8 subsequent runs (3 under `-race`, plus
      `internal/operations/registry`) were green and the failure detail was not
      captured before re-running. If it recurs, capture the output first.
