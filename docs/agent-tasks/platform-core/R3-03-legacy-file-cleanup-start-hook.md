<!-- file: docs/agent-tasks/platform-core/R3-03-legacy-file-cleanup-start-hook.md -->
<!-- version: 1.1.1 -->
<!-- guid: b76a7b67-773f-4425-8bf4-d8294b15c6cb -->
<!-- last-edited: 2026-11-05 -->

# R3-03 — Books' start hook legacy-file-cleanup: delete .encryption_key and HEAD's library.bleve, each a crash-labelled boot-writer row

| Field | Value |
|---|---|
| ID | R3-03 |
| Release | R3 |
| Tier | **T3** (Opus / high): a boot-time deletion of a secret file with a skip rule; deleting the key while it is the only copy loses every pre-R2 archive's secrets |
| Wave | W44 (beside R3-01; file-disjoint: this row owns `internal/books/module.go` in W44, R3-01 owns `internal/books/services.go`) |
| Depends on | R3-02 |
| Spec | 05-storage §5S.3.8 (the `legacy-file-cleanup` paragraph), §5S.3.16 (the crash matrix kit), §5S.10 (`TestLegacyFileCleanup`); 06 §6.5.1 writer table row "Run 2 (R3+)", §6.5.1 "Releases" R3, §6.5.2 start order step (2); 05-config §1.12.2 ("HEAD's key"), §2.4 (state-file table, row `encryption_key`); 09 §9.4 R3 "Content", "Verification"; 03 §3.8 R3 |

**Gate:** plan, then execute autonomously in a worktree with its own PR. R3-02 must be merged (the floor guarantees
M2 ran before this hook runs).

## START HERE (do this first, exactly)

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/pc-r3-03-legacy-files" -b agent/platform-core-r3-03-legacy-files origin/main
cd "$REPO/.worktrees/pc-r3-03-legacy-files"
git rebase origin/main
```

## Goal

Add books' start-hook step `legacy-file-cleanup` (05-storage §5S.3.8; 06 §6.5.1 writer table "Run 2 (R3+)"). At
every boot after the floor has passed it:

- deletes HEAD's `.encryption_key` at the path 05-config §2.4's state-file table resolves (the state directory,
  else HEAD's fallback `<root_dir>/.appdata/.encryption_key`), through the labelled state filesystem, crash label
  `books.legacy-file-cleanup.encryption_key`;
- deletes HEAD's `library.bleve` directory beside the database, through the labelled state filesystem, crash label
  `books.legacy-file-cleanup.library_bleve`;
- is idempotent: an absent file is done;
- while health raises `secrets_keyring_not_exported`, still deletes `library.bleve` but **skips only**
  `.encryption_key`, logs why, and retries the key at the next boot;
- when it deletes the key, logs that the off-host `.encryption_key` taken before R2 is now the only copy.

R3 runs no migration step; these two deletions are the only stored data R3 changes (05-storage §5S.3.8).

## Invariants

- **I1.** No deletion is an untracked write: both go through the labelled state FS with the two labels above, and
  the crash matrix knows both labels (05-storage §5S.3.16; `TestBootCrashMatrixCoversEveryWriter`).
- **I2.** `.encryption_key` is never deleted while `secrets_keyring_not_exported` is raised.
- **I3.** `library.bleve` is deleted regardless of the keyring token.
- **I4.** A kill at any point leaves a state the next boot completes; a second boot after completion does nothing.
- **I5.** The hook never fails boot for an absent file; a real I/O error is logged and retried at the next boot
  (05-config §1.12.2: "retries the key at the next boot").
- **I6.** The hook imports nothing R3-02 deleted (`internal/books/cutover`, the frozen HEAD codec). The fallback
  path needs `root_dir`: read it from the migrated canonical config key (the rename table's successor of HEAD's
  `root_dir`), not from HEAD's blob, which M6 deleted.

## Files

| Action | Path |
|---|---|
| create | `internal/books/legacy_file_cleanup.go` |
| create | `internal/books/legacy_file_cleanup_test.go` |
| modify | `internal/books/crash_matrix_test.go` (R1-23's file, beside `TestBootCrashMatrix_ABO`): the "R3 legacy-file-cleanup" scenario. It names ABO's files and books' labels, so it lives in ABO's instance, never in core's app-neutral kit (05-storage §5S.3.16; `TestCoreHoldsNoAppKeyNames`); `core/` is untouched |
| modify | `internal/books/module.go` (register the start-hook step) |
| create | one `changelog.d/` fragment (headerless) |

## Re-verify before editing (expected results at your base)

```bash
# 1. The floor is declared (R3-02 merged). Expected: 1 line
grep -rn 'UpgradeFloor{' --include='*.go' internal/books/ | grep -v _test.go

# 2. The labelled state FS a module receives. Expected: >=1 hit naming the type/func books' hooks use
#    (05-storage §5S.3.16: core/internal/statefs is the seam; modules reach it through a public handle).
grep -rn 'statefs\|StateFS\|LabelledFS\|Labeled' --include='*.go' core/module core/app core/storage | head

# 3. The keyring token. Expected: >=1 hit (the token string as raised by core secrets since F3)
grep -rn '"secrets_keyring_not_exported"' --include='*.go' core/

