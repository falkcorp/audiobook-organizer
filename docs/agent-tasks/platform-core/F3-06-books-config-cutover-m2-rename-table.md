<!-- file: docs/agent-tasks/platform-core/F3-06-books-config-cutover-m2-rename-table.md -->
<!-- version: 1.1.5 -->
<!-- guid: a42617df-0a4d-49ef-a19d-48538878932a -->
<!-- last-edited: 2026-11-05 -->

# F3-06 — Books' config-and-secrets step M2, built dark in `internal/books/cutover`: the generated rename table and its golden, HEAD's frozen `Config`, codec and cipher, M2a and M2b, HEAD's key guard; the M7 and M8 books step definitions

| Field | Value |
|---|---|
| ID | F3-06 |
| Release / flip | R2, config flip F3 (dark PR: built and tested, **unregistered** until F3-07) |
| Tier | **T4** (Opus, xhigh–max): a one-way migration of every setting and secret |
| Wave | W28 |
| Depends on | F3-01, F3-02, F3-03, F3-04, F2-13, F2-15 |
| Owns gate tests | `TestRenameTable_Golden`; `TestConfigCutover_EffectiveConfigUnchanged` (the config oracle); `TestConfigCutover_SecretsReadable`; `TestConfigCutover_KeyGuard`; `TestConfigCutover_QuarantineNotDrop`; `TestConfigCutover_GapFillStripped`; `TestConfigCutover_Idempotent`; `TestConfigCutover_RestoreDrill`; `TestConfigCutover_RowsGone` |
| Gate | Plan, then execute autonomously in your own worktree and PR. |

