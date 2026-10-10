- [ ] **CHROMEM-LIVE-VS-HYDRATED-VECTORS** The batch-embed path mirrors the raw
      API vector into chromem (`internal/dedup/engine.go:2957`), while the
      Pebble store keeps the float16 round trip and the startup hydrate
      (`engine.go:649`) mirrors that decoded vector. So the live index and a
      freshly hydrated one hold slightly different vectors (float16 drift,
      documented as under 1e-3 at 3072 dims). That is negligible at the
      0.85/0.95 dedup thresholds, but a candidate scored right at a threshold
      can flip after a restart. Fix: mirror the decoded vector on the embed path
      too, so both indexes hold exactly what the store holds. Found while fixing
      PROP-CHROMEM-FLAKE (2026-10-10).
