<!-- file: docs/agent-tasks/platform-core/PC-07-09-strict-claims-default.md -->
<!-- version: 1.0.0 -->
<!-- guid: c9d1e65b-5011-4782-aaef-bd6a2e7b5cc2 -->
<!-- last-edited: 2026-11-04 -->

# PC-07-09 — S2b r2: `core.storage.claims.mode=strict` by default

| Field | Value |
|---|---|
| ID | PC-07-09 |
| Tier | **T1 (Sonnet, medium).** One default value and one marker line behind a soak-gated release marker; every edit site is located by a grep with an exact expected count. |
| Wave | W07 (stage S2b). **First PR of release `S2br2`** (no flip: `shipped_stage:` stays `S2b`). |
| Depends on | OWNER-07 (the current `S2br1` soak row, whose evidence is a claim-violation record with no entry whose `last_at` is at or after the row's `<since>`) |
| Trailer | `Stage-Release: S2b 2` on every commit |
| Required checks | `compat-rehearsal` (this PR changes `release:`), `extraction-gates`, `core-ci` if `core/` changes, `docker-image`, `vet-darwin`, `vet-integration` |
| Dial back | `core.storage.claims.mode=shadow` in the operator layer (file, env or flag) undoes it without a rollback |

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add ../audiobook-organizer-pc-07-09 -b feat/pc-07-09-strict-claims origin/main
cd "$REPO/../audiobook-organizer-pc-07-09"
git rebase origin/main
git worktree list
```

## Step 0 — gate (STOP if any line differs)

```bash
grep -E '^(shipped_stage|release):' ci/stage.txt      # exactly: shipped_stage: S2b / release: S2br1
grep -n '^S2br1 ' ci/soak.txt                          # exactly 1 line (OWNER-07's row)
```

If `ci/soak.txt` has no `S2br1` row, STOP: the merge would be refused by `check_soak.py` (09 §9.2.1 (a), (b)).
You may still prepare the PR, but do not ask for merge.

## Re-verify: locate the one default (expected counts)

PC-07-05 set ABO's S2br1 default to `shadow` through an app-supplied default whose name it coined and marked.
Find it:

```bash
grep -rn 'claims.mode' --include=*.go internal/books core/app core/kernel core/storage | grep -v _test
grep -rn -i 'shadow' --include=*.go internal/books | grep -v _test
```

Expected: **exactly one** non-test line in `internal/books/` (most likely `internal/books/module.go`) that sets
ABO's default for `core.storage.claims.mode` to shadow, and core's own default in `core/storage/` (or
`core/kernel/`) equal to `strict` (05-storage §5S.7). Record both `file:line` in the PR.

- If ABO's default is set in more than one place, or in `core/storage/claims.go` as a **core-wide** default of
  `shadow`, STOP and report: changing a core-wide default is a design change, not this task (Planner notes).
- If core's own default is not `strict`, STOP and report.

## Exact files

Modify:

- The one ABO default site found above (expected `internal/books/module.go`): `shadow` → `strict`. If PC-07-05's
  mechanism has a single "first release on core" switch instead of a value, remove the switch so ABO uses core's
  default (`strict`); either way exactly one line or block changes.
- `ci/stage.txt`: `release: S2br1` → `release: S2br2`. Leave `shipped_stage: S2b` unchanged. Bump the file's
  header if it has one.
- `core/storage/claims.go`: **no change expected** (the row lists it; see Planner notes). Touch it only to fix a
  comment that says ABO's default is shadow.

Plus one `changelog.d/` fragment:

```markdown
### Changed
- Storage claims are now enforced (strict) by default. An operator can return to log-only mode without a
  rollback by setting `core.storage.claims.mode=shadow`.
```

## Code contract

One value: ABO's effective default of `core.storage.claims.mode` becomes `strict`; the key, its type
(`strict`/`shadow`), `ProtectOperator` and "not hot-reloadable" are unchanged (05-storage §5S.7).

## Steps

1. Run Step 0 and the re-verify block.
2. Make the default change and the marker change in one commit:
   `feat(storage): enforce books' claims by default (S2b r2)`, body with the two trailer lines below.
3. Run the tests below and the acceptance block.

## Tests (spec name; already exists from PC-07-05)

`TestS2bBooksWritesEveryFamily` must pass in strict mode **with no test-side override**, and must include a
scan's import-root statistics update and a settings save that changes a secret (09 S2b "Done when"; 05-storage
§5S.10). Confirm both cases are present:

```bash
grep -n 'func TestS2bBooksWritesEveryFamily' -r internal/books        # 1 hit
grep -n -i 'import.root\|importpath\|UpdateImportPath' $(grep -rl 'func TestS2bBooksWritesEveryFamily' internal/books)   # >= 1
grep -n -i 'secret\|openai_api_key\|SaveConfigToDatabase' $(grep -rl 'func TestS2bBooksWritesEveryFamily' internal/books)   # >= 1
```

If either case is missing, add it to that test in this PR (a scan that updates one import root's `LastScan` and
`BookCount`; a `PUT /api/v1/config` or `SaveConfigToDatabase` that changes `openai_api_key`), each asserting no
`ErrOutsideKeyspace` and no new persisted violation under `c/storage/claimviol/`.

Add one test (name coined here) `TestABOClaimsModeDefaultStrict`: boot ABO's app in a test with no operator
setting for the key and assert the effective `core.storage.claims.mode` is `strict`; with
`ABO_CORE_STORAGE_CLAIMS_MODE=shadow` (or the env spelling the config loader prints for the key; read it from
the failing message if it differs) it is `shadow`.

## Acceptance

```bash
grep -E '^(shipped_stage|release):' ci/stage.txt                      # shipped_stage: S2b / release: S2br2
go build ./... && go -C core build ./...                              # exit 0
go test -count=1 -run 'TestS2bBooksWritesEveryFamily|TestABOClaimsModeDefaultStrict|TestFactoryResetUnderClaims' ./internal/books/...   # ok
make compat-rehearsal                                                 # exit 0
make ci                                                               # exit 0
git log --format=%B origin/main..HEAD | grep -c '^Stage-Release: S2b 2$'   # equals the commit count
```

## Out of scope

Any other claim change (owner, mode per family, `Serialized` flags never change, 05-storage §5S.9.1); new
families; S3 work.

## Rollback

Dial first: `core.storage.claims.mode=shadow` in the unit's environment or config file, then restart. A binary
swap to the `S2br1` build also works (no data shape change).

## Repo rules

- Headers bumped on every changed file that has one; the fragment has none.
- Commit body ends with:

  ```
  Stage-Release: S2b 2
  Co-Authored-By: Claude <noreply@anthropic.com>
  ```
- `gh pr merge <n> --rebase`; never squash; never `--admin`. The merge waits for `check_soak.py` (needs the
  current `S2br1` row ≥ 14 days old).

## Planner notes

- **Row lists `core/storage/claims.go`,** which implies a core default change. 05-storage §5S.7 makes `strict` the
  core default and shadow a books-only first-release setting (D-S2: shadow is "for the books module's legacy
  ranges only"), so the edit belongs on the ABO side. The brief expects no change in `claims.go` and STOPs if the
  shadow default turns out to be core-wide.
