### Fixed

- **Operation progress labels reported state from before their own item ran.**
  `registry.RunItems` rendered each item's label *before* invoking the work
  function and reused that same string for the post-completion
  `UpdateProgress`. Because labels typically close over running tallies and all
  `Concurrency` workers snapshot their label at dispatch — before any of them
  finishes — the reported counts lagged by up to one full worker pool. Measured
  on `maintenance.chapters-backfill`: a 12-book apply run that persisted all 12
  printed `persist=0` on ten of its twelve lines and never rose above 2. On a
  whole-library run this reads as hours of total failure. The label is now
  re-rendered after the work function; `SetCurrentItem` still receives the
  pre-work label, which correctly names the item being started.
- **`maintenance.chapters-backfill` progress counted only persisted books**, so
  a dry run — which never increments that counter — sat at zero for its entire
  duration and was indistinguishable from a run that found nothing. The first
  production cohort pass printed `persist=0 markers=0` throughout while
  identifying 33 eligible books and 1,247 chapters. The label now reports
  `eligible` (persisted + would-persist), which advances in both modes.
