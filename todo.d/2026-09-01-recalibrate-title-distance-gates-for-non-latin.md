## Re-calibrate the absolute title-distance gates for non-Latin scripts

`levenshteinDistance` became rune-based (fix/levenshtein-rune-unify). That fixed
the similarity *ratio*, but the same function also feeds three **absolute**
distance gates, where a smaller distance ADMITS more pairs:

- `internal/dedup/engine.go:1458` — `if dist >= 3 { continue }`, and a pair that
  passes is filed by `upsertExactCandidate(..., "exact", 1.0)`
- `internal/dedup/engine.go:1619`, `:1646` — `titleDist <= durationLevenshteinMax` (6)
- `internal/dedup/collectors_metadata.go:224`, `:258` — same via `cfg.LevenshteinMax`

The threshold "within 2 edits" was calibrated against 25-character ASCII titles,
where 2 edits is noise. On a 6-rune CJK title, 1 edit is a different word:

    銀河鉄道の夜 / 銀河鉄道の父   byte d=3 (rejected)  rune d=1 (accepted)
    吾輩は猫である / 吾輩は猫でない  byte d=3 (rejected)  rune d=2 (accepted)

The byte count was accidentally supplying length-scaling; rune distance is
correct but exposes that the gate itself is ASCII-shaped. The two downstream
guards are ASCII-shaped too: `extractSeriesNumberFromTitle` (`engine.go:2111`)
and `titlesDifferOnlyInDigits` (`engine.go:2119`) key on ASCII digits and
`book`/`bk`/`vol`, so CJK volume markers (`巻`, `上/中/下`, `一二三`) pass unguarded.

Bounds: same-author pairs only; `hasUsableTitle` needs >2 runes; no auto-merge
results (`autoResolvePrimaryKinds` has no title-based kind, `handleFileHashMatch`
merges on file hash only). Ceiling is review-queue pollution labelled
"exact"/1.0, not data loss.

Decide whether these gates should become length-relative. Needs calibration data
— a naive relative bound also changes behaviour for SHORT ASCII titles ("Dune"
vs "Rune" is 1 edit), which is the population that currently works. Measure
before picking a constant.
