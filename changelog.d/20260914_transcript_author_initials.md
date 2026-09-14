### Fixed

- The apply gate's transcription check no longer blocks a matching book
  because of how the author's initials are spaced: the catalog's "R. A.
  Salvatore" now agrees with Whisper's "R.A. Salvator" (Sojourn was blocked as
  transcription_mismatch on a transcript naming it). The author name index
  keys are unchanged.
