<!-- file: docs/agent-tasks/platform-core/PC-03-02-merge-library-leaves.md -->
<!-- version: 1.0.0 -->
<!-- guid: f549bb82-454a-4ac2-9fe0-a87cc6949a87 -->
<!-- last-edited: 2026-11-04 -->

# PC-03-02 — Merge the library leaves into `core/library`: `safepath`, `pathvalidation`, `libroot`, `boundedcall`, `diskstats` and `util` `path.go`/`perms.go`

| Field | Value |
|---|---|
| ID | PC-03-02 |
| Tier | **T1 Sonnet/medium.** Six sources become one package, so qualifiers change in the importers; the commands below were dry-run on a copy of the planning checkout and build cleanly. Still mechanical. |
| Wave | W03 (S1, release `S1`) |
| Depends on | PC-03-01 |
| Blocks | PC-03-05 |
| Trailer | `Stage-Release: S1 1` |
| Move window | Yes, announced one working day ahead (09 §9.2.10). Feature PRs touching the six source paths are held until this merges. |
| Digest | `docs/plans/platform-core/digests/S1.md` §3 (row `mediacore/library`, move rules), §5 rules 7–9 |
| Spec | 09 S1 row "Moves" ("`internal/security/{safepath,pathvalidation}`, `internal/libroot`, `internal/boundedcall`, `internal/diskstats`, `internal/util` `path.go`/`perms.go` → `mediacore/library`"); 04 §8.1 row S1 and §8.2 row `mediacore/library`; 05-library §5L.10 (later rows); 09 S0 row "CI classification" |

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-03-02" -b platform-core/pc-03-02-library-leaves origin/main
cd "$REPO/../audiobook-organizer-pc-03-02"
git rebase origin/main
git worktree list
make web-deps
export GOTOOLCHAIN=$(make -s -C core print-toolchain)
MOD=$(go -C core list -m); echo "$MOD"         # expected: github.com/falkcorp/mediacore
OLD=github.com/falkcorp/audiobook-organizer
```

Precondition: PC-03-01 merged (`ls core/lifecycle core/log` lists both). Otherwise STOP.

## Exact files

Create (by `git mv`): `core/library/` (the 13 files of the five packages, `path.go`, `perms.go`, `path_test.go`) and
`core/library/perms_test.go` (new). Modify: the 29 importer files (computed by `FILES` below),
`internal/util/util_coverage_test.go`, `internal/arch/layering_test.go`, and `tools/cmd/sdkguard/internal-deps.txt`
only if `make sdkguard` fails.

## Code contract

None new: a merge move. Every exported identifier keeps its name in `library` (`Join`, `Validate`, `SafePath`,
`SecureJoin`, `SecureJoinResolved`, `CleanAbsolutePath`, `IsDangerousRoot`, `SanitizeFilename`, the `Err*` values,
`ReportDirName`, `EnsureDirWritable`, `Mounted`, `OwnedByCurrentUser`, `ReportPathNotify`, `Call`, `ErrTimeout`,
`Stats`, `SafeJoin`, `WithinRoot`, `DirMode`, `FileMode`, `SecretFileMode`); importers change `<old pkg>.X` to
`library.X`.

## TDD tests

No new behaviour test; `TestCoverage_PermConstants` moves to `core/library/perms_test.go` unchanged. Every moved
test and every importer package's tests stay green, with `TestCoreImportsOnlyCore` and `TestCoreHoldsNoAppKeyNames`.
Run every command of this brief in one shell: `FILES` is set by the re-verify block and used by the steps.

## Re-verify (expected values at the planning checkout `51a2c2acd`; any difference: STOP and report)

```bash
ls internal/security/safepath internal/security/pathvalidation internal/libroot internal/boundedcall internal/diskstats | grep -c '\.go$'   # 13
ls core/library 2>&1 | grep -c 'No such file'                                                                         # 1
FILES=$( (grep -rlE --include=*.go "\"$OLD/internal/(security/safepath|security/pathvalidation|libroot|boundedcall|diskstats)\"" . ; grep -rlE --include=*.go "\butil\.(SafeJoin|WithinRoot|DirMode|FileMode|SecretFileMode)\b" .) | sort -u | grep -vE '^\./internal/(security/(safepath|pathvalidation)|libroot|boundedcall|diskstats)/')
echo "$FILES" | wc -l                                                                                                 # 29
go list -f '{{join .Imports "\n"}}' ./internal/security/safepath ./internal/security/pathvalidation ./internal/libroot ./internal/boundedcall ./internal/diskstats | grep falkcorp | sort -u
                                         # github.com/falkcorp/audiobook-organizer/internal/security/pathvalidation   (its external test)
                                         # github.com/falkcorp/audiobook-organizer/internal/util                      (libroot: DirMode, SafeJoin)
