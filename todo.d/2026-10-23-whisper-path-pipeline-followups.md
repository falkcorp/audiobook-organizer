- [ ] **TODO-WHISPER-PATH-FOLLOWUPS** Whisper path pipeline follow-ups (#4115):
      (1) abort a transcribe-book-intros run after K consecutive fully-deferred
      pages, so a lost mount or a misconfigured worker does not walk the whole
      library deferring every page; (2) add a Python test that drives the real
      ffmpeg timeout/kill path in `_extract_clip` (only the classification of a
      nonzero exit is tested today, with subprocess faked); (3) validate the
      worker's result ids against the chunk that was sent in
      `internal/transcribe/path.go`, so an unexpected or missing id is an error
      rather than silently ignored.
