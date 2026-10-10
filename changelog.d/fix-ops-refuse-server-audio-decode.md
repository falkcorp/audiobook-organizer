### Fixed

- `acoustid.fingerprint-rescan`, `maintenance.extract-wav-clips` and `maintenance.transcribe-book-intros` now refuse to decode audio in-process unless the host sets `ALLOW_SERVER_DECODE=1`; `reparse_only` runs are unaffected and the library-optimize sweep skips the fingerprint-rescan child when decoding is not allowed.
