<!-- file: docs/agent-tasks/platform-core/PC-19-04-contract-c6-legacy-streams-gone.md -->
<!-- version: 1.0.0 -->
<!-- guid: a5eb61e5-380d-4a96-b7cf-406b3485a7f5 -->
<!-- last-edited: 2026-11-04 -->

# PC-19-04 — Contract C6 (Gone step): events: the three legacy streams answer 410

| Field | Value |
|---|---|
| ID | `PC-19-04` |
| Tier | T1 Sonnet/medium — three manifest entries change handler from a projection to the existing gone stub; the ledger rows decide the set |
| Wave | W19 — contracts C3–C9, the Gone step (no release ID of its own) |
| Depends on | `OWNER-26` (the W owner check for C6) |
| Priority / effort | P2 / S |
| Spec | 09 §9.5 C6; 09 §9.2.1 "Contract" step and W (a)–(c); 09 §9.6 row 4; 09 S6 "Endpoints"; 05-realtime §4.9; 05-http-api §4.6 |
| Digest | `docs/plans/platform-core/digests/S8-S10.md` §5 rule 19 and §8 (contract checkpoints) |
| `Stage-Release:` | `none` (plan §(b) decision 4: contract PRs touch only packages of flipped stages and 09 names no stage for them) |

## Gate (read before anything else)

- **Do not start without the owner's W readings.** `OWNER-26` (NOT AGENT WORK) hands the coordinator the counter readings of `audiobook_organizer_compat_legacy_use_total` for this contract's surfaces, each with its process start time, covering 14 consecutive days with no use, plus the dates for W (a) and the release-note check for W (b) (09 §9.2.1 W, §9.7 P8). If the coordinator has not given you that text to paste into the PR description, STOP and ask. You never take the readings yourself.
- **The date gate is a test.** `TestNoContractBeforeSunset` fails a build whose code removes or retires an entry before its ledger row's earliest removal date (09 §9.2.9). If it fails after your change, the window has not run: STOP, revert your working tree, report.
- **One contract at a time** (plan §(d) "W19 (contracts)"): the coordinator dispatches this PR only when no open PR touches one of its files. Files this task shares: `docs/compat-surfaces.md`, `internal/books/compat-surfaces.md`, `internal/books/module.go` (with PC-19-05, PC-19-06, PC-19-07).
- **No stored data changes.** A contract retires surfaces only; it never deletes, rewrites or re-levels stored data (09 §9.5).

## Goal

After window W for the legacy streams (and not before S9 has shipped): the three legacy stream paths
`GET /api/events`, `GET /api/v1/operations/events` and `GET /api/v3/ops/events` answer 410 instead of serving the hub's
projections. An `EventSource` stops on a non-200 answer (09 §9.5 C6). The projections themselves are deleted later, by
PC-20-02.

## Background (the rules, cited)

- 09 §9.5 C6: earliest R(S6) + W **and after S9**; owner check: no use on `sse`; action "legacy streams → 410 (EventSource
  stops on non-200) → delete projections".
- 05-realtime §4.9: the three URLs are projections, **not** `httpapi.Alias`es; books obtains each handler from
  `Registrar.Realtime().Projection(p)` during P6 and serves it from the **gin legacy manifest** at the legacy path
  (`Perm: rbac.AnyAuthenticated`, `OwnAuth: false`); use is counted on surface `sse`.
- 05-http-api §4.6 and §8.4: legacy stubs stay in gin with HEAD's `gone()` convention
  (`internal/server/gone.go`, `func gone(replacement string) gin.HandlerFunc`, line 28 at HEAD), registered on the same
  route group so auth runs first.
- After S9 the shell's realtime client uses the native stream and the Activity page moved to native REST and stream
  together (PC-18-03; 05-realtime §4.9 S9 row), so no first-party client reads the legacy streams.

## START HERE (worktree off fresh `origin/main`)

```bash
REPO=<your clone>                       # never write a real machine path into a commit or PR
TASK=pc-19-04-contract-c6
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-$TASK" -b "agent/$TASK" origin/main
cd "$REPO/../audiobook-organizer-$TASK"
git rebase origin/main
git worktree list                       # expected: this path is listed and is NOT the primary checkout
npm ci --prefix web                     # or `make web-deps` once PC-01-06 has merged
git rev-parse --short HEAD              # record it: the BASE commit every count below is measured at
```

Do not run `go work init` in the worktree (CLAUDE.md: it breaks `go build ./...`). Never edit on `main`.

## Files

| Action | Path | Why |
|---|---|---|
| modify | `internal/books/module.go` and the gin manifest file R-C6.2 finds (expected `internal/server/legacy_manifest.go`) | the three stream entries serve `gone("GET /api/core/v1/events")` |
| inventory lists, **no diff expected** | `core/realtime/` | projections stay until PC-20-02 deletes them |
| modify | `docs/compat-surfaces.md`, `internal/books/compat-surfaces.md` (generated) | three `sse` rows `aliased` → `gone` |
| create | `changelog.d/<date>_contract_c6.md` | fragment |

