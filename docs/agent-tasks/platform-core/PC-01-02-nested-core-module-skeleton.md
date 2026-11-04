<!-- file: docs/agent-tasks/platform-core/PC-01-02-nested-core-module-skeleton.md -->
<!-- version: 1.0.0 -->
<!-- guid: 47dbde13-97ac-43d8-8550-e2e607dcda37 -->
<!-- last-edited: 2026-11-04 -->

# PC-01-02 — Nested module skeleton with the L0 leaves `buildid` and `storage/keys`, per-module `make ci`, module-skew and genproto guards, `core-ci` workflow

| Field | Value |
|---|---|
| ID | PC-01-02 |
| Tier | **T2 (Sonnet/high)**: module mechanics with known `ambiguous import` and tidy-coupling traps (03 §3.2); the two leaves are small but their signatures and encodings are normative |
| Wave | W01 (S0, release `S0`) |
| Depends on | PC-01-01 (toolchain pin `go1.27.3`), OWNER-01 (Q-H: the module path) |
| Priority / effort | P1 / M |
| Branch | `feat/pc-01-02-core-module` |
| Trailer on every commit | `Stage-Release: S0 1` |
| Digest | `docs/plans/platform-core/digests/S0.md` §3, §4.1, §4.2, §6.3 |
| Owning spec | `03-architecture.md` §3.2 items 1, 4, 6 and §3.3 rows `buildid`, `storage/keys`; `05-storage.md` §5S.3.3; `09-abo-on-core.md` S0 "Moves", "Module coupling", "CI classification" and the `make ci` table; `13-toolchain.md` §13.2.2 items 1–4; `12-repo-split.md` §12.3.11, SC7, SC10; `04-package-mapping.md` §8.1 row S0 |

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
grep -n 'go1.27.3' <(git -C "$REPO" show origin/main:Makefile) | head -1   # PC-01-01 merged; else STOP
grep -n 'Q-H' <(git -C "$REPO" show origin/main:docs/plans/platform-core/stage-ledger/S0.md)   # the recorded module path; else STOP
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-01-02" -b feat/pc-01-02-core-module origin/main
cd "$REPO/../audiobook-organizer-pc-01-02"
npm ci --prefix web
```

The module path below is `github.com/falkcorp/mediacore` (14 Q-H's recommendation). If OWNER-01 recorded a different
path, use that one everywhere this brief says `github.com/falkcorp/mediacore` and say so in the PR.

**Never run `go work init`, `go work use` or create a `go.work` in the checkout or the worktree** (CLAUDE.md). The
only workspaces are `check_genproto_exclude.py`'s throwaway ones in `mktemp -d` outside the repository (03 §3.2 item 1).

## Exact files

Create (inventory row): `core/go.mod`, `core/go.sum` (empty, committed), `core/Makefile`, `core/buildid/buildid.go`,
`core/buildid/buildid_test.go`, `core/storage/keys/keys.go`, `core/storage/keys/keys_test.go`,
`core/scripts/ci/check_core_toolchain.py`, `core/.errcheck-baseline`, `core/.interface-width-baseline`,
`scripts/ci/check_module_skew.py`, `scripts/tests/test_check_module_skew.py`, `.github/workflows/core-ci.yml`.

Create (needed by the row's own done-checks, absent from the inventory; see Planner notes):
`core/scripts/ci/check_genproto_exclude.py`, `core/scripts/ci/test_check_genproto_exclude.py`,
`core/scripts/ci/check-errcheck-ratchet.sh`, `core/scripts/ci/check-interface-width.sh`,
`core/tools/staticcheck/go.mod`, `core/tools/staticcheck/go.sum`, `internal/arch/core_module_test.go`.

Modify: `go.mod`, `Makefile`, `scripts/check_toolchain_versions.py`, `scripts/tests/test_check_toolchain_versions.py`,
`.github/dependabot.yml`, `scripts/ci/short_test_shards.py`, `scripts/ci/short_test_weights.json`; one
`changelog.d/` fragment.

## Re-verify (expected at `ac4368bdb`, origin/main when this brief was written, plus PC-01-01)

```bash
test -e core && echo EXISTS || echo absent                       # absent
grep -c 'mediacore' go.mod                                       # 0
grep -n '^ignore ./web$' go.mod                                  # 166:ignore ./web
grep -n '^exclude' go.mod | wc -l                                # 0 (and it must stay 0: the exclude lives only in core/go.mod)
grep -n '^ci:' Makefile                                          # 789:ci: mocks-check staticcheck oplint sdkguard ops-check types-check bench-check pprof-check fmt-check test-all-short coverage-check-short lint-errcheck-ratchet lint-deadcode-ratchet
grep -n '^export GOTOOLCHAIN := go1.27.3$' Makefile              # 43:...
grep -n '^go.work$\|^go.work.sum$' .gitignore                    # 36, 37
grep -n 'package-ecosystem: gomod' -A1 .github/dependabot.yml   # "directory: /"
grep -c 'cockroachdb/errors v1.14.0' go.mod                      # 1 (the stale requirer of monolithic genproto)
```

Any difference: STOP and report.

## Normative content

### `core/go.mod` (03 §3.2)

```
module github.com/falkcorp/mediacore

