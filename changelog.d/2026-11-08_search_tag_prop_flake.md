### Fixed

- Fixed an intermittent `TestProp_TranslateValidQueries` failure: the property generator could draw `tag` as a field name, and `TranslateAt` refuses a `tag:` term by design (it is split off by `SplitTagTerms` first). The generator now skips `tag`, and a deterministic test pins the refusal.
