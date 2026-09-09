### Fixed

- **AI filename parsing could never succeed on a CPU-only LLM backend.** The
  per-batch deadline and the batch size were both hardcoded (30 seconds, 20
  filenames) and calibrated for a hosted API or a GPU. Measured against a
  CPU-only Ollama, one 20-filename batch emits ~1,350 completion tokens and takes
  **105s on qwen2.5:3b and 201s on qwen2.5:7b** — so every batch hit the deadline,
  the phase parsed 0 books, and the summary looked the same as a healthy run.

  Both are now configurable as a pair via `ai_backend.parse_batch_size` and
  `ai_backend.parse_batch_timeout_seconds` (env: `AI_BACKEND_PARSE_BATCH_SIZE`,
  `AI_BACKEND_PARSE_BATCH_TIMEOUT_SECONDS`). Unset keeps the historical 20/30s,
  so installs pointed at a hosted API are unaffected. The timeout is clamped
  below the stuck-op watchdog's 5-minute ProgressTimeout, because a batch may
  occupy the whole deadline and going over it converts a batch failure into a
  killed operation.

### Changed

- `OpenAIParser.ParseBatch` now **rejects** an over-ceiling batch instead of
  silently truncating it to 20 filenames. The truncation was invisible to
  callers — the scanner phase counts a batch as successful by the absence of an
  error — so a larger configured batch size would have dropped filenames while
  reporting success.
