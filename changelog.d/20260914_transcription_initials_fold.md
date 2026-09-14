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
- **Classification rule: a token is initials only by its raw, un-lowercased
  text.** A token containing dots whose letters are each single ("R.",
  "R.A."), a single letter ("R"), or a dotless run of letters that are all
  uppercase ("RA", "JRR"). An all-caps run counts only when the name also
  has a lowercase letter somewhere, because in an all-caps "KIM STANLEY" case
  says nothing. A dotless mixed- or lower-case token is a first name, so
  "Ra Salvatore" vs "R. A. Salvatore", "Ed McBain" vs "E. D. McBain",
  "Jo Nesbo" vs "J. O. Nesbo", "Kim Stanley" vs "K. I. M. Stanley" and
  "Al Franken" vs "A. L. Franken" all refuse. Hyphen, apostrophe and
  non-ASCII dot tokens are never folded. The transcribed author keeps
  Whisper's casing through the intro parser, so the rule applies on both
  sides.
