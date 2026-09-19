### Changed

- `dedup.embed-scan` sizes its worker pool from the embedding pool's routed capacity when `ai_endpoints_routing` is on (the sum of the same-model, local `embed.text` endpoints' concurrency caps), instead of a fixed 4. With the fixed 4 equal to one endpoint's slots, a second embed host never received work. With routing off it keeps the fixed 4. New `EmbeddingClient.RoutedCapacity` and `dedup.Engine.EmbedConcurrency`.
