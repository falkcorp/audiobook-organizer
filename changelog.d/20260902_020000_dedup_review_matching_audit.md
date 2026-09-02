### Documentation

- Added `docs/audits/2026-09-02-dedup-review-matching-path-audit.md`: a census of every
  dedup and review surface (13 `/review` rows, 13 `/dedup` rows), the five verdict systems
  that rate book pairs independently of `unified.ComposeScore`, ten concrete divergence
  pairs, and eight defects — including `Engine.SetScoreConfig` having zero callers (so the
  configured band thresholds are inert for live scoring) and legacy `Layer=="exact"` rows
  being permanently unable to acquire a score breakdown. Ends with a `MatchExplanation`
  unification design in three tiers and ten owner decisions.
