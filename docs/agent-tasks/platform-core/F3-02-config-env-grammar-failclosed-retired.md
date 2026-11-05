<!-- file: docs/agent-tasks/platform-core/F3-02-config-env-grammar-failclosed-retired.md -->
<!-- version: 1.1.0 -->
<!-- guid: 8aaa153a-536e-4d8d-9361-4796ae3f6beb -->
<!-- last-edited: 2026-11-05 -->

# F3-02 — Config env and file grammar, the fail-closed rule, strict decoding, retired-name refusal and `config check`

| Field | Value |
|---|---|
| ID | F3-02 |
| Release / flip | R2, config flip F3 (dark PR) |
| Tier | **T3** (Opus, high): precedence and the fail-closed rule decide security controls |
| Wave | W25 |
| Depends on | F3-01 |
| Owns gate test | `TestFailClosed_MoreRestrictiveWins` |
| Gate | Plan, then execute autonomously in your own worktree and PR. |

## ⛔ START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f3-02-config-grammar
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
make web-deps
git rev-parse HEAD       # BASE
```

Confirm F3-01 is merged. If not, stop.

## Goal

Add the operator layers that F3-01 left as a seam, with the exact rules of 05-config §1.3.5 and §1.3.6: canonical
env names, `<NAME>_FILE`, empty-is-unset, the nested YAML file form, strict decoding for env, file and flags,
ecosystem bindings and their conflict rule, the **one fail-closed exception** (`FailClosed` keys:
`core.auth.mode`, `core.http.basic_gate.enabled`), the **retired-name rule** (refuse boot for a protected or secret
key, warn otherwise), `Store.RetiredSet`, and the offline command `<app> config check`. Add the two `app.Options`
fields that carry the app's lists (`RetiredConfigNames`, `ConfigEnvBindings`). Dark: ABO passes neither field until
F3-07, so nothing changes for ABO.

## Spec

- 05-config §1.3.1 (`EnvBinding`, `FailClosed` option contracts), §1.3.3 (`Options.Environ`, `FilePath`,
  `EnvBindings`, `Retired`; `EnvName`, `RetiredName`, `RetiredUse`, `RetiredNameError`), §1.3.5 (fail-closed keys,
  empty env values, in-layer conflict rule), §1.3.6 (all of it), §1.4 steps 2–3, §1.5 rows on file, env, retired
  names, bindings, strict decoding and `_FILE`, §1.8 (`mediacore_config_retired_names_set`,
  `mediacore_config_fail_closed_overrides`), §1.11 tests, §1.12.4 (the ABO list these mechanisms serve).
- 05-http-api §10 (`core.http.basic_gate.enabled` is `FailClosed`, order `true` then `false`); 05-authn §8
  (`core.auth.mode`, order `"required"` then `"disabled"`).
- 06 §6.4.5 (`Options.RetiredConfigNames`, `Options.ConfigEnvBindings`, the `config check` entry of
  `OfflineCommands`, which opens no store); 06 §6.4.10 F3 row.
- 09 §9.2.5 (policy); 14 K15, E5, E6 (the risks this closes: an ignored renamed DB path opens an empty store, a stale
  operator value weakens a gate, an empty store opens).

## Files

| Action | Path |
|---|---|
| create | `core/config/env.go` (env names, `_FILE`, bindings, strict decode of env and flag values) |
| create | `core/config/precedence.go` (the file layer's merge, the in-layer conflict rule, `FailClosed`) |
| create | `core/config/retired.go` (retired names, `RetiredSet`, `RetiredNameError`) |
| create | `core/app/options_config.go` (`Options.RetiredConfigNames`, `Options.ConfigEnvBindings`, passed to `config.Options`) |
| create | `core/app/offline_config.go` (`config check`, an `app.Command` in `OfflineCommands`) |
| modify | `core/kernel/core_sections.go` (add the `FailClosed` option to the two declarations, nothing else) |
| modify | `core/go.mod`, `core/go.sum` (require `go.yaml.in/yaml/v3` **v3.0.6**, 05-config §1.10.1) |
| modify | `go.sum` (ABO's module sums for the same version; HEAD's `go.mod` carries `go.yaml.in/yaml/v3 v3.0.5 // indirect`) |
| create | tests beside each file (`*_test.go`) |

Put the YAML file parser in `precedence.go` or `env.go`; do not add files outside the list. `core/kernel/core_sections.go`
is also edited by F3-03 (an earlier wave) and F2-04 (a later one): touch only the two `FailClosed` options.

## Re-verify at BASE

