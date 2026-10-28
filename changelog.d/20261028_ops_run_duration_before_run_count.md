### Fixed

- `opsmetrics.Recorder.Finished` now records `ops.run.duration` before it
  increments `ops.runs{outcome}`. The two instruments were written count-first,
  so a reader that saw a run-end in `ops.runs` could still read zero
  observations in `ops.run.duration` for that run; this made
  `TestTimeoutCountsAsFailedLegacyAndTimedOutOtel` flake on loaded `-race`
  runners. Any collection that sees a run end counted now also sees its duration.
