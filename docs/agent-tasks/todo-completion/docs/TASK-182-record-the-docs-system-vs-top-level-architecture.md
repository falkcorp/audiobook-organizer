<!-- file: docs/agent-tasks/todo-completion/docs/TASK-182-record-the-docs-system-vs-top-level-architecture.md -->
<!-- version: 1.0.0 -->
<!-- guid: f38b49e4-76d4-4905-a6e5-efd0fc44ff89 -->
<!-- last-edited: 2026-08-21 -->

# TASK-182 — Record the docs/system vs top-level architecture classification decision in the docs inventory (TODO.md L101)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · docs subagent · **Why:** The classification judgment itself is already made by the docs' own cross-references (verified above); the remaining work is reading each of the 13 files (9 docs/system + 4 docs/architecture) to confirm none is an accidental true duplicate, then writing a short resolution paragraph into the audit doc -- comprehension task, but small and mostly confirmatory. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 101 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "📚 **Docs consolidation follow-ups (from the 2026-0" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-17-rescope.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/docs-182-record-the-docs-system-vs-top-level-architecture" -b agent/docs-182-record-the-docs-system-vs-top-level-architecture origin/main
cd "$REPO/.worktrees/docs-182-record-the-docs-system-vs-top-level-architecture"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Append a 'Resolved 2026-08-2x' note to docs/audits/2026-08-11-docs-inventory.md's open item at L224 stating: docs/system/** and the top-level architecture docs are NOT duplicates (the top-level doc is a deliberate overview that defers to docs/system/ for depth, per its own text), and the 4 remaining docs/architecture/** files are point-in-time ADR/design records, a distinct genre from both. Read all 13 files first to confirm none is an accidental true duplicate before writing the resolution.

## Background (verify before editing)

- docs/architecture.md:6-8 and docs/system/README.md:7-11 already state the intended relationship between the two doc sets -- this item's classification question is largely pre-answered by the docs' own content, verified above.
- The docs/architecture/** count dropped from 9 (audit time) to 4 between the audit and now, meaning some consolidation already happened outside this item's tracking; the remaining 4 are dated design docs (2026-05-11 to 2026-06-01), not living architecture references.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  sed -n '6,9p' docs/architecture.md   # 1 hit: 'See also: the deeper, per-subsystem system-documentation set lives in docs/system/' — docs/architecture.md explicitly defers subsystem detail to docs/system/
  grep -n 'DOCS-1 workstream complete' docs/system/README.md   # 1 hit ~L7 — docs/system/README.md declares the DOCS-1 workstream complete, no scope item deferred
  ls docs/system | wc -l; ls docs/architecture | wc -l   # 9 and 4 — docs/system has 9 entries, docs/architecture has 4 (down from the audit's 9) at HEAD
  grep -n 'docs/system.*and.*docs/architecture' docs/audits/2026-08-11-docs-inventory.md   # 1 hit ~L224 — the docs inventory still lists this classification as an open follow-up
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Read each of the 9 docs/system/*.md files' first ~20 lines (already sampled: api.md, architecture.md, components.md, deploy-and-gpu-ops.md, incidents.md, pipelines.md, README.md, runbooks.md, storage.md) and the 4 docs/architecture/*.md files, confirming none duplicates the top-level docs/architecture.md or docs/database-architecture.md beyond the deliberate cross-reference already found.
2. If any genuine duplicate content is found (not just topical overlap), archive the narrower file under docs/archive/ with a pointer comment, following the existing docs/archive/ naming convention (date-prefixed filename).
3. Append the resolution to docs/audits/2026-08-11-docs-inventory.md's item at L224, replacing 'follow-up pass' with the decision and citing the docs/architecture.md:6-8 / docs/system/README.md:7-11 cross-references as evidence no consolidation is needed beyond what already happened.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_docs_182.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A docs/system file describing a subsystem's internals could still look like a duplicate by title alone but not be one by content -- confirmed by reading, not grepping titles, per the original item's own caution.

## Tests

- N/A -- docs only.

Anti-over-suppression: N/A

## How to test

```bash
git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] docs/audits/2026-08-11-docs-inventory.md's L224 entry (or its replacement) records an explicit keep/archive decision for both docs/system/** and docs/architecture/**, citing the cross-reference evidence.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_docs_182.md`.

## Commit message

```
feat(docs): Record the docs/system vs top-level architecture classificat (TODO L101)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`docs/audits/2026-08-11-docs-inventory.md's L224 entry (or its replacement) records an explicit keep/archive decision for both docs/system/** and docs/architecture/**, citing the cross-reference evidence.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

The original scope-01 scout flagged this as needing re-scoping because docs/architecture drifted from 9 to 4 files; this rescope found the underlying classification question already effectively pre-answered in the docs themselves, so the real remaining deliverable is much smaller than 'read+judge 13 files from scratch' -- mostly confirmatory reading plus one documentation edit.
