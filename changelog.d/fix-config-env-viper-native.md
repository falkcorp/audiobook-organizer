<!-- file: changelog.d/fix-config-env-viper-native.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3f9a2c71-8d4e-4b06-9a15-6c0e7b23d4a8 -->
<!-- last-edited: 2026-07-27 -->

### Fixed

#### Environment-driven config is viper-native again (removed the os.Getenv workaround)

The OAuth / Cloudflare-Access config was being read via `os.Getenv` scattered at call
sites as a workaround for a misdiagnosed "viper doesn't pick up env" problem. viper was
never broken — every key has an explicit `viper.BindEnv`, so `viper.GetString` honors the
environment. The real bug was load-order: `LoadConfigFromDatabase` restores the whole
`Config` from the DB `config_blob` (`*c = loaded`) *after* `InitConfig` populated it from
viper, zeroing every env-derived field except `DatabaseType`.

The fix re-establishes the standard precedence **env > blob > file > default** natively:
a single `applyEnvAuthoritativeConfig` re-applies only the environment-authoritative keys
(OAuth, Cloudflare Access, Whisper) as the last step of the DB-load path, using
`viper.IsSet` + `viper.GetX`. `IsSet` is false for a key that only has a default, so a
UI-managed value living in the blob (`itunes.*`, `scheduled.*`) survives untouched when no
env override is present. `WHISPER_REMOTE_URL` now flows through a real `Config` field
instead of `os.Getenv`, and all `os.Getenv` call-site reads were removed. Regression test
`TestLoadConfigFromDatabaseEnvAuthoritative` locks both directions (env wins over blob;
blob survives when env unset).
