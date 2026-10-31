<!-- file: docs/decisions/2026-10-30-reset-to-defaults-from-viper.md -->
<!-- version: 1.0.0 -->
<!-- guid: 032dd959-8c25-49f8-aa79-0314e8bf459e -->
<!-- last-edited: 2026-10-30 -->

# ResetToDefaults is derived from the viper defaults

## Decision log

Chosen: `InitConfig` is split into `registerConfigDefaults(r defaultRegistrar)` (every
`SetDefault` and `BindEnv`) and `configFromViper(v *viper.Viper) Config` (every read).
`InitConfig` runs both on the process-wide viper. `ResetToDefaults` runs them via
`factoryDefaultConfig()` on a private `viper.New()` and deletes its ~300-line `Config{...}`
literal. The helpers `viperDecodeJSON`, `viperPathList`, `configFileValue`, `getFloat64Slice`,
`applyInviteEnv`, `registerABSDefaults` and `registerInviteDefaults` now take the viper or
registrar as a parameter.

Environment variables: NOT applied by the reset (pure defaults, no env, no file). The private
viper registers through `noEnvRegistrar`, whose `BindEnv` is a no-op. A reset means "back to
the shipped defaults"; baking a host override (for example `FP_PARALLEL_WORKERS=77`) into the
stored blob would present it as a default. Settings the host owns through the environment
(owner email, CF Access, ABS, invitation keys) are still re-applied afterwards by
`applyEnvAuthoritativeConfig`, as before. Library paths (`root_dir`, `database_path`,
`playlist_dir`) and the boot-only `enabled_sort_indexes` are still carried over from the running
config; FactoryReset then clears `RootDir` and `SetupComplete` itself.

Callers: `system.Handler.ResetSystem` and `system.Handler.FactoryReset` (the latter persists
the result with `SaveConfigToDatabase`); the rest are tests. Both expect a full Config, which
this gives them.

Rejected:
- Fill in the 131 missing fields in the literal: keeps two sources of truth, the exact cause of
  the bug (it had already been patched piecemeal for `scheduled.*`, vector backend, TTLs).
- Walk `viper.AllKeys()` of a private viper and decode with `Unmarshal`: a different decode path
  from `InitConfig` (the comments there record why it reads key by key), so the two could
  disagree.
- Reset the process-wide viper: would drop the running server's config-file and env state.
- Apply env overrides in the reset: see above.

Guards: `TestResetToDefaultsRestoresEveryDefaultedKey` enumerates every viper key with a
non-zero default and requires its Config leaf to be non-zero after a reset;
`TestResetToDefaultsIgnoresEnvironment` pins the no-env choice;
`TestFactoryResetPersistsNonZeroForPreviouslyMissingLeaves` reads the stored blob back.

Found, not changed: the defaults for `metadata_scoring.duration_tier_*` were read through a
helper that used the global viper; it is parameterized as part of this change because the reset
would otherwise have read the live viper (config file and env).
