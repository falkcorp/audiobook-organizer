<!-- file: docs/agent-tasks/todo-completion/operations/TASK-117-give-prodschedulerstore-an-unwrap-so-capability-.md -->
<!-- version: 1.0.0 -->
<!-- guid: 73782e3e-bde7-4349-b9d2-aff07114564c -->
<!-- last-edited: 2026-08-21 -->

# TASK-117 — Give prodSchedulerStore an Unwrap() so capability lookups can see past it (TODO.md L4703)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · operations subagent · **Why:** Needs a design call the item itself doesn't spell out: prodSchedulerStore only holds the narrow opRegistryStore interface today, so Unwrap() needs a second, wider field captured at construction time — not a pure copy-paste like items 4a/4b. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 4703 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "`internal/operations/registry/register.go:40-42` —" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-09.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/operations-117-give-prodschedulerstore-an-unwrap-so-capability-" -b agent/operations-117-give-prodschedulerstore-an-unwrap-so-capability- origin/main
cd "$REPO/.worktrees/operations-117-give-prodschedulerstore-an-unwrap-so-capability-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Make prodSchedulerStore implement database.StoreUnwrapper by capturing the full database.Store alongside the narrow opRegistryStore it already embeds, so a future AsCapability lookup against a *prodSchedulerStore-wrapped value can reach the underlying store.

## Background (verify before editing)

- opRegistryStore (register.go ~L44-50) is a narrow interface (database.OpsV2Store + GetBookByID), not database.Store — it cannot itself serve as Unwrap()'s return value without widening.
- opregistry's Build func (register.go ~L79-95) already resolves `store := serviceregistry.Get[opRegistryStore](c, serviceregistry.KeyStore)` from the same container KeyStore that also holds the full database.Store — the same singleton, viewed through a narrower interface.
- This is defect-shaped, not live: `grep -rn "AsCapability" internal/operations/registry/*.go` (run this as part of triage) currently finds no capability lookup running through prodSchedulerStore, matching the item's own 'no capability lookup currently runs through it' framing.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "type prodSchedulerStore struct" -A 3 internal/operations/registry/register.go   # 1 hit ~L59 — prodSchedulerStore embeds opRegistryStore and adds only BookFiles
  grep -n "func (p \*prodSchedulerStore)" internal/operations/registry/register.go   # exactly 1 hit (BookFiles), no Unwrap — no Unwrap method defined on prodSchedulerStore
  grep -n "type StoreUnwrapper interface" -A 3 internal/database/store_capability.go   # 1 hit ~L68 — StoreUnwrapper requires Unwrap() Store
  grep -n "schedStore := &prodSchedulerStore" internal/operations/registry/register.go   # 1 hit ~L91 — prodSchedulerStore is instantiated from the container's KeyStore resolution and handed to SetDepBookStore/NewDepsScheduler
  ```

### Reuse — don't invent

- Use `database.StoreUnwrapper` in `internal/database/store_capability.go` (verify: `grep -n "type StoreUnwrapper interface" internal/database/store_capability.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/operations/registry/register.go, add a `full database.Store` field to `prodSchedulerStore` (alongside its embedded `opRegistryStore`).
2. In the "opregistry" ServiceDef's Build func (~L79-95), also resolve `full := serviceregistry.Get[database.Store](c, serviceregistry.KeyStore)` (same KeyStore, wider generic type) and pass it when constructing `schedStore := &prodSchedulerStore{opRegistryStore: store, full: full}`.
3. Add `func (p *prodSchedulerStore) Unwrap() database.Store { return p.full }` near the existing BookFiles method, with a comment stating the same rationale as indexed_store.go's Unwrap: capability lookups may reach past prodSchedulerStore for methods it does not itself narrow.
4. Add `var _ database.StoreUnwrapper = (*prodSchedulerStore)(nil)` as a compile-time proof, mirroring wire_abs_routes.go:27's pattern.
5. Bump the file's version header and last-edited date.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_operations_117.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- serviceregistry.Get[database.Store](c, KeyStore) must resolve the SAME underlying store instance as serviceregistry.Get[opRegistryStore](c, KeyStore) — confirm serviceregistry.Get is not itself constructing a new value per call (it should be a singleton container lookup); if it constructs fresh per-type wrappers, this whole approach needs revisiting and the item becomes needs_design.

## Tests

- internal/operations/registry/register_test.go (or a new file): TestProdSchedulerStoreUnwrap — construct a prodSchedulerStore with a known full store, call database.AsCapability[SomeKnownCapability](schedStore), assert it resolves through to the full store instead of stopping at prodSchedulerStore.

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/operations/registry/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go build ./internal/operations/registry/... exits 0.
- [ ] go vet ./internal/operations/registry/... exits 0 (confirms the var _ StoreUnwrapper assertion compiles).
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/operations/registry/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_operations_117.md`.

## Commit message

```
feat(operations): Give prodSchedulerStore an Unwrap() so capability lookups ca (TODO L4703)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`go build ./internal/operations/registry/... exits 0.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Marked review_critical=false because the item itself says no capability lookup currently runs through prodSchedulerStore — this is pure preventive hardening matching the audit's stated pattern (SERVER-GLOBAL-STORE-AUDIT / the third-capability-lost incident at server_lifecycle.go:1737-1766). If serviceregistry.Get's per-generic-type resolution turns out not to return the same singleton for KeyStore under two different type parameters, downgrade this to needs_design and ask: does the container guarantee identity across generic Get[T] calls on the same key?
