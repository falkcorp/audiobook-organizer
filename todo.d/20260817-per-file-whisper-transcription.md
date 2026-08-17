### 🎯 Move intro transcription from per-book to per-file (LOW priority)

Stated goal 2026-08-17: **every file** should carry a Whisper transcription, to help
connect books and for other matching work. Today `maintenance.transcribe-book-intros`
(`internal/plugins/maintenance/intro_transcribe.go`) paginates **books** — 44,877 of
them, 42,884 already transcribed — while there are **532,296** `book_file` rows, so
per-file is roughly a 12× larger job.

Per-file storage already exists and is well shaped for it: `IntroTranscription` is on
`BookFile` (`internal/database/store.go:854`), and the parsed fields
(`TranscribedTitle` / `Author` / `Narrator` / `Translator` / `CoverArtist`) are
**retained** in the memdb core — only the raw transcript blob is stripped, because it
carries ~99% of the group's bytes (`internal/database/bookfilecore.go:84`).

Sizing notes:
- Transcription is a single remote faster-whisper endpoint (`WHISPER_REMOTE_URL`).
  `WHISPER_ENDPOINTS` supports a **pool** and is unset — that is the throughput lever
  if this is ever run at per-file scale.
- The GPU thermal block was lifted 2026-08-17 (cooling improved), but post-fix
  thermals are unmeasured; record temp and clock alongside any throughput figure.

Explicitly LOW priority — per-book is fine for now.
