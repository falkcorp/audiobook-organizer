<!-- file: docs/agent-tasks/todo-completion/database/TASK-179-database-store-40-build-the-ast-go-types-ci-gate.md -->
<!-- version: 1.0.0 -->
<!-- guid: b61da377-5a66-4123-bc38-6694649670d1 -->
<!-- last-edited: 2026-08-21 -->

# TASK-179 — database.Store (40) -- build the AST/go-types CI gate that makes it unreachable in new files (Phase 2 item 2 of the kill-v1-and-narrow plan) (TODO.md L969)

**Priority:** P2 · **Effort:** L · **Recommended subagent:** Opus-class · database subagent · **Why:** requires an AST/go-types-based static check (grep undercounts by 15% per the plan doc's own measurement) integrated into CI, plus a grandfather list for the 91 existing legitimate uses -- real tooling work, not a mechanical edit. · **Depends on:** none · **Wave:** 2

Source: `TODO.md` line 969 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "`database.Store` (40) — make unreachable rather th" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-17-rescope.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/database-179-database-store-40-build-the-ast-go-types-ci-gate" -b agent/database-179-database-store-40-build-the-ast-go-types-ci-gate origin/main
cd "$REPO/.worktrees/database-179-database-store-40-build-the-ast-go-types-ci-gate"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Build a new Go tool (tools/cmd/storewidthgate) using go/packages + go/types (NOT go/parser alone, since resolving `database.Store` as a TYPE requires type-checking, not just syntax -- oplint's go/parser-only approach is a starting structural template, not sufficient on its own for this gate) that walks the module, finds every func/method parameter, struct field, and var declared with the resolved type database.Store, and fails when a file NOT in a checked-in baseline list uses it. Seed the baseline from the current 91-file grep result. Wire it into CI following the existing Interface Width Ratchet job's pattern (a new job or a new step in that job), with a Makefile target.

## Background (verify before editing)

- docs/plans/2026-08-17-kill-v1-and-narrow-store-interfaces.md Phase 2 (lines 107-124): 'Not "shrink BookStore". Instead, remove the paths by which a new file can acquire 398 methods without deciding to.' Item 2: 'A build gate on database.Store in new files. Must be AST/go-types, not grep.'
- store.go's own comment on the regrouping explicitly frames it as satisfying the interfacebloat LINTER's declared-entries count, not the reachability goal -- confirmed the existing Interface Width Ratchet CI job (scripts/check-interface-width.sh) targets interfacebloat exclusively and by design ('interfacebloat ONLY, deliberately' -- counting anything else would conflate two different measurements), so this new gate is genuinely additive, not a duplicate of existing CI.
- The 91-file baseline is the number a new gate would need to grandfather at introduction time; the plan doc separately measured grep undercounting the true count by ~15% (11 seen vs 87 real in one measurement), so the AST tool's own baseline-seeding run should be trusted over this grep number if they diverge.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  find internal/database -iname 'iface_misc*'   # 0 hits — iface_misc.go no longer exists -- already split (differently than the plan literally described)
  grep -n 'This is a REGROUPING, not a narrowing' internal/database/store.go   # 1 hit ~L20 — Store is now a 6-entry regrouping of the original 40, explicitly NOT a narrowing
  grep -rln 'database\.Store\b' --include='*.go' . | grep -v _test | grep -v vendor | grep -v .worktrees | wc -l   # 91 — 91 files still reference database.Store directly -- reachability is unchanged
  grep -n 'interfacebloat ONLY, deliberately' scripts/check-interface-width.sh   # 1 hit -- the script's own comment names interfacebloat as its sole, deliberate target — the existing Interface Width Ratchet CI job measures a DIFFERENT metric (interfacebloat declared width, not database.Store parameter usage)
  find . -iname '*store_width*' -o -iname '*interfacebloat*'   # 0 hits outside .golangci.yml — no width-reachability gate targeting database.Store as a parameter type exists yet
  grep -n 'A build gate on .database.Store. in new files' docs/plans/2026-08-17-kill-v1-and-narrow-store-interfaces.md   # 1 hit ~L117 — the plan doc's Phase 2 item 2 explicitly demands AST/go-types, not grep
  grep -n '"go/parser"' tools/cmd/oplint/main.go   # 1 hit — an AST-parsing Go tool precedent already exists to model the new tool on
  sed -n '101,107p' .github/workflows/ci.yml   # shows the 'interface-width' job name and its steps, the pattern to mirror for a new 'store-width' job — a baseline-ratchet CI job pattern already exists to model the new gate's wiring on
  ```

### Reuse — don't invent

- Use `docs/plans/2026-08-17-kill-v1-and-narrow-store-interfaces.md Phase 2 item 2 (the design already written)` in `docs/plans/2026-08-17-kill-v1-and-narrow-store-interfaces.md` (verify: `grep -n 'A build gate on .database.Store. in new files' docs/plans/2026-08-17-kill-v1-and-narrow-store-interfaces.md`) — do NOT write a parallel helper.
- Use `tools/cmd/oplint -- existing AST-based (go/parser) CI lint tool to model the new tool's structure on` in `tools/cmd/oplint/main.go` (verify: `grep -n 'package main' tools/cmd/oplint/main.go`) — do NOT write a parallel helper.
- Use `scripts/check-interface-width.sh's baseline-ratchet pattern (bump-the-number-down-when-it-improves convention) to reuse for the new gate's baseline file` in `scripts/check-interface-width.sh` (verify: `grep -n 'BASELINE_FILE=' scripts/check-interface-width.sh`) — do NOT write a parallel helper.
- Use `the 'Interface Width Ratchet' CI job as the wiring template for a new job/step` in `.github/workflows/ci.yml` (verify: `grep -n 'interface-width:' .github/workflows/ci.yml`) — do NOT write a parallel helper.

## Step-by-step

1. Write tools/cmd/storewidthgate/main.go using golang.org/x/tools/go/packages to load the module and go/types to resolve every func/method parameter, struct field, and var declaration, flagging any whose resolved type is github.com/falkcorp/audiobook-organizer/internal/database.Store.
2. Seed .store-width-gate-baseline from the tool's own first run against HEAD (not the raw 91-file grep, since the plan doc warns grep undercounts by ~15% -- trust the AST tool's own count once it exists).
3. Exempt internal/database itself (the type's home package) entirely from the gate.
4. Wire `go run ./tools/cmd/storewidthgate` into a new Makefile target (e.g. `store-width-gate`) and add it either as a new step in the existing 'Interface Width Ratchet' job (.github/workflows/ci.yml:101-146) or a new job following that job's structure (Go setup, cache restore, golangci-lint install is NOT needed here since this is a custom tool, not a golangci-lint invocation).
5. Provide an escape hatch (e.g. adding a file to the baseline in the same PR) for a genuinely justified new wide use, mirroring the interfacebloat //nolint-with-reason convention.
6. Update docs/plans/2026-08-17-kill-v1-and-narrow-store-interfaces.md to mark Phase 2 item 2 complete, and correct item 3's status to reflect the regrouping-not-narrowing outcome if not already noted there.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_database_179.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- database.MockStore (399 methods, 108 files) is explicitly NOT in scope per the plan doc -- the gate must target database.Store specifically, not MockStore or any test-only type.
- A file inside internal/database itself declaring Store-typed values (the type's own home package) should be exempt entirely, not counted against the baseline.

## Tests

- tools/cmd/storewidthgate/main_test.go: a small fixture module with one file using database.Store as a parameter type is correctly flagged when NOT in the baseline, and correctly passed when it IS.
- A synthetic PR-diff fixture (or a dry-run mode) proving the gate fires when wired into the workflow, not just in a local unit test.

Anti-over-suppression test: `The synthetic-new-file-not-in-baseline test above is the anti-over-suppression case: proves the gate actually fires rather than being permissive-by-default.` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./tools/cmd/storewidthgate/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] The new tool run against current HEAD reports the true count (expected close to but possibly above 91, per the plan doc's undercount warning) with zero unexpected new hits once the baseline is seeded from its own output.
- [ ] A synthetic test file added with a database.Store parameter and NOT added to the baseline causes the CI job to fail.
- [ ] Anti-over-suppression test: `The synthetic-new-file-not-in-baseline test above is the anti-over-suppression case: proves the gate actually fires rather than being permissive-by-default.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./tools/cmd/storewidthgate/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_database_179.md`.

## Commit message

```
feat(database): database.Store (40) -- build the AST/go-types CI gate that m (TODO L969)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — `the artifact this task adds is present: re-run find internal/database -iname 'iface_misc*'` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Corrects the prior scout's proposed reuse target: tools/cmd/oplint uses go/parser (syntax only), which cannot resolve `database.Store` as a TYPE across package boundaries -- the new tool needs go/packages + go/types (type-checked), a materially bigger dependency than oplint's plain AST walk. This is real, unstarted work despite the interfacebloat linter score having already been fixed by the store.go regrouping -- do not let a green Interface Width Ratchet run be mistaken for this item being done; they measure different things, exactly as store.go's own comment warns.
