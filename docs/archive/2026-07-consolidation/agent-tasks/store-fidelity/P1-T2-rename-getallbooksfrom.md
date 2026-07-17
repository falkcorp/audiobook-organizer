<!-- file: docs/agent-tasks/store-fidelity/P1-T2-rename-getallbooksfrom.md -->
<!-- version: 1.0.0 -->
<!-- guid: d18b61ce-6b40-46c5-87bd-9c6869d6ae94 -->
<!-- last-edited: 2026-07-05 -->

# P1-T2 — Rename `GetAllBooksFrom` → `GetAllBooksFullFrom`

**Tier:** Haiku · **Depends on:** none · **Risk:** low (2 callers)

## ⛔ START HERE
```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/storefid-p1t2" -b agent/storefid-p1t2 origin/main
cd "$REPO/.worktrees/storefid-p1t2"
```

## Goal
`GetAllBooksFrom` returns FULL rows (it takes the memdb branch but re-hydrates each book via
`GetBookByID`), yet its name collides conceptually with the SLIM `GetAllBooks`. Rename it to
`GetAllBooksFullFrom` so the fidelity is explicit. Pure rename — behavior unchanged.

Re-verify sites before editing:
```bash
grep -rn "GetAllBooksFrom" --include='*.go' .   # interface (store.go) + PebbleStore def + MemStore? + ~2 prod + ~2 test callers
```
Known caller: `internal/dedup/engine.go` `getAllPrimaryBooksWithFullFields` (from PR #1830).

## Steps
1. Rename the method on the `Store` interface (`internal/database/store.go`), `PebbleStore`
   (`pebble_store.go`), and any other impl (MemStore/MockStore) that declares it.
2. Update all callers (`grep` list above) to `GetAllBooksFullFrom`.
3. Regenerate the Store mock **scoped only** (`mockery --name Store --dir internal/database`);
   verify the diff renames just this one method.
4. Bump version headers on every changed file.

## Gate
```bash
go build ./... && go vet ./internal/... && go test -race ./internal/database/ ./internal/dedup/ -count=1
```

## Acceptance
- [ ] `grep -rn "GetAllBooksFrom\b" --include='*.go' .` → 0 (all now `GetAllBooksFullFrom`).
- [ ] behavior unchanged; `go build ./...` clean; mock diff scoped to the one rename.
- [ ] headers bumped.

## Commit
```
refactor(store): rename GetAllBooksFrom -> GetAllBooksFullFrom for explicit fidelity (STOREFID P1-T2)
```
## Done — STOP; coordinator owns push/PR/merge.
