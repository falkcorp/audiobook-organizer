<!-- file: docs/agent-tasks/todo-completion/config/TASK-015-rename-write-back-metadata-config-key-to-auto-wr.md -->
<!-- version: 1.0.0 -->
<!-- guid: f83a1f31-a3b3-4a30-ad74-5252197e8679 -->
<!-- last-edited: 2026-08-21 -->

# TASK-015 — Rename write_back_metadata config key to auto_write_tags_on_fetch with deprecated-alias migration (TODO.md L1247)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · config subagent · **Why:** Mechanical rename but with a correctness-critical backward-compat alias (a bug here silently reverts a file-mutation behavior in prod); needs care, not novel design. · **Depends on:** none · **Wave:** 6 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 1247 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Rename `write_back_metadata` → `auto_write_tags_" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-03.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/config-015-rename-write-back-metadata-config-key-to-auto-wr" -b agent/config-015-rename-write-back-metadata-config-key-to-auto-wr origin/main
cd "$REPO/.worktrees/config-015-rename-write-back-metadata-config-key-to-auto-wr"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Rename the config key/field from write_back_metadata/WriteBackMetadata to auto_write_tags_on_fetch/AutoWriteTagsOnFetch everywhere, while making the OLD persisted key still work: on load, if the new key is absent from viper/db config but the old key is present, use the old key's value and log once at WARN that the alias was used. Prod's persisted config.yaml/db snapshot has the OLD key today, so a bare rename would silently revert AutoWriteTagsOnFetch to its (currently `false`) default on next load and change which fetches write tags without anyone asking for that.

## Background (verify before editing)

