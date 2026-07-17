---
name: project-context
description: Load project context for the audiobook-organizer codebase. Invoke this skill at the start of any agent that needs project knowledge. Reads live docs files — no hardcoded values. Falls back to generic behavior on non-audiobook-organizer projects.
version: 1.1.0
---

# Project Context Loader

## Step 1 — Detect project type

Check if `docs/AI-REFERENCE.md` exists in the current working directory.

- If YES: this is the audiobook-organizer repo. Load the full corpus below.
- If NO: fall back — read `CLAUDE.md` and any files in `docs/` that describe architecture. Continue with whatever you find.

## Step 2 — Load the knowledge corpus (audiobook-organizer only)

Read each file below in order. Stop if the context window is getting full (skip later files).

1. `docs/AI-REFERENCE.md` — architecture overview, package map, API surface, critical gotchas
2. `TODO.md` — live work items ONLY (the 2026-H1 history is frozen in `docs/archive/todo-2026-H1.md`; do not load it unless researching history)
3. `docs/dedup/STATUS.md` — single source of truth on dedup state: real backlog numbers, remediation path, what is and is not safe to merge/purge
4. `docs/database-architecture.md` — DB design decisions, rationale, schema overview
5. `docs/database-pebble-schema.md` — PebbleDB key format reference
6. `CLAUDE.md` — workflow rules, constraints, build commands
7. `docs/operations/pending-prod-actions.md` — queued run-on-prod actions and their gating
8. `docs/plans/DECISIONS-PENDING.md` — STOP-FOR-HUMAN decision queue (never resolve these autonomously)
9. The 3 most recently dated files in `docs/specs/` (by filename prefix YYYY-MM-DD) — recent architectural decisions
10. The most recent file in `docs/audits/` — latest review findings (as of 2026-07-17: the multi-discipline review)

Use `ls docs/specs/ | sort -r | head -3` to identify the newest spec files.

## Step 3 — Emit context summary

After reading, emit this block (fill in from what you read):

```
=== PROJECT CONTEXT ===
Language/Framework: Go 1.24 backend + React 18/TypeScript frontend (Gin, Material UI)
Build: make build (full) | make build-api (backend only) | make deploy (prod)
Test:  make test | make test-all | make test-e2e | make ci (fast gate)
DB:    PebbleDB (sole production store) | NutsDB (activity/metrics) — SQLite backend REMOVED

Key constraints:
- UpdateBook / UpdateBookFile do FULL column replacement — always fetch-mutate-write, never write a partial struct
- memdb-tier getters strip heavy fields (e.g. AcoustIDFingerprint) — never round-trip a memdb read into a write
- Dedup candidate Status is a VERDICT: never let "pending" overwrite dismissed/merged (terminal-status guard in UpsertCandidateNew)
- Whole-library loops MUST use bounded worker pools (registry.RunItems pattern) and MUST log start/progress/complete/skip
- Whole-backlog candidate listings use the 1M limit + truncation warning — no silent caps
- runApplyPipeline must check isProtectedPath; Purge skips books with iTunes PIDs
- Use LSP (gopls hover/goToDefinition/findReferences) instead of grep for Go symbols
- NEVER commit internal IPs/hostnames to this public repo — hosts are "the prod server" / "the GPU box"; fleet detail lives in falkcorp/infra-docs (private)

Path mapping (CRITICAL — never forget this):
- W:\ on the Windows iTunes machine maps to the books tree on the prod server
- BookFile stores TWO paths: FilePath (translated Linux path) and ITunesPath (original Windows path)
- ALL files are on the prod server — there are ZERO Windows-local-only files. NEVER dismiss iTunes file_not_found as "Windows-only expected"
- When FilePath doesn't resolve for an iTunes file, the file was MOVED on the server (organize bug) — find it on disk by filename/hash

Dedup state: [fill from docs/dedup/STATUS.md — real backlog counts, remediation stage]
Recent decisions: [list 1-3 key points from the newest spec files]
Pending human decisions: [count from DECISIONS-PENDING.md]
=== END CONTEXT ===
```

## Step 4 — Proceed to specialty

After emitting the context summary, the invoking agent takes over.
Do not answer any questions yet — just load context and hand off.
