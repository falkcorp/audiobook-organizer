### Fixed

#### `ACTIVITY_BACKEND` env var now actually selects the activity-log store backend

The `Config.ActivityBackend` field (and `ActivityDBPath`) carried a
`mapstructure` tag but was never read from viper — this config is assembled by
explicit `viper.GetString` calls, not `viper.Unmarshal` — so nothing ever
populated it. The field was dead: neither `config.yaml`'s `activity_backend`
key nor any environment variable reached it, and it always evaluated empty,
which silently forces the SQLite activity backend on.

That made the documented Pebble rollback lever inoperative. When the
Pebble→SQLite activity backfill OOM-looped production on 2026-09-07, there was
no way to force `activity_backend=pebble` without a code change.

Fixed by reading `activity_backend`/`activity_db_path` from viper in
`InitConfig` (so both `config.yaml` and the env reach the field), binding
`ACTIVITY_BACKEND`/`ACTIVITY_DB_PATH` via `viper.BindEnv` (this codebase does
not run `AutomaticEnv()`), and re-applying both in `applyEnvAuthoritativeConfig`
so the env lever survives the `LoadConfigFromDatabase` blob overlay — the same
protection the `ABS_*` and OAuth keys already have. Regression test covers the
env→field path and the post-blob re-apply.
