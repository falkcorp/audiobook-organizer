<!-- file: docs/agent-tasks/store-fidelity/P1-T1-unexport-pebble-twins.md -->
<!-- version: 1.0.0 -->
<!-- guid: a66fa6cd-34fb-4f2c-86e9-02e1d74c870f -->
<!-- last-edited: 2026-07-05 -->

# P1-T1 — Unexport the zero-caller `_Pebble` full-fallback twins

**Tier:** Haiku · **Depends on:** none · **Risk:** near-zero (0 external callers)

## ⛔ START HERE
```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/storefid-p1t1" -b agent/storefid-p1t1 origin/main
cd "$REPO/.worktrees/storefid-p1t1"
```

## Goal
Three exported `..._Pebble` methods are the `UseMemDB=false` full-fidelity fallbacks called
ONLY by their own slim wrappers — they have **zero external callers**. Unexport them (lowercase
first letter) so they stop polluting the public `Store` surface with a misleading name. Do NOT
change behavior.

Targets (re-verify each has 0 callers outside its wrapper before editing):
```bash
grep -rn "GetBooksByAuthorID_Pebble\|GetBooksBySeriesID_Pebble\|GetAllBookSummaries_Pebble" --include='*.go' . | grep -v "_test.go"
# expect: only the definition + the call from its own slim wrapper (GetBooksByAuthorID, etc.)
```

## Steps
1. For each of `GetBooksByAuthorID_Pebble`, `GetBooksBySeriesID_Pebble`, `GetAllBookSummaries_Pebble`:
   rename to unexported `getBooksByAuthorIDFull` / `getBooksBySeriesIDFull` / `getAllBookSummariesFull`
   (camelCase, `Full` suffix — matches the spec's naming). Update the single internal caller (the
   slim wrapper's `return p.<name>(...)`).
2. If any is declared on the `Store` interface (`internal/database/store.go`), remove it from the
   interface (it's an impl detail now). Check: `grep -n "_Pebble" internal/database/store.go`.
3. If removed from the interface, regenerate the MockStore mock **scoped only**:
   `mockery --name Store --dir internal/database` (verify the diff drops only these 3 methods; do
   NOT commit an unscoped repo-wide mock regen). If they were never on the interface, no mock change.
4. Bump version headers on every changed file (`last-edited: 2026-07-05`).

## Gate
```bash
go build ./... && go vet ./internal/database/ && go test -race ./internal/database/ -count=1
```

## Acceptance
- [ ] `grep -rn "_Pebble" --include='*.go' internal/database/` → 0 (or only unrelated names).
- [ ] the three methods are unexported; their single callers updated; behavior unchanged.
- [ ] `go build ./...` clean; mock diff (if any) scoped to these 3 methods only.
- [ ] headers bumped.

## Commit
```
refactor(store): unexport the zero-caller _Pebble full-fallback twins (STOREFID P1-T1)
```
## Done — STOP; coordinator owns push/PR/merge.
