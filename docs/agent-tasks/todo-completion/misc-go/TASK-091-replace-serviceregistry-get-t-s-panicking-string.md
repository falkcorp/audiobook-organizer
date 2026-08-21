<!-- file: docs/agent-tasks/todo-completion/misc-go/TASK-091-replace-serviceregistry-get-t-s-panicking-string.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3d02ce36-5073-4d68-9072-5762b6b6b30c -->
<!-- last-edited: 2026-08-21 -->

# TASK-091 — Replace serviceregistry.Get[T]'s panicking string-key lookups with typed accessors (ARCH-8)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · misc-go subagent · **Why:** touches every Get[T](c, "name") call site across the service registry's consumers to introduce typed keys without breaking the Needs-declaration mechanism · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 4214 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "ARCH-3/4/5/7/8 remain structural programs; ARCH-8'" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-07.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/misc-go-091-replace-serviceregistry-get-t-s-panicking-string" -b agent/misc-go-091-replace-serviceregistry-get-t-s-panicking-string origin/main
cd "$REPO/.worktrees/misc-go-091-replace-serviceregistry-get-t-s-panicking-string"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Per the audit doc's own recommendation ('Add typed service keys or generated accessors for core services'), replace bare-string Get[T](c, "serviceName") call sites for the small set of CORE services with typed accessor functions (e.g. func GetDatabase(c *Container) database.Store) that fail at compile time on a typo instead of panicking at runtime — this is the smallest of the ARCH items (ARCH-8) per the TODO's own framing; ARCH-3/4/5/7 are large structural programs out of scope for this item (see part 2).

## Background (verify before editing)

- container.go:248's panic fires when name is not declared in the active builder's Needs slice — a programmer error caught only at Build() time, and only if that code path executes (i.e. never for an unused/rarely-exercised builder).
- container.go:255's panic fires when the service was never built at all, same fail-fast-but-runtime-only characteristic.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "panic(fmt.Sprintf" internal/serviceregistry/container.go   # 2 hits at L248 and L255 — Get[T] panics when the requested service name is not in the active builder's Needs
  grep -n "func Get\[T any\](c \*Container, name string) T" internal/serviceregistry/container.go   # 1 hit — Get is a generic function keyed by a bare string name
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Identify the 'core services' worth typed accessors — grep -rn 'serviceregistry.Get\[' internal --include=*.go to enumerate every call site and its string name, then group by which names appear across MANY builders' Needs (those are the 'core' ones worth a typed wrapper) versus one-off names (leave those on the generic Get[T] path).
2. For each core service, add a typed accessor in container.go (or a new typed_accessors.go) of the shape `func Get<Name>(c *Container) <ConcreteType> { return Get[<ConcreteType>](c, "<name>") }`, and update call sites to use it.
3. Do NOT remove the generic Get[T] — it stays for the long tail of one-off services; this is additive, not a replacement of the whole mechanism.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_misc-go_091.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A service intentionally optional (built conditionally based on config) should NOT get a typed accessor that assumes it's always present — only truly-core, always-built services qualify.

## Tests

- internal/serviceregistry/container_test.go: add a test that a typed accessor for a service not in Needs still panics with the same message (behavior preserved, just called through a named wrapper) — a compile-time-safety improvement, not a runtime-behavior change.

Anti-over-suppression: N/A

## How to test

```bash
make ci && npm --prefix web run lint && npm --prefix web test
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] go build ./... succeeds; go vet ./internal/serviceregistry/... is clean.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci && npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_misc-go_091.md`.

## Commit message

```
refactor(misc-go): Replace serviceregistry.Get[T]'s panicking string-key lookup (ARCH-8)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

This is a partial, incremental step toward ARCH-8's full recommendation, not a wholesale registry redesign — matches the TODO's own framing of it as 'the smallest' of the five ARCH items.
