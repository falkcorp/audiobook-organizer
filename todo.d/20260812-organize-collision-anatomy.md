## ✅ ANSWERED — the organize target-path collisions are DISTINCT BOOKS, not one book's files

This closes the "are the 3,194 collisions distinct books or one book's files?" question.
**They are distinct book rows, confirmed by ID.** But the headline number was wrong in a
way that matters, and the shape of the collision is not what the earlier note implied.

### How it was measured

Every `target path already occupied by a different file` line on the production host
since 2026-08-10: **19,519 lines, fully accounted for** —

| bucket | lines |
|---|---|
| `operation: Organize failed for <title>: …` | (parsed) |
| `operation: Failed to organize <title>: …`  | (parsed) |
| **both shapes together** | **18,519** |
| `[WARN] activity channel full, dropped: operation: …` | 1,000 |

Two different phrasings for the identical failure means **two emit sites**. Any grep that
matches only one of them under-reports by ~40%. That is how the first pass at this
counted 12,213 and thought it had everything.

### Per-run breakdown (runs split on a >300s gap between failures)

| run | window (Aug 11) | failures | distinct titles | distinct targets | contain "read by narrator" |
|---|---|---|---|---|---|
| 0 | 02:00:30 → 02:01:00 | 1,098 | 837 | 874 | 827 |
| 1 | 02:17:18 → 02:34:07 | 782 | 735 | 743 | 750 |
| 2 | 06:36:18 → 06:36:22 | 1,098 | 837 | 874 | 827 |
| 3 | 06:53:15 → 08:41:12 | 6,514 | 2,702 | 3,418 | 5,956 |
| 4 | 09:09:14 → 10:04:15 | 4,736 | 4,703 | 4,716 | 4,184 |
| 5 | 10:25:23 → 10:44:50 | 1,636 | 971 | 1,005 | 1,258 |
| 6 | 22:34:02 → 22:37:16 | 2,655 | 475 | 478 | **0** |

### Finding 1 — the collisions are distinct books, verified against the DB

The top colliding target in run 6 is hit **848 times by a single title** (the empty
string). A title alone cannot distinguish "848 distinct books that all have a blank
title" from "one book logged 848 times", so the log was not sufficient. Querying the
API by exact title settles it:

| title | books in DB with that exact title | times it collides in run 6 |
|---|---|---|
| `Clarke, Susanna` | **128** (distinct IDs) | 120 |
| `nobody103 (Jack Voraces)` | **176** (distinct IDs) | 84 |

Distinct book IDs, same title, same author → **identical expanded target path**. Every
book after the first finds the path occupied. This is a stampede of distinct books onto
one name, and it is the dominant failure mode.

Note what those two titles are: an **author name** (`Clarke, Susanna`) and a
**narrator credit** (`nobody103 (Jack Voraces)`) sitting in the *title* field. The
collision is downstream of the metadata-parser contamination already tracked elsewhere —
fixing the titles would dissolve most of these collisions without touching the organizer.

### Finding 2 — the "read by narrator" fix was ORTHOGONAL to the collisions

Run 3 (pre-fix, paths contain the literal) and run 6 (post-fix, zero occurrences) show
the same pileup on the same degenerate name:

```
run 3:  .../Unknown Author/Unknown Title/Unknown Title - Unknown Author - read by narrator.mp3   x852
run 6:  .../Unknown Author/Unknown Title/Unknown Title - Unknown Author.mp3                       x848
```

852 → 848 across the fix. **The narrator literal never caused these collisions.** It was
correlated with them only because both are symptoms of the same missing metadata. The
comment in `internal/organizer/organizer_test.go` that read "2,611 of 3,194 occupied-path
organize failures contained 'read by narrator'" invited exactly the wrong inference and
is corrected in the same PR as this fragment.

### Finding 3 — the mode is not constant across runs, and that is unexplained

Run 4 is 4,736 failures over 4,716 distinct targets — essentially **1:1, no stampede at
all** — while runs 3 and 6, on the same day and the same library, are heavily stamped.
Whatever distinguishes run 4 (a different book population, a filter, a different entry
path into organize) is **not measured**. Do not assume a single cause for all seven runs.

### ⚠️ The 3,194 figure is not reproduced by this data

No run produced 3,194 failures, and no run produced 2,611 narrator-literal lines. The
closest candidates are run 6 (2,655) and run 5 (1,636). The original 3,194/2,611 pair
came from some other source — most likely one operation's `stats.Failed`, which counts
*all* failures rather than only occupied-target ones, or a run before the 2026-08-10 log
horizon. **Treat 3,194 as unverified** until whoever recorded it says where it came from.
The per-run table above is the measured replacement.

### What is NOT claimed

- How many books have a genuinely blank title. The obvious query for it is broken — see
  the sibling fragment on empty `FieldFilter` values returning the whole library.
- Whether the occupying file at each target is a real organized book (case 3) or an
  orphan from a partial organize (case 4). `organizer.go` distinguishes these internally
  but logs both identically.
- Why run 4 shows no stampede.

### 🔴 OWNER DECISION — do not pick this unilaterally

`generateTargetPath` has **no uniqueness guarantee**. When the naming pattern expands to
the same string for N books, all N target one path and N−1 fail. The existing empty-stem
fallback in `generateTargetPath` makes this worse in a specific way: it was added because
an empty stem produced a bare `.m4b` that "EVERY such book collides on", and it falls
back to `defaultTitle` — *a constant*. That trades a collision on one name for a
collision on another name. It is working exactly as written.

Three ways out, and the choice changes what lands on disk in a 63,870-row production
library, so it is yours:

1. **Refuse** to organize a book whose expanded path is degenerate (title and author both
   defaulted), reporting "insufficient metadata to name a unique file" instead of a
   collision. Honest, but it is a real behaviour change: the *first* such book currently
   succeeds and would stop succeeding.
2. **Disambiguate** — append the book ID or the source filename stem when the expanded
   path is already claimed. Nothing stops being organized, but ~848 files get names with
   an ID in them.
3. **Leave it** and fix the upstream metadata instead. Given that 128 books are titled
   with an author's name and 176 with a narrator credit, this may dissolve the problem at
   the source — but it is the slowest of the three.

A detection-only counter (report "N books have insufficient metadata to name a unique
file" at the end of an organize run, changing nothing on disk) is safe to add ahead of
this decision and would give a real number for option 3.
