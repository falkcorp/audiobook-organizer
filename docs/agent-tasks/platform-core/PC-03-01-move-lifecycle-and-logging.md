<!-- file: docs/agent-tasks/platform-core/PC-03-01-move-lifecycle-and-logging.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8fea87db-1491-4ace-9db9-489be9361c25 -->
<!-- last-edited: 2026-11-04 -->

# PC-03-01 — Move `internal/lifecycle` to `core/lifecycle` and `internal/logging` to `core/log` (import-path-only)

| Field | Value |
|---|---|
| ID | PC-03-01 |
| Tier | **T0 Haiku/low.** An import-path rewrite of two leaf packages and their importers, done by the exact commands below; every commit must build. |
| Wave | W03 (S1, release `S1`) |
| Depends on | OWNER-02 (the `S0` soak row; `check_soak.py` refuses this PR's `release: S1` change and its `Stage-Release: S1 1` commits before it) |
| Blocks | PC-03-02, PC-03-03, PC-03-04 |
| Trailer | `Stage-Release: S1 1` |
| Move window | Yes. The coordinator announces it in the stage record and the tracking issue one working day ahead (09 §9.2.10; plan §(f)). Do not start before the window opens. |
| Digest | `docs/plans/platform-core/digests/S1.md` §3 (move rules), §5 rules 7–9 |
| Spec | 09 S1 row "Moves"; 04 §8.1 row S1 and §8.2 rows `mediacore/lifecycle` and `mediacore/log`; 09 S0 row "CI classification" (move-PR rules); 04 §2.2 (move = path change + `go fix`) |

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-03-01" -b platform-core/pc-03-01-move-lifecycle-logging origin/main
cd "$REPO/../audiobook-organizer-pc-03-01"
git rebase origin/main
git worktree list
make web-deps
export GOTOOLCHAIN=$(make -s -C core print-toolchain)   # the pinned toolchain (09 S1 "Done when")
MOD=$(go -C core list -m); echo "$MOD"                    # expected: github.com/falkcorp/mediacore
OLD=github.com/falkcorp/audiobook-organizer
```

If `$MOD` prints anything else, use the printed value everywhere below. Never run `go work init` (CLAUDE.md).

## Exact files

Create (by `git mv`): `core/lifecycle/` (2 files), `core/log/` (8 files). Modify: the 6 importer files of
`internal/lifecycle` and the 47 of `internal/logging` (in `internal/aiscan`, `internal/operations/registry`,
`internal/scheduler`, `internal/dedup`, `internal/httputil`, `internal/importer`, `internal/metabatch`,
`internal/metafetch`, `internal/plugins/dedup`, `internal/plugins/maintenance`, `internal/scanner`, `internal/server/...`),
`ci/stage.txt`, `internal/arch/layering_test.go`, `tools/cmd/sdkguard/internal-deps.txt`.

## Code contract

None new: an import-path-only move (04 §2.2). `old ==> new`:
`github.com/falkcorp/audiobook-organizer/internal/lifecycle ==> github.com/falkcorp/mediacore/lifecycle`,
`github.com/falkcorp/audiobook-organizer/internal/logging ==> github.com/falkcorp/mediacore/log` (package name `log`;
importers keep the local name `logging` through an alias).

## TDD tests

No new test. These must stay green: every moved test (`go -C core test ./lifecycle/ ./log/`), every importer
package's tests, `TestCoreImportsOnlyCore`, `TestCoreHoldsNoAppKeyNames`, `internal/arch`'s layering tests.

## Re-verify (run every line; compare with the expected value; any difference: STOP and report)

```bash
grep -n '^release: S0$' ci/stage.txt                                                        # 1 hit
ls internal/lifecycle                                                                       # shutdown.go  shutdown_test.go
ls internal/logging | wc -l                                                                  # 8
grep -rl --include=*.go "\"$OLD/internal/lifecycle\"" . | wc -l                              # 6
grep -rl --include=*.go "\"$OLD/internal/logging\"" . | wc -l                                # 47
grep -rh --include=*.go "$OLD/internal/logging\"" . | sort -u                                # exactly one line: 	"github.com/falkcorp/audiobook-organizer/internal/logging"
grep -h '^package' internal/logging/*.go | sort -u                                           # package logging
go list -f '{{join .Imports "\n"}}' ./internal/lifecycle ./internal/logging | grep -c falkcorp   # 0
ls core/lifecycle core/log 2>&1 | grep -c 'No such file'                                     # 2
grep -n '"internal/lifecycle"\|"internal/logging"' internal/arch/layering_test.go            # 3 hits (layerOf x2, namedLayer x1)
grep -n 'internal/lifecycle' tools/cmd/sdkguard/internal-deps.txt                            # 1 hit
```

Counts measured at the planning checkout `51a2c2acd`; the importers are 3 packages for `lifecycle`
(`internal/aiscan`, `internal/operations/registry`, `internal/scheduler`) and 13 for `logging` (04 §8.2). A
different count means feature work changed the importers: re-run the commands below anyway, but report the new
counts in the PR description.

## Steps (four commits, each must build)

Save this helper first (it bumps the header of every file you pass it):

```bash
cat > "${TMPDIR:-/tmp}/bump_headers.py" <<'EOF'
import re, sys, uuid
TODAY = "2026-11-04"   # replace with today's date
for p in sys.argv[1:]:
    lines = open(p).read().split("\n")
    head = lines[:6]
    def idx(prefix):
        return next((i for i, l in enumerate(head) if l.startswith(prefix)), None)
    v = idx("// version:")
    if v is not None:
        a, b, c = lines[v].split(":", 1)[1].strip().split(".")
        lines[v] = f"// version: {a}.{b}.{int(c)+1}"
        if idx("// guid:") is None:
            lines.insert(v + 1, f"// guid: {uuid.uuid4()}")
        le = idx("// last-edited:")
        if le is None:
            g = idx("// guid:")
            lines.insert(g + 1, f"// last-edited: {TODAY}")
        else:
            lines[le] = f"// last-edited: {TODAY}"
    open(p, "w").write("\n".join(lines))
EOF
```

**Commit 1 — `lifecycle`:**

```bash
git mv internal/lifecycle core/lifecycle
sed -i 's#^// file: internal/lifecycle/#// file: core/lifecycle/#' core/lifecycle/*.go
FILES=$(grep -rl --include=*.go "\"$OLD/internal/lifecycle\"" .)
sed -i "s#\"$OLD/internal/lifecycle\"#\"$MOD/lifecycle\"#" $FILES
gofmt -w $FILES
python3 "${TMPDIR:-/tmp}/bump_headers.py" core/lifecycle/*.go $FILES
go build ./... && go -C core build ./...          # exit 0
git add -A && git commit -F- <<'EOF'
refactor(core): move internal/lifecycle to core/lifecycle

github.com/falkcorp/audiobook-organizer/internal/lifecycle ==> github.com/falkcorp/mediacore/lifecycle

Stage-Release: S1 1
Co-Authored-By: Claude <noreply@anthropic.com>
EOF
```

**Commit 2 — `logging` → `log`** (the package name changes; importers keep the name `logging` through an import
alias, so no identifier in an importer changes):

```bash
git mv internal/logging core/log
sed -i 's#^// file: internal/logging/#// file: core/log/#; s/^package logging$/package log/' core/log/*.go
FILES=$(grep -rl --include=*.go "\"$OLD/internal/logging\"" .)
sed -i "s#^\t\"$OLD/internal/logging\"#\tlogging \"$MOD/log\"#" $FILES
gofmt -w $FILES
python3 "${TMPDIR:-/tmp}/bump_headers.py" core/log/*.go $FILES
go build ./... && go -C core build ./...          # exit 0
git add -A && git commit -F- <<'EOF'
refactor(core): move internal/logging to core/log

github.com/falkcorp/audiobook-organizer/internal/logging ==> github.com/falkcorp/mediacore/log

Stage-Release: S1 1
Co-Authored-By: Claude <noreply@anthropic.com>
EOF
```

**Commit 3 — guards, `go fix` and registries:**

```bash
go -C core fix ./lifecycle/ ./log/                # core's fix-check modernizers on the moved files (04 §2.2)
# internal/arch: delete the three map entries for the moved paths (the lines the re-verify grep printed)
sed -i '/^\t"internal\/lifecycle": *[0-9]*, \/\//d; /^\t"internal\/logging": *[0-9]*, \/\//d' internal/arch/layering_test.go
sed -i 's/"internal\/logging": 0, //' internal/arch/layering_test.go
grep -n '"internal/lifecycle"\|"internal/logging"' internal/arch/layering_test.go | wc -l   # 0
go run ./tools/cmd/sdkguard/main.go -update        # regenerates internal-deps.txt (lifecycle leaves internal/)
python3 "${TMPDIR:-/tmp}/bump_headers.py" internal/arch/layering_test.go
gofmt -w core/lifecycle core/log internal/arch/layering_test.go
gofmt -l core/lifecycle core/log internal/arch      # prints nothing
go build ./... && go -C core build ./...           # exit 0
git add -A && git commit -m "chore(core): go fix, layering and sdkguard updates for the S1 leaf moves" \
  -m "Stage-Release: S1 1" -m "Co-Authored-By: Claude <noreply@anthropic.com>"
```

If `tools/cmd/sdkguard/internal-deps.txt` changed, bump its `# version:` patch number and `# last-edited:` date by
hand before committing. If `git diff --stat HEAD~1` shows changes in any file other than files under `core/`,
`internal/arch/layering_test.go` and `tools/cmd/sdkguard/internal-deps.txt`: STOP and report.

**Commit 4 — the release marker** (the first PR of S1 sets `release: S1`; 09 S1 "Flip PR"):

```bash
sed -i 's/^release: S0$/release: S1/' ci/stage.txt
# bump ci/stage.txt's header version and last-edited if it has one
git add ci/stage.txt && git commit -m "chore(stage): release S1" -m "Stage-Release: S1 1" -m "Co-Authored-By: Claude <noreply@anthropic.com>"
```

Then add a `changelog.d/` fragment (no header) saying the two packages moved with no behaviour change, commit it with
the same two trailer lines, and push.

## Acceptance (each command and its expected output)

```bash
grep -rn --include=*.go "\"$OLD/internal/lifecycle\"\|\"$OLD/internal/logging\"" . | wc -l   # 0
ls internal/lifecycle internal/logging 2>&1 | grep -c 'No such file'                         # 2
go build ./... && (cd core && go build ./...) && echo built                                   # built
go vet ./... && go -C core vet ./...                                                          # exit 0
go test ./internal/arch/ -count=1                                                             # ok  github.com/falkcorp/audiobook-organizer/internal/arch
make -C core ci; echo "exit=$?"                                                               # exit=0 (TestCoreImportsOnlyCore included)
make sdkguard                                                                                 # ✅ SDK guard passed
python3 scripts/ci/check_commits_build.py --help                                              # prints its usage; then run it as the usage says against origin/main: exit 0
make ci; echo "exit=$?"                                                                       # exit=0
grep -n '^release: S1$' ci/stage.txt                                                          # 1 hit
```

PR: `gh pr create`; the description lists the two `old ==> new` lines exactly as in commits 1 and 2 and the
re-verify counts. Required checks: `extraction-gates` (it runs `check_commits_build.py` because files are renamed
into `core/`), `core-ci`, `compat-rehearsal` (required because this PR changes `release:`), `docker-image`,
`vet-darwin`, `vet-integration`. Merge with `gh pr merge <n> --rebase` (never squash: the four commits must land
as they are).

## Out of scope

Any change inside the moved code other than `go fix`; renaming identifiers; `internal/logger` (no file of it moves,
09 S1 "Moves"); `internal/telemetry` (PC-03-04).

## Rollback

Revert the PR (all four commits). No stored data or behaviour changes.

## Repo rules

- Worktree only; never commit on `main`; never `go work init`.
- Headers: every moved or edited file has its version bumped by the helper; `changelog.d/` fragments get no header.
- Every commit carries `Stage-Release: S1 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`; every commit builds.
- Concurrency (CLAUDE.md): do not add or change concurrency in moved code in a move PR (09 S1 digest §5 rule 8).

## Planner notes

- The inventory row omits `internal/arch/layering_test.go` (its `layerOf` and `namedLayer` maps name both moved
  paths) and `tools/cmd/sdkguard/internal-deps.txt` (it lists `internal/lifecycle`); both are edited in commit 3.
- 04 §8.2 lists `internal/server/handlers/dedup`, `internal/server/handlers/operations` and
  `internal/server/middleware` among `log`'s importers; the row's `internal/server/` entry covers them.
