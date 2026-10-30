<!-- file: docs/decisions/2026-10-30-transcription-mismatch-detail.md -->
<!-- version: 1.0.0 -->
<!-- guid: c477b298-c258-45e0-9fbe-69a3f513180a -->
<!-- last-edited: 2026-10-30 -->

# transcription_mismatch refusal detail

## Decision log

- Chosen: `util.MainTranscriptionConfirms` (bool) was replaced by `util.MainTranscriptionMatch` returning a `TranscriptionMatch` struct (title leg, author leg, normalized values compared; `Confirms()` is the old bool). All callers updated.
- Chosen: `applygate.CheckTranscription` returns a `TranscriptionCheck` that also covers the shared-matcher leg (`TitleAgrees`/`AuthorAgrees`), because `TranscriptionConfirms` ANDs it and it can be the failing leg even when main's two legs pass. `TranscriptionConfirms` is now its `.Confirms`.
- Rejected: a parallel helper that recomputes the legs for the Detail alone (can drift from the decision).
- Rejected: changing `ScoreGate`'s return signature to carry the detail (4 external callers; the Detail is built where runtime_mismatch's already is).
- No gate decision changes.