grep -nE '^func (SafeJoin|WithinRoot)|^const (DirMode|FileMode|SecretFileMode)' internal/util/path.go internal/util/perms.go | wc -l   # 5
grep -n '^func TestCoverage_PermConstants' internal/util/util_coverage_test.go                                       # 1 hit
grep -nE '"internal/(boundedcall|diskstats|libroot|security/pathvalidation|security/safepath)": ' internal/arch/layering_test.go | wc -l   # 5
grep -rnw library $FILES | grep -v '//' | grep -v '"' | wc -l                                                         # 0 (no identifier named library)
```

The 29 importer files span `internal/audiobooks`, `internal/backup`, `internal/covers`, `internal/deluge`,
`internal/fileops`, `internal/maintenance/jobs`, `internal/organizer`, `internal/plugins/maintenance`,
`internal/reconcile`, `internal/repairs` and `internal/server/...` (`internal/fileops` is missing from the
inventory row; see Planner notes). No exported or unexported top-level name collides when the files share one
package (checked: the only duplicates are the build-tag pairs `OwnedByCurrentUser` and `Stats`).

## Steps

Save the importer fixer (it rewrites imports and qualifiers, removes duplicate `library` imports, adds the import
where only `util` symbols were used, and drops an `internal/util` import that is no longer used):

```bash
cat > "${TMPDIR:-/tmp}/fix_library_imports.py" <<'EOF'
import re, sys
OLD = "github.com/falkcorp/audiobook-organizer"
MOD = sys.argv[1]
LIB = f'"{MOD}/library"'
PKGS = ["security/safepath", "security/pathvalidation", "libroot", "boundedcall", "diskstats"]
NAMES = ["safepath", "pathvalidation", "libroot", "boundedcall", "diskstats"]
UTIL_SYMS = ["SafeJoin", "WithinRoot", "DirMode", "FileMode", "SecretFileMode"]
UTIL_IMP = f'"{OLD}/internal/util"'
for p in sys.argv[2:]:
    s = open(p).read()
    for pkg in PKGS:
        s = s.replace(f'"{OLD}/internal/{pkg}"', LIB)
    s = re.sub(r"\b(" + "|".join(NAMES) + r")\.([A-Z])", r"library.\2", s)
    s = re.sub(r"\butil\.(" + "|".join(UTIL_SYMS) + r")\b", r"library.\1", s)
    out, seen = [], False
    for l in s.split("\n"):
        if l.strip() == LIB:
            if seen:
                continue
            seen = True
        out.append(l)
    s = "\n".join(out)
    if "library." in s and LIB not in s:
        s = s.replace("import (\n", "import (\n\t" + LIB + "\n", 1)
    if UTIL_IMP in s and not re.search(r"\butil\.", s):
        s = re.sub(r"\n\t" + re.escape(UTIL_IMP) + r"\n", "\n", s)
    open(p, "w").write(s)
