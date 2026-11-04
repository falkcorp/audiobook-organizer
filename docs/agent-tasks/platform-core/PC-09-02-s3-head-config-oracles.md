<!-- file: docs/agent-tasks/platform-core/PC-09-02-s3-head-config-oracles.md -->
<!-- version: 1.0.0 -->
<!-- guid: b72d8b2e-2e22-43c1-ba42-03a48d3c1dd2 -->
<!-- last-edited: 2026-11-04 -->

# PC-09-02 — S3 HEAD oracles, test first: the precedence matrix, the compose effective-config golden, and TestLegacyEnvDecodeMatchesHEAD's generated corpus

**ID:** PC-09-02 · **Wave:** W09 (S3, release `S3`) · **Priority:** P1 · **Effort:** L
**Tier:** T3 Opus/high — these differential tests are the only guard of viper's removal; a corpus that misses a
name or a spelling lets the flip change an operator's effective config silently.
**Depends on:** OWNER-08 (the `S2br2` soak row and P4, which lists every env name production sets). Merges only
after OWNER-08's row is in `ci/soak.txt`.
**Stage-Release trailer:** `Stage-Release: S3 1` on every commit.
**Normative digest:** `docs/plans/platform-core/digests/S3.md` (Behaviour rules 1-8; Done-when).
**Owning spec:** 09 S3 "Done when"; 09 §9.2.5; 05-config §1.3.5, §1.3.6 ("Legacy decoding"), §1.11
(`TestLegacyEnvDecodeMatchesHEAD`), §1.12.4.

