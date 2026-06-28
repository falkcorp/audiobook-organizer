<!-- file: docs/agent-tasks/dedup-ui/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 1b2c3d4e-9405-4132-9d74-0f2031428304 -->
<!-- last-edited: 2026-06-28 -->

# Workstream D — Dedup & library UI tasks

Five independent React/TypeScript front-end tasks from TODO.md (CONS-4, CONS-6,
CONS-11, C6, DEDUP-KB-1). Each is self-contained — run all five in parallel.

| Task | TODO id | Title | Priority | Effort |
|------|---------|-------|----------|--------|
| TASK-01 | CONS-4 | BookDedup row redesign | P2 | M |
| TASK-02 | CONS-6 | Metadata-compare tab in compare drawer | P2 | M |
| TASK-03 | CONS-11 | Manual-import button on Library | P2 | S |
| TASK-04 | C6 | Dedup label review panel | P3 | M |
| TASK-05 | DEDUP-KB-1 | Keyboard shortcuts for dedup page | P3 | S |

All are independent → one wave, up to 5 agents in parallel. See `orchestration.md`.

## Front-end ground rules (all tasks)

- Code lives under `web/src/`. Build + test:
  ```bash
  cd web && npm install && npm run build && npm test
  ```
- Match the existing component style (Material UI, hooks). Reuse existing API
  helpers (e.g. the `apiFetch` wrapper) rather than raw `fetch`.
- Recommended subagent: **frontend subagent**; optionally a **code-exploration
  subagent** first to locate the component and its props.
- Bump the file-header (`<!-- file/version/guid/last-edited -->`) on every `.tsx`/`.ts` you touch.
- Verify any API path against the backend before wiring it:
  ```bash
  grep -rn "dedup/candidates\|operations/v2\|dedup:label" internal/server/ | head
  ```
