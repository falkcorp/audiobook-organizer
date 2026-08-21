<!-- file: docs/agent-tasks/todo-completion/database/TASK-034-add-func-override-fields-to-mockstore-s-86-hardw.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2ce5ab37-3d39-4a8e-88c2-6316375a3502 -->
<!-- last-edited: 2026-08-21 -->

# TASK-034 — Add Func override fields to MockStore's ~86 hardwired-zero-return methods (TODO.md L4728)

**Priority:** P2 · **Effort:** L · **Recommended subagent:** Haiku-class · database subagent · **Why:** Purely mechanical — for each of the ~86 methods, add one `XFunc func(...) (...)` field to the MockStore struct and wrap the existing hardwired return in `if m.XFunc != nil { return m.XFunc(args...) }` before it, following the pattern of the other 313 methods verbatim. Large in count but zero design judgment per method; a strong candidate for the repo's parallel-refactor-sweep skill, sharded by method-name ranges within the single file. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 4728 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "⚠ `internal/database/mock_store.go` — ~88 of `Mock" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-09.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/database-034-add-func-override-fields-to-mockstore-s-86-hardw" -b agent/database-034-add-func-override-fields-to-mockstore-s-86-hardw origin/main
cd "$REPO/.worktrees/database-034-add-func-override-fields-to-mockstore-s-86-hardw"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Give every one of MockStore's remaining ~86 hardwired-zero-return methods an XFunc override field, following the exact `if m.XFunc != nil { return m.XFunc(args...) }; return <existing zero literal>` shape already used by the other 313 methods, so any test can override any mock method's behavior instead of being stuck with a compile-time-fixed zero value.

## Background (verify before editing)

- This is documentation debt as much as code debt: a caller reading mock_store.go's existing 313-method pattern reasonably assumes ALL methods are overridable; the 86 silent exceptions are a trap for the next test author.
- Because MockStore is a single generated-by-hand file (not mockery-generated per .mockery.yaml — grep -n "MockStore" .mockery.yaml to confirm it is absent from the mockery config, meaning it is a hand-maintained double), there is no regeneration step; every one of the 86 methods needs a manual edit.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -c "^func (m \*MockStore)" internal/database/mock_store.go   # 399 — total MockStore methods
  grep -c "Func != nil" internal/database/mock_store.go   # 313 — methods with a Func-override guard
  grep -n "func (m \*MockStore) GetAllAuthorBookCounts" -A 4 internal/database/mock_store.go   # 1 hit, body is `if m.GetAllAuthorBookCountsFunc != nil { return m.GetAllAuthorBookCountsFunc() }` — GetAllAuthorBookCounts DOES have an override field, contradicting the item's specific example
  grep -n "func TestListAuthors_Success" -A 25 internal/server/handlers_integration_test.go   # 1 hit; body only asserts resp.Count == 2, no BookCount assertion, no GetAllAuthorBookCountsFunc set — TestListAuthors_Success never sets GetAllAuthorBookCountsFunc and never asserts BookCount
  ```

### Reuse — don't invent

- Use `existing `if m.XFunc != nil { return m.XFunc(...) }` pattern already used by 313 of 399 methods` in `internal/database/mock_store.go` (verify: `grep -n "Func != nil" internal/database/mock_store.go | head -3`) — do NOT write a parallel helper.

## Step-by-step

1. Run `grep -n "^func (m \*MockStore)" internal/database/mock_store.go` to enumerate all 399 methods with line numbers.
2. Run `grep -L` is not directly usable per-method in one file; instead scan the output of the enumeration and, for each method, check the 1-4 lines following its signature for `Func != nil` — build the list of the ~86 methods lacking it.
3. For each such method: (a) find its 1-3 line body (`return <expr1>, <expr2>` or similar), (b) add a new struct field to the `MockStore` struct definition (near the top of the file, alongside the other ~313 `XFunc func(...)` fields, keeping the same declared-order-follows-method-order convention already used) named `<MethodName>Func func(<same params>) (<same returns>)`, (c) rewrite the method body to `if m.<MethodName>Func != nil { return m.<MethodName>Func(<params>) }` followed by the original hardwired return as the fallback.
4. This is large enough (86 methods) to warrant the `/parallel-sweep` or `parallel-refactor-sweep` skill per CLAUDE.md's '≥3 mechanically-similar refactor tasks' rule — shard the 86 methods into ~4-6 worker batches by method name alphabetical range, each producing a small diff to the same two regions of the same file (struct field block + method bodies), then integrate serially to avoid merge conflicts within one file.
5. Bump the file's version header and last-edited date once, after the full sweep lands.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_database_034.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A method whose current hardwired return references mutable receiver state (e.g. increments a counter, appends to a slice) rather than a pure literal — for those, the Func-override fallback must still execute the side effect when no override is set, i.e. wrap `if m.XFunc != nil { return m.XFunc(...) } <original side-effecting body>`, not silently drop it.

## Tests

- For at least the GetAllAuthorBookCounts case the item calls out by name: update TestListAuthors_Success (or add a new test) to set GetAllAuthorBookCountsFunc and assert the returned BookCount is non-zero for at least one author, proving the override field is both present and wired through the real handler path — this doubles as the fix for this scope's L4732 test-quality item's *spirit*, though the concrete L4732 item targets a different test (TestOrganizeService_PerformOrganize_NoBooksToOrganize).
- No existing test should break: adding a Func field with a nil zero-value preserves every current call site's behavior exactly (the fallback path is the previous hardwired return, verbatim).

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/database/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go build ./internal/database/... exits 0.
- [ ] grep -c "Func != nil" internal/database/mock_store.go returns ≥399 (every method now checks its own override, some methods may have >1 check if they branch).
- [ ] go test ./... exits with the SAME pass/fail set as before the change (a snapshot `go test ./... 2>&1 | md5` before/after, restricted to test names/counts not timing, should match) — this is a behavior-preserving structural change.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/database/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_database_034.md`.

## Commit message

```
feat(database): Add Func override fields to MockStore's ~86 hardwired-zero-r (TODO L4728)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`go build ./internal/database/... exits 0.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

L-effort, not S/M, because of the sheer method count (~86), even though each individual edit is trivial — flagged per CLAUDE.md's 'Fix It Right: depth' rule rather than silently downgrading to a partial pass over 'the important ones'. Good candidate for the repo's parallel-refactor-sweep skill given ≥20 similar call sites.
