### Fixed

- Long-running operations no longer get cancelled after five minutes while they
  are working normally. `LoggerFromReporter` — the adapter between an operation's
  work and the operations registry — discarded the progress reporter it was
  given and returned a logger whose progress method did nothing, so every
  progress update from a library scan, import, organize, iTunes sync, reconcile
  or folder autoscan was thrown away. The registry's stall detector, seeing an
  operation that had never once reported progress, cancelled it at the
  five-minute mark no matter how healthy it was; the operation's real time limit
  (four hours for a library scan) never applied. Progress now reaches the
  registry, so these operations run to completion and report live progress in the
  UI instead of stopping partway with a "stuck" error.
