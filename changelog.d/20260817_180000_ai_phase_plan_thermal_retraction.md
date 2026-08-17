### Changed

- **Retracted the AI-batch concurrency recommendation in the scan-split plan.**
  `docs/plans/2026-08-17-split-scan-ai-phase.md` previously recommended giving the
  scan's AI batch loop a bounded worker pool of 2–4, on the strength of a probe
  showing 1.86×/2.43× speedups from concurrent requests. That probe used 8-token
  completions at ~0.27s each, while a real batch is 20 filenames taking ~21s — it
  measured request-latency headroom, not compute headroom. Direct GPU telemetry
  taken during the production scan shows the device at 92–93% utilization, i.e.
  saturated: a client-side pool has no spare throughput to claim. The plan's
  first PR is now just the inter-batch sleep removal (~10% of AI time).
- **Recorded that the LLM host was hardware thermal-throttling throughout the
  measurement.** The card ran at 97 C against its own 95 C shutdown spec and 92 C
  slowdown threshold, with `HW Thermal Slowdown: Active` and a cumulative
  slowdown counter of ~2h25m, holding clocks at 1860 MHz against a 2130 MHz
  maximum (~87% of rated clock). The measured 69.4% AI share of scan wall-clock
  therefore has a hardware component, not a purely architectural one, and any
  before/after comparison for the sleep removal must record GPU temperature and
  clock alongside it. Cancelling the load dropped the card 97 C → 61 C in 70s and
  cleared the latched throttle, so the cooler works; sustained 100%-duty inference
  simply exceeds it.
