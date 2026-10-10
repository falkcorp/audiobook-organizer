### Fixed

#### `TestProp_ChromemMatchesSqlite` no longer fails on scores that sit on the threshold

The property test compared chromem's and the embedding store's above-0.5 sets
with zero tolerance. When a document's cosine landed within rounding distance
of 0.5, the two backends could legitimately disagree. The embedding store
persists float16 and scores the decoded vector, which can move a cosine by
about 1e-4. chromem normalises and dots in float32, and its result differs by
one ulp between amd64 and arm64 because arm64 fuses the multiply-adds. Both CI
failures were this: on 2026-10-10 a score of 0.500041 dropped to 0.499969
after float16 storage, and on 2026-10-09 an exact score of 0.4999999928 came
out as 0.5 in one backend and 0.49999997 in the other (amd64 only). Neither
backend is wrong, so production code is unchanged.

The test now checks both backends against a float64 reference computed on the
vector each one actually scores: float32 for chromem, the float16 round trip
for the store. Documents within 1e-5 of the threshold, or whose two references
fall on opposite sides of it, are excluded. Every other document must be in
both result sets or in neither, so the test now requires exact agreement
instead of 50% overlap. The 50% slack was justified by HNSW, but chromem-go
v0.7.0 is a brute-force scan. Each returned score must also be within 1e-5 of
its reference. Both CI counterexamples are pinned in
`TestChromemMatchesSqlite_PinnedCounterexamples`.

`testdata/rapid/` (rapid's failfiles, which are replayed first on every later
run) is now ignored anywhere in the tree, so a failing run cannot be committed
by accident.
