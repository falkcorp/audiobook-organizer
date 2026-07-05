<!-- file: docs/agent-tasks/store-fidelity/P2-T2-bookfilecore-type.md -->
<!-- version: 1.0.0 -->
<!-- guid: 46664a8a-bcf4-423f-8e2e-f3a508081350 -->
<!-- last-edited: 2026-07-05 -->

# P2-T2 — Introduce `BookFileCore` + `BookFile.Core()` (additive)

**Tier:** Opus · **Depends on:** none · **Risk:** low (purely additive)

## ⛔ START HERE
```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/storefid-p2t2" -b agent/storefid-p2t2 origin/main
cd "$REPO/.worktrees/storefid-p2t2"
```
(Independent of P2-T1 — different type; safe to run in parallel. If both edit `store.go`, the
coordinator sibling-rebases.)

## Goal
Add `BookFileCore` (fields surviving the memdb strip) + `BookFile.Core()`. Purely additive.

## The partition (authoritative)
`BookFileCore` = every `BookFile` field EXCEPT the heavy set stripped by `stripBookFileForMemdb`:
`FingerprintFailureReason, FingerprintFailureDetail, FingerprintDiagnosticJSON,
AcoustIDFingerprint, AcoustIDSeg0, AcoustIDSeg1, AcoustIDSeg2, AcoustIDSeg3, AcoustIDSeg4,
AcoustIDSeg5, AcoustIDSeg6`.
**KEEP on Core (NOT stripped — verify in memdb_strip.go):** `FingerprintFailedAt`,
`AcoustIDFingerprintDurationSec`.
Enumerate first:
```bash
awk '/^type BookFile struct/{f=1} f{print} f&&/^}/{exit}' internal/database/store.go
sed -n '/func stripBookFileForMemdb/,/^}/p' internal/database/memdb_strip.go | grep "= nil"
```

## Steps
1. Define `type BookFileCore struct { … }` = `BookFile` minus the stripped set, json tags carried.
2. Add `func (f *BookFile) Core() BookFileCore { … }` copying every `BookFileCore` field.
3. Two reflection tests (`internal/database/bookfilecore_test.go`):
   `TestBookFileCoreIsBookFileMinusHeavyFields` (name-set diff == the stripped set) and
   `TestBookFileCoreCopiesAllFields` (populate-all → `Core()` → every field copied).
4. No getter/interface/mock change. Bump header.

## Gate
```bash
go build ./... && go vet ./internal/database/ && go test -race ./internal/database/ -run 'BookFileCore' -count=1 && go test ./internal/database/ -count=1
```

## Acceptance
- [ ] `BookFileCore` = BookFile minus exactly the stripped set (FingerprintFailedAt +
  AcoustIDFingerprintDurationSec RETAINED); json tags carried.
- [ ] `BookFile.Core()` copies every field; both reflection tests pass.
- [ ] additive only (`git diff` = type + method + test + header).

## Commit
```
feat(store): add BookFileCore projection type + BookFile.Core() with parity+copy tests (STOREFID P2-T2)
```
## Done — STOP; coordinator owns push/PR/merge.
