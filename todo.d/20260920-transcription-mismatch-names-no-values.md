- [ ] **`transcription_mismatch` refusals name neither side's values.**
      It is the single biggest refusal bucket (147 events / 81 books on
      2026-09-20, 41%), and its Detail is the fixed sentence "book has a
      transcribed title the candidate does not match" — no titles, no authors,
      no indication of WHICH leg failed. `runtime_mismatch` next to it prints
      "files 905 min, candidate 452 min (50% off)" and is diagnosable from the
      log alone; this one required reading `applygate.ScoreGate` and
      `util.MainTranscriptionConfirms` to explain a single refusal.
      Make it report the failing leg and both values. Pure observability, no
      behaviour change — and it is the prerequisite for answering whether the
      gate's exact-title + substring-author rule is calibrated right for Whisper
      output, where a one-letter surname error is the norm.
      Evidence: per-book `book not applied` lines in the op logs (`GET /operations/v2/<id>/logs/download`, zstd).
