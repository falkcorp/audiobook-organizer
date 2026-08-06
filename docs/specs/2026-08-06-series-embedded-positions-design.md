<!-- file: docs/specs/2026-08-06-series-embedded-positions-design.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3b7e0a94-52d1-4c68-91af-7d0c48e35b26 -->
<!-- last-edited: 2026-08-06 -->

# Series names that carry a book position — embedded shapes (design)

Owner item 4. Extends `maintenance.series-denumber`, which today only handles
**trailing** positions, to the shapes where the number sits at the front or in
the middle.

## Measurement (whole population, production, 2026-08-06)

12,201 distinct series names covering 32,119 books. Bucketed by shape:

| Shape | names | books | status |
|---|---:|---:|---|
| trailing keyword + number (`… Book 11`) | 18 | 31 | already handled |
| trailing bare number (`Discworld 05`) | 936 | 1,434 | already handled |
| **`<Series> N: <Title>`** | **303** | **343** | **NEW** |
| **`<Series> (N)` / `[N]`** | **238** | **307** | **NEW** |
| **`N - <Title>`** | **211** | **271** | **NEW** |
| **`<Series> Book N: <Title>`** | **17** | **61** | **NEW** |
| no position embedded | 10,478 | 29,672 | — |

**982 books across 769 distinct names** are in scope. (The owner's estimate was
~874; the difference is the parenthesised shape, which is easy to miss by eye.)

## 🔴 The reason this is dangerous, in the data

The existing parser matches trailing positions only. That is **not an oversight —
it is the safe half.** A leading or embedded number is far more often part of the
real title:

| Name | Reality |
|---|---|
| `86—EIGHTY-SIX` (17 books) | **Real series title.** "86" IS the name. |
| `5-Minute Sherlock` | **Real title.** "5-Minute" is a descriptor. |
| `08. Battle for the Abyss` | Genuine Horus Heresy position 8. |
| `11. Fallen Angels` | Genuine position 11. |
| `Station 64: The Doll Dungeon` | **Ambiguous.** Position 64, or a series called "Station 64"? |
| `The Demon Wars Saga [07] Immortalis [02]` | **TWO** numbers; 07 is the series position, 02 is not. A naive last-bracket match takes the wrong one. |
| `Renegade Star: Publisher's Pack 7: Renegade Star` | "Pack 7" is a bundle number, not a series position. |
| `The Best of the Best: Volume 2: …` (27 books) | Genuine anthology volume. |

Same shape, opposite meaning. This is why `IsJunkSeriesBase` exists and why it
must be extended **alongside** the parser, never after: that guard is what stopped
**285 bad merges** in the earlier dry run, and series merges are destructive.

## Design

### Locked decisions

**D1 — This is a DATA fix, not display.** The number belongs in the series
*position* field; the series *name* keeps only the base. The owner has corrected
this reading twice; do not re-derive it.

**D2 — Confidence tiers, not one regex.** Each shape gets a confidence, and only
`high` is eligible for auto-apply:

- **high** — an explicit keyword introduces the number (`Book 4`, `Vol 09`,
  `Part 2`). Keywords do not appear by accident.
- **medium** — a bracketed/parenthesised number at the end (`Dragon Born [04]`),
  with exactly ONE bracketed group in the name.
- **low** — a bare leading number (`08. Battle for the Abyss`). Correct often,
  catastrophically wrong on `86—EIGHTY-SIX`.

**D3 — Low confidence NEVER auto-applies.** It produces a review hold. There is
no threshold tuning that separates `86—EIGHTY-SIX` from `08. Battle for the
Abyss` on the string alone — they are the same shape. Corroboration must come
from outside the name (below).

**D4 — Corroborate a leading number against its siblings.** A bare leading number
is trustworthy only when the *same base* appears with *several different*
leading numbers. `08. Battle for the Abyss` / `11. Fallen Angels` /
`22. Shadows of Treachery` share no base text at all, so that signal is weak
here — but the books DO share an author and folder. Use: sibling books whose
series names differ only in the leading number, or which share the parent folder.
A lone occurrence stays `low`.

**D5 — Refuse multi-number names.** `The Demon Wars Saga [07] Immortalis [02]`
has two candidate positions. Refuse and hold for review rather than guessing
which one is the series position — guessing wrong writes a wrong position AND a
wrong base.

**D6 — Extend `IsJunkSeriesBase` in the same change.** New rejections needed:
- a base that is now empty or a single character after stripping
- a base that is purely a number-word artefact (`Volume`, `Pack`, `Publisher's Pack`)
- a base identical to the stripped title (`Rebirth Online 2: Rebirth Online` →
  base and title are the same string, which means the split found nothing real)

### Non-goals

- Renaming the underlying `series` rows or merging series. This change writes
  `series_position` and a corrected `series_name` per book; any merging of
  now-identical series names is a SEPARATE, later step, gated on its own dry run.
- Touching `books/itunes/**`.
- Guessing on the `low` tier.

## Failure modes

| Failure | Consequence | Mitigation |
|---|---|---|
| Real title parsed as a position | Series renamed to a fragment; books scattered | D3 low tier holds; D6 guard; dry run |
| Wrong number chosen (multi-number) | Wrong position AND wrong base | D5 refuses outright |
| Base collapses to junk | Bogus series absorbing unrelated books | D6 `IsJunkSeriesBase` extension |
| Position written, name unchanged | Duplicate-looking series persist | Both fields written in one re-fetch-then-mutate |

## Open questions

1. Should a corrected series name that now collides with an existing series
   auto-merge, or hold? (Proposed: hold — merging is the destructive part.)
2. `Renegade Star: Publisher's Pack 7` — pack numbers are not series positions.
   Add "pack" to the junk-keyword list, or treat as `low`?
