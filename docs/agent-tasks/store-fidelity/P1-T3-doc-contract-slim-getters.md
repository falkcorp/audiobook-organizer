<!-- file: docs/agent-tasks/store-fidelity/P1-T3-doc-contract-slim-getters.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4c1417d3-bb83-4af6-964d-41f06ffd2345 -->
<!-- last-edited: 2026-07-05 -->

# P1-T3 — Doc-contract the 9 slim (memdb-projection) getters

**Tier:** Haiku · **Depends on:** none · **Risk:** none (comments only)

## ⛔ START HERE
```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/storefid-p1t3" -b agent/storefid-p1t3 origin/main
cd "$REPO/.worktrees/storefid-p1t3"
```

## Goal
Until the typed `BookCore` migration lands (Phase 3), make the fidelity trap visible in docs.
Add a doc comment to each of the 9 SLIM getters naming the stripped fields + the full-fetch
escape hatch. **Comments only — no code changes.** (This is a stopgap; the type is the real fix.)

The 9 slim getters (each delegates to `p.mem()` in `PebbleStore`):
`GetAllBooks`, `GetBooksBySeriesID`, `GetAllBookFiles`, `GetBooksByAuthorID`,
`GetBooksByAuthorIDWithRole`, `GetBookFilesForIDs`, `GetBookFilesNeedingDelugeImport`,
`GetDuplicateBooksByMetadata`, `GetFolderDuplicates`.

Stripped fields to cite — Book: `Description, VersionNotes, BookSigV1, BookSigV1Mask,
BookSigSegments, BookSigBuiltAt, BookSigCoveragePct, Author, Series`. BookFile:
`FingerprintFailureReason/Detail/DiagnosticJSON, AcoustIDFingerprint, AcoustIDSeg0..6`
(kept: `FingerprintFailedAt`, `AcoustIDFingerprintDurationSec`).

## Steps
1. Above each of the 9 getter definitions on `PebbleStore`, add:
   `// SLIM (memdb projection): returns rows with heavy fields nil'd — <the relevant stripped`
   `// list>. A caller that needs any of those MUST fetch via GetBookByID / GetBookFiles(bookID)`
   `// / GetAllBooksFullFrom (full Pebble). See docs/specs/2026-07-05-store-getter-fidelity-unification.md.`
   (Use the Book list for book getters, the BookFile list for file getters.)
2. Also add a one-line pointer on the `Store` interface declaration of each.
3. Bump version headers on every changed file.

## Gate
```bash
go build ./... && go vet ./internal/database/
```

## Acceptance
- [ ] all 9 getters carry the stripped-field doc comment; interface decls point to the spec.
- [ ] no behavioral/signature change (`git diff` is comments + headers only).

## Commit
```
docs(store): document the stripped fields on the 9 memdb-slim getters (STOREFID P1-T3)
```
## Done — STOP; coordinator owns push/PR/merge.
