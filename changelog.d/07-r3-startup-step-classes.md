### Changed

- Every server startup step is now classified in one reviewed table (`internal/startup.Table`) as fatal (the process stops before it serves) or degraded (the server keeps running and `/health` lists a fixed token in `degraded`). A failure to start OpenTelemetry metrics no longer stops the server: it serves with metrics off and `/health` reports `metrics_disabled`. A search index that fails to open is now reported as `search_index` instead of the server running without search silently.
- A failure to read the persisted settings at startup now stops the server. It used to log a note and run on defaults, and because a settings save writes the whole configuration, the first save from that process would have overwritten the stored configuration with those defaults.