```bash
# F3-01's seam and store exist (expect >= 1 each)
grep -rn 'func NewStore' core/config/ | wc -l
grep -rn 'func (s \*Store) RetiredSet' core/config/ | wc -l
# your three file names are free (expect 0 each)
ls core/config/env.go core/config/precedence.go core/config/retired.go 2>/dev/null | wc -l
# the FailClosed option constructor (F2-09) and the two declarations you add (expect 1, then 0, 0)
grep -rn 'func FailClosed(' core/config/ | wc -l
grep -rn 'FailClosed("core.auth.mode"' core/ | wc -l
grep -rn 'FailClosed("core.http.basic_gate.enabled"' core/ | wc -l
# both keys are declared in the kernel's core-section list (expect >= 1 each)
grep -c 'core.auth' core/kernel/core_sections.go
grep -c 'basic_gate' core/kernel/core_sections.go
# the YAML library is not yet in core's module (expect 0; this PR adds it)
grep -c 'go.yaml.in/yaml/v3' core/go.mod
# OfflineCommands exists from R1 (expect >= 1)
grep -rn 'func OfflineCommands' core/app/ | wc -l
# HEAD's hazards these rules close (HEAD f7ac5b11e; informational)
grep -c 'BindEnv(' internal/config/config.go internal/config/abs_config.go internal/config/invite_config.go
```

05-config §1.12.3 counts 142 `BindEnv` calls at `748fd787a` (132 + 9 + 1). Record what you see.

## STOP conditions

1. F3-01's store has no seam for file and env sources and adding one needs a change to F3-01's types beyond this row's
   files: report.
2. `func FailClosed(` is missing (F2-09 did not land it), or either key is declared outside
   `core/kernel/core_sections.go` (the `basic_gate` or `core.auth` greps print 0). Report; do not declare the keys
   elsewhere.
3. A `FailClosed(...)` declaration already exists for either key (the second or third grep prints 1): someone else
   added it; report instead of declaring it twice.
4. A change makes ABO read an env or file name differently before F3-07 (ABO sets neither new `app.Options` field).

## Steps

1. Write the tests below first.
2. **Dependency and declarations.** In `core/`: `go get go.yaml.in/yaml/v3@v3.0.6 && go mod tidy`; then at the root
   `go mod tidy` so `go.sum` carries the same version (05-config §1.10.1). In `core/kernel/core_sections.go` add
   `config.FailClosed("core.auth.mode", json.RawMessage(`"required"`), json.RawMessage(`"disabled"`))` (05-authn §8
   key table) and `config.FailClosed("core.http.basic_gate.enabled", json.RawMessage("true"), json.RawMessage("false"))`
   (05-http-api §10) to the two existing section declarations, most restrictive first. Change nothing else there.
3. **Env names** (§1.3.6): `<EnvPrefix>_` + upper(path) with `.` and `-` → `_`, leading `MODULES_` dropped for
   module paths (`core.log.level` → `ABO_CORE_LOG_LEVEL`; `modules.books.scan.on_startup` →
   `ABO_BOOKS_SCAN_ON_STARTUP`; `integrations.metadata.google-books.api_key` →
   `ABO_INTEGRATIONS_METADATA_GOOGLE_BOOKS_API_KEY`). Two paths with one name → `ErrDuplicate` at `Load`. Exact case
   only. Unknown `<EnvPrefix>_*` → one warning each. A value of `""` is **unset** (no value, no lock). `<NAME>_FILE`
   is read once at `Load`, one trailing newline trimmed; unreadable → boot error. Objects and arrays take JSON.
   Config reads only `Options.Environ`, never `os.Getenv`.
4. **File** (§1.3.6): a YAML mapping nested by path segments; top-level keys `core`, `modules`, `integrations`; an
   unknown top-level key warns; unreadable or unparsable file → boot error; a file that sets
   `core.paths.state_dir` fails boot (§1.7).
5. **Strict decoding** for env, file and flags: JSON for objects and arrays, a Go duration, `true`/`false`, the key's
   own type. No legacy spellings (`yes`, bare nanoseconds, space-separated lists). Failure names the variable or file
   key. Exception: `OTEL_METRIC_EXPORT_INTERVAL`'s binding keeps the OTel bare-integer-milliseconds decoder.
6. **Bindings** (§1.3.1 `EnvBinding`, `Options.EnvBindings`): within the env layer the canonical name beats a binding;
   different values on a `ProtectAdmin`+ key → boot fails naming both; on any other key → canonical wins, warning,
   health `config_binding_conflict`. A binding equal to a canonical name, or bound twice → `ErrDuplicate`.
7. **Fail-closed** (§1.3.5): for a `FailClosed` key the effective value is the more restrictive (by the declared
   order) of the operator value and the stored value; with one set, that one. Stored beats a weaker operator value →
   `AttachReport.FailClosed[key] = <operator source>`, one warning, health `config_fail_closed_override` on
   `core.config`, gauge `mediacore_config_fail_closed_overrides`. `Apply` with an operator value set accepts a value
   at least as restrictive (it becomes effective) and an unset, refuses a weaker one with `*LockedError`.
   `KeyState.FailClosed = true`. `Register` refuses `FailClosed` on a non-bool/enum key, a secret key or an order
   missing a value (`ErrInvalid`).
