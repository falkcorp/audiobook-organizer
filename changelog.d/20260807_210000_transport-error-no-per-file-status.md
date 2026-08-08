<!-- file: changelog.d/20260807_210000_transport-error-no-per-file-status.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3d9e6f1a-4b7c-4e28-a5d0-8c2f9b1e6d47 -->
<!-- last-edited: 2026-08-07 -->

### Fixed

- **Transport failures no longer write per-file transcription status.** When
  the Whisper batch call fails with a `*transcribe.TransportError` (endpoint
  unreachable, connection refused, timeout — the request never reached a
  model), `processTranscribePage` now writes ZERO `TranscribeStatus` rows and
  defers the page for retry on the next run, instead of stamping every book
  in the batch as `whisper_error`. This closes the caller-side half of the
  bug that recorded the 2026-07-01 day-long endpoint outage as ~34,000 false
  per-book `whisper_error` verdicts (the error-classification half shipped in
  PR #2173; the historical rows were repaired by
  `maintenance.repair-transcribe-status`). Per-file errors reported inside a
  successful batch (`BatchResult.Error`) still write `whisper_error` for that
  book, and genuine non-transport batch failures keep the old whole-batch
  behaviour as defence for the local path. Regression tests cover both
  directions in `internal/plugins/maintenance/intro_transcribe_transport_test.go`.
