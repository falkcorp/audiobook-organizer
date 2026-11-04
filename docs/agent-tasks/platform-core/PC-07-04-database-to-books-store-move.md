<!-- file: docs/agent-tasks/platform-core/PC-07-04-database-to-books-store-move.md -->
<!-- version: 1.0.0 -->
<!-- guid: d4dbf469-b9b9-4d37-afb3-a9160565cad8 -->
<!-- last-edited: 2026-11-04 -->

# PC-07-04 — Import-path move: internal/database becomes internal/books/store

**Full title:** Import-path move: `internal/database` becomes `internal/books/store`; `internal/compactprogress`
joins `core/storage`; `dbtest/newstore.go` joins `coretest`. (The last two parts are **not** done here; see
"Out of scope" and Planner notes.)

| Field | Value |
|---|---|
| ID | PC-07-04 |
| Tier | **T1 (Sonnet, medium).** A repo-wide mechanical rename driven by one script; every decision is made in this brief. |
| Wave | W07 (stage S2b, release `S2br1`) |
| Depends on | PC-07-03 |
| Trailer | `Stage-Release: S2b 1` on every commit |
| Kind | **Move PR: import-path only.** No behaviour change. Its description lists `old ==> new` paths. |
| Window | Runs inside an announced **move window** (09 §9.2.10): at least one working day's notice in `docs/plans/platform-core/stage-ledger/S2b.md` and the tracking issue, at most two working days, `internal/database` frozen for its duration. |

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add ../audiobook-organizer-pc-07-04 -b refactor/pc-07-04-books-store-move origin/main
cd "$REPO/../audiobook-organizer-pc-07-04"
git rebase origin/main
git worktree list
```

## Step 0 — gates (do all; STOP if any fails)

```bash
# 1. The move window is announced and open (expect a line naming internal/database and today's date range)
grep -rn -i 'move window' docs/plans/platform-core/stage-ledger/
# 2. Predecessors merged: the composition root exists, the target does not
ls internal/books/module.go                     # listed (PC-04-05)
ls internal/books/store 2>&1                    # "No such file or directory"
# 3. No open PR touches internal/database (expect no rows other than this branch)
gh pr list --state open --search 'internal/database in:title,body'
# 4. OWNER-06 recorded the mock-conversion decision (expect one line: "in-stage" or "deferred")
grep -rn -i 'mock conversion' docs/plans/platform-core/stage-ledger/   # OWNER-06 records it (its brief, step 11)
```

If item 4 says **in-stage**, STOP: converting 114 mock importers is not a move and needs its own brief. This
brief implements the **deferred** branch: the mocks move with the package and are regenerated.

## Re-verify (counts at the plan's HEAD; recount at your base and record both in the PR)

```bash
M=github.com/falkcorp/audiobook-organizer
grep -rh '^package ' internal/database --include=*.go | sort | uniq -c
#   HEAD: 1 aggtest, 670 database, 4 database_test, 5 dbtest, 1 dbtest_test, 3 mocks
find internal/database -type d                                   # HEAD: database, dbtest, aggtest, mocks
grep -rl --include=*.go "\"$M/internal/database\"" . | grep -v '^./internal/database/' | wc -l   # HEAD: 2500
grep -rl --include=*.go "\"$M/internal/database/mocks\"" . | wc -l     # HEAD: 114
grep -rl --include=*.go "\"$M/internal/database/dbtest\"" . | wc -l    # HEAD: 31
grep -rl --include=*.go "\"$M/internal/database/aggtest\"" . | wc -l   # HEAD: 9
grep -rhoE --include=*.go '^\s*[A-Za-z_][A-Za-z0-9_]* +"github.com/falkcorp/audiobook-organizer/internal/database(/[a-z]+)?"' . | sed 's/^\s*//' | sort | uniq -c
#   HEAD: 43 dbmocks ".../mocks", 18 databasemocks ".../mocks", 20 `import "...internal/database"` single-line forms
git grep -ohE 'internal/database[A-Za-z0-9_-]' -- ':!docs' | sort | uniq -c     # HEAD: 3 "internal/database_" (test frame names; handled)
ls core/storage/db.go                                            # listed (PC-07-01)
```

A count that moved since HEAD is expected (main moves); a zero where HEAD had hits is a STOP.

## Decisions (fixed here; do not re-decide)

| Old path | New path | Package clause | How importers refer to it |
|---|---|---|---|
| `internal/database` | `internal/books/store` | `package store` (`database_test` → `store_test`) | aliased import `database "github.com/falkcorp/audiobook-organizer/internal/books/store"`, so no call site changes |
| `internal/database/dbtest` | `internal/books/store/storetest` (04 row `internal/database/dbtest`) | `package storetest` (`dbtest_test` → `storetest_test`) | aliased import `dbtest ".../internal/books/store/storetest"` |
| `internal/database/aggtest` | `internal/books/store/aggtest` | unchanged | path only |
| `internal/database/mocks` | `internal/books/store/mocks` | unchanged | path only; existing aliases `dbmocks`, `databasemocks` kept |

Why the alias: 1 792 of the 2 500 importer files already declare an identifier named `store`, so switching
the qualifier to `store.` cannot be done mechanically. Dropping the aliases is a follow-up (Planner notes).

## Exact files

- Rename (git mv): the whole `internal/database/` tree as in the table.
- Modify (every hit of the old paths, by the script): every `.go` file in the repo that imports or names
  the old paths (HEAD: about 2 550 files, `bench`-tagged files included); and these non-Go files:
  `.mockery.yaml`, `tygo.yaml`, `ci/gates.txt`, `scripts/ci/short_test_weights.json`,
  `scripts/ci/fixture_weights.json`, `scripts/ci/go_test_timings.json`, `scripts/ci_remote.py`,
  `scripts/test_ci_remote.py`, `scripts/add_short_skip.py`, `scripts/ci/go_test_shards.py`,
  `scripts/ci/short_test_shards.py`, `scripts/setup-mockery.sh`, `scripts/deadcode-baseline.txt`,
  `scripts/test_deadcode_ratchet.py`, `scripts/mutation-tables/*.muts`, `.interface-width-baseline`,
  `.golangci.yml`, `.github/workflows/ci.yml`, `.github/workflows/frontend-ci.yml`,
  `.woodpecker/test-database.yaml`, `tools/cmd/sdkguard/internal-deps.txt`, `Makefile`, and the oplint
  analysistest fixture directory `tools/cmd/oplint/testdata/src/github.com/falkcorp/audiobook-organizer/internal/database/`
  (git mv to `.../internal/books/store/`).
- Do **not** edit history documents: `CHANGELOG.md`, `TODO.md`, `docs/**`, `changelog.d/**`, `todo.d/**`,
  `agents/**`, `.claude/**`, `skills/**`, `.github/prompts/**`, `*.md` at the root, `scripts/gen_pipeline_bot_tasks.py`.

## Code contract and tests

No exported identifier, signature or behaviour changes; only import paths, package clauses of the moved
packages and path strings. No new test: the existing suites, `check_commits_build.py`, `bench-check`,
`mocks-check`, `types-check`, `oplint` and `sdkguard` are the oracle.

## Steps

1. Run Step 0 and the re-verify block. Paste both outputs into the PR description.
2. Save this script **outside the repo** (for example `$TMPDIR/pc0704_move.py`) and run it from the worktree
   root with `python3 -I "$TMPDIR/pc0704_move.py"`:

   ```python
   #!/usr/bin/env python3
   """PC-07-04 mechanical move: internal/database -> internal/books/store. Run from the repo root."""
   import datetime, pathlib, re, subprocess
   MOD = "github.com/falkcorp/audiobook-organizer"
   TODAY = datetime.date.today().isoformat()
   # (old, new, alias to add when the import has none). Longest first.
   MAP = [("internal/database/dbtest", "internal/books/store/storetest", "dbtest"),
          ("internal/database/aggtest", "internal/books/store/aggtest", None),
          ("internal/database/mocks", "internal/books/store/mocks", None),
          ("internal/database", "internal/books/store", "database")]
   CONFIGS = [".mockery.yaml", "tygo.yaml", "ci/gates.txt", "scripts/ci/short_test_weights.json",
              "scripts/ci/fixture_weights.json", "scripts/ci/go_test_timings.json", "scripts/ci_remote.py",
              "scripts/test_ci_remote.py", "scripts/add_short_skip.py", "scripts/ci/go_test_shards.py",
              "scripts/ci/short_test_shards.py", "scripts/setup-mockery.sh", "scripts/deadcode-baseline.txt",
              "scripts/test_deadcode_ratchet.py", ".interface-width-baseline", ".golangci.yml",
              ".github/workflows/ci.yml", ".github/workflows/frontend-ci.yml",
              ".woodpecker/test-database.yaml", "tools/cmd/sdkguard/internal-deps.txt", "Makefile"]
   CONFIGS += [str(p) for p in pathlib.Path("scripts/mutation-tables").glob("*.muts")]
   OLDFIX = "tools/cmd/oplint/testdata/src/" + MOD + "/internal/database"
   NEWFIX = "tools/cmd/oplint/testdata/src/" + MOD + "/internal/books/store"

   def sh(*a): subprocess.run(a, check=True)

   def bump_header(text, prefix):
       lines = text.split("\n")
       for i in range(min(8, len(lines))):
           m = re.match(re.escape(prefix) + r" version: (\d+)\.(\d+)\.(\d+)\s*$", lines[i])
           if m:
               lines[i] = f"{prefix} version: {m[1]}.{m[2]}.{int(m[3]) + 1}"
           if re.match(re.escape(prefix) + r" last-edited: ", lines[i]):
               lines[i] = f"{prefix} last-edited: {TODAY}"
           for old, new, _ in MAP:
               if re.match(re.escape(prefix) + r" file: ", lines[i]):
                   lines[i] = lines[i].replace(old + "/", new + "/", 1)
       return "\n".join(lines)

   def plain(text):  # every remaining occurrence, longest mapping first
       for old, new, _ in MAP:
           text = re.sub(re.escape(old) + r"(?![A-Za-z0-9-])", new, text)
       return text

   IMP = re.compile(r'^(\s*(?:import\s+)?)(?:([A-Za-z_][A-Za-z0-9_]*|_|\.)\s+)?"' + re.escape(MOD) + r'/([a-z/]+)"(.*)$')

   def rewrite_imports(text):
       out, in_block = [], False
       for line in text.split("\n"):
           s = line.strip()
           if s.startswith("import ("):
               in_block = True
           elif in_block and s == ")":
               in_block = False
           if in_block or s.startswith("import "):
               m = IMP.match(line)
               if m:
                   lead, alias, path, rest = m.groups()
                   for old, new, add in MAP:
                       if path == old:
                           a = alias or add
                           line = f'{lead}{a + " " if a else ""}"{MOD}/{new}"{rest}'
                           break
           out.append(line)
       return "\n".join(out)

   # 1. moves
   sh("git", "mv", "internal/database/dbtest", "internal/database/storetest")
   sh("mkdir", "-p", "internal/books")
   sh("git", "mv", "internal/database", "internal/books/store")
   if pathlib.Path(OLDFIX).exists():
       pathlib.Path(NEWFIX).parent.mkdir(parents=True, exist_ok=True)
       sh("git", "mv", OLDFIX, NEWFIX)
   # 2. package clauses in the moved tree
   for p in pathlib.Path("internal/books/store").rglob("*.go"):
       t = p.read_text()
       t2 = re.sub(r"(?m)^package database$", "package store", t)
       t2 = re.sub(r"(?m)^package database_test$", "package store_test", t2)
       t2 = re.sub(r"(?m)^package dbtest$", "package storetest", t2)
       t2 = re.sub(r"(?m)^package dbtest_test$", "package storetest_test", t2)
       if t2 != t:
           p.write_text(t2)
   # 3. every Go file: imports, then remaining path strings and comments, then the header
   files = subprocess.run(["git", "ls-files", "*.go"], capture_output=True, text=True, check=True).stdout.split()
   for f in files:
       p = pathlib.Path(f)
       t = p.read_text()
       t2 = plain(rewrite_imports(t))
       if t2 != t or f.startswith("internal/books/store/"):
           p.write_text(bump_header(t2, "//"))
   # 4. non-Go configs (header bump only where a header exists)
   for f in CONFIGS:
       p = pathlib.Path(f)
       if not p.exists():
           continue
       t = p.read_text()
       t2 = plain(t)
       if t2 != t:
           p.write_text(bump_header(t2, "#") if not f.endswith(".json") else t2)
   ```
3. `gofmt -w` the changed Go files: `git diff --name-only -- '*.go' | xargs gofmt -l | xargs -r gofmt -w`.
4. Build every tag set: `go build ./... && go build -tags bench ./... && go build -tags integration ./... &&
   go vet ./...`. A failure here is a script miss: fix it by hand, add the fix to the PR description.
5. `ci/gates.txt` rows `store-width` and `store-references`: the script moved their paths to
   `./internal/books/store/`; keep both rows (the `Store` interface still exists after a rename). Do not retire
   them in this PR (Planner notes).
6. Commit 1: `refactor(store): move internal/database to internal/books/store` with the trailer lines.
7. `make mocks` then `make types-check` (`tygo`). If either changes generated files, commit them as commit 2
   (`chore(store): regenerate mocks and types after the move`). Each commit must build:
   `python3 scripts/ci/check_commits_build.py --base origin/main` (or run `go build ./...` at each commit).
8. Search for misses; every command below must print nothing:

   ```bash
   git grep -n 'internal/database' -- ':!docs' ':!CHANGELOG.md' ':!TODO.md' ':!changelog.d' ':!todo.d' ':!agents' ':!.claude' ':!skills' ':!.github/prompts' ':!*.md' ':!scripts/gen_pipeline_bot_tasks.py'
   grep -rn --include=*.go '^package database$' internal/books/store
   ls internal/database 2>/dev/null
   ```
9. Run the acceptance block, open the PR with the description template below, and announce the merge in
   the stage record so held feature branches rebase (09 §9.2.10 "Rebase onto a move").

PR description must contain:

```
Move PR (import-path only; 09 §9.2.10, 04 §2.2).
old ==> new
internal/database ==> internal/books/store
internal/database/dbtest ==> internal/books/store/storetest
internal/database/aggtest ==> internal/books/store/aggtest
internal/database/mocks ==> internal/books/store/mocks
Counts at base: <paste Step 0 and re-verify outputs>
```

## Acceptance

```bash
go build ./... && go build -tags bench ./... && go build -tags integration ./...   # exit 0
go -C core build ./...                                                            # exit 0
make bench-check                                                                  # exit 0
make mocks-check                                                                  # exit 0
make types-check                                                                  # exit 0
make oplint && make sdkguard                                                      # exit 0
go test -short -count=1 ./internal/books/store/... ./internal/arch/...            # ok
make ci                                                                           # exit 0
git log --format=%B origin/main..HEAD | grep -c '^Stage-Release: S2b 1$'           # equals the commit count
```

`check_commits_build.py` in `extraction-gates` must be green at every commit.

## Out of scope

- `internal/compactprogress` → `core/storage` and `internal/database/dbtest/newstore.go` → `coretest`: both
  need code changes (compactprogress reads `database.CompactionStats`; `newstore.go` needs the books store to
  open over core handles, which happens at the flip). See Planner notes.
- Any rename of the `database.` qualifier at call sites; any behaviour change; deleting `database.Store`.
- History documents (list above).

## Rollback

Revert both commits (`git revert`), which is the exact inverse; `check_soak.py` admits a pure revert of a
trailered commit.

## Repo rules

- Headers: the script bumps the patch version and `last-edited` of every Go file it changes and of every
  config with a header; JSON files have none.
- Conventional commits with `Stage-Release: S2b 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`;
  `gh pr merge <n> --rebase`; never squash; never `--admin`. Move PRs merge first inside their window.
- CLAUDE.md concurrency: no loop is added.

## Planner notes

- **Two parts of the row cannot be import-path-only at this point.** `internal/compactprogress` imports
  `database` for `CompactionStats` (04 row `internal/compactprogress`; `stats.go:22` at the evidence commit)
  and moves into core `storage` behind `storage.Admin` statistics, and `dbtest/newstore.go` becomes
  `coretest.NewStorage(t)` (04 row `internal/database/dbtest`), which only works once the books store opens
  over core handles (PC-07-05). Recommend moving both into PC-07-05 or a new row after it.
- **`ci/gates.txt` rows are not retired here.** 09 S2b and the digest retire `store-width` and
  `store-references` "in the PR that deletes `database.Store`"; a rename does not delete it. The row's
  done-check "retired with the reason recorded" belongs to whichever later PR deletes the interface (04 §4.6
  says the interim composite ends at S4).
- **Files outside the row's list** that the move must touch: `scripts/ci_remote.py`, `scripts/test_ci_remote.py`,
  `scripts/add_short_skip.py`, `scripts/ci/go_test_shards.py`, `scripts/ci/short_test_shards.py`,
  `scripts/setup-mockery.sh`, `scripts/deadcode-baseline.txt`, `scripts/test_deadcode_ratchet.py`,
  `scripts/mutation-tables/*.muts`, `.interface-width-baseline`, `.golangci.yml`, `.github/workflows/ci.yml`,
  `.github/workflows/frontend-ci.yml`, `.woodpecker/test-database.yaml`, `tools/cmd/**` (sdkguard, oplint and
  its testdata), `Makefile`.
- **Call-site count.** The row says "about 850 call sites" (09's direct engine call sites); the import
  rewrite touches about 2 500 importer files at HEAD.
- **Alias follow-up.** Importers keep the qualifier `database` through an import alias; replacing it with
  `store` collides with an existing `store` identifier in 1 792 files. A later cleanup can drop the alias
  file by file where no collision exists; it is surfaced here with its cost, not deferred silently.
