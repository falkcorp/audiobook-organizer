<!-- file: docs/agent-tasks/library-ui/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 09967eaa-f2ef-4570-9bcc-332961dbc1ae -->
<!-- last-edited: 2026-07-01 -->

# Workstream — Library UI

Frontend tasks on the Library page and Settings embeddings section: fix a
stale-cache correctness bug, and add two user-facing filter features. From
EMB-UI-1, USER-QUICK-FILTERS, TAG-SEARCH, and library-cache-bug.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-01 | EMB-UI-1 | "Download latest Ollama" deep-link on Settings embeddings section | P3 | XS | Haiku | 1 |
| TASK-02 | USER-QUICK-FILTERS | Save current filter set as a named preset | P2 | M | Sonnet | 2 |
| TASK-03 | TAG-SEARCH | "Has tag X" filter + browsable tag cloud on Library | P2 | M | Sonnet | 3 |
| TASK-04 | library-cache-bug | Clear `useLibraryCache` on all mutation handlers in Library.tsx | P1 | M | Sonnet | 1 |

## Ground rules

- React/TS code under `web/src`.
- Build + test gate for every task in this workstream:
  ```bash
  cd web && npm install && npm run build && npm test
  ```
- Reuse `apiFetch` and existing hooks (`useLibraryQuery`, `useLibraryCache`,
  `useLibraryFilters`, `useLibrarySelection`) — do not invent parallel data
  fetching or state-management paths.
- Match the existing Material-UI component style already used on the page
  you're editing (menus, dialogs, chips, etc.) — do not introduce a different
  UI kit or ad hoc CSS.
- Bump `.tsx`/`.ts` file headers (version + `last-edited`) on every file you
  touch, per `.standards/instructions/file-headers.md`.
- **Verify every file:line anchor with `grep` before editing** — the codebase
  moves fast and the line numbers in each task brief are a starting point, not
  a guarantee.

## ⚠️ Collision / wave note

**TASK-02, TASK-03, and TASK-04 all edit `web/src/pages/Library.tsx`.** They
MUST be serialized into three separate waves — running any two of them in
parallel guarantees a same-file merge conflict on every rebase cycle:

- **Wave 1:** TASK-04 (cache-invalidation bug fix — highest priority, merges
  first so TASK-02/TASK-03 build on top of the fixed cache-clearing pattern)
  and TASK-01 (Settings-only, disjoint file, safe to run in parallel with
  TASK-04).
- **Wave 2:** TASK-02, serialized after TASK-04 merges and the TASK-02
  worktree is rebased onto `origin/main`.
- **Wave 3:** TASK-03, serialized after TASK-02 merges and the TASK-03
  worktree is rebased onto `origin/main`.

TASK-01 touches only `web/src/components/settings/EmbeddingSettingsSection.tsx`
and never touches `Library.tsx`, so it is independent and runs in wave 1
alongside TASK-04.

See [ORCHESTRATION.md](../ORCHESTRATION.md) (one level up) for the coordinator
+ worker protocol.
