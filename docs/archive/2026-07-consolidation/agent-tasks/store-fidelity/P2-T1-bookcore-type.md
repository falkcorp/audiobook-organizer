<!-- file: docs/agent-tasks/store-fidelity/P2-T1-bookcore-type.md -->
<!-- version: 1.0.0 -->
<!-- guid: 42b1abf4-b3eb-4765-ac56-070e4dd5dc39 -->
<!-- last-edited: 2026-07-05 -->

# P2-T1 — Introduce `BookCore` + `Book.Core()` (additive)

**Tier:** Opus · **Depends on:** none · **Risk:** low (purely additive; no getter changes)

## ⛔ START HERE
```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/storefid-p2t1" -b agent/storefid-p2t1 origin/main
cd "$REPO/.worktrees/storefid-p2t1"
```

## Goal
Add the compiler-enforced slim type `BookCore` (the fields that SURVIVE the memdb strip) plus a
`Book.Core()` projection. **Purely additive** — nothing returns `BookCore` yet (that's Phase 3),
so this task cannot break callers and stays trivially green. Getting the field partition exactly
right is the whole job; two tests lock it.

## The partition (authoritative)
`BookCore` = every `Book` field EXCEPT these 9 heavy fields, which stay ONLY on `Book`:
`Description, VersionNotes, BookSigV1, BookSigV1Mask, BookSigSegments, BookSigBuiltAt,
BookSigCoveragePct, Author, Series`.
Enumerate the live `Book` fields first — do not hand-transcribe from memory:
```bash
awk '/^type Book struct/{f=1} f{print} f&&/^}/{exit}' internal/database/store.go
grep -n "cp\.\w* = nil" internal/database/memdb_strip.go   # the strip = the heavy set; must match the 9 above
```

## Steps
1. In `internal/database/` define `type BookCore struct { … }` containing every `Book` field
   except the 9 heavy ones, **with the same `json:"…"` struct tags** copied verbatim from `Book`.
2. Add `func (b *Book) Core() BookCore { return BookCore{ …copy every BookCore field… } }`.
3. Add two tests (`internal/database/bookcore_test.go`):
   - `TestBookCoreIsBookMinusHeavyFields` (reflection): `fieldNames(Book) − fieldNames(BookCore)`
     equals exactly the 9 heavy names.
   - `TestBookCoreCopiesAllFields` (reflection): set EVERY `Book` field to a non-zero value, call
     `Core()`, assert every `BookCore` field equals its `Book` counterpart (catches a `Core()`
     that forgets to copy a field — a silent-drop bug hiding in the fix). Iterate fields
     reflectively so new fields are covered automatically.
4. Do NOT change any getter or the `Store` interface. Bump the header on `store.go` (or wherever
   `BookCore` lands).

## Gate
```bash
go build ./... && go vet ./internal/database/ && go test -race ./internal/database/ -run 'BookCore' -count=1 && go test ./internal/database/ -count=1
```

## Acceptance
- [ ] `BookCore` = Book minus exactly the 9 heavy fields; json tags carried over.
- [ ] `Book.Core()` copies every `BookCore` field; both reflection tests pass.
- [ ] no getter/interface/mock change (`git diff` adds only the type + method + test + header).

## Commit
```
feat(store): add BookCore projection type + Book.Core() with parity+copy tests (STOREFID P2-T1)
```
## Done — STOP; coordinator owns push/PR/merge.
