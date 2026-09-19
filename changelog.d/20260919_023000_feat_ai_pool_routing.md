### Added

- `ai_endpoints_routing` config switch (default off). When on, scan filename parsing (`llm.filename_parse`, llm_mode local or openai-fallback-local) and embeddings (`embed.text`, embedding_mode local) pick their server from `ai_endpoints` through the dispatcher: only enabled rows that tick the capability, priority order, free-slot spillover under each row's concurrency, and failover to a peer on a transport error. Embeddings are pinned to the configured embedding model and fail closed when no row serves it.
- `GET /api/v1/ai/endpoints/status` reports `routing_active` from the switch and a per-endpoint `attribution` block (requests, failures, last_used, per-capability counts); every routed request logs its endpoint and capability.
