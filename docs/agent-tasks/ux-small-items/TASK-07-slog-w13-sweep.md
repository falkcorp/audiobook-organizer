<!-- file: docs/agent-tasks/ux-small-items/TASK-07-slog-w13-sweep.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5628dca6-e82f-4aff-a03c-04b9a04feed0 -->
<!-- last-edited: 2026-07-10 -->

# TASK-07 — SLOG-W13: wire op-context raw slog calls to logging.* (#1254) — /parallel-sweep

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY where cheap (worktree/PR/CI per item); defer with a written note where not. SLOG-PROD-VERIFY (#1255) is read-only prod verification — allowed autonomously via SSH per house rules, but any prod file/data change discovered as needed -> AskUserQuestion.
**File-ownership:** ⚠ TWO ACTIVE CONSTRAINTS — (1) the candidate file set includes `internal/server/metadata_ops.go` (contains `runBulk*` slog calls), which INIT-3 also plans to touch. Before dispatching ANY shard into `internal/server/`, verify INIT-3's `metadata_ops.go` work has merged (or is not started); EXCLUDE `internal/server/metadata_ops.go` from all shards until that check clears, then cover it in a trailing shard. (2) **INIT-1/INIT-2 dedup partition** (master plan §INIT-1 locked decisions — "the overlapping-wave rebase trap"): INIT-2 OWNS all structural edits to `internal/dedup/engine.go` (76 slog Info/Warn/Error/Debug sites at HEAD `fce58498` — a sweep shard covering `internal/dedup/` WILL edit it) and `internal/database/embedding_store.go`; INIT-1 owns rules.go/dataset/calibration/label-review in the same territory. EXCLUDE `internal/dedup/**`, `internal/plugins/dedup/**`, and `internal/database/embedding_store.go` from ALL shards until BOTH INIT-1 and INIT-2 have merged their work there, then cover them in a trailing shard (identical treatment to the metadata_ops.go rule). Also shares `TODO.md` with TASK-08/TASK-05 — wave 5.

**Priority:** P2 · **Effort:** L · **Recommended subagent:** Sonnet-class sweep-coordinator + Haiku-class shard workers · mechanical-refactor sweep · **Why:** per-shard edits are fully mechanical (Haiku); scoping which call sites are in-scope needs judgment (Sonnet coordinator) · **Depends on:** TASK-08 merged (TODO.md serialization); INIT-3 ownership check; INIT-1/INIT-2 dedup-partition check (excluded paths → trailing shard)

**Execution mode: /parallel-sweep — trigger: 1162 `slog.Info/Warn` call sites across 212 files measured at HEAD `fce58498` (≥20-call-site / ≥3-similar-task threshold), sharded per package with disjoint file sets, gate = `make ci` per shard.** Re-verify the count first (Step 1); if the in-scope subset after filtering turns out < 20 call sites, downgrade to a single Sonnet-class task and note it in the PR.

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/ux-small-items-slog-w13-sweep" -b agent/ux-small-items-slog-w13-sweep origin/main
cd "$REPO/.worktrees/ux-small-items-slog-w13-sweep"
git rebase origin/main
```
(The /parallel-sweep skill creates one worktree per shard on top of this; shard workers edit ONLY their shard's file list. Shard workers never push — the sweep coordinator owns git/PR/merge per the sweep protocol.)

## Goal

Reroute raw `slog.Info/Warn/Error/Debug` call sites that sit INSIDE op-context flows (code reachable below a `logging.WithOp`) to the structured `logging.Info(ctx, ...)` family, so those lines carry the op-ID chain (GitHub issue #1254, TODO SLOG-W13). REUSE `internal/logging/structured.go`'s existing `Info/Warn/Error/Debug(ctx, msg, attrs...)` — do NOT write wrappers, do NOT add new logging helpers, do NOT invent a new package.

## Background (verify before editing)

- TODO.md SLOG-W13 (~:1408) scopes this precisely: **priority = code inside op-context flows (where `logging.WithOp` was called upstream); code outside ops (startup, background goroutines) can STAY raw slog.** That sentence is the in/out-of-scope test.
- Fresh measurement at HEAD `fce58498` (2026-07-10): 1162 `slog.Info(`/`slog.Warn(` occurrences across 212 files under `internal/`; all four levels ≈ 1410 across 220 files. TODO's "~1363/193" figure is stale — re-measure, don't trust either number.
- Already-converted prior waves (do NOT redo): batch poller (commit `7f5c28f1`), writeback + ISBN flows (PR #1715), scanner deep paths (PR #1724).
- HARD RULE: a call site whose enclosing function has NO `ctx context.Context` in scope is OUT of scope for this sweep — do NOT thread new ctx parameters (that is a signature change; forbidden in a sweep shard; record it in the residual report instead).

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n "SLOG-W13" TODO.md                                                     # 2 hits: index line (~:347) + the item itself (~:1408)
  grep -n "func Info(ctx context.Context" internal/logging/structured.go         # API to REUSE, 1 hit
  grep -n "func WithOp" internal/logging/opcontext.go                            # op-context source, 1 hit
  grep -rn "slog\.\(Info\|Warn\|Error\|Debug\)(" internal/ --include="*.go" | wc -l   # fresh total (was 1410)
  grep -n 'func (s \*Server) runBulk' internal/server/metadata_ops.go            # INIT-3-contested territory — ownership check, 3 hits (~:55, ~:439, ~:630; they are methods, so a bare "func runBulk" grep finds nothing)
  grep -c "slog\.\(Info\|Warn\|Error\|Debug\)(" internal/dedup/engine.go          # INIT-2-owned battleground (was 76) — MUST stay excluded until INIT-1+INIT-2 merge
  ```

## Step-by-step

1. Fresh count (grep above). Build the candidate file list: `grep -rln "slog\.\(Info\|Warn\|Error\|Debug\)(" internal/ --include="*.go"`.
2. Filter to IN-SCOPE files: keep files whose flagged call sites live in functions with `ctx context.Context` available AND that sit on op flows (op plugins under `internal/plugins/`, op runner paths, handlers invoked with op contexts). Startup/lifecycle/background-daemon files are OUT — list them in the residual note, untouched.
3. FILE-OWNERSHIP CHECKS (two): (a) remove `internal/server/metadata_ops.go` from every shard unless INIT-3's work there is confirmed merged/not-started; if excluded, add it to a trailing shard executed after INIT-3 clears. (b) remove `internal/dedup/**`, `internal/plugins/dedup/**`, and `internal/database/embedding_store.go` from every shard unless BOTH INIT-1 and INIT-2 have confirmed merged their work in those files (INIT-2 owns engine.go/embedding_store.go structural edits; INIT-1 owns rules.go/dataset/calibration/label-review); if excluded, add them to a trailing shard executed after both clear. Excluding a path means the ENTIRE path is untouched — do not "partially sweep" a contested package.
4. Shard the in-scope list per package (disjoint file sets — compute, don't eyeball); invoke `/parallel-sweep` with one task per shard. Each shard's mechanical edit: `slog.X(msg, args...)` → `logging.X(ctx, msg, args...)`; add the `internal/logging` import; remove `log/slog` import ONLY if unused after the edit. No other changes — no signature changes, no reordering, no drive-by fixes.
5. Each shard runs `make ci`; after the LAST shard merges, run the FULL `go test ./... -short` on main (store/logging-adjacent sweeps must not vacuously pass a subset).
6. Update TODO.md SLOG-W13 note with the post-sweep residual count (out-of-scope call sites remaining, by category) — this is the final shard's edit.
7. Anti-over-suppression: N/A (behavior-preserving reroute; no filter/guard/veto/skip/dedupe path added).
8. Bump headers on every touched file.

## How to test

```bash
make ci
go test ./... -short   # full suite after the final shard
```
staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you changed; the merge gate is Minimal CI green.

## Acceptance criteria

- [ ] For every swept file: `grep -n "slog\.\(Info\|Warn\|Error\|Debug\)(" <file>` returns 0 hits for in-scope sites (documented out-of-scope sites may remain and are listed in the residual note).
- [ ] `git diff origin/main --stat` per shard shows only that shard's files + headers — zero signature changes (`grep -rn "func .*ctx context.Context" --include="*.go"` diff shows no NEW ctx params added by the sweep).
- [ ] Anti-over-suppression: N/A
- [ ] Full `go test ./... -short` green after the final shard; Minimal CI green on every shard PR.
- [ ] `grep -n "SLOG-W13" TODO.md` shows the updated residual count with a 2026-07 date.
- [ ] File headers bumped on every changed file.

## Commit message

```
refactor(logging): wire op-context slog call sites to logging.* — shard <N>/<M> (SLOG-W13, #1254)

Mechanical reroute so op-flow log lines carry the op-ID chain; call sites
without ctx in scope are left raw and recorded in the residual note. No
signatures changed.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/ux-small-items-slog-w13-sweep   # coordinator only; shard branches per sweep protocol
gh pr create --fill
gh pr merge <number> --rebase
```
One PR per shard (sweep protocol); the sweep coordinator merges each on Minimal CI green and rebases sibling shards after every merge.

## Idempotency / Rollback

Per file (transform polarity): if `grep -n "logging\.\(Info\|Warn\|Error\|Debug\)(ctx" <file>` hits at the converted sites AND `grep -n "slog\.\(Info\|Warn\|Error\|Debug\)(" <file>` returns 0 for in-scope sites, that file is already swept — skip it and move to the next shard file (re-dispatching a converted shard is a no-op if this grep runs first). Rollback = revert individual shard PRs independently; a reverted shard returns those files to raw slog with zero behavior change; no data or schema touched.
