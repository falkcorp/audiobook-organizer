<!-- file: docs/archive/2026-08-consolidation-wave2/plans/2026-08-06-series-embedded-positions-plan.md -->
<!-- version: 1.0.1 -->
<!-- guid: 6f28c0d3-91ba-4e75-83c1-05de7492ab61 -->
<!-- last-edited: 2026-08-12 -->

# Series embedded positions — implementation plan

Spec: [`2026-08-06-series-embedded-positions-design.md`](../specs/2026-08-06-series-embedded-positions-design.md)

## Goal

Teach `maintenance.series-denumber` the three embedded-position shapes (982 books
across 769 distinct series names), with a confidence tier so the genuinely
ambiguous cases go to review instead of being guessed.

## Files

- `internal/plugins/maintenance/series_denumber.go` — add the embedded-shape
  regexes, a `Confidence` field on `SeriesSplit`, and the `IsJunkSeriesBase`
  extensions. **Both in the same commit** — the guard must never lag the parser.
- `internal/plugins/maintenance/series_denumber_test.go` — table tests built from
  the real production names below.
- `internal/plugins/maintenance/series_denumber_op.go` — thread confidence
  through; only `high`/`medium` are apply-eligible, `low` emits a review hold.
- `changelog.d/`, `todo.d/` fragments.

## Steps

1. **`Confidence` on `SeriesSplit`** (`high` | `medium` | `low`), defaulting the
   existing trailing shapes to their current behaviour so nothing regresses.
   Pure type change + existing tests still green.
2. **Extend `IsJunkSeriesBase` FIRST** (spec D6), with tests, before any new
   shape can reach it. Landing the guard ahead of the parser means a mistake in
   step 3 fails closed.
3. **Add the embedded shapes**, each with its confidence:
   - keyword-embedded (`… Book 4: …`, `… Vol 09: …`) → **high**
   - single bracketed at end (`Dragon Born [04]`) → **medium**
   - bare leading (`08. Battle for the Abyss`) → **low**
   - **multi-number names refuse outright** (spec D5)
4. **Sibling corroboration** (spec D4): promote `low` → `medium` when other books
   share the base/folder with a different leading number. Lone occurrences stay
   `low`.
5. **Op wiring**: `high`/`medium` apply under the existing apply flag; `low`
   emits a review hold. Report counts per tier.
6. **Dry run on production, read the numbers, then apply.**

## Test strategy

Table tests must include these REAL production names as explicit cases:

**Must NOT be split** (real titles):
- `86—EIGHTY-SIX` (17 books — the loudest failure available)
- `5-Minute Sherlock`
- `Rebirth Online 2: Rebirth Online` (base == title → junk)

**Must split, high confidence:**
- `Vampire Hunter D: Vol 09: The Rose Princess` → base `Vampire Hunter D`, pos 9
- `Evil Genius: Book 4: Becoming the Apex Supervillain` → pos 4
- `Frontiers Saga Part 2: Rogue Castes` → pos 2

**Must split, medium:**
- `Dragon Born [04]` → base `Dragon Born`, pos 4

**Must REFUSE (multi-number):**
- `The Demon Wars Saga [07] Immortalis [02]`
- `The Stormlight Archive [01] The Way Of Kings [02]`

**Must be low (held, not applied):**
- `08. Battle for the Abyss`
- `Station 64: The Doll Dungeon`

Commands:

```
go test ./internal/plugins/maintenance/... -race -run TestSeriesDenumber
go test ./internal/plugins/maintenance/... -race -run TestIsJunkSeriesBase
make ci
```

Green means: every "must NOT be split" case returns no split; every refuse case
returns no split; tier assignments match exactly.

## Dry-run acceptance gate

Before ANY apply, the production dry run must report:

- total candidates ≈ **982 books / 769 names** (the measured figure; a wildly
  different number means the parser is matching something unintended)
- **zero** candidates whose base fails `IsJunkSeriesBase`
- `86—EIGHTY-SIX` absent from the apply set entirely
- per-tier counts, with `low` clearly separated and NOT in the apply set

If the candidate count materially exceeds 982, stop — the parser is over-matching.

## Rollback

- Parser changes are additive and behind the existing dry-run/apply flag.
- Each book write is re-fetch-then-mutate on `series_name` + `series_position`
  only — never a partial `UpdateBook` write-back.
- The dry-run report lists every (book, old name, new name, position) so an
  incorrect apply can be reversed from it. **Write that report to a file before
  applying**, as with the multidisc canary.
- `git worktree remove .worktrees/series && git branch -D fix/series-embedded-positions`
