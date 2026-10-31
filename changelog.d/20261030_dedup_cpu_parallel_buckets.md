### Changed

- Metadata duplicate detection (`GetDuplicateBooksByMetadataCore`, both the Pebble scan path and the in-memory store) now scores its title buckets on a bounded worker pool instead of one core. Buckets are disjoint, each writes only its own result slot, and the slots are merged in first-seen order, so the groups and their order are identical to the serial result. Each title is normalized once per bucket and each pair is scored once through the new `matcher.Prepare` / `matcher.SymmetricScore`, instead of normalizing both sides from scratch for each of two `ScoreMatch` calls. On a synthetic 2,000-item set (20 buckets of 100) the pass went from about 3.1 s to 1.05 s on one core and 0.55 s at two cores, with allocations down from 7.7M to 19K.
- `matcher.LevenshteinDistance`, `matcher.LevenshteinDistanceCaseSensitive` and the author Jaro-Winkler comparison no longer allocate: they reuse pooled rune and DP buffers (the author phase-3 scan workers hold one scratch each). Results are bit-identical to the previous implementations on random mixed-case, non-ASCII and invalid-UTF-8 input.

### Removed

- `matcher.ScoreMatch`, `matcher.TokenSetRatio` and the unexported `tokenSimilarity`, which the change above left with no production caller (the dead-code ratchet requires deletion). `matcher.SymmetricScore` returns exactly the larger of the two directional `ScoreMatch` scores.
