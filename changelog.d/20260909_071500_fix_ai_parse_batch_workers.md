### Fixed

- **AI filename parsing lost batches to its own concurrency against a
  one-at-a-time LLM backend.** The phase ran 4 batches in flight (a hardcoded
  `aiBatchWorkers = 4`), but Ollama defaults to `OLLAMA_NUM_PARALLEL=1` and
  serves a single request at a time — so 3 of every 4 batches sat in the
  server's queue spending their own per-batch deadline without doing any work.

  Measured on prod: 20 books at batch size 4 produced 5 batches; batches 1, 3
  and 5 parsed 12 books while batches 2 and 4 timed out having only queued.
  Total throughput was unchanged by the extra workers — the backend was the
  bottleneck either way — so the concurrency bought nothing and cost 8 books.

  In-flight batches are now configurable via `ai_backend.parse_batch_workers`
  (env `AI_BACKEND_PARSE_BATCH_WORKERS`), defaulting to the previous 4 so
  backends that genuinely fan out are unaffected. Setting it to 1 for a serial
  backend makes the per-batch deadline mean "the model is too slow" again
  rather than "something else was ahead of me in line".

### Changed

- `Config.ResolveAIParseBatch` now returns an `AIParseBatchSettings` struct
  (size, timeout, workers) instead of a size/timeout pair. All three are coupled
  through the backend's concurrency: size sets how long a call takes, workers
  sets how long a call *waits* before starting on a serial backend, and the
  timeout has to cover whichever the deployment actually produces.
