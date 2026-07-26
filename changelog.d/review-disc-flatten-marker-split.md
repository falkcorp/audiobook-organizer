<!-- file: changelog.d/review-disc-flatten-marker-split.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7d2f9a04-3b61-4e58-8c07-1a5e2b9d6c83 -->
<!-- last-edited: 2026-07-26 -->

### Changed

#### Review queue: flatten multi-disc to continuous tracks; split single- vs multi-book markers

**Discs are flattened away (owner decision).** A combined book is now ONE continuous
track list — approving a multi-disc set no longer preserves disc numbers. Every file
gets `DiscNumber = 0` and a single continuous `TrackNumber` 1..N over play order,
including across former disc boundaries:

```
Disc 1/Ch1 → t1   Disc 1/Ch2 → t2   Disc 2/Ch1 → t3   Disc 2/Ch2 → t4
```

The classifier now parses the within-disc chapter from the filename so that ordering is
correct (disc-then-chapter), then discards the disc — it's used only to sort. This
revises the disc-preserving behavior shipped earlier in the day.

**Anthology markers split by single- vs multi-book (owner: "make it smarter").** The
folder-marker detection is now two regexes:

- **Single-book** (`anthology` / `omnibus` / `collection`) = one published book, one
  ISBN → still **combine** into one audiobook.
- **Multi-book** (`trilogy` / `tetralogy` / `quartet` / `boxed set`) = potentially
  *several* books, each its own ISBN → no longer suggested for combine; held as
  **ambiguous** with a "may be several separate books" note so the human decides. If a
  folder carries both markers, the multi-book (safer) reading wins.

This closes the trilogy/boxed-set edge flagged in the previous change: a "Foundation
Trilogy" folder of three distinct novels is no longer proposed as a single-book combine.
Labels updated accordingly ("Multi-disc source: N tracks → 1 book (flattened)").