EOF
```

Use the header helper of PC-03-01 (`bump_headers.py`; recreate it from that brief if it is gone).

**Commit 1 — the merge and the importers** (one commit: no intermediate state builds):

```bash
mkdir -p core/library
for f in internal/security/safepath/*.go internal/security/pathvalidation/*.go internal/libroot/*.go \
         internal/boundedcall/*.go internal/diskstats/*.go internal/util/path.go internal/util/perms.go internal/util/path_test.go; do
  git mv "$f" core/library/
done
cd core/library
sed -i -E 's/^package (safepath|pathvalidation|libroot|boundedcall|diskstats|util)$/package library/; s/^package pathvalidation_test$/package library_test/' *.go
sed -i -E 's#^// file: internal/(security/safepath|security/pathvalidation|libroot|boundedcall|diskstats|util)/#// file: core/library/#' *.go
sed -i "s#\"$OLD/internal/security/pathvalidation\"#\"$MOD/library\"#; /\"github.com\/falkcorp\/audiobook-organizer\/internal\/util\"/d" *.go
sed -i -E 's/\butil\.(SafeJoin|WithinRoot|DirMode|FileMode|SecretFileMode)\b/\1/g; s/\bpathvalidation\.([A-Z])/library.\1/g' *.go
cd ../..
python3 "${TMPDIR:-/tmp}/fix_library_imports.py" "$MOD" $FILES
# the perms test moves with perms.go: cut TestCoverage_PermConstants out of the util test file ...
sed -i '/^\/\/ --- Coverage for perms.go ---$/d; /^func TestCoverage_PermConstants/,/^}/d' internal/util/util_coverage_test.go
# ... and add it to core/library/perms_test.go (package library), the same function body, with a new 4-line header
gofmt -w core/library $FILES internal/util/util_coverage_test.go
python3 "${TMPDIR:-/tmp}/bump_headers.py" core/library/*.go $FILES internal/util/util_coverage_test.go
go build ./... && go -C core build ./...                  # exit 0
go vet ./internal/util/ ./internal/audiobooks/ ./internal/backup/ ./internal/covers/ ./internal/deluge/ ./internal/fileops/ \
       ./internal/maintenance/jobs/ ./internal/organizer/ ./internal/plugins/maintenance/ ./internal/reconcile/ ./internal/repairs/ ./internal/server/... \
  && go -C core vet ./library/                            # exit 0
git add -A
git commit -F- <<'EOF'
refactor(core): merge the path-safety leaves into core/library

github.com/falkcorp/audiobook-organizer/internal/security/safepath ==> github.com/falkcorp/mediacore/library
github.com/falkcorp/audiobook-organizer/internal/security/pathvalidation ==> github.com/falkcorp/mediacore/library
github.com/falkcorp/audiobook-organizer/internal/libroot ==> github.com/falkcorp/mediacore/library
github.com/falkcorp/audiobook-organizer/internal/boundedcall ==> github.com/falkcorp/mediacore/library
github.com/falkcorp/audiobook-organizer/internal/diskstats ==> github.com/falkcorp/mediacore/library
github.com/falkcorp/audiobook-organizer/internal/util (path.go, perms.go) ==> github.com/falkcorp/mediacore/library

Stage-Release: S1 1
Co-Authored-By: Claude <noreply@anthropic.com>
EOF
```

**Commit 2 — `go fix`, guards, registries:**

```bash
go -C core fix ./library/
sed -i -E '/^\t"internal\/(boundedcall|diskstats|libroot|security\/pathvalidation|security\/safepath)": *[0-9]*, \/\//d' internal/arch/layering_test.go
gofmt -w core/library internal/arch/layering_test.go
python3 "${TMPDIR:-/tmp}/bump_headers.py" internal/arch/layering_test.go
make sdkguard || go run ./tools/cmd/sdkguard/main.go -update    # regenerate only if the guard fails; bump its header by hand
go build ./... && go -C core build ./...
git add -A && git commit -m "chore(core): go fix and guard updates for core/library" -m "Stage-Release: S1 1" -m "Co-Authored-By: Claude <noreply@anthropic.com>"
```

If commit 2 changes any file outside `core/library/`, `internal/arch/layering_test.go` and
`tools/cmd/sdkguard/internal-deps.txt`: STOP and report. Add a `changelog.d/` fragment (no header) in a third commit
with the same two trailer lines. The PR description lists the six `old ==> new` lines of commit 1.

## Acceptance

```bash
ls internal/security/safepath internal/security/pathvalidation internal/libroot internal/boundedcall internal/diskstats 2>&1 | grep -c 'No such file'   # 5
ls internal/util/path.go internal/util/perms.go 2>&1 | grep -c 'No such file'          # 2
grep -rlE --include=*.go "\"$OLD/internal/(security/safepath|security/pathvalidation|libroot|boundedcall|diskstats)\"" . | wc -l   # 0
grep -rnE --include=*.go '\butil\.(SafeJoin|WithinRoot|DirMode|FileMode|SecretFileMode)\b' . | wc -l   # 0
go build ./... && (cd core && go build ./...) && echo built                                 # built
go test ./internal/util/ ./internal/arch/ -count=1                                         # ok (both)
go -C core test ./library/ -count=1                                                        # ok  github.com/falkcorp/mediacore/library
go test ./internal/arch/ -run TestCoreHoldsNoAppKeyNames -count=1 -v | grep -E '^(--- PASS|ok)'   # --- PASS and ok
make -C core ci; echo "exit=$?"                                                            # exit=0
make ci; echo "exit=$?"                                                                    # exit=0
```

Required checks: `extraction-gates` (runs `check_commits_build.py`: both commits build, every moved file is a
rename), `core-ci`, `docker-image`, `vet-darwin`, `vet-integration`. Merge with `gh pr merge <n> --rebase`.

## Out of scope

The `library.Root`/`Manager` API of 05-library (S7); `internal/pathutil`, `internal/fileops`, `internal/watcher`
(S7); renaming any moved identifier; adding concurrency to moved code.

## Rollback

Revert the PR. No behaviour or data change.

## Repo rules

- Worktree only; never `go work init`. Every moved or edited file's header is bumped; the new `perms_test.go` gets
  a fresh 4-line header; fragments get none.
- Every commit carries `Stage-Release: S1 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`; every commit builds.
- Concurrency (CLAUDE.md): no change to concurrency in moved code (09 S1 digest §5 rule 8).

## Planner notes

- The inventory row omits `internal/fileops/` (2 importer files of `safepath` at `51a2c2acd`), `internal/util/util_coverage_test.go`
  (its perms test must move with `perms.go`), `internal/arch/layering_test.go` (five `layerOf` entries) and, if the
  guard needs it, `tools/cmd/sdkguard/internal-deps.txt`.
- `libroot`'s `ReportDirName = ".reports"` is ABO's app-dir name (05-library §5L.10: "books keeps `.reports` as its
  app-dir name"). Moving the file verbatim puts it in core until S7. `TestCoreHoldsNoAppKeyNames` checks key names
  only, so it will not flag it; the reviewer should decide whether the S1 move keeps it or leaves `ReportPathNotify`
  in ABO.
- 05-library §5L.10 maps `internal/diskstats` to "an internal package" of core (`Root.Usage`), while 09 S1 and 04
  §8.2 map it to `mediacore/library` at S1; this brief follows 09 (it owns sequencing).