8. **Retired names** (§1.3.6 table): never read. After `Boot` (core keys) and after `Load` (module and passthrough
   keys), before storage opens: a set retired name whose key is `ProtectAdmin`+ (owner and operator included), a
   `secrets.Ref` key, a passthrough path marked `Refuse`, or a file key whose subtree holds one → `*RetiredNameError`
   listing **every** such use with its canonical env name and file path; otherwise ignored, one warning per boot
   naming the canonical name or the drop message, health `config_retired_name_ignored`, gauge
   `mediacore_config_retired_names_set`. A file key spanning several keys is judged by its most protected key.
   `RetiredSet()` lists every use.
9. **`app.Options`** (`options_config.go`): `RetiredConfigNames []config.RetiredName`, `ConfigEnvBindings
   []config.EnvName`, copied into `config.Options.Retired` and `.EnvBindings`.
10. **`config check`** (`offline_config.go`): an `app.Command` that opens no store, reads only the operator layers
   (file, env, flags) through the same code as `Boot`/`Load`, prints each retired use with its canonical env name and
   file path and whether it would refuse, and exits non-zero when any would refuse. Register it in
   `OfflineCommands`.
11. Headers, one `changelog.d/` fragment, Acceptance.

## Code contract (05-config §1.3.1, §1.3.3; copy exactly)

```go
func EnvBinding(p Path, name string) SectionOption
func FailClosed(p Path, order ...json.RawMessage) SectionOption

type EnvName struct {
	Name string
	Path Path
}
type RetiredName struct {
	Name      string
	File      bool
	Canonical Path
	Refuse    bool
	Message   string
}
type RetiredUse struct {
	Name         string
	File         bool
	Canonical    Path
	CanonicalEnv string
	Refused      bool
}
type RetiredNameError struct{ Uses []RetiredUse }
var ErrRetiredName = errors.New("config: a retired name is set")
```

In `core/app`: `Options.RetiredConfigNames []config.RetiredName` and `Options.ConfigEnvBindings []config.EnvName`
(06 §6.4.5).

## Tests first (spec names, 05-config §1.11)

`TestFailClosed_MoreRestrictiveWins` (gate test: both keys, both directions; `Apply` of an equal-or-stricter value
succeeds and is effective, a weaker one → `*LockedError`; `FailClosed` on a string key or with a missing value fails
`Register`), `TestPrecedence_Matrix`, `TestEnv_EmptyValueIsUnset`, `TestEnv_StrictDecode`,
`TestEnvBinding_EcosystemNames`, `TestRetiredName_ProtectedOrSecretRefusesBoot` (a recording fake storage sees no
open), `TestRetiredName_OtherWarns`, `TestEnvName_Derivation`, `TestEnvName_CollisionRejected`, `TestEnvFileSuffix`,
`TestEnvBindingConflictOnProtectedKeyRefusesBoot`, `FuzzEnvDecode`; the env and file cells of
`TestOverride_IgnoredWhenOperatorLocked`. For `config check`: `TestConfigCheck_ListsRetiredNamesOpensNoStore` (name
coined here): a fixture retired list, two set names (one refusing, one warning), the command prints both with
canonical names, exits non-zero, and a recording storage sees no open.

## Invariants

- The DB layer only ever overrides defaults, except the fail-closed exception.
- A refused boot has opened nothing and written nothing.
- Core holds no app name: the retired list and ABO's bindings arrive only through `app.Options`.

## Acceptance

```bash
(cd core && go vet ./config/... ./app/...)                                              # exit 0
(cd core && go test -race -count=1 ./config/... ./app/...)                              # ok
(cd core && go test -count=1 -run 'TestFailClosed_MoreRestrictiveWins|TestRetiredName_' ./config/...)  # PASS
(cd core && go test -run XXX -fuzz FuzzEnvDecode -fuzztime 30s ./config/)               # no failure
go test -count=1 ./internal/config/...                                                  # ok (ABO unchanged)
make -C core ci                                                                         # exit 0
make ci                                                                                 # exit 0
```

## Out of scope

ABO's rename table and the generated retired list (F3-06); passing them in ABO's `main` (F3-07); the deploy-file test
(F3-07); storage's refusal to **create** a store while a retired database-path name is set (F3-07, the kernel's P2 in
`core/kernel/storage.go`; this PR only makes `RetiredSet()` report the use); the Basic gate (F3-05).

## Rollback

Revert the PR; ABO passes none of the new options before F3-07.

## Repo rules

Worktree only; file headers on every file (fragments headerless); conventional commit ending
`Co-Authored-By: Claude <noreply@anthropic.com>`, no release or flip trailer; `gh pr merge <n> --rebase`, never squash,
never `--admin`.

```
feat(core/config): env and file grammar, fail-closed keys, retired names, config check (dark)

Co-Authored-By: Claude <noreply@anthropic.com>
```
