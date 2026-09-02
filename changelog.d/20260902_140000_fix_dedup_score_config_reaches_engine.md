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

There is now one channel: `internal/dedup.LoadScoreConfig(cfg.Dedup.Signals)` builds
the effective `unified.ScoreConfig` (defaults → Viper → persisted settings, then
`Validate`) and `dedup.NewEngine` takes it as a constructor argument. An invalid
ladder fails server startup with an error naming `dedup.signals` rather than
silently coming up on defaults. The calibrate op sweeps around the live engine's
config and, after persisting new bands, reloads them into the running engine via
`Engine.SetScoreConfig` — no restart needed. The `unified.SetBandThresholds` /
`SetKindConfidenceOverrides` package-global override channel is deleted.
