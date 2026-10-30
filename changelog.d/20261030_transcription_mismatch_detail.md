### Fixed

- `transcription_mismatch` refusals now say which leg failed (exact title, substring author, or the shared matcher) and print the transcribed and candidate title/author, in the style of `runtime_mismatch`. `util.MainTranscriptionConfirms` became `util.MainTranscriptionMatch`, returning a structured result; the gate decision is unchanged.
