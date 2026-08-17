- [ ] **Investigate the LLM host's GPU cooling before running another full `library.scan`.**
      Measured 2026-08-17 during the scan's AI-parsing phase: the card held **97 °C against
      its own 95 °C shutdown spec** (slowdown 92 °C, max-operating 88 °C, target 83 °C),
      with `HW Thermal Slowdown: Active` and a cumulative slowdown counter of
      8,737,239,236 us (~2h 25m). Clocks were pinned at 1860 MHz against a 2130 MHz
      maximum — ~87% of rated clock — at 92–93% sustained utilization.
      Cancelling the load dropped it 97 °C → 61 °C in 70 s and cleared the latched
      throttle, so the cooler does move heat; sustained 100%-duty inference simply
      exceeds it. **`nvidia-smi` reports `[Unknown Error]` for fan speed on this card,
      which is unexplained and is the one thing warranting a physical look.**
      Two knock-ons, both recorded in `docs/plans/2026-08-17-split-scan-ai-phase.md`:
      a client-side worker pool on the AI batch loop is off the table (the GPU is
      saturated, not idle), and the measured "AI parsing is 69.4% of scan wall-clock"
      figure is thermally confounded — any re-measurement must record GPU temperature
      and clock alongside it. Recovering 1860 → 2130 MHz is ~14.5% on the phase that is
      ~69% of the scan, for zero code risk.
