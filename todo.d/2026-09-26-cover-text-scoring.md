- [ ] **COVER-TEXT-SCORING** Score the stored cover text. `unified.SigCoverText`
      (`cover_text`) is registered but non-scoring (supporting kind, no boost),
      and `covertext.ForBook` is its reader. Decide and calibrate: (1) dedup —
      two books whose covers read the same title+author (normalized) as a
      supporting boost, never a primary on its own; (2) identification — cover
      title/author/series agreeing with a provider candidate as evidence in
      the candidate ranking. Measure on the prod store after
      `maintenance.cover-text-read` has run, before picking weights.
- [ ] **COVER-VISION-LEGACY-SITE** `entities_ops.go` still calls the legacy
      cloud `ParseCoverArt` for its cover-art AI pass. Move it onto
      `ai.RoutedCoverTextReader` (or read the stored cover text) so every
      vision call goes through the pool routing.
