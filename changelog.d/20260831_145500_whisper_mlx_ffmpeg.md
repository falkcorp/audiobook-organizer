### Fixed

- **The Metal Whisper worker silently failed every transcription when started by
  launchd.** `mlx_whisper` shells out to `ffmpeg`, and a launchd agent gets the
  minimal default PATH, which does not include Homebrew. The failure was
  invisible in the worst possible way: the batch protocol carries per-file errors
  *inside* a 200 response, so the transport was healthy, `/health` reported `ok`,
  and the caller recorded one `whisper_error` per book. A full backfill made
  2,472 batch requests, every one HTTP 200, and produced **zero transcripts and
  21,443 `whisper_error` rows**.

  The agent now sets PATH explicitly, and the worker **refuses to start** without
  `ffmpeg` rather than serving requests it cannot fulfil — no process means no
  `/health`, so the dispatcher's capability gate refuses the endpoint and defers
  the page instead of writing errors across the library. `/health` also reports
  the resolved `ffmpeg` path.