## ⛔ START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f3-06-config-cutover-m2
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
make web-deps
git rev-parse HEAD       # BASE
```

Confirm F3-01, F3-02, F3-03, F3-04, F2-13 and F2-15 are merged. If not, stop.

## Goal

Create the books package `internal/books/cutover` (04 §2.4: one of 03 §3.5.1's five new paths) holding:

1. the **rename table**, generated from HEAD's code (05-config §1.12.3 steps 1–4), with its golden
   `internal/books/cutover/testdata/rename_table.golden`, and the two outputs other PRs consume: the retired-name list
   for `app.Options.RetiredConfigNames` and the canonical-name list the deploy-file test reads (F3-07);
2. **HEAD's frozen `Config` struct, blob codec and row cipher**, as files of this package (not a sub-package), so later
   changes to ABO's façade cannot change what M2 reads;
3. **M2** as a books `datamigrate.DataMigration` (`Cutover: true`, `Breaking`, `BootBlocking`): M2a (one batch) and
   M2b (the gap-fill strip), with HEAD's key guard;
4. books' **M7** half (the level step raising `books`, with the legacy projection) and books' **M8** definition
   (`books.rewrite-after-<step name>`), on F3-04's machinery;
5. `internal/books/cutover/stepids.go` (path and names coined here), the one place that **reserves the step IDs of
   M2–M6** (and names books' level steps and holds one constant per books cutover level), as exported constants. F4-06 (M3), F5-16 (M4),
   F5-15 (M5), F7-08 (M6) and R3-02's `UpgradeFloor.Requires` take their IDs from it instead of coining their own;
6. the **cutover-list check** (order M2): a function (name coined here, `ValidateCutoverList`) that refuses a
   registered cutover list unless the levels are raised to the build's target before the first step of each flip,
   each owner's versions are contiguous, and exactly one M8 is registered, last.

**The flip mechanism** (stated in F3-04's "Flip mechanism"; follow it exactly): level steps (M7) are named by target
level, `books.level-<n>` and `core.level-<n>`, and M8 by the step it compacts after, `books.rewrite-after-<step name>`.
Every later flip that adds a step (F4-08, F5-18, F7-09) **adds** a new level step for each owner whose level it raises
(books; core when core's level changes), at that owner's next contiguous version, numbered immediately before the
flip's own new migration step, and bumps that owner's declared `Spec.Level` in the same PR; earlier level steps stay
registered unchanged (05-storage §5S.3.5: versions are 1..n, contiguous per owner, recorded by version number). It
**replaces** M8 with a newly named one, so exactly one M8 is registered. So the constants here are per level: this row
defines `books.level-<n>` for F3's level `<n>` and one constant for that level (`BooksCutoverLevel`, name coined here,
that M2's `RaisesLevel` and F3's books level step both read); each later flip adds its own level constant and
level-step ID beside these and replaces the M8 ID, never editing an earlier level's constant or ID.

Nothing is registered: F3-07 creates `internal/books/cutover/steps.go`, adds `module.Manifest.Cutover` and
`module.Importers{Config, Secrets}` (06 §6.4.2, §6.4.10 F3 row) and puts the list in `books.Module()`'s manifest. Do
not create `steps.go` and do not touch `core/module/`: your step constructors take the importers directly.

## Spec

- 05-config §1.12.2 (all: inputs, M2a class table, the same-batch rules, `Residual`, sealed quarantine, M2b, HEAD's
  key, operator values, defaults, rollback, the archive case, oracle and tests), §1.12.3 (grammar, the generator's
  four steps, the section table, the whole rename table and its sub-key rows), §1.12.4 ("Not config at all"),
  §1.6 "Schedule keys" (`Convert` onto `tasks.ScheduleConfig`), §2.2 (secret-bearing fields and their canonical
  names), §2.4 state-file table (`encryption_key`, `gap_fill`), §2.10.
- 03 §3.8 M2, M7, M8 rows and the order M1, M7, M2, M3, M4, M5, M6, M8.
- 04 §2.4 row `internal/books/cutover`; §8.4 (its computed layer).
- 05-storage §5S.3.6 (`DataMigration` fields), §5S.3.7 (`LegacyGuardProjection.Stage`), §5S.3.13 (principles,
  importer rules, quarantine sealing, "The list", the M2 row, Verify, M8), §5S.9.1 rows 7 and 10 (the settings blob
  as a books `ClaimReadWrite` exact key in M2's `Source`; the format stamp rows).
- 09 §9.2.3, §9.2.5, §9.3 (the corpus: stage 1 written by the R0 binary, stage 2 the labelled overlay).

## State at HEAD and at BASE

| Thing | HEAD (`ac4368bdb`) | At BASE |
|---|---|---|
| HEAD's `Config` struct | `internal/config/config.go:1048-1912` (anchor at `748fd787a`; `internal/config` unchanged at HEAD) | same file, unchanged until F3-07 |
| blob codec | `internal/config/persistence.go:359-824` (`migrate*Blob`), `:1088-1129` (one-shots), `:1932-2016` | same |
| row cipher | `internal/database/settings.go:110-140,189-235` | after R1-26: `internal/books/store/settings.go` |
| key guard | `cmd/root.go:760` `guardAgainstKeyRegeneration` | same |
| `invite_config.go`, `bootstrap_key_ttl.go` | `internal/config/` | **moved into core by F4-02** (W27, before this row): read HEAD's copy with `git show 748fd787a:internal/config/<file>` |
| removed keys | `internal/config/removed_keys.go` | same |
| `BindEnv` calls | 132 in `config.go`, 9 in `abs_config.go`, 1 in `invite_config.go` (05-config §1.12.3) | same |
| corpus | — | `internal/books/testdata/r2corpus/` (F2-12 stage 1, F2-13 overlay) |
| cutover-suite harness | — | `internal/books/cutover_suite_test.go`, `restore_drill_test.go` (F2-15) |

## Files

| Action | Path |
|---|---|
| create | `internal/books/cutover/` (package; the generator; the frozen HEAD files; M2; books' M7 and M8 definitions; `stepids.go`; tests; `testdata/rename_table.golden`) |
| modify | `internal/arch/layering_test.go` (add `internal/books/cutover` to `layerOf` at its computed layer) |

## Re-verify at BASE

```bash
ls -d internal/books/cutover 2>/dev/null | wc -l                                   # expect 0
grep -c '^type Config struct' internal/config/config.go                            # expect 1
grep -c 'BindEnv(' internal/config/config.go                                       # expect 132
grep -c 'BindEnv(' internal/config/abs_config.go                                   # expect 9
ls internal/config/invite_config.go internal/config/bootstrap_key_ttl.go 2>/dev/null | wc -l   # expect 0 (F4-02 moved them)
git show 748fd787a:internal/config/invite_config.go | grep -c 'BindEnv('            # expect 1 (read the frozen source here)
grep -rhoE 'func migrate[A-Za-z]+Blob' internal/ | sort -u | wc -l             # HEAD: 11 (persistence.go 9, ai_endpoints.go 1, log_retention.go 1; R1-07 moves log_retention.go). Freeze every transform the decode path calls at BASE (HEAD persistence.go:976-1129)
grep -c 'func guardAgainstKeyRegeneration' cmd/root.go                             # expect 1
grep -rn 'func DecryptValue' internal/books/store/settings.go | wc -l             # expect 1 (after R1-26)
grep -c 'ABK_STATE_DIR\|ABK_ALLOW_ENCRYPTION_KEY_REGEN' internal/config/state_dir.go cmd/root.go   # >= 2 in total
ls internal/books/testdata/r2corpus/manifest.json | wc -l                         # expect 1
grep -rn 'func (s \*Store) Importer' core/config/ core/secrets/ | wc -l            # expect 2
grep -rn 'Sensitive\|Residual' core/storage/datamigrate/*.go | wc -l               # expect >= 2
grep -rn 'Cutover ' core/storage/datamigrate/*.go | wc -l                          # expect >= 1
```

Counts that differ from these (they are the spec's, taken at `748fd787a`) are recorded in the PR and the generator's
checks decide; a zero on an edit target stops the task.

## STOP conditions

1. The generator cannot assign a HEAD field (05-config §1.12.3 step 4 makes this a generator error): stop and report
   the field; never invent a target the spec does not name. A target the spec names first is marked "(coined)" in the
   golden only where the spec's table already says "(coined)".
2. Counts in the golden differ from 05-config's (134 `k`, 11 `s`, 9 `i`, 16 `o`, 3 `x`; 173 top-level rows, two with two
   targets) and the difference is not explained by a HEAD change since `748fd787a`: stop and report.
3. A config oracle cell disagrees with the R0 binary's answer.
4. Any registration of M2, M7 or M8 in `books.Module()`, a `steps.go` file, or an edit under `core/module/`.
5. F3-04's level-step constructor cannot take a target level and a version as parameters, so the flip mechanism
   cannot add a level step by name and version: report it.

## Steps

1. **Freeze HEAD.** Copy into this package, as unexported code: HEAD's `Config` struct and its sub-structs from BASE's
   `internal/config/config.go` (wherever BASE no longer has a HEAD file, F4-02's two moved config files among them,
   read it from the spec's evidence commit with `git show 748fd787a:<path>` so the generator still sees all 142
   `BindEnv` names), the decode of `LoadConfigFromDatabase` with the ten `migrate*Blob` read transforms and
   the three marker-guarded one-shots (`persistence.go:359-824,1088-1129`), the gap-fill read
   (`persistence.go:42-104,150-178`), the row cipher's decrypt half and HEAD's key search order (the state directory,
   then `<root_dir>/.appdata/`; `settings.go:110-140`), and the guard (`cmd/root.go:726-783` at `748fd787a`; line 760 at
   HEAD). Each frozen file's header says it is a frozen copy deleted in R3.
2. **Generator** (a test-driven Go program in this package): source = every field of the frozen `Config` in source
   order, recursively; HEAD name = `json` tag, or the `mapstructure` tag where `json` is `-`; retired names = the HEAD
   name as a top-level file key, its upper-cased form, every `BindEnv` name (read from the frozen copy of HEAD's bind
   list), `ABK_STATE_DIR` → `core.paths.state_dir`, `ABK_ALLOW_ENCRYPTION_KEY_REGEN` → `core.secrets.allow_key_regen`;
   `removed_keys.go` entries as dropped rows with HEAD's message. Explicit rows first (05-config §1.12.3's table and
   sub-key rows, copied exactly), then books rows via the section table (exact name, exact key, longest prefix; two
   matches of one rank fail). Checks of step 4. Also list every `os.Getenv` literal outside `internal/config`
   (§1.12.4 "Not config at all"); an unlisted literal fails `TestRenameTable_Golden`.
3. **Golden**: per row HEAD name, retired names, canonical path, canonical env name (05-config §1.3.6 derivation),
   blob field, class, `Convert`, and the refuse-or-warn verdict of each retired name under §1.3.6.
4. **Outputs** (names coined here; F3-07 consumes them): a function returning `[]config.RetiredName` for
   `app.Options.RetiredConfigNames` (with `Refuse` set for secret and destination passthrough fields, and `Message` from
   `removed_keys.go`), and one returning the canonical env names the deploy files may use.
5. **M2** (`DataMigration`, owner `books`, `Cutover: true`, `Compat: Breaking`, `Mode: BootBlocking`, `RaisesLevel` =
   M7's books level (`BooksCutoverLevel`), `After` books' M7, `Parallelism: tasks.Sequential("tens of settings rows")`, `Source` = the four
   encrypted rows, `Residual` = the settings blob key, `Sensitive` = the blob and the secret rows):
   - **Guard first:** any of the four rows non-empty and no key file found → fail before M2a, nothing written, naming
     the file, the directories searched and `ABO_CORE_SECRETS_ALLOW_KEY_REGEN`.
   - **M2a, one batch:** per rename-table row of HEAD's effective stored value (blob value, gap-filled): `k` → `Convert`
     then `config.Importer.Stage` (a default stages nothing; `core.storage.path` stages nothing); `s` → non-empty value
     through `secrets.Importer` under the canonical name (an empty value leaves it unset); `i` → left in the blob, but its
     secrets (the Deluge passwords, metadata-source credentials, the webhook secret) moved out and sealed under their
     canonical names; `o` → nothing; `x` → nothing, one log line for a non-default value. In the same batch: rewrite the
     blob in place without every consumed field; delete the four rows; quarantine (sealed) an unknown blob field and a
     value `Importer.Stage` refuses; an undecryptable row (override set, wrong or corrupt key) → ciphertext quarantined
     sealed and `ImportUnreadable` under the secret's name; userinfo in a stored URL → URL staged without it, userinfo
     kept as one encrypted record `<key path>.userinfo` with no key and reported; any other `plugins.<id>` settings → one
     encrypted record `modules.books.plugins.<id>.settings`, reported.
   - **M2b:** after M2a committed, idempotently strip the fifteen managed keys from `<db parent>/config.yaml` (read,
     drop, temp file, fsync, rename, mode 0600), keep every other key, delete an empty file; rerun every boot until
     clean; crash-labelled in the boot-writer table.
   - **Verify:** the four rows absent; every moved field absent from the blob; read = imported + quarantined +
     deleted-by-rule; the config oracle on the decoded input (effective config before = after).
   - M2 never touches `.encryption_key`.
6. **Books' M7 half:** a `Breaking` versioned step with no writes of its own raising `books` to `BooksCutoverLevel`
   through F3-04's level step, at books' next contiguous version (books' versioned steps start at `LegacyCap+1`,
   05-storage §5S.3.5), named `books.level-<n>`, staging the
   legacy projection (`.storage-format` = `LegacyMaxFormat + 1` and the marker) through books'
   `LegacyGuardProjection.Stage`. **Books' M8:** the rewrite on F3-04's engine, ID `books.rewrite-after-<M2's step
   name>`, built through the M8 invocation API F3-04 coined (copy its signature from F3-04's PR description).
   **Step IDs** (`<owner>.<name>`, `<name>` matching `[a-z][a-z0-9-]*`): coin them in `stepids.go` for M2, M3, M4, M5,
   M6 (names coined here), with books' M7 half as `books.level-<n>` per the flip mechanism, each a comment citing its
   03 §3.8 row, and use only these constants in this package. A unit test (name coined here:
   `TestStepIDs_UniqueAndWellFormed`) checks uniqueness and the grammar. **List check:** `ValidateCutoverList` (step 6
   of the Goal) with `TestCutoverList_LevelsFirstContiguousOneM8Last` (name coined here): over the list built from
   this row's constructors in 03 §3.8's order (books' level step, a core level step from F3-04's constructor, M2, M8)
   it passes, and so does a two-flip list (a second books level step at the next version, ordered before a second data
   step); a list with no level step for an owner whose level the build raises (no core step), a data step ordered
   before its flip's level step, a gap in an owner's versions, two M8s, or M8 before a data step fails. The step list is registered only in
   F3-07's `steps.go`, so F3-07 adds the cell that runs the check over the registered list (books' manifest list plus
   the kernel's core half).
7. **Layering:** add `internal/books/cutover` to `layerOf` in `internal/arch/layering_test.go` at its computed layer.
8. Run every test against the corpus: stage 1 (R0-written) and the overlay's config cases (a corrupt encrypted row, a
   blob field no row names, a value R0's validation refuses), driving M2 directly through F2-15's harness (not through
   `books.Module()`).
9. Headers, one `changelog.d/` fragment, Acceptance.

## Code contract

```go
// Built by F3-06, registered by F3-07 (names coined here).
func ConfigStep(cfg config.Importer, sec secrets.Importer, opts ConfigStepOptions) datamigrate.DataMigration
func BooksLevelStep(/* F3-04's level-step inputs: target level, version */) storage.VersionedStep   // books.level-<n>; type per F3-04
func ValidateCutoverList(/* the registered cutover list */) error            // levels before each flip's first step, versions contiguous, one M8, last
func RewriteStep(after string) datamigrate.DataMigration                     // books.rewrite-after-<after>
func RetiredConfigNames() []config.RetiredName
func CanonicalEnvNames() []string
```

Adapt the parameter types to what F3-04 and `module.Importers` (06 §6.4.2) actually expose at BASE; record any change
in the PR.

## Tests first (spec names, 05-config §1.12.2–§1.12.3)

`TestRenameTable_Golden`, `TestConfigCutover_EffectiveConfigUnchanged` (for every row, R2's effective value with empty
operator layers equals `Convert` of R0's effective stored value from R0's own `GET /api/v1/config` on the same store;
with canonical operator values set they win, except a `FailClosed` key keeps the stricter value),
`TestConfigCutover_SecretsReadable`, `TestConfigCutover_KeyGuard`, `TestConfigCutover_QuarantineNotDrop`,
`TestConfigCutover_GapFillStripped`, `TestConfigCutover_Idempotent` (with the crash matrix over M2a and M2b),
`TestConfigCutover_RestoreDrill` (run with the latest production release through F2-15's
`scripts/ci/fetch_prefreeze_binary.sh` until F3-08 records the pre-freeze tag in `ci/prefreeze_release.txt`),
`TestConfigCutover_RowsGone` (its "after M6" clause is F7-08's, which extends this test; here: the four rows gone, the blob holds only class-`i` fields with no moved field and no class-`i` secret, every key range
outside the listed keys byte-identical). `TestRenameTable_EveryKeyRegistered` is F3-07's.

## Invariants

- Core holds no ABO key name: the table, the frozen code and every HEAD row format live here.
- One batch per chunk: a crash leaves HEAD's rows or the finished result.
- No plaintext credential in quarantine, under `c/` or in a log.
- Nothing is dropped silently; every value is imported, quarantined, left for M6, or logged as dropped.
- IDs and the `.encryption_key` file are untouched.

## Acceptance

```bash
go vet ./internal/books/cutover/...                                             # exit 0
go test -race -count=1 ./internal/books/cutover/...                             # ok
go test -count=1 -run 'TestRenameTable_Golden|TestConfigCutover_|TestStepIDs_UniqueAndWellFormed|TestCutoverList_LevelsFirstContiguousOneM8Last' ./internal/books/...   # PASS for each
go test -count=1 ./internal/arch/...                                            # ok
grep -rn 'cutover\.' internal/books/module.go | wc -l                           # 0: nothing registered
ls internal/books/cutover/steps.go 2>/dev/null | wc -l                          # 0
make ci                                                                         # exit 0
```

## Out of scope

Registration, `Manifest.Cutover` and `Importers`, the façade, deleting HEAD's loader, the deploy files and `TestRenameTable_EveryKeyRegistered`,
`TestConfigCutover_NoPlaintextSecretOnDisk` (F3-07); M6 and `TestConfigCutover_RowsGone`'s after-M6 clause (F7-08); R3's deletion of the frozen copy.

## Rollback

Revert the PR; nothing is registered.

## Repo rules

Worktree only; file headers on every file, including the frozen copies and the golden's generator (the golden data
file is generated: give it the header the generator emits); fragments headerless; conventional commit ending
`Co-Authored-By: Claude <noreply@anthropic.com>`, no release or flip trailer; `gh pr merge <n> --rebase`, never squash,
never `--admin`. CLAUDE.md concurrency: M2 is `tasks.Sequential` with a reason (tens of rows).

```
feat(books/cutover): generated rename table, frozen HEAD codec and cipher, M2, books' M7 and M8 (unregistered)

Co-Authored-By: Claude <noreply@anthropic.com>
```