## Re-verify at your base commit

Paste this helper once; later steps use it.

```bash
# ledger_rows SECTION SURFACES  -- prints every ledger row of docs/compat-surfaces.md whose section heading
# matches SECTION (a regex) and whose surface cell is in SURFACES (comma list). Output: "<state> | <whole row>".
ledger_rows() {
python3 - "$1" "$2" <<'PY'
import re, sys
sec_re, surfaces = sys.argv[1], set(sys.argv[2].split(','))
cur, hdr = '', None
for ln in open('docs/compat-surfaces.md', encoding='utf-8'):
    if ln.startswith('#'):
        cur, hdr = ln.strip(), None
        continue
    if not ln.startswith('|'):
        hdr = None if not ln.strip() else hdr
        continue
    cells = [c.strip().strip('`') for c in ln.strip().strip('|').split('|')]
    if hdr is None:
        hdr = [c.lower() for c in cells]
        continue
    if all(re.fullmatch(r':?-+:?', c or '-') for c in cells):
        continue
    if 'surface' not in hdr or 'state' not in hdr:
        continue
    row = dict(zip(hdr, cells))
    if re.search(sec_re, cur) and row.get('surface') in surfaces:
        print(row.get('state'), '|', ' | '.join(cells))
PY
}
```

Common anchors (every contract):

```bash
# R1. The ledger source and its embedded copy (PC-07-06). At HEAD 9b1545c1a the source exists in its pre-ledger
#     v1.1.0 form and the copy is absent.
test -f docs/compat-surfaces.md && test -f internal/books/compat-surfaces.md && echo OK      # expected: OK
# R2. The compat package (PC-07-06). At HEAD core/ does not exist.
ls core/compat/*.go | wc -l                                                                 # expected: >= 1
# R3. The four ledger tests, each defined exactly once (09 §9.2.9, §9.10).
for t in TestCompatLedgerMatchesCode TestNoContractBeforeSunset TestCompatLedgerCopyFresh TestCompatLedgerHoldsOnlyRows; do
  echo "$t $(grep -rn "func $t(" --include='*_test.go' . | wc -l)"; done                    # expected: every line ends in " 1"
# R4. The ledger's state vocabulary (09 §9.2.9: aliased | gone | retired | removed).
grep -c -E 'aliased|retired' core/compat/*.go | awk -F: '{s+=$2} END {print s}'               # expected: >= 2
# R5. The regeneration target for the embedded copy.
grep -c '^compat-ledger:' Makefile                                                          # expected: 1
# R6. The ledger header row names the columns of 09 §9.2.9 (surface, legacy, canonical, flip PR, shipped release
#     and date, earliest removal date, gate, state, removal PR) plus the row ID. Read it once:
grep -n -m3 '^| ' docs/compat-surfaces.md
```
Any expected value that does not hold at your base commit: **STOP and report** (the predecessor task has not landed
or has drifted).

Anchors for this contract:

```bash
ledger_rows '\bS6\b' sse | awk '{print $1}' | sort | uniq -c                                       # expected: "3 aliased"
grep -rn 'Projection(' internal/books internal/server --include='*.go' | grep -v _test.go          # expected: 3 call sites (one per legacy path); record file:line
grep -rn '"/api/events"\|"/api/v1/operations/events"\|"/api/v3/ops/events"' internal/ --include='*.go' | grep -v _test.go   # the manifest entries; record file:line
# No first-party client still opens a legacy stream (S9 moved them; PC-18-03):
grep -rn "/api/events\|/api/v1/operations/events\|/api/v3/ops/events" web/src --include='*.ts' --include='*.tsx' | grep -v '\.test\.\|\.spec\.' | wc -l   # expected: 0 (else STOP)
grep -n '^func gone(replacement string) gin.HandlerFunc' internal/server/gone.go | wc -l          # expected: 1
grep -E '^shipped_stage: ' ci/stage.txt                                                           # expected: S9 or later (C6 runs after S9)
```

## Steps

1. Run re-verify. If the web grep is non-zero or `shipped_stage` is before `S9`, STOP.
2. At each of the three manifest entries, replace the projection handler with `gone("GET /api/core/v1/events")`. Keep the
   entry's path, method, group and `Perm` (`rbac.AnyAuthenticated`) unchanged, so an unauthenticated request still gets
   401 first. Remove the three `Registrar.Realtime().Projection(p)` calls in `internal/books/module.go` only if nothing
   else uses their results; the projections' definitions in `core/realtime/` stay.
3. Ledger: the three S6 `sse` rows `aliased` → `gone`. `make compat-ledger`.
4. Tests first (below), then "Acceptance". PR description: **Release notes** naming the three removed streams and the
   successor `GET /api/core/v1/events`; the owner's W readings.

## Code contract (from the spec; do not rename)

```go
// internal/server/gone.go (HEAD, unchanged)
func gone(replacement string) gin.HandlerFunc  // 410 {"error":"gone","message":"this endpoint has been removed; use <replacement> instead"}
```

## Tests to write first (TDD)

- Update the legacy-stream golden tests (PC-15-04's "legacy-stream goldens per event type") to expect, for each path,
  401 without a credential and 410 with the legacy gone body with one; no `text/event-stream` content type.
- `TestProjectionTopicsExistAtStage` keeps passing (projections still bind at P7 if still registered; if step 2 removed
  the `Projection` calls, the test's S6/S8 catalog cases are unchanged because they test core's catalog).

## Acceptance (commands and expected output)

```bash
# A1. Ledger tests: each ran and passed (a "no tests to run" line is a failure).
for t in TestCompatLedgerMatchesCode TestNoContractBeforeSunset TestCompatLedgerCopyFresh TestCompatLedgerHoldsOnlyRows; do
  go test ./... -run "^${t}\$" -count=1 -v 2>&1 | grep -c -- "--- PASS: ${t}" ; done      # expected: four lines, each >= 1
go test ./... -run '^TestCompat|^TestNoContractBeforeSunset$' -count=1 2>&1 | grep -c '^FAIL'  # expected: 0
# A2. The embedded copy is byte-equal to the source.
make compat-ledger && git diff --exit-code internal/books/compat-surfaces.md && echo FRESH      # expected: FRESH
# A3. Full gates.
make ci; echo "make ci exit=$?"                                                               # expected: make ci exit=0
make -C core ci; echo "core ci exit=$?"                                                       # expected: core ci exit=0 (only when core/ changed)
```
```bash
ledger_rows '\bS6\b' sse | grep -c '^gone'                                                     # expected: 3
grep -rn 'text/event-stream' internal/server internal/books --include='*.go' | grep -v _test.go | wc -l   # expected: 0 (only the core hub serves streams)
go test ./internal/server/... ./internal/books/... -count=1 2>&1 | grep -c '^FAIL'            # expected: 0
```

## Out of scope

- Deleting the `Gone` stubs or the alias code: that is PC-20-02, one production minor later (09 §9.5, 05-http-api §4.6).
- Any ledger row outside this contract's set, any row whose gate is `permanent`, and every other contract's rows.
- `ci/stage.txt`: a contract is not a stage release and changes no marker line.
- Stored data of any kind: rows, blob fields, sidecars, levels (09 §9.5; 09 §9.2.1 "Stored data has no window").
- Deleting the projections, their topics or their tests (PC-20-02).
- The native stream `GET /api/core/v1/events` and the realtime hub.

## Rollback

Before merge: close the PR and remove the worktree. After merge and before a release: a revert PR (an exact inverse
of the commit). After a production release: `make rollback` to the previous binary, which still serves every surface
this PR retired (binary swap; plan §(b) W19 row). No data step exists to undo.

## Repo rules

- **File headers:** every new or changed file gets or bumps its 4-line versioned header
  (`.standards/instructions/file-headers.md`); keep an existing `guid`. `changelog.d/` and `todo.d/` fragments get
  **no** header (CLAUDE.md).
- **Commits:** conventional commits. Every commit of this PR carries the trailer line `Stage-Release: none`
  (09 §9.2.1 "The trailer") and ends with `Co-Authored-By: Claude <noreply@anthropic.com>`. The commit-msg hook and
  `.gitmessage` from PC-01-07 carry the line; `check_soak.py` check (d) fails a PR whose commit lacks it.
- **PR and merge:** `gh pr create`; the required checks of 09 §9.2.1 (`extraction-gates`, `docker-image`,
  `vet-darwin`, `vet-integration`, `core-ci` when `core/` changes) green on an up-to-date branch; then
  `gh pr merge <n> --rebase`. Never squash, never `--admin` past a required check (14 Q-I). Push workflow-file changes
  with `git push`, never the MCP contents API (CLAUDE.md).
- **Bookkeeping in the same PR:** one `changelog.d/` fragment (`scriv create` or by hand, `changelog.d/README.md`);
  edit `TODO.md` if the PR closes a tracked item; check the executive-summary criteria
  (`docs/process/executive-summaries.md`).
- **Owner files:** never edit `ci/soak.txt`, `ci/soak_health_allow.txt` or `ci/prev_ref.txt`, and never run
  `scripts/soak_record.py` (09 §9.2.1 "Soaks"; NOT AGENT WORK).
- **Concurrency (CLAUDE.md):** this task adds no loop over a library-scale collection. If you find you need one, STOP and report.
- **Confidentiality:** no hostnames, IPs, usernames, emails, secrets or absolute machine paths in commits, PRs or
  test fixtures.

## Planner notes

- **Files:** the projections are mounted in books' **gin legacy manifest** (05-realtime §4.9; 09 S6 "Endpoints"), whose file
  (PC-04-01) is `internal/server/legacy_manifest.go`; the inventory row lists only `internal/books/module.go`. If the
  manifest entries live in `internal/server`, the plan's statement that PC-19-07 is W19's only `internal/server` task
  (plan §(b)) no longer holds. `core/realtime/` is listed but has no change in the Gone step.