## ⛔ START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-09-02" -b agent/pc-09-02-s3-oracles origin/main
cd "$REPO/../audiobook-organizer-pc-09-02"
git rebase origin/main
git worktree list
npm ci --prefix web
```

No `go work init` (CLAUDE.md).

## Goal

Record HEAD's config behaviour **before** the code that replaces it exists (advisor G1 risk 6), so the S3 flip
(PC-09-06) is checked against HEAD and not against itself:

1. `TestLegacyEnvDecodeMatchesHEAD` — the generated corpus of every legacy env name and legacy file key × the
   representative spellings of its HEAD decode type, decoded by HEAD's `InitConfig` built from `PREV_REF`; this PR
   commits HEAD's output as `internal/config/testdata/legacy_decode.golden` and, before the flip, also decodes the same
   corpus with the current tree's loader (still viper) and diffs it, so the test is green before the flip.
2. The compose effective-config golden (`internal/config/testdata/effective/compose.golden`), HEAD's effective config
   for the compose fixture of 09 S3 Done when.
3. The precedence matrix: HEAD's effective value and source for each legacy-name class over
   (default, file, env, flag, stored blob) cells, recorded as goldens next to `compose.golden`.
4. `scripts/staging/effective_config_diff.py`, the owner's tool for the production-env golden on the staging copy
   (OWNER-10 runs it; no agent runs it against production data).
5. Wire `TestLegacyEnvDecodeMatchesHEAD` into the `extraction-gates` workflow.

## Exact files

Create: `internal/config/legacy_env_decode_test.go`, `internal/config/effective_config_golden_test.go`,
`internal/config/testdata/effective/compose.golden`, `scripts/staging/effective_config_diff.py`.
Also created (see Planner notes): `internal/config/testdata/legacy_decode.golden`,
`internal/config/testdata/effective/precedence.golden` (name coined here), and the test for the Python script under
`scripts/tests/` if the repo keeps one per script (check `ls scripts/tests/`).
Modify: `.github/workflows/extraction-gates.yml` (created by PC-01-04).

## Re-verify before editing (expected counts at the plan checkout `f07f516be`; rerun at your base)

```bash
grep -c 'BindEnv(' internal/config/config.go          # 132
grep -c 'BindEnv(' internal/config/abs_config.go      # 9
grep -c 'BindEnv(' internal/config/invite_config.go   # 1   (05-config §1.12.4: 142 non-test BindEnv names)
grep -n 'viper.AutomaticEnv()' cmd/root.go            # 1 hit (line 625 at the plan checkout): prefix-less AutomaticEnv
grep -n '^func InitConfig()' internal/config/config.go                # 1 hit (HEAD's loader)
grep -n '^var envLockedSettings\|^var absEnvOnlySettings\|^var configFileLockedSettings\|^func applyEnvAuthoritativeConfig\|^func SyncConfigFromEnv' internal/config/config.go internal/config/persistence.go   # 5 hits
grep -n 'GetBool("enable_auth")\|GetBool("enable_rate_limit")\|GetBool("basic_auth_enabled")' internal/config/config.go   # 3 hits (05-config §1.3.6 anchors :3346,3355,3358)
grep -n 'spf13/cast' go.mod                           # 1 hit, v1.10.0 (the decode rules of §1.3.6 cite it)
test -f ci/prev_ref.txt && cat ci/prev_ref.txt        # OWNER-02 creates it; must name a commit before S3
test -f .github/workflows/extraction-gates.yml && echo ok
grep -n 'BASIC_AUTH_ENABLED\|BASIC_AUTH_USERNAME\|BASIC_AUTH_PASSWORD' docker-compose.yml   # 3 hits (the compose fixture)
```

A moved count is not patched around: record both values in the stage ledger
(`docs/plans/platform-core/stage-ledger/S3.md`, the coordinator's file) and continue only if the rule still applies.
A missing `ci/prev_ref.txt` = STOP (OWNER-02 creates it; agents never edit it).

## Steps

1. **Corpus generator (in `legacy_env_decode_test.go`).** Build the name set from HEAD's own lists, never by hand:
   every `BindEnv` name (parse the Go source of the three files with `go/parser`, collect the string arguments), the
   upper-cased form of every flat key viper knows (prefix-less `AutomaticEnv`), and every legacy file key (the YAML
   keys of `Config`'s `mapstructure`/`yaml` tags). For each name take HEAD's getter for the key (`GetBool`,
   `GetInt`, `GetFloat64`, `GetDuration`, `GetStringSlice`, `GetString`; parse the call sites) and map it to the
   decode type of 05-config §1.3.6. Spellings per type (05-config §1.11): bools — every `strconv.ParseBool` spelling
   plus `yes`, `on`, ` true`, garbage; slices — one, two and comma-separated words; ints — `08`, `0x10`, `1.0`,
   `abc`; durations — `90`, `90s`, `1h`, `abc`; strings — one plain value. Each cell runs twice: through env and
   through the legacy file key. Env names are case-sensitive (also emit a lower-case env cell that must be ignored).
   An empty env value is unset (one cell).
2. **HEAD side.** A driver that sets the cell's environment or writes the cell's YAML file, calls HEAD's
   `config.InitConfig()` (and nothing that touches a store) and prints `config.AppConfig` as canonical JSON. It must use
   only `internal/config` symbols present at the evidence commit `748fd787a`, so the same source compiles against
   `PREV_REF` and against the current tree. The test checks out `PREV_REF` (from `ci/prev_ref.txt`, or the `PREV_REF`
   env var CI sets) into a temporary worktree, writes the driver there, builds it once, runs every cell (bounded worker
   pool of `runtime.NumCPU()` processes; CLAUDE.md concurrency rule: the corpus is hundreds of cells), and caches the
   build by commit. Regenerating `testdata/legacy_decode.golden` is `go test ./internal/config -run
   TestLegacyEnvDecodeMatchesHEAD -update`; without `-update` the test compares.
3. **Current side (before the flip).** The same driver compiled against the current tree; diff every effective value
   cell by cell against the golden. Mark the cells the fail-closed rule will refuse after the flip (05-config §1.3.6:
   a bool spelling `ParseBool` rejects on a key under `core.auth.` or `ProtectAdmin` or higher; until PC-09-03 lands
   the alias table, derive that set from HEAD's names `ENABLE_AUTH`, `enable_auth`, `BASIC_AUTH_ENABLED`,
   `basic_auth_enabled` and the other admin-protected bools listed in `internal/config/protected_fields.go`) as
   `expect: boot_error` in the golden; before the flip those cells are skipped with a log line, after the flip
   PC-09-06 asserts the boot error.
4. **Compose golden (`effective_config_golden_test.go`).** Fixture: a temp Pebble store opened through the residue
   (`internal/books/store` at your base; the same path the existing config tests use — find one with
   `grep -rln 'LoadConfigFromDatabase' internal/config/*_test.go`), holding the settings blob with
   `basic_auth_enabled: true`, a username and an encrypted `basic_auth_password` row written by HEAD's own save path;
   environment exactly as `docker-compose.yml` sets it (`BASIC_AUTH_ENABLED=false`, `BASIC_AUTH_USERNAME=admin`,
   `BASIC_AUTH_PASSWORD=`). Run HEAD's full load order (`InitConfig`, encryption init, `LoadConfigFromDatabase`,
   `SyncConfigFromEnv`, `Validate`) and write the effective config as sorted `key=value` lines; secret-class fields
   are written as `key=sha256:<hex>` (never the value). Expected at HEAD: `basic_auth_enabled=true` (the stored blob
   wins over compose's env default, 05-config §1.12.4 "Precedence").
5. **Precedence matrix.** For one representative key of each class — a plain blob key with a `BelowStored` legacy
   env name, an `envLockedSettings` key, an `absEnvOnlySettings` key, a `configFileLockedSettings` key, a
   `SyncConfigFromEnv` key, `enable_auth`, a `json:"-"` file-only key, `--log-level` and `--playlists` — record
   HEAD's effective value and which input won for every combination of (default, file, env, flag, stored blob)
   present or absent. Golden `testdata/effective/precedence.golden`. This is HEAD's half of 05-config's
   `TestPrecedence_Matrix`; the S3 behaviour change for `--log-level` and `--playlists` (09 §9.2.6) is recorded in
   the golden as HEAD's value and asserted as changed by PC-09-06.
6. **`scripts/staging/effective_config_diff.py`.** Python 3, stdlib only, `-I` safe. Inputs: `--prev-ref <sha>`,
   `--cur <worktree>`, `--env-names <file>` (one name per line, from P4), `--values <file>` (name=value with
   secret-class values already replaced by placeholders, read from `scripts/staging/secret_fields.txt`), `--db-copy
   <dir>` (the staging copy; refuses a store without 09 §9.3's staging marker, same check as `staging-scrub`).
   It builds the step-2 driver at both refs, runs each against the copy, and prints only key names and
   `sha256` value hashes that differ; exit 0 when equal, 1 when not. It never writes into the copy and never prints a
   value.
7. **Workflow.** Add a job (or a step of the existing gate job) to `extraction-gates.yml` running
   `go test ./internal/config -run 'TestLegacyEnvDecodeMatchesHEAD|TestEffectiveConfig' -count=1` with `PREV_REF`
   exported from `ci/prev_ref.txt`, `timeout-minutes` ≤ 30, and the `PREV_REF` build cached by commit (09 §9.2.10
   CI budget). Register nothing in `ci/gates.txt` unless the job is new (then add it there as the S0 rule requires).
8. Headers, `changelog.d/` fragment, acceptance.

## Code contract

No production code changes. Test-only helpers live in `_test.go` files of package `config` (or `config_test` if the
existing tests use the external package; follow the directory's convention). Golden formats are line-oriented and
sorted so a diff is reviewable.

## TDD

The tests are the deliverable: `TestLegacyEnvDecodeMatchesHEAD` (name fixed by 05-config §1.11 and 09),
`TestEffectiveConfigComposeGolden` (name coined here) and `TestEffectiveConfigPrecedenceGolden` (name coined here).
Write a failing self-check first: a deliberately wrong golden line must make each test fail with a message naming
the cell.

## Invariants (STOP conditions)

- No cell is skipped silently: every name of the three source lists appears in the golden at least once per
  surface (env, file). A name the generator cannot classify = STOP and report it.
- No secret value is written to any golden, log or script output.
- The HEAD side is built from `PREV_REF`, never from the current tree (09 §9.2.1); if `PREV_REF` already contains
  S3 (no viper loader), STOP: later stages compare against the committed golden only (05-config §1.11).

## Acceptance

```bash
go test ./internal/config -run 'TestLegacyEnvDecodeMatchesHEAD|TestEffectiveConfigComposeGolden|TestEffectiveConfigPrecedenceGolden' -count=1   # ok
grep -c '' internal/config/testdata/legacy_decode.golden     # > 1000 lines (142 BindEnv names + every AutomaticEnv key + file keys, × spellings × 2 surfaces)
grep -n '^basic_auth_enabled=true$' internal/config/testdata/effective/compose.golden   # 1 hit
grep -c 'sha256:' internal/config/testdata/effective/compose.golden                    # >= 1 (the stored Basic password, hashed)
python3 -I scripts/staging/effective_config_diff.py --help                             # usage text, exit 0
python3 -I -m py_compile scripts/staging/effective_config_diff.py                      # exit 0
grep -n 'TestLegacyEnvDecodeMatchesHEAD' .github/workflows/extraction-gates.yml        # >= 1 hit
make ci                                                                                # exit 0
```

## Out of scope

The alias table (PC-09-03), the core loader and façade (PC-09-06), the production-env run on the staging copy
(OWNER-10), any change to HEAD's precedence.

## Rollback

Revert the PR: tests and goldens only, no runtime effect.

## Repo rules

Headers on every new or changed file; `changelog.d/` fragment, no header. Conventional commits with
`Stage-Release: S3 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`. `gh pr merge <n> --rebase`, never squash.
CLAUDE.md concurrency: the cell runner is a bounded pool (`errgroup.Group` with `SetLimit(runtime.NumCPU())`).
Python per CLAUDE.md (non-trivial scripts in Python).

## Planner notes

- The row lists one golden (`compose.golden`) but 05-config §1.11 requires this PR to commit
  `testdata/legacy_decode.golden`, and the precedence matrix needs its own golden; both are added under
  `internal/config/testdata/`. No other row touches these paths.
- The corpus is generated from HEAD's name lists because the alias table it will later be checked against is built by
  PC-09-03, which depends on this row; PC-09-06 switches the current side to core `Load` + aliases.
