<!-- file: docs/agent-tasks/bug-techdebt/TASK-02-staticcheck-burndown.md -->
<!-- version: 1.0.0 -->
<!-- guid: b0b6d4db-131c-4c41-802c-b230e024496e -->
<!-- last-edited: 2026-07-10 -->

# TASK-02 — Drain the staticcheck backlog to green (STATICCHECK-BURNDOWN, #1796)

**Priority:** P2 · **Effort:** L · **Recommended subagent:** Sonnet-class · dead-code-audit subagent · **Why:** every U1000 deletion needs a "really dead?" grep and every SA1019 needs a keep-vs-fix judgment — too much judgment for Haiku-class despite the mechanical shape · **Depends on:** TASK-01, TASK-03, TASK-05, TASK-07 merged (wave 2 — the file set is derived at run time; those four tasks' merges can change the findings AND their code files may overlap your run-time-derived file set. TASK-04 is exempt: it touches only `.github/workflows/ci.yml`, which cannot alter any Go finding — do not wait on it)

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY per item (worktree/PR/CI) EXCEPT REPO-SIZE-1 (#1650) which is STOP-FOR-HUMAN: a git-history rewrite is destructive and invalidates every clone/worktree — produce the migration plan (BFG/filter-repo vs LFS options, coordination checklist, backup strategy) as a TASK brief whose ONLY deliverable is the plan document, then STOP.
**File-ownership:** `internal/dedup/engine.go` is INIT-2-owned (structural edits). Your only touch there is deleting the unused `bestSeg` func (U1000). If INIT-2 work is in flight when you start (check open PRs touching engine.go: `gh pr list --search "engine.go"`), SKIP that finding with `//lint:ignore U1000 kept: INIT-2 in flight (2026-07-10)` and report it. COORDINATOR HARD GATE: the grep is a point-in-time check and can miss an INIT-2 PR that opens mid-flight — when this task runs under a coordinator, the coordinator must confirm no INIT-2 wave is ACTIVE (session/state-level check, per the plan's cross-initiative section) before dispatching you; solo executors keep the grep as the best available check. Everything else in your run-time file set was verified disjoint from all INIT-9 wave-1 tasks at planning time.

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/bug-techdebt-staticcheck-burndown" -b agent/bug-techdebt-staticcheck-burndown origin/main
cd "$REPO/.worktrees/bug-techdebt-staticcheck-burndown"
git rebase origin/main
```

## Goal

Make `staticcheck ./...` exit 0 on main so the `make ci` staticcheck step stops being
red for every developer. At planning time (HEAD `fce58498`, 2026-07-10) there were
**41 findings: 37 U1000 (unused symbol) + 4 SA1019 (use of deprecated symbol)**. Your
authoritative list is the one you generate fresh in step 1 — NOT the planning-time
snapshot (wave-1 merges will have shifted it).

## Background (verify before editing)

- The per-PR merge gate for this repo is **Minimal CI (ci.yml) green** — it does NOT
  run staticcheck. Local `make ci` runs staticcheck (Makefile ~:228) and has been red
  on main since before #1767's partial cleanup. This task is the drain that makes the
  local gate honest again.
- The 4 planning-time SA1019s reference deprecated SQLite-era sentinels
  (`database.ErrSQLiteRBACUnsupported` in `internal/server/bootstrap.go` and
  `internal/server/middleware/auth.go` ×2; `database.SQLiteTableStat` in
  `internal/server/handlers/diagnostics.go`). The sentinels' own doc comments say
  "will be removed once all callers have been updated" — updating the callers IS this
  task. `SQLiteTableStat` is documented as kept for JSON/API compat in the db-health
  endpoint: that one is a deliberate keep → `//lint:ignore SA1019 <reason>`.
- staticcheck is installed via `go install honnef.co/go/tools/cmd/staticcheck@latest`
  (unpinned — note which version you used in the PR body).

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n 'STATICCHECK-BURNDOWN' TODO.md
  # Expected: 1 hit (~:1285), the tracking item you will check off
  grep -n 'staticcheck' Makefile
  # Expected: target ~:228-:233 and its membership in the ci target ~:350
  ```

## Step-by-step

1. Generate the authoritative list: Run: `staticcheck ./... | tee /tmp/sc-findings.txt; wc -l /tmp/sc-findings.txt`
   Expected: ~35-45 lines (41 at planning time; MUST be re-derived now).
2. For each **U1000** finding: run `grep -rn '<symbol>' --include='*.go' . | grep -v .worktrees`.
   - Only hit is the declaration → delete the symbol (whole func/type/const/var + its
     doc comment).
   - Additional hits exist (e.g. build-tag-guarded or generated code) → do NOT delete;
     add `//lint:ignore U1000 <one-line reason>` directly above the declaration.
   - Test-file findings (`*_test.go` helpers): same rule — delete if truly unused.
3. For each **SA1019** finding: update the caller off the deprecated symbol where the
   deprecation notice prescribes a replacement; if the caller is a deliberate
   API/JSON-compat keep (the `SQLiteTableStat` case), use
   `//lint:ignore SA1019 kept for db-health JSON compat (<date>)` instead. If removing
   the LAST caller of a deprecated sentinel, also delete the sentinel itself (its doc
   comment says to) — then re-run step 1 to catch the new U1000 that deletion may expose.
4. Iterate steps 1-3 until: Run: `staticcheck ./...` Expected: exit 0, empty output.
5. Do NOT refactor anything beyond the findings: no renames, no signature changes, no
   import reordering beyond gofmt. Deletions must be surgical.
6. Check off the TODO.md STATICCHECK-BURNDOWN item (~:1285, grep above) with a
   `✅ done 2026-07-10+ (TASK-02, N findings drained)` note; prepend a CHANGELOG.md entry.
7. Bump the file header (version + last-edited) on EVERY file you touch; keep guids.
8. Run the gate (below) — after this task, the staticcheck step of `make ci` must pass.

Anti-over-suppression: N/A (no filter/guard/veto/skip/dedupe path is added; the
`//lint:ignore` directives are per-line and each carries its reason).

## How to test

```bash
staticcheck ./...
# Expected: exit 0, no output
go test ./... -short
# Expected: all green — deletions can break builds of build-tag variants; -short is the store-getter-discipline full run
make ci
```

staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files
you changed; the merge gate is Minimal CI green. (This task IS the backlog drain — for
this task only, the expectation is the FULL `staticcheck ./...` at exit 0. The
`sdkguard` step should already be green if TASK-03 merged; if not, treat a sdkguard
failure listing only `internal/logger` + `internal/dedup/unified` as pre-existing.)

## Acceptance criteria

- [ ] `staticcheck ./...` exits 0 (empty output)
- [ ] Every `//lint:ignore` added by this task carries an inline reason (verify: `grep -rn 'lint:ignore' --include='*.go' . | grep -v .worktrees` — each hit self-explanatory)
- [ ] `go test ./... -short` green (full suite, not a subset)
- [ ] `go vet ./...` clean
- [ ] Anti-over-suppression: N/A
- [ ] TODO.md item checked off; CHANGELOG.md prepended
- [ ] File headers bumped on every changed file (`grep -n 'last-edited:' <file>` shows the run date)

## Commit message

```
chore(lint): drain staticcheck backlog to zero findings (#1796)

Deletes grep-verified-dead U1000 symbols, migrates SA1019 callers off the
deprecated SQLite sentinels (lint:ignore for the deliberate JSON-compat keep),
so make ci's staticcheck step is green on main for the first time since #1767.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/bug-techdebt-staticcheck-burndown
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `staticcheck ./...` already exits 0 with empty output, the drain is already done —
run the acceptance checks instead of re-applying. Rollback = revert the single commit
to restore the deleted symbols and deprecated-caller code; no data, schema, or runtime
behavior is touched (everything deleted was statically unreachable or deprecated-but-
functional).
