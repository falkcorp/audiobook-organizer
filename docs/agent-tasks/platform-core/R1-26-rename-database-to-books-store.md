<!-- file: docs/agent-tasks/platform-core/R1-26-rename-database-to-books-store.md -->
<!-- version: 1.2.0 -->
<!-- guid: 320631e7-248c-44f9-9695-42e3a066fa6e -->
<!-- last-edited: 2026-11-08 -->

# R1-26 — Rename the internal/database residue to internal/books/store (+storetest, aggtest, mocks), import paths only

| Field | Value |
|---|---|
| ID | `R1-26` |
| Tier | **T2** (Sonnet / high): about 850 call sites, import paths only, every commit builds |
| Release / wave | R1 / W20 |
| Depends on | R1-23, R1-25 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 04 §2.4 (internal/books/store), §4, §1 headline 1; 09 §9.2.10 (move window, about 850 engine call sites) |
| Branch | `agent/platform-core-r1-26-rename-database-to-books-store` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the named required checks of 09 §9.2.1 that exist at your base. The R1 freeze is on from R1-35 until OWNER-03 cuts R1 (09 §9.2.1): nothing in this task changes that.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this list without recording it under the PR's "Scope notes"):

- `internal/`
- `cmd/`
- `main.go`
- `.mockery.yaml`
- `tygo.yaml`
- `tools/`
- `ci/gates.txt`
- `scripts/ci/`
- `web/src/types/generated/database.ts`
- `scripts/deadcode-ratchet.py`

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-26" -b agent/platform-core-r1-26-rename-database-to-books-store origin/main
cd "$REPO/.worktrees/platform-core-r1-26"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and `go -C core build ./...` (once `core/` exists). Line numbers below were read at the planning base `ac4368bdb`; the spec's anchors are at `748fd787a`. Where they differ the brief says so. Re-run every grep at your base before editing; a count that differs only because a listed predecessor merged is expected, and the brief says what to do with it.

## Goal

