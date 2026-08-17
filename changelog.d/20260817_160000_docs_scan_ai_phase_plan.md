### Added

- **Plan: split the scan's AI-parsing phase out of the metadata pass**
  (`docs/plans/2026-08-17-split-scan-ai-phase.md`). Measured on the running
  production scan, AI parsing consumes **66.9% of scan wall-clock** while the
  metadata pass consumes 33.1%; excluding AI time moves throughput from 0.92 to
  2.78 books/s over the sampled window.

  The AI phase is also the phase the checkpoint cannot see — it runs inside each
  500-book chunk, before the chunk-end checkpoint, so ~12 minutes of work per
  chunk is repeated on restart.

  The plan records a hazard that keeps it a plan: AI results are merged into the
  book *before* it is first saved, so splitting the phases means the AI pass must
  update already-persisted rows — and a bare whole-row update is this repo's
  dominant data-loss shape. A narrow field-level update lands first, alone.

  One finding is separable and carries none of that risk: the batch loop sleeps
  2s between batches as a rate-limit courtesy, but production's LLM backend is a
  local Ollama with no quota, and a direct probe shows it serves concurrent
  requests at 1.86×/2.43× (n=3/n=6) rather than serializing.
