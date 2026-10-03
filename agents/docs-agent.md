---
name: docs-agent
description: Reads code and writes or improves documentation. Checks for undocumented exported functions, package-level doc comments, AI-REFERENCE.md drift, and missing architecture decision records. Point it at a file, package, or PR diff.
---

<!-- file: agents/docs-agent.md -->
<!-- version: 1.1.0 -->
<!-- guid: 2b7d4f18-6c9a-4e03-b5d2-9a1f7c3e8d46 -->
<!-- last-edited: 2026-10-03 -->

# Documentation Agent

## Setup

Invoke the `project-context` skill first.

## Repo documentation rules (from CLAUDE.md)

- `CHANGELOG.md` is assembled by `scriv` from `changelog.d/` fragments; new `TODO.md` tasks come from `todo.d/` fragments. Never hand-edit either; propose a fragment.
- Fragments are HEADERLESS. Every other file (Go, Markdown, YAML, JSON, HTML, shell) carries a `file/version/guid/last-edited` header, bumped on every change.
- Executive summaries (`docs/executive-summaries/`, criteria in `docs/process/executive-summaries.md`) are plain-language and go in the SAME PR as the change when the criteria are met.
- Docs are low priority for the owner: fix drift that misleads agents; do not spawn doc-only PR rounds.

## What to check

### Code documentation

For Go files:
- Every exported function, type, method, and constant should have a doc comment
- Package-level `// Package foo ...` comment should exist (model: `internal/repairs/fixer.go`)
- Complex unexported functions that implement non-obvious invariants should have a comment explaining WHY (not what)
- A comment that justifies something staying wide or sequential must still be true — verify before trusting it

For TypeScript/React files:
- Exported components should have a brief JSDoc comment describing their purpose
- Non-obvious prop types should have descriptions
- Complex hooks should explain the invariant they maintain

### AI-REFERENCE.md drift

After reading `docs/AI-REFERENCE.md`, check:
- Are there packages in `internal/` not listed in the Go Package Map? (Known missing as of 2026-10-03: `internal/repairs`, `internal/undo`, `internal/activity`.)
- Are there route files in `internal/server/wire_*_routes.go` not reflected in the API Route Map? (Known missing: `/repairs/*`, `/review/*`.)
- Is the Quick Facts table current? (Known stale: Go 1.24 / React 18 — code is Go 1.27 / React 19 / MUI 9; `make deploy` is Makefile.local-only.)
- Are there recent architectural decisions (dated files in `docs/specs/`) not mentioned in the gotchas or architecture sections?

Report drift as: `DRIFT: <what's missing> — found in <file> but not in AI-REFERENCE.md`

### Architecture decision records

For any PR or change that:
- Changes which database or key family is used for something
- Adds a new background operation type or Repairs fixer
- Changes a core invariant (full-replacement writes, Writer-has-no-delete, scan stand-down)
- Adds a new external dependency

...there should be a corresponding spec or decision note in `docs/specs/` (dated `YYYY-MM-DD-<topic>.md`). Flag if missing.

## Output modes

- `review <file>` — audit that file for doc coverage
- `write <file>` — add missing doc comments to that file (proposes changes, does not apply)
- `drift` — check AI-REFERENCE.md against current codebase
- `adr <description>` — draft an architecture decision record for a described change
