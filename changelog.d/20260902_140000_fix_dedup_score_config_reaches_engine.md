### Fixed

#### Dedup scoring now uses the configured band thresholds instead of always the compiled-in 97/90/75/60

The unified dedup scorer's `dedup.signals.*` settings — `band_certain_min` and
friends from `config.yaml`, the DB-persisted copy the settings UI writes, and the
thresholds `dedup.calibrate-composite apply=true` reported it had applied — were
all inert. The engine's `getScoreConfig()` returned `unified.DefaultScoreConfig()`
unless `SetScoreConfig` had been called, and nothing in production called it;
`registry_wire.go` pushed the configured values into `unified` package globals whose
only reader was `unified.LoadScoreConfig`, whose only caller was the calibrate op's
own sweep. Every real scoring path (`CheckBook`, rescore, `ScorePairsForBook`) banded
on the hard-coded ladder, so an operator raising `band_certain_min` to keep
auto-resolve from merging anything short of near-certainty changed nothing.

There is now one channel: `config.DedupSignalConfig.ScoreConfig()` builds the
effective `unified.ScoreConfig` (defaults → Viper → persisted settings, then
`Validate`) and `dedup.NewEngine` takes it as a constructor argument. An invalid
ladder fails server startup with an error that names the field AND where the
effective value lives (config.yaml overlaid by the settings blob written through
`PUT /api/v1/config`), rather than silently coming up on defaults. The effective
ladder is logged once at startup. The `unified.SetBandThresholds` /
`SetKindConfidenceOverrides` package-global override channel is deleted.

Runtime changes reach the engine too, and re-band what is already stored:

- `PUT /api/v1/config` (and the Settings → Dedup page) validates the ladder
  **before** persisting — an unordered ladder, a `band_certain_min` above the
  100-point score cap, or a per-kind confidence override naming an unknown
  signal kind is rejected with 400 and nothing is written (previously unknown
  kinds were silently ignored and a bad ladder was saved, then refused at the
  next restart). A valid change is pushed into the live engine through
  `Engine.ReloadScoreConfig`, which swaps the ladder and re-bands every stored
  pending candidate under it; if that fails the blob is rolled back.
- `dedup.calibrate-composite apply=true` validates its recommendation before
  persisting, then uses the same `ReloadScoreConfig`, and reports how many
  stored candidates changed band. Rows already in the store no longer keep the
  previous ladder's band — which matters because `AutoResolveCertain` decides
  on the **stored** band, not a recomputed one.
- `Engine.FullScan` snapshots the score config once per scan instead of
  re-reading it per book across the worker pool, so a ladder change mid-scan
  cannot band half a scan's pairs on one ladder and half on another.
- `RescoreResult` gains `write_errors`; a rescore that could not write some
  rows back is reported as a failure rather than a green "re-banded".
- The Settings UI's band inputs were bounded 0–1 step 0.01 for a 0–100 scale;
  they are now 0–100 step 0.5 with helper text naming the ordering rule, and
  the pre-load placeholders are the real 97/90/75/60 defaults.
