<!-- file: docs/agent-tasks/todo-completion/server/TASK-139-reproduce-and-classify-the-persistent-ui-lockup-.md -->
<!-- version: 1.0.0 -->
<!-- guid: f7386402-7f5c-49a6-8803-798cf697cea1 -->
<!-- last-edited: 2026-08-21 -->

# TASK-139 — Reproduce and classify the persistent UI lockup (backend vs frontend vs warmup-sequencing) with DevTools evidence, before writing any fix (UI-LOCKUP-2)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · server subagent · **Why:** A structured reproduce-and-classify investigation (DevTools Network open, correlate with server logs/restart windows) with a clear decision tree already given by the item -- bounded, but requires live reproduction against a running instance, not pure static analysis. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 2431 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**UI-LOCKUP-2**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-17-rescope.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-139-reproduce-and-classify-the-persistent-ui-lockup-" -b agent/server-139-reproduce-and-classify-the-persistent-ui-lockup- origin/main
cd "$REPO/.worktrees/server-139-reproduce-and-classify-the-persistent-ui-lockup-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Reproduce the reported lockup with browser DevTools Network open and classify it: are the slow-to-render views waiting on PENDING network requests (backend-bound), or are requests returning promptly while the page itself janks (frontend-bound)? If backend: identify the specific slow/unbounded endpoint(s) among the known candidates (searchWithBleve, the activity-log query in maintenance_fixups.go). If frontend: profile whether virtualization is actually active on the specific list that janks. Separately, check whether the lockup correlates with the ~10-minute post-restart memdb-warmup window. Write the classification to docs/audits/2026-08-21-ui-lockup-classification.md.

## Background (verify before editing)

- Already measured on prod the same night (2026-08-11): GET /api/v1/audiobooks?library_state=imported&limit=1 took 36 seconds for one row; GET /api/libraries/{id}/personalized took 2m10s; the server was OOM-killed 4 times in 90 minutes; memdb warmup takes 568s during which the list cache fails to warm; 30 abandoned-activity-log-query goroutines were found pinning 30GB with zero clients connected.
- The previous round of this same UI-lockup investigation was closed against a DOM-volume/virtualization hypothesis -- this item explicitly asks to state whether that hypothesis still holds or was wrong, rather than silently assuming virtualization work is still the right lever.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'func.*searchWithBleve' internal/audiobooks/service_query.go   # 1 hit ~L671 — the SEARCH-CACHE-related candidate function exists
  find internal/server -iname 'maintenance_fixups*'   # 1 hit: internal/server/maintenance_fixups.go — the activity-log/maintenance-fixup candidate file exists
  grep -rln 'WipeAllActivity' --include='*.go' internal | grep -v _test   # ≥5 hits including internal/server/maintenance_fixups.go — WipeAllActivity, the other named candidate contributor, appears across multiple activity-store files including maintenance_fixups.go
  ```

### Reuse — don't invent

- Use `L1980 SEARCH-CACHE and L1970 WipeAllActivity-cancellation work in this same scope (both plausible contributors to 'requests never return')` in `internal/audiobooks/service_query.go` (verify: `grep -n 'func.*searchWithBleve' internal/audiobooks/service_query.go`) — do NOT write a parallel helper.

## Step-by-step

1. Reproduce the reported freeze with browser DevTools Network tab open, on both a freshly-restarted server (within the ~10min warmup window) and a warmed-up one, to separate startup-sequencing from a steady-state bug.
2. Record whether the janking view's requests are PENDING at the time the page appears frozen, vs. returning quickly while the render itself stalls.
3. If backend-bound: identify which endpoint(s) are slow, cross-referencing internal/audiobooks/service_query.go's searchWithBleve and internal/server/maintenance_fixups.go's activity-log query handling, and note whether the L1970/L1980 fixes elsewhere in this scope would already address it.
4. If frontend-bound: use the React DevTools Profiler to confirm whether virtualization is actually mounted and active on the SPECIFIC list component that janks in this reproduction.
5. Check correlation with server restart/warmup timing.
6. Write up the finding to docs/audits/2026-08-21-ui-lockup-classification.md, explicitly stating which of the three (backend steady-state, frontend rendering, warmup-window sequencing) is the binding constraint, with the DevTools/Profiler evidence, before any fix is written.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server_139.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- The three causes are not mutually exclusive -- the report must not force a single answer if evidence points at more than one; state the actual causal chain rather than picking one category to satisfy the decision-tree framing.

## Tests

- (none)

Anti-over-suppression test: `N/A -- investigation task.` — a known-good input still passes with the new guard active.

## How to test

```bash
git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] docs/audits/2026-08-21-ui-lockup-classification.md exists and states, with DevTools/Profiler evidence, which of the three candidate causes is the actual binding constraint for the reproduced freeze.
- [ ] Anti-over-suppression test: `N/A -- investigation task.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server_139.md`.

## Commit message

```
fix(server): Reproduce and classify the persistent UI lockup (backend vs  (UI-LOCKUP-2)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`docs/audits/2026-08-21-ui-lockup-classification.md exists and states, with DevTools/Profiler evidence, which of the three candidate causes is the actual binding constraint for the reproduced freeze.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Cross-reference this scope's own L1970 (WipeAllActivity cancellation) and L1980 (SEARCH-CACHE) items -- both are plausible contributors, and their fixes may partially or fully explain/resolve what this investigation finds; worth checking whether they've landed before concluding this needs an independent fix.
