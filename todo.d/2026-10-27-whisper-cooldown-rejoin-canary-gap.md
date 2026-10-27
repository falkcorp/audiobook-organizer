- [ ] **TODO-WHISPER-COOLDOWN-REJOIN-CANARY** Check (unverified finding from the 2026-10-27 canary-flake
      investigation): a sick machine's port that is already in cooldown when a dispatch starts never faults, so
      `runCanaries` never asks it (it only treats a port that faulted *and* is in cooldown as unaskable), while the
      machine passes the canary on its sibling ports and `canaryRan[machine]` is set. If that port's cooldown ends
      mid-dispatch it takes work with no canary of its own; if it is truly sick its sub-breaker units could produce
      believed per-clip strikes — the gap the per-port canary ("SF2") was meant to close. Write a repro first
      (internal/transcribe dispatcher; land after #4201, which rewrites the canary path).
