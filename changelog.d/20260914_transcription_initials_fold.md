### Fixed

- **The strict transcription author check treats initials spacing and
  punctuation as equal.** `util.MainTranscriptionConfirms` (the rule every
  unreviewed apply ANDs through `applygate.TranscriptionConfirms`) now compares
  authors through a comparison-only `foldInitials`, so "R.A." = "R. A." = "RA"
  = "R A". Sojourn (Drizzt #3), heard as "R.A. Salvator" against the provider's
  "R. A. Salvatore", was blocked with `transcription_mismatch` for that spacing
  alone and now confirms. Different initials ("J.R." vs "R.A.") and a different
  surname still refuse; nothing else changes (owner decision 2026-09-14).
  `util.NormalizeAuthor`, which keys the Pebble name indexes, is unchanged.
