### Added

#### `embedding.request_timeout_seconds` — the embeddings request budget is now configurable

The per-attempt timeout for embeddings requests was a hardcoded 30 seconds, and a cold model load on a local Ollama backend was measured at 25 seconds of it. The new key (env `EMBEDDING_REQUEST_TIMEOUT_SECONDS`, also in Settings → Embeddings) feeds `WithRequestTimeout` at both construction sites: 0 keeps the 30 s default, values above 90 s are clamped and logged at startup so three retries plus backoff stay under the operations watchdog's 5-minute inactivity kill. The OpenAI path is unchanged unless the key is set.