# 4. How books registers a start hook today. Expected: >=1 hit in internal/books
grep -rn 'r\.Hook(\|\.Hook(' --include='*.go' internal/books/ | head

# 5. The ABO crash-matrix instance and its writer registry. Expected: 1 definition each;
#    TestBootCrashMatrix_ABO in internal/books/crash_matrix_test.go (R1-23)
grep -rn 'func TestBootCrashMatrix_ABO\|func TestBootCrashMatrixCoversEveryWriter' --include='*.go' . core/
grep -c 'func TestBootCrashMatrix_ABO' internal/books/crash_matrix_test.go      # 1

# 6. HEAD's index path at your base (the directory name the hook deletes). Expected: >=1 hit naming library.bleve
grep -rn 'library\.bleve' --include='*.go' internal/ | head
```

STOP and report if grep 1 or 3 returns nothing, if grep 2 finds no public handle a module can use for labelled
state-FS deletions (do not reach into `core/internal`), or if grep 5 finds no ABO crash-matrix instance in
`internal/books/crash_matrix_test.go` (the row's file must follow where it is).

## Steps

1. Read the spec sections listed above, in full.
2. Write the tests (next section) first.
3. Implement `legacy-file-cleanup` in `internal/books/legacy_file_cleanup.go` as a books start-hook step registered
   through `Registrar.Hook` (06 §6.4.3; start order step (2) of 06 §6.5.2). Suggested shape (names coined here):
   `func legacyFileCleanup(deps legacyCleanupDeps) module.Hook` where `legacyCleanupDeps` carries the labelled state
   FS, the resolved state directory, `<db parent>`, a function returning the canonical `root_dir`, the health query
   for `secrets_keyring_not_exported` and a logger.
4. Resolve the key path exactly as 05-config §2.4's row says: the state directory (with its fallback rule, §1.7 C15)
   first, then `<root_dir>/.appdata/.encryption_key`. Delete each candidate that exists (both, if both exist).
5. Delete `<db parent>/library.bleve` (the directory HEAD wrote; confirm the name with grep 6) recursively through
   the labelled FS; HEAD's index sibling markers that live beside it (the mapping and rebuilding markers, 05-storage
   §5S.3.8) go with it if they sit outside the directory.
6. Register the two crash labels with the crash matrix and add the scenario "R3 legacy-file-cleanup" to
   `TestBootCrashMatrix_ABO`'s scenarios in `internal/books/crash_matrix_test.go` (05-storage §5S.3.16 lists it), with
   fixtures: both files present, key only, index only, neither, and the keyring token raised.
7. Log lines (INFO): one per deletion; on the key deletion, the sentence that the off-host `.encryption_key` taken
   before R2 is now the only copy; on a skip, that the key was kept because the keyring has not been exported.
8. Register the step in `internal/books/module.go`. Run the acceptance block. Bump headers. Add a `changelog.d/` fragment.

## Code contract

No public API. Behaviour as in the writer-table row (06 §6.5.1 "Run 2 (R3+)"), with the labels exactly:

```text
books.legacy-file-cleanup.encryption_key
books.legacy-file-cleanup.library_bleve
```

## Tests (write first; spec name copied exactly)

| Test | Proves |
|---|---|
| `TestLegacyFileCleanup` (05-storage §5S.10) | an R3 boot deletes `.encryption_key` (at its state-dir path and at HEAD's fallback) and `library.bleve` through labelled writes, skips only the key (still deleting `library.bleve`) while `secrets_keyring_not_exported` is raised, logs that the off-host copy is the only one, and is a no-op on the next boot |
| the crash-matrix scenario in `TestBootCrashMatrix_ABO` | a kill and an error return (ENOSPC, EACCES) at each of the two labelled operations; the next boot reaches readiness and completes the cleanup (I4) |
| `TestLegacyFileCleanup_NoCutoverImports` (name coined here) | `go list -deps` of `internal/books` contains no deleted package (I6) |

## Acceptance (commands and expected output)

```bash
# Labels present. Expected: 2
grep -rhoE 'books\.legacy-file-cleanup\.(encryption_key|library_bleve)' --include='*.go' internal/ | sort -u | wc -l

# The tests. Expected: ok, no FAIL
go test -count=1 -run 'TestLegacyFileCleanup' ./internal/books/...
go test -count=1 -run 'TestBootCrashMatrix_ABO|TestBootCrashMatrixCoversEveryWriter' ./...

# Builds. Expected: exit 0
go build ./... && (cd core && go build ./...)

# Full local gate. Expected last line: ✅ All CI checks passed!
make ci
```

## Out of scope

- The floor and the deletions of R3-02; R3-01's package deletions.
- Exporting the keyring or touching `secrets.keyring` (owner step, OWNER-06).
- The R3 release note (R3-04).

## Rollback

Revert the commit. Files already deleted on a host are not restored by a revert: the key's off-host copy (kept by
the owner before R2, OWNER-05 step 1 and OWNER-06) is the recovery, and R2 reads neither file on a migrated store
(09 §9.4 R3 "Rollback").

## Repo rules

- Worktree off fresh `origin/main`; headers on every new or changed file.
- Conventional commit, for example `feat(books): legacy-file-cleanup start hook for R3`, ending with
  `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`; never squash; never `--admin`.
- A boot-time durable write extends the crash matrix in the same PR (09 §9.3): this PR does.

