### Fixed

- **Write-back no longer rewrites every multi-file book on every run.**
  `FilterUnchangedTags` had no mapping for the `track` tag, while the multi-file
  write path always emits one (`"n/total"`). Track therefore fell through to the
  "unknown key — always write" branch, which made the skip condition
  `len(tagMap) == 0` unreachable for every multi-file book: each rewrote all of
  its audio files on every write-back run, indefinitely. Since a single tag write
  costs several full-file copies and SHA-256 passes over the audio, this was the
  dominant cost of write-back on large libraries.