- internal/config/config.go:649 — `WriteBackMetadata bool `json:"write_back_metadata"``. Rename to `AutoWriteTagsOnFetch bool `json:"auto_write_tags_on_fetch"``.
- internal/config/config.go:1213 — `viper.SetDefault("write_back_metadata", false)`. Change key to `auto_write_tags_on_fetch`, keep default `false`.
- internal/config/config.go:1660 — `WriteBackMetadata: viper.GetBool("write_back_metadata")` inside the struct-literal load. Must become the alias-aware read described in steps below, not a bare `viper.GetBool` rename.
- internal/config/persistence.go:1084-1086 — the legacy flat-key snapshot loader's switch case `case "write_back_metadata": c.WriteBackMetadata = b`. This is the exact mechanism that will silently drop prod's stored value if only the struct field is renamed and this case is deleted rather than kept as an alias.
- internal/metafetch/service_fetch.go:309 — `if config.AppConfig.WriteBackMetadata { mfs.writeBackMetadata(updatedBook, meta) }`. Update to the renamed field.
- internal/config/config_unit_test.go:654 — test row asserting the json-tag round-trip: `{"write_back_metadata", func() bool { return AppConfig.WriteBackMetadata }}`.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'WriteBackMetadata ' internal/config/config.go   # 1 hit at L649 — struct field WriteBackMetadata with json tag write_back_metadata
  grep -n 'viper.SetDefault("write_back_metadata"' internal/config/config.go   # 1 hit at L1213 — viper default for write_back_metadata
  grep -n 'WriteBackMetadata: viper.GetBool' internal/config/config.go   # 1 hit at L1660 — viper load into struct
  grep -n 'case "write_back_metadata"' internal/config/persistence.go   # 1 hit at L1084 — persistence snapshot-load switch case
  grep -n 'config.AppConfig.WriteBackMetadata' internal/metafetch/service_fetch.go   # 1 hit at L309 — the only fetch-path read site
  grep -n 'write_back_metadata' internal/config/config_unit_test.go   # 1 hit at L654 — existing round-trip test row
  grep -n 'AutoWriteTagsOnApply' internal/config/config.go internal/metafetch/service_writeback.go   # hits at config.go:961,1847,2527 and service_writeback.go:604 — sibling flag already named for its call site (apply path), the model for the rename
  ```

### Reuse — don't invent

- Use `AutoWriteTagsOnApply field/json-tag pattern (exact naming symmetry target)` in `internal/config/config.go` (verify: `grep -n 'AutoWriteTagsOnApply bool' internal/config/config.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/config/config.go:649, rename the field to `AutoWriteTagsOnFetch bool` and its json tag to `json:"auto_write_tags_on_fetch"`.
2. In internal/config/config.go:1213, change `viper.SetDefault("write_back_metadata", false)` to `viper.SetDefault("auto_write_tags_on_fetch", false)`.
3. In internal/config/config.go around L1660 (the struct-literal load inside InitConfig/LoadConfig), replace the bare `WriteBackMetadata: viper.GetBool("write_back_metadata")` with alias-aware logic: read `auto_write_tags_on_fetch` if viper has it explicitly set (`viper.IsSet("auto_write_tags_on_fetch")`); otherwise fall back to `viper.GetBool("write_back_metadata")` and `slog.Warn("config: using deprecated key write_back_metadata; rename to auto_write_tags_on_fetch")` once. Put this in a small local helper near the load site rather than inline if the surrounding code already uses helpers for other renamed keys (grep for an existing pattern first: `grep -n 'deprecated' internal/config/config.go`).
4. In internal/config/persistence.go:1084-1086 (the legacy flat-key DB-row loader), KEEP the `case "write_back_metadata": c.WriteBackMetadata = b` case (renamed field) as-is — this is exactly the deprecated-alias path for old persisted rows, do not delete it — and ADD a new `case "auto_write_tags_on_fetch": c.AutoWriteTagsOnFetch = b` case for newly-saved config.
5. In internal/metafetch/service_fetch.go:309, change `config.AppConfig.WriteBackMetadata` to `config.AppConfig.AutoWriteTagsOnFetch`.
6. In internal/config/config_unit_test.go:654, rename the test row's key string to `auto_write_tags_on_fetch` and its accessor to `AppConfig.AutoWriteTagsOnFetch`.
7. Add a new unit test (see tests below) proving the alias: set only `write_back_metadata=true` via viper (simulating prod's persisted snapshot), load config, assert `AppConfig.AutoWriteTagsOnFetch == true` and that the WARN log fired once.
8. Grep once more for stragglers before finishing: `grep -rn 'WriteBackMetadata\|write_back_metadata' --include='*.go' .` must show zero non-comment hits outside the alias-handling code path.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_config_015.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Both keys present with different values: new key must win (it's what a user just changed).
- Neither key present: default (false) applies, no WARN.
- Old key present but false: must still be honored as false via the alias, not silently ignored because 'false' looks like 'absent'.

## Tests

- internal/config/config_unit_test.go — extend/add TestConfigDeprecatedAliases (or similar existing table) with a case for auto_write_tags_on_fetch, asserting the renamed field round-trips through the new key.
- internal/config/config_unit_test.go — new test TestWriteBackMetadataDeprecatedAlias: viper.Set("write_back_metadata", true) only (new key absent), call the load path, assert AppConfig.AutoWriteTagsOnFetch == true.
- internal/config/config_unit_test.go — anti-suppression twin: viper.Set("auto_write_tags_on_fetch", false) AND viper.Set("write_back_metadata", true) both set, assert the NEW key wins (false), proving the alias only fires when the new key is genuinely absent, not merely falsy.

Anti-over-suppression test: `TestWriteBackMetadataDeprecatedAlias_NewKeyWins (both keys set, new key must take precedence — proves the alias doesn't clobber an explicit new-key value)` — a known-good input still passes with the new guard active.

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] `go build ./...` succeeds.
- [ ] `go test ./internal/config/... ./internal/metafetch/...` passes.
- [ ] `grep -rn 'WriteBackMetadata\b' --include='*.go' . | grep -v _test.go` returns only the alias-handling lines (persistence.go legacy case, and the fallback read in config.go), not the struct field name itself.
- [ ] Manual: start with a config file containing only `write_back_metadata: true` (no new key) and confirm a WARN log line naming the deprecated key appears once at startup, and that AppConfig.AutoWriteTagsOnFetch is true.
- [ ] Anti-over-suppression test: `TestWriteBackMetadataDeprecatedAlias_NewKeyWins (both keys set, new key must take precedence — proves the alias doesn't clobber an explicit new-key value)` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_config_015.md`.

## Commit message

```
refactor(config): Rename write_back_metadata config key to auto_write_tags_on_ (TODO L1247)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Owner decision context: this item's own instructions require the alias migration (see item text 'Migration matters — do not do a bare rename'). review_critical=true because a broken alias silently changes whether the app writes tags to the user's audio files during auto-fetch, in prod, without any explicit action.
