## ⚠️ `TestSearch_ConformsToOracle` failed once and has not reproduced in 27 runs

Recording this because it was observed, not because it is understood. Do not treat the
suite as proven stable until someone explains it or it recurs with a mechanism attached.

On the very first execution of the reseeded ABS conformance suite (2026-08-12, before the
work was committed), `TestSearch_ConformsToOracle` failed with six `value_mismatch`
findings:

```
value_mismatch at book[1].libraryItem.media.duration:
    want 9975.431111000002, got 9976 — gap 0.5688889999983076 EXCEEDS the 0.5 allowed by
    "book[].libraryItem.media.duration"
value_mismatch at book[1].libraryItem.media.tracks[1].startOffset: want 1386.057143, got 1386
value_mismatch at book[1].libraryItem.media.tracks[2].startOffset: want 2788.702041, got 2789
value_mismatch at book[1].libraryItem.media.tracks[3].startOffset: want 4309.211429, got 4310
value_mismatch at book[1].libraryItem.media.tracks[4].startOffset: want 6928.9792290000005, got 6930
value_mismatch at book[1].libraryItem.media.tracks[5].startOffset: want 8602.200453000001, got 8603
```

**The part that does not add up.** The message says a **0.5** bound was applied to
`book[].libraryItem.media.duration`, but that key is declared in `browse_test.go` with
`Within: 3.0`, and `book[].libraryItem.media.tracks[].startOffset` is declared `Within: 3.0`
as well — so on the code as written, none of those six gaps (max 1.02) should have been
findings at all. Either the run used a build that predated those two entries, or allowance
selection can pick a different entry than the one intended.

**Reproduction attempts, all clean:**

| invocation | runs | failures |
|---|---|---|
| `-run TestSearch_ConformsToOracle` alone | 20 | 0 |
| full `./internal/server/handlers/abs/` package | 2 | 0 |
| `./internal/server/handlers/abs/ ./internal/syncapi/conformance/` (the exact original command) | 5 | 0 |

27 runs, zero failures.

**Hypothesis considered and NOT confirmed:** that allowance lookup ranges over the
`map[string]allowance` and takes the first pattern that matches, making the chosen bound
depend on Go's randomised map iteration order. That would make the suite flaky by
construction and would explain a 0.5 bound being applied where 3.0 is declared. 20 runs of
the isolated test did not produce it, so this is a lead, not a finding. If it recurs, look
at the matcher first: check whether more than one declared pattern can match
`book[].libraryItem.media.duration`, and whether selection is deterministic (most-specific
wins) or iteration-order dependent.

**Why it matters even though it passes now.** The whole point of the bounded-allowance
design is that a bound states the widest gap its stated cause can produce. If which bound
applies is nondeterministic, the design's guarantee does not hold — a too-wide bound could
silently accept a real divergence on some runs. That is a worse failure than the one
observed, because it is invisible.

Related, and already filed separately:
`todo.d/20260812-bookfile-duration-integer-seconds.md` — `BookFile.Duration` is an `int`,
so the store holds whole seconds and per-track truncation accumulates across `startOffset`.
That is the underlying product issue the allowances exist to bound.
