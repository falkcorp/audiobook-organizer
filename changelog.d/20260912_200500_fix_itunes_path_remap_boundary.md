### Fixed

- iTunes path remapping now matches a configured prefix only on a
  path-separator boundary. Previously a mapping for `/x/lib` also rewrote
  `/x/lib2/...` into `<To>2/...`, producing a wrong path. Fixed in
  `itunes.ImportOptions.RemapPath` (including its case-insensitive drive-letter
  fallback), `itunes.ReverseRemapPath`, `reconcile.TranslateITunesPath`, the
  importer's `remapWindowsPath`, and write-back's `metafetch.ComputeITunesPath`,
  via the new shared helpers `pathutil.CutPathPrefix` / `CutPathPrefixFold`
  (`/` and `\` both count as separators). Output for every path that still
  matches is byte-identical to before.