go 1.27.0

exclude google.golang.org/genproto v0.0.0-20230410155749-daa745c078e1

ignore ./web

ignore ./examples/hello/web
```

No `toolchain` line, no `require`, no `replace` (SC7, SC10). `core/go.sum` is committed **empty** (`go mod tidy`
writes none for a module with no requirements and keeps an existing empty one; `check_module_skew.py` asserts it
exists, so `COPY core/go.mod core/go.sum` in the Dockerfiles never fails).

### Root `go.mod`

Add `require github.com/falkcorp/mediacore v0.0.0-00010101000000-000000000000` and
`replace github.com/falkcorp/mediacore => ./core`. **No `exclude` in the root** (03 §3.2 item 1). Go drops an unused
`require` on `go mod tidy`, so this PR also adds the root test `internal/arch/core_module_test.go` that imports both
leaves (it keeps the requirement, proves the `replace` resolves and is the first ABO importer of core).

### `core/buildid` (03 §3.3; 03 §3.6 "Build identity"), L0, stdlib only

```go
package buildid
// Binary returns version + "+" + the lowercase hex SHA-256 of the running executable
// (os.Executable, symlinks resolved). No ldflags, no .git. It is never the UI build ID.
func Binary(version string) (string, error)
// Commit returns debug.ReadBuildInfo's vcs.revision, "" when absent.
func Commit() string
```

Tests (`buildid_test.go`, names coined here): `TestBinary_HashesTheRunningExecutable` (the suffix equals the SHA-256 of
`os.Executable()`'s file read by the test; the prefix is the given version and `+`), `TestBinary_StableAcrossCalls`,
`TestCommit_NoPanicWithoutBuildInfo`. (`--version-json`'s `+dirty` suffix is PC-02-02's, not this leaf's.)

### `core/storage/keys` (05-storage §5S.3.3, exactly these signatures), L0, stdlib only

```go
package keys
type Seg []byte
func Str(s string) Seg
func Bytes(b []byte) Seg
func U64(n uint64) Seg       // 8-byte big-endian
func Time(t time.Time) Seg   // 8-byte big-endian UnixNano, sign bit flipped
func Join(segs ...Seg) []byte // each segment: 0x00 -> 0x00 0xFF; terminator 0x00 0x01
func Prefix(segs ...Seg) []byte
func Split(key []byte) ([]Seg, error)
func PrefixEnd(prefix []byte) []byte // smallest key greater than every key with prefix; nil if none
func ValidName(s string) error       // [a-z0-9_]{1,32}
```

Tests: **`TestJoinOrderMatchesSegmentOrder`** (the spec's name: a property test with `testing/quick` or a seeded
`math/rand/v2` loop over random segment lists, any byte including 0x00 and 0xFF, asserting
`bytes.Compare(Join(a...), Join(b...))` equals the lexicographic comparison of the lists and that every extension of a
list lies in `[Prefix(list), PrefixEnd(Prefix(list)))`); plus coined cases `TestSplitInvertsJoin`,
`TestPrefixEndAllFF` (nil for all-0xFF input), `TestU64AndTimeSortNumerically` (negative times included),
`TestValidName`, and the notation example: `append([]byte("c/storage/"), Join(Str("served"), Str("books"), U64(0))...)`
round-trips through `Split`.

### `core/Makefile` (12 §12.3.11)

- `export GOTOOLCHAIN := go1.27.3` (equal to the root pin; the 35th machine-checked copy, 13 §13.2.2 item 2) and a
  `print-toolchain` target printing exactly `go1.27.3`. Every later command uses `$(make -s -C core print-toolchain)`.
- `ci` target with 12 §12.3.11's prerequisites, in that order: `build vet vet-darwin vet-integration test-short
  fmt-check fix-check tidy-check staticcheck archtest exclude-check coverage-floor replace-check toolchain-check
  version-check errcheck-ratchet interface-width mocks-check pprof-check web-test embed-check`.
- At S0 these run for real: `build`, `vet`, `vet-darwin` (`GOOS=darwin CGO_ENABLED=0 go vet ./...`, no
  `embed_frontend`), `vet-integration` (`go vet -tags integration ./...`), `test-short` (`go test -race -short ./...`),
  `fmt-check` (`gofmt -l` excluding `web/` and `examples/*/web/`), `fix-check` (`go fix -diff ./...` prints nothing),
  `tidy-check` (`go mod tidy -diff`), `staticcheck` (built from `core/tools/staticcheck`, a copy of the root
  `tools/staticcheck` module at the same version), `exclude-check`, `replace-check` (SC7's core command),
  `toolchain-check` (`core/scripts/ci/check_core_toolchain.py`), `errcheck-ratchet` and `interface-width` (core copies
  of the root scripts, baselines `core/.errcheck-baseline` and `core/.interface-width-baseline`, each holding `0`).
- These print `<target>: not applicable at S0 (<reason>)` and exit 0, each naming the row that makes it real:
  `archtest` (PC-01-03 adds the engine and its tests), `coverage-floor` (S2b), `version-check` (S1, PC-03-03),
  `mocks-check` (the first core interface with a mock), `pprof-check` (S1), `web-test` (UI-0), `embed-check`
  (S9, PC-18-01). A target never fakes a pass for a subject that exists.
- `exclude-check` (core mode): runs `core/scripts/ci/check_genproto_exclude.py --mode core`; at S0 it reports
  "not applicable" for the core probe (core's graph does not reach `cockroachdb/errors` until S2b) **and** still runs
  the present/misplaced scan of `core/` (the exclude line is present exactly).
- Every script the targets call lives under `core/scripts/ci/`; nothing reads outside `core/` (12 SC4(a)).

### `core/scripts/ci/check_genproto_exclude.py` (03 §3.2 item 1; both modes)

Works only on a temp copy of the checkout made with `mktemp -d` outside the repository and every worktree (copy the
tracked tree, e.g. `git archive HEAD | tar -x -C "$tmp"` plus the working-tree diff, or `git ls-files -z` + copy);
never in the checkout. Fails unless all hold:

- **present**: `core/go.mod` carries exactly the exclude line; no other `go.mod` in the tree carries a genproto
  exclude (else *missing* / *misplaced*);
- **green with it**: root mode `go work init . ./core && go build ./... ./core/...` in the copy; core mode
  `(cd core && go work init . && go build ./...)` when applicable;
- **load-bearing without it**: after `go mod edit -dropexclude=google.golang.org/genproto@v0.0.0-20230410155749-daa745c078e1`
  in the copy's `core/go.mod`, at least one applicable probe's `go mod graph` lists an edge
  `<requirer> google.golang.org/genproto@v0.0.0-20230410155749-daa745c078e1` (print the requirer; today
  `github.com/cockroachdb/errors@v1.14.0`) **and** that probe's `go build` fails with `ambiguous import`. If none does,
  the exclude is **stale**: fail naming the requirer that disappeared. A non-applicable probe is reported, not failed.

`--mode core` (`make -C core exclude-check`): core probe plus the scan of `core/`. `--mode root` (root target
`genproto-exclude-root`): root probe plus the tree-wide "no other `go.mod`" scan. At S0 the root probe is applicable
and load-bearing. Unit-test the parsing and verdict logic in `core/scripts/ci/test_check_genproto_exclude.py` with
canned `go mod graph`/build outputs (stdlib `unittest`); the real probe runs in the make targets.

### `scripts/ci/check_module_skew.py` (03 §3.2 item 6; 12 SC4(c)), blocking, root `make ci`

Fails when: any module path in `(cd core && go list -m all)` resolves to a different version in the root's
`go list -m all`; `core/go.sum` does not exist; a `--pin module=version` does not hold. A pin holds when the module is
absent from a build list or resolves to exactly the pinned version there (rule fixed here; a pinned module that is not
yet required cannot drift). Root `make ci` calls it with the two pins of 13 §13.5.2:
`--pin github.com/google/jsonschema-go=v0.4.3 --pin go.opentelemetry.io/otel/exporters/prometheus=v0.69.0`. It also
compares the requirements of `tools/staticcheck/go.mod` and `core/tools/staticcheck/go.mod` (12 §12.3.11 "Tools").
Tests `scripts/tests/test_check_module_skew.py` (stdlib `unittest`, canned `go list -m all` outputs): no skew passes;
one skewed module fails naming it; missing `core/go.sum` fails; a pin lifted transitively fails; an absent pinned
module passes; differing staticcheck tool requirements fail.

### Root `Makefile`

- `GO_MODULE_DIRS := . core`; `vet`, `test-short`, `staticcheck` and `fmt-check` run per module (`for d in
  $(GO_MODULE_DIRS); do (cd $$d && …) || exit 1; done`); `fmt-check` excludes `web/`, `core/web/`,
  `core/examples/*/web/`. The root runs no `go fix` gate (09 S0 table).
- New targets, each added to `ci:`: `tidy-check` (`go mod tidy -diff` in each module dir), `genproto-exclude-root`
  (`python3 core/scripts/ci/check_genproto_exclude.py --mode root`), `module-skew` (the command above).
- Do not add `vet-darwin`/`vet-integration` to `ci:` here (PC-01-04 does), nor `check_embed_context.py` (PC-01-05).

### `scripts/check_toolchain_versions.py` (13 §13.2.2 items 1–4) and its tests

1. Tier 3 also reads `core/go.mod`, `core/tools/*/go.mod` and the existing `tools/*/go.mod` (same `go` directive as
   the root, no `toolchain` line).
2. Tier 1 reads `export GOTOOLCHAIN :=` in `core/Makefile` and requires it to equal the root `Makefile` pin.
3. Workflow `go-version` lines of `core-ci.yml` are covered by the existing workflow scan (check that they are).
4. New check: no `go1.N.N` literal in `scripts/**`, `core/scripts/**` or a workflow `run:` step, except an allowlist by
   path and line holding 13 §13.2.2's "Not a pin" rows under those paths (`scripts/check-errcheck-ratchet.sh:121`,
   `scripts/check-interface-width.sh:128`, `scripts/setup-mockery.sh:11`, `scripts/check_toolchain_versions.py:39`,
   `scripts/tests/test_check_toolchain_versions.py:31`) and the same comments in the two core script copies.
   Add a test per new rule (pass and fail fixture) in `scripts/tests/test_check_toolchain_versions.py`.
`core/scripts/ci/check_core_toolchain.py` checks only the copies core carries (`core/Makefile`, `core/go.mod`,
`core/tools/*/go.mod`, `core-ci.yml` is outside `core/` and is not its business; 12 §12.3.11).

### `.github/dependabot.yml`

The gomod entry: replace `directory: /` with `directories: ["/", "/core"]`, keeping
`multi-ecosystem-group: dependencies` (03 §3.2 item 6), and add `ignore` entries (all update types) for the two pinned
modules (13 §13.5.2 "How pinned works"). Do not add a `groups` key.

### `.github/workflows/core-ci.yml`

A required check named `core-ci`: a `changes` job that sets `core=true` when the diff touches `core/**`, `go.mod`,
`go.sum` or `.github/workflows/core-ci.yml` (fail open on a diff error, as `ci.yml`'s classifier does), and a job
`core-ci` with job-level `if:` on it, so a skip reports success. Steps: checkout, `actions/setup-go` with
`go-version: '1.27.3'` (a guarded literal), `make -C core ci`, and, when `core/go.mod` or `core/go.sum` changed, the
root `go build ./...` (09 S0 "Module coupling"). Pin every action to the SHAs `ci.yml` uses
(`actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1`,
`actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0`); `timeout-minutes` ≤ 30; `permissions:
contents: read`. Push workflow files with `git push`, never through the MCP contents API.

### Short-test shards

`scripts/ci/short_test_shards.py` and `short_test_weights.json`: one core shard (09 S0 table row
`go-test-short-shards`: "root module, plus one core shard"); `full_check` must still pass (stale entries fail).

## Steps

1. Re-verify. 2. Write the failing tests first: `keys_test.go`, `buildid_test.go`, `test_check_module_skew.py`, the
new cases in `test_check_toolchain_versions.py`, `test_check_genproto_exclude.py`. 3. Create `core/` (module, leaves,
`core/tools/staticcheck`, scripts, baselines, Makefile). 4. Root `go.mod` require + replace, `internal/arch/core_module_test.go`,
then `go mod tidy` at the root and in `core/` (`core/go.sum` stays empty). 5. Root Makefile. 6. Guard script.
7. Dependabot. 8. `core-ci.yml`. 9. Shards. 10. Changelog fragment (`### Added`: the nested module
`github.com/falkcorp/mediacore` at `core/` with two leaf packages; no runtime change). 11. Headers on every new and
changed file (`core/go.sum` is empty and carries none; `go.mod` files keep `//` header comments as `tools/*/go.mod`
do; `core/.errcheck-baseline` and `core/.interface-width-baseline` hold only the integer, as the root baselines do).

## Acceptance

```bash
cat core/go.sum | wc -c                                     # 0
go build ./... && go -C core build ./...                    # exit 0
go test -race -count=1 ./internal/arch/ -run TestCoreModule # ok
go -C core test -race -count=1 ./...                        # ok .../buildid, ok .../storage/keys
go -C core test -run TestJoinOrderMatchesSegmentOrder -count=1 ./storage/keys/   # ok
make -C core print-toolchain                                # go1.27.3
make -C core exclude-check                                  # exit 0; prints the core probe as not applicable and "present"
make genproto-exclude-root                                  # exit 0; prints requirer github.com/cockroachdb/errors@v1.14.0, load-bearing
python3 scripts/ci/check_module_skew.py --pin github.com/google/jsonschema-go=v0.4.3 --pin go.opentelemetry.io/otel/exporters/prometheus=v0.69.0   # exit 0
make tidy-check                                             # exit 0, no diff in either module
python3 scripts/check_toolchain_versions.py | tail -1       # Toolchain versions consistent
python3 -m unittest discover -s scripts -p 'test_check_*.py' 2>&1 | tail -1      # OK
python3 -m unittest discover -s core/scripts/ci -p 'test_*.py' 2>&1 | tail -1    # OK
make -C core ci                                             # exit 0
test ! -e go.work && test ! -e core/go.work && echo no-workspace   # no-workspace
make ci                                                     # last line: ✅ All CI checks passed!
```

## Out of scope

`archtest` and the layering redesign (PC-01-03); `vet-darwin`/`vet-integration` in root `make ci`, `extraction-gates`,
the classifier (PC-01-04); Docker `COPY core/go.mod core/go.sum` (PC-01-05); any ABO runtime import of core (W02);
the todo.d follow-up that proposes a genproto exclude in the **root** `go.mod` (it contradicts 03 §3.2 item 1; do not
do it).

## Rollback

Revert the PR. No runtime behaviour changes (nothing outside tests imports core yet).

## Repo rules

- Headers on every new/changed file; fragments headerless. Conventional commit `feat(core): nested mediacore module
  with buildid and storage/keys leaves`, ending `Stage-Release: S0 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`.
- 09 S0 "Module coupling": a `core/go.mod` change carries the root `go mod tidy` result in the same PR.
- `gh pr create`; wait for every required check; `gh pr merge <n> --rebase`; never squash, never `--admin`.

## Planner notes

- Files the row's own done-checks need but the inventory lists for no row: `core/scripts/ci/check_genproto_exclude.py`
  (the script behind both `exclude-check` and `genproto-exclude-root`, 03 §3.2 item 1) and its test;
  `core/tools/staticcheck/{go.mod,go.sum}` (core's `staticcheck` target, 12 §12.3.11 "Tools"); the core copies of
  `check-errcheck-ratchet.sh` and `check-interface-width.sh` (the baselines the row creates are useless without them,
  12 §12.3.11, MOD5-15).
- `internal/arch/core_module_test.go` is added because `go mod tidy` removes a `require` nothing imports, which would
  make the row's own `tidy-check` red; it is the first ABO importer of core. PC-01-03 also edits `internal/arch`; the
  serial W01 order covers it.
- The `--pin` behaviour for a module absent from a build list (jsonschema-go is not required anywhere at S0) is not
  stated by the spec; this brief fixes it as "passes". The owner may prefer adding the pin only from the stage that
  requires the module.
