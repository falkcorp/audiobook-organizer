<!-- file: changelog.d/20260803_202000_transcription_fields_allowlist.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5e83b06f-1d47-4a29-95c8-72fb3e0a1d64 -->
<!-- last-edited: 2026-08-03 -->

### Fixed

- Auto-applying a transcription match wrote the **entire** metadata candidate —
  narrator, series, series position, year, publisher, ISBN, description,
  language and cover URL — even though only **title and author** were ever
  checked by the gates that authorised the apply.

  `ApplyTranscriptionCandidate` passed a nil field list to
  `ApplyMetadataCandidate`, and nil means "no allowlist". The apply is now
  narrowed to `["title", "author"]`, matching exactly what
  `runAutoMatchTranscribed` gates on (normalized exact title equality plus author
  containment). Widening it is now a deliberate edit rather than a default.
