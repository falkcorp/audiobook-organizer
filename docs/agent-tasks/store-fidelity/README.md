<!-- file: docs/agent-tasks/store-fidelity/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8570de43-ebd1-4468-8c24-135ec1fa9a61 -->
<!-- last-edited: 2026-07-05 -->

# Workstream — store-getter fidelity unification (STOREFID)

Executes [`docs/specs/2026-07-05-store-getter-fidelity-unification.md`](../../specs/2026-07-05-store-getter-fidelity-unification.md)
per [`docs/plans/2026-07-05-store-getter-fidelity-unification.md`](../../plans/2026-07-05-store-getter-fidelity-unification.md).

Goal: make Full vs Core (memdb-slim) fidelity **explicit and compiler-enforced** so the
`BookSignatureScan`-class silent no-op (a Core row read where Full was needed) becomes a
compile error. Root cause + full rename table + type design: the spec.

## Ground rules
- Reuse the spec's locked names/types exactly — do NOT invent alternative names.
- Store getters live on the `database.Store` interface + `PebbleStore` + `MemStore` +
  `MockStore` + **mockery-generated mocks**. **Regenerate mocks SCOPED to the changed
  interface only** (local mockery v3.7.1 vs pinned v2.53.6 — an unscoped regen rewrites every
  mock repo-wide). Hand-verify the mock diff.
- Local gate: `go build ./... && go vet ./<pkg> && go test -race ./<pkg>`. Authoritative gate:
  GitHub **Minimal CI** (NOT local `make ci` — its staticcheck is red on main from backlog).
- Worktree per task; version headers mandatory; coordinator owns git/gh.

## Waves

| Wave | Task | What | Tier | Depends |
|---|---|---|---|---|
| **Phase 1** (cleanup, parallel) | P1-T1 | unexport the 0-caller `_Pebble` twins | Haiku | — |
| | P1-T2 | `GetAllBooksFrom` → `GetAllBooksFullFrom` | Haiku | — |
| | P1-T3 | doc-contract the 9 slim getters (stripped-field list) | Haiku | — |
| **Phase 2** (types, parallel w/ P1) | P2-T1 | `BookCore` + `Book.Core()` + parity & copy-completeness tests | Opus | — |
| | P2-T2 | `BookFileCore` + `BookFile.Core()` + same tests | Opus | — |
| **⛔ HARD CHECKPOINT** | — | STOP; re-evaluate appetite + option (B) projection vs (C) memdb-native before Phase 3 | — | P1,P2 merged |
| **Phase 3** (per-getter, SERIALIZED, ascending) | P3-W1..W6 | migrate each slim getter → `…Core`/`[]BookCore`, fix callers | Sonnet/Haiku | Phase 2 |
| **Phase 4** | P4-T1 | naming lint (no exported getter returns Book while delegating to mem()) | Sonnet | Phase 3 |

Phase-3 wave order (ascending call count so the pattern proves small first): W1
`GetBookFilesForIDs` (4) → W2 `GetBooksByAuthorID[WithRole]` (9) → W3 `GetAllBookFiles` (18)
→ W4 `GetBooksBySeriesID` (22) → W5 `GetAllBooks` (79) → W6 the three ≤2-caller getters.
**Phase 3 briefs are generated after the checkpoint** — they depend on the (B)-vs-(C) decision.

## Orchestration
Phase 1 (3 tasks) and Phase 2 (2 tasks) touch mostly disjoint files and run in one parallel
wave; only `store.go` is shared (P1-T2 renames one interface line; P2 adds new type decls —
different regions, sibling-rebase resolves). After all 5 merge → the hard checkpoint. Phase 3
waves SERIALIZE (shared `Store` interface + mock file). See the plan's dependency graph.
