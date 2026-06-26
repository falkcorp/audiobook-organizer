---
name: project-context
description: Load project context for the audiobook-organizer codebase. Invoke this skill at the start of any agent that needs project knowledge. Reads live docs files — no hardcoded values. Falls back to generic behavior on non-audiobook-organizer projects.
version: 1.0.0
---

# Project Context Loader

## Step 1 — Detect project type

Check if `docs/AI-REFERENCE.md` exists in the current working directory.

- If YES: this is the audiobook-organizer repo. Load the full corpus below.
- If NO: fall back — read `CLAUDE.md` and any files in `docs/` that describe architecture. Continue with whatever you find.

## Step 2 — Load the knowledge corpus (audiobook-organizer only)

Read each file below in order. Stop if the context window is getting full (skip later files).

1. `docs/AI-REFERENCE.md` — architecture overview, package map, API surface, critical gotchas
2. `docs/database-architecture.md` — DB design decisions, rationale, schema overview
3. `docs/database-pebble-schema.md` — PebbleDB key format reference
4. `CLAUDE.md` — workflow rules, constraints, build commands
5. The 3 most recently dated files in `docs/specs/` (by filename prefix YYYY-MM-DD) — recent architectural decisions

Use `ls docs/specs/ | sort -r | head -3` to identify the newest spec files.

## Step 3 — Emit context summary

After reading, emit this block (fill in from what you read):

```
=== PROJECT CONTEXT ===
Language/Framework: Go 1.24 backend + React 18/TypeScript frontend (Gin, Material UI)
Build: make build (full) | make build-api (backend only) | make deploy (prod)
Test:  make test | make test-all | make test-e2e
DB:    PebbleDB (primary) | NutsDB (activity log) | SQLite (opt-in)

Key constraints:
- UpdateBook does FULL column replacement — always supply all fields
- ensureLibraryCopy returns stale data — follow with syncMetadataToLibraryCopy
- runApplyPipeline must check isProtectedPath
- Purge skips books with iTunes PIDs
- Use LSP (gopls hover/goToDefinition/findReferences) instead of grep for Go symbols

Path mapping (CRITICAL — never forget this):
- W:\ on the Windows iTunes machine maps to /mnt/bigdata/books/ on the server (172.16.2.30)
- iTunes XML at /mnt/bigdata/books/itunes/iTunes Library.xml contains file://localhost/W:/itunes/... URLs
- BookFile stores TWO paths: FilePath (translated Linux path) and ITunesPath (original Windows path)
- ALL files are on the NAS (172.16.2.30) — there are ZERO Windows-local-only files. NEVER dismiss iTunes file_not_found as "Windows-only expected"
- When FilePath doesn't resolve for an iTunes file, the file was MOVED on the server (organize bug) — find it on disk by filename/hash

Recent decisions: [list 1-3 key points from the newest spec files]
=== END CONTEXT ===
```

## Step 4 — Proceed to specialty

After emitting the context summary, the invoking agent takes over.
Do not answer any questions yet — just load context and hand off.