Rename what is left of `internal/database` after R1-23 (books' own store, "the residue") to its permanent home,
**import paths only**, every commit builds (04 §2.4 `internal/books/store`; 04 §4; 04 §1 headline 1; 09 §9.2.10:
"R1's leaf moves and its `internal/database` re-home, about 850 direct engine call sites", move window of at most two
working days, announced a day ahead):

| old ==> new | package clause |
|---|---|
| `internal/database` ==> `internal/books/store` | `store` (04 §4.6 writes `*store.PebbleStore`) |
| `internal/database/dbtest` ==> `internal/books/store/storetest` | `storetest` |
| `internal/database/aggtest` ==> `internal/books/store/aggtest` | `aggtest` |
| `internal/database/mocks` ==> `internal/books/store/mocks` | `mocks` (the optional mock conversion is not this task, 04 §4.6, 09 §9.4.1 last row) |

**Import-path-only rule (how):** every importer keeps its current qualifier by importing the new path under the old
name: `database "github.com/falkcorp/audiobook-organizer/internal/books/store"`, `dbtest ".../internal/books/store/storetest"`,
`aggtest`/`mocks` unchanged names. No call site's identifiers change, so files that have a local variable named
`store` (very common) are untouched. Inside the moved packages nothing changes except the package clause of
`internal/books/store` (`database` → `store`) and `storetest` (`dbtest` → `storetest`), and their own internal
imports of each other (aliased the same way).

## Dependency check

R1-23 and R1-25 merged (R1-24 touches no `internal/database` file). R1-23 did **not** move the engine files out:
its brief step 3 is deferred to `todo.d/2026-11-08-r1-23-engine-rehome-into-core.md`, which runs **after** this
task so its split of each file into an engine half and an ABO half lands on the final paths. So this rename carries
`pebble_gate.go`, `pebble_store_open.go`, `storage_format.go`, `census*.go`, `store_init.go` and
`migrations_runner.go` into `internal/books/store` with the rest of the residue, unchanged; the residue is final for
R1 only once that follow-up has moved the engine halves to core. Check:
`git grep -q 'func (h \*Handler) FactoryReset' -- internal/server/handlers/system || echo RESET_GONE` prints
`RESET_GONE`, and `test -d core/storage && echo ok`.

## Re-verify (counts at `ac4368bdb`; R1-23 kept the engine files here, so expect counts close to these)

```bash
M=github.com/falkcorp/audiobook-organizer
for p in internal/database internal/database/dbtest internal/database/aggtest internal/database/mocks; do echo "$p $(git grep -l "\"$M/$p\"" -- '*.go' | wc -l)"; done
#   expect at ac4368bdb: 2513, 31, 9, 114 files (record the counts at your base in the PR)
grep -n 'internal/database' .mockery.yaml tygo.yaml ci/gates.txt tools/cmd/sdkguard/main.go
#   expect .mockery.yaml:14,19; tygo.yaml:28,37; ci/gates.txt:27,28 (store-width, store-references); sdkguard main.go:79
grep -ln 'internal/database' scripts/ci/*.py scripts/ci/*.json scripts/*.py
#   expect at least: scripts/ci/short_test_weights.json, go_test_timings.json, fixture_weights.json, short_test_shards.py, go_test_shards.py
grep -n '"dbtest"' scripts/deadcode-ratchet.py      # expect TEST_HELPER_DIRS entry (line 68)
grep -c 'internal/database' internal/arch/layering_test.go   # the layerOf keys to rename
```

## Steps (move window; one rename commit that builds, then nothing else needed)

1. `git mv internal/database internal/books/store` (its sub-directories come along), then
   `git mv internal/books/store/dbtest internal/books/store/storetest`. Set the two package clauses.
2. Rewrite every import, preserving the qualifier (run from the repo root; `goimports` is not used):

   ```bash
   python3 - $(git grep -l '"github.com/falkcorp/audiobook-organizer/internal/database' -- '*.go') <<'PY'
   import re, sys
   M = 'github.com/falkcorp/audiobook-organizer/internal/'
   rules = [
       (r'^(\s*)(\w+ )?"' + M + r'database/dbtest"', lambda m: f'{m.group(1)}{m.group(2) or "dbtest "}"{M}books/store/storetest"'),
       (r'^(\s*)(\w+ )?"' + M + r'database/aggtest"', lambda m: f'{m.group(1)}{m.group(2) or ""}"{M}books/store/aggtest"'),
       (r'^(\s*)(\w+ )?"' + M + r'database/mocks"', lambda m: f'{m.group(1)}{m.group(2) or ""}"{M}books/store/mocks"'),
       (r'^(\s*)(\w+ )?"' + M + r'database"', lambda m: f'{m.group(1)}{m.group(2) or "database "}"{M}books/store"'),
   ]
   for f in sys.argv[1:]:
       s = open(f).read()
       for pat, rep in rules:
           s = re.sub(pat, rep, s, flags=re.M)
       open(f, 'w').write(s)
   PY
   gofmt -l $(git diff --name-only -- '*.go') ; gofmt -w $(git diff --name-only -- '*.go')
   ```

   A file inside `internal/books/store` itself that imported `internal/database` (none should; it is the same
   package) would now import itself: `go build` reports it; remove that import.
3. Non-Go references: `.mockery.yaml` (`dir:` and the package path; regenerate nothing: the mocks moved verbatim),
   `tygo.yaml` (`path:` and the "Source of truth" comment; then `make generate-types` and commit the regenerated
   `web/src/types/generated/database.ts` header, in the row), `ci/gates.txt`
   (`store-width`, `store-references` commands and baselines), `tools/cmd/sdkguard/main.go` (`allowedRoots`) and
   `go run ./tools/cmd/sdkguard/main.go -update`, `scripts/ci/*.json` weight and timing keys and the two shard
   scripts, `scripts/deadcode-ratchet.py` (`TEST_HELPER_DIRS`: `dbtest` → `storetest`), `internal/arch/layering_test.go`
   (rename the keys), `internal/books/store/TESTING.md` paths, and any `scripts/ci/` path. `bench-check` covers `cmd`'s
   `bench`-tagged files (09 §9.4 R1 classification): they are rewritten by step 2.
4. Header bumps: every moved file's `file:` line updated (a scripted pass over `git diff --name-status -M` renames);
   every edited file patch-bumped.
5. Fragment `changelog.d/<today>_books_store_rename.md` (`### Changed`: "Internal: the books store package moved to
   internal/books/store."). Commit: `refactor(books): rename internal/database to internal/books/store, import paths only`.
   The PR description lists the four `old ==> new` lines and the move-window issue link.

## Code contract

None: no identifier changes; only import paths and two package clauses.

## Acceptance

```bash
M=github.com/falkcorp/audiobook-organizer
git grep -n "\"$M/internal/database" -- '*.go' | wc -l      # expect 0
test ! -e internal/database && echo GONE                      # expect GONE
go build ./... && go build -tags bench ./... && go vet -tags integration ./...   # expect exit 0
make mocks-check types-check sdkguard bench-check             # expect each passes
python3 scripts/ci/short_test_shards.py --check               # expect exit 0
go test ./internal/books/store/ -run 'TestStoreFlattenedWidth|TestStoreReferenceCount' -count=1   # expect ok (store-width, store-references)
make ci && make -C core ci                                    # expect exit 0
```

## STOP conditions

- Any call-site identifier would have to change (the alias rule forbids it).
- A ratchet baseline changes (the rename must not re-baseline).

## Out of scope

The mock conversion (owner decision, 04 §4.6); narrowing `database.Store`; renaming qualifiers.

## Rollback

Revert the commit (a pure rename).

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. A moved file keeps its `guid` and gets its `file:` line set to the new path. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green, `gh pr merge <n> --rebase` (enqueues it; the merge queue re-tests it on top of `main`, D15). Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** A loop over a library-scale collection with per-item I/O, hashing or subprocess work uses a bounded worker pool (`errgroup` + `SetLimit`); never unbounded goroutines.
- **No `go.work`.** Never run `go work init` in a checkout or worktree; the genproto probe runs only in a throwaway copy under `mktemp -d` (03 §3.2 item 1).
- **Move window (09 §9.2.10).** This task moves or re-homes packages. Before you start, the lead
  announces a move window in the tracking issue at least one working day ahead, naming the 04 rows it
  freezes and its end (at most two working days). If no announcement exists, STOP and ask the lead. The PR
  description lists every `old ==> new` package path, one per line. Every commit of the PR builds:
  `go build ./... && go -C core build ./...` at each commit (`scripts/ci/check_commits_build.py` checks it
  in `extraction-gates`); keep the rename and the import rewrite in one commit, or make each commit build.
  Apply in the same PR the `go fix` modernizers core's `fix-check` requires in the moved files
  (`go -C core fix ./...`, 04 §2.2).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.

