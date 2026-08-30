### Fixed

#### The config default-inheritance audit no longer hides the key it exists to report

`logDefaultsPreservedOverBlob` enumerates the config keys a stored `config_blob`
did not contain, which therefore kept their shipped default. It exists because
that inheritance used to be invisible: on production, `scheduled.library_scan`
came back `{enabled:false, interval:0}` from a blob that never mentioned it, and
nothing had been scanning for new books.

The line caps its enumeration at 40 keys. A real install inherits ~122, and
`scheduled.library_scan.interval` sorts past the cut — so the audit written to
make that key visible was dropping that exact key, while saying only
"(list truncated)" with no indication of how much was missing.

Two changes:

- The audit's key set is now computed by `defaultsPreservedOverBlob`, which is
  complete and never truncates. Truncation belongs to the log renderer alone, so
  callers and tests can ask what actually inherited rather than what fit on a
  line.
- A truncated line now reports `omitted=<N>`, so `count` reconciles against the
  names printed instead of leaving the operator to measure the gap by hand.

The enumeration is still bounded, because it is data-scaled rather than
source-bounded: `Config` carries operator-controlled maps (plugins, per-source
credentials, per-kind dedup confidence, per-model embedding thresholds), so an
unbounded line would grow with the install.

#### `internal/config` no longer depends on test execution order

`go test ./internal/config -shuffle=on` failed on 20 of 24 seeds on unmodified
main. CI does not shuffle, so every green run in the package was weaker evidence
than it appeared.

`TestDefaultInheritanceIsLogged` asserted that the *rendered* log line contained
`scheduled.library_scan.interval` — an assertion on presentation, not behaviour.
Whether it held depended on how many keys were non-zero in the process-wide
`AppConfig` when it ran, which depended on which test ran before it: after a test
that zeroed the struct ~2 keys inherited and the assertion passed; after one that
called `ResetToDefaults()` 122 inherited, the list truncated, and it failed.

The test now seeds `ResetToDefaults()` deliberately — the production shape, which
exercises the truncation boundary rather than dodging it — and asserts the key
against the untruncated set. The log assertions remain, so deleting the
`slog.Info` call still reddens it, and the line is additionally checked to
reconcile against itself (`count == printed + omitted`).

The tests that mutate the process-wide config without restoring it were the
underlying order dependence and now restore it via `t.Cleanup`:
`TestResetToDefaults` (restored 3 of ~122 fields), `TestResetToDefaultsPreservesPaths`,
and all five tests in `blob_preserves_defaults_test.go` — including
`TestNoAuditLineWhenBlobIsComplete`, which zeroed `AppConfig` wholesale and was
the polluter that made the default ordering pass by accident.

#### `appdirs.Current()` is now tested for what it returns, not just for repeating itself

`TestCurrent_IsDeterministic` asserted `Current() == Current()`. Nothing in that
test binary calls `InitConfig`, so `config.AppConfig` was the zero value and both
sides were the empty `AppDirs{}` — the assertion held for precisely the failure it
was meant to catch. Stubbing `Current()` to `return pathutil.AppDirs{}` left it
green.

Replaced with tests that seed a production-shaped config and assert the resolved
directories, that a relative `backup_dir` anchors to the database directory, that
the result excludes the dump subtree and not ordinary content, and that `Current()`
agrees with `FromConfig` on the same config. Determinism is still asserted, on top
of a value proven non-empty.
