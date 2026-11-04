<!-- file: docs/agent-tasks/platform-core/PC-19-03-contract-c5-tasks-gone.md -->
<!-- version: 1.0.0 -->
<!-- guid: d5a9cb80-5e0f-4462-afa2-27c687483bac -->
<!-- last-edited: 2026-11-04 -->

# PC-19-03 — Contract C5 (Gone step): tasks: the /api/v3/ops, op-defs and tasks aliases answer 410; task_kind input aliases refused

| Field | Value |
|---|---|
| ID | `PC-19-03` |
| Tier | T1 Sonnet/medium — alias registrations become `httpapi.Gone`; legacy kind IDs on API input stop resolving; stored-row resolution is explicitly kept |
| Wave | W19 — contracts C3–C9, the Gone step (no release ID of its own) |
| Depends on | `OWNER-25` (the W owner check for C5) |
| Priority / effort | P2 / S |
| Spec | 09 §9.5 C5; 09 §9.2.1 "Contract" step and W (a)–(c); 09 §9.6 rows 1 (tasks 3, op-defs 2), 3 (`/api/v3/ops/*` 12) and 28 (task kinds); 05-tasks §5T.3.10 "Compat"; 05-http-api §3, §4.6 |
| Digest | `docs/plans/platform-core/digests/S8-S10.md` §5 rule 19 and §8 (contract checkpoints) |
| `Stage-Release:` | `none` (plan §(b) decision 4: contract PRs touch only packages of flipped stages and 09 names no stage for them) |

## Gate (read before anything else)

- **Do not start without the owner's W readings.** `OWNER-25` (NOT AGENT WORK) hands the coordinator the counter readings of `audiobook_organizer_compat_legacy_use_total` for this contract's surfaces, each with its process start time, covering 14 consecutive days with no use, plus the dates for W (a) and the release-note check for W (b) (09 §9.2.1 W, §9.7 P8). If the coordinator has not given you that text to paste into the PR description, STOP and ask. You never take the readings yourself.
- **The date gate is a test.** `TestNoContractBeforeSunset` fails a build whose code removes or retires an entry before its ledger row's earliest removal date (09 §9.2.9). If it fails after your change, the window has not run: STOP, revert your working tree, report.
- **One contract at a time** (plan §(d) "W19 (contracts)"): the coordinator dispatches this PR only when no open PR touches one of its files. Files this task shares: `docs/compat-surfaces.md`, `internal/books/compat-surfaces.md` (with PC-13-14, PC-16-06, PC-17-04 and the other PC-19 tasks).
- **No stored data changes.** A contract retires surfaces only; it never deletes, rewrites or re-levels stored data (09 §9.5).

## Goal

After window W for the S5 task aliases: every `httpapi.Alias` whose ledger row is an S5-section `http` row for
`/api/v3/ops/*`, `/api/v1/op-defs` or `/api/v1/tasks` becomes an `httpapi.Gone` stub, and a legacy kind ID (a
`FormerKinds`/`LegacyDefID` entry rather than a live `Kind`) given as **input** to a native task route is no longer
translated. Stored-row kind resolution through `FormerKinds`/`LegacyDefID` is **kept** (09 §9.5 C5; 09 §9.6 row 28:
"stored-row resolution **permanent**").

## Background (the rules, cited)

- 09 §9.5 C5: earliest R(S5) + W; owner check: no use of `task_kind` input and of the task `http` aliases; action
  "aliases → `Gone` → delete; stored-row kind resolution through `FormerKinds`/`LegacyDefID` is **kept**".
- 05-tasks §5T.3.10 "Compat": `/api/v3/ops/*`, `/api/v1/op-defs` and `/api/v1/tasks` are aliases onto the native task
  routes; alias handlers translate legacy def IDs through `FormerKinds`/`LegacyDefID`. **No `/api/v1/operations*` route
  is a core alias** (R11-3): all of them stay books' gin manifest routes at every stage, and
  `/api/v1/maintenance-window/*` is kept, not aliased (FIN12-4). Neither is touched here.
- 09 §9.6 row 28: task kinds — input W after S5, input use counted on `task_kind` (C5); stored-row resolution permanent.
- 05-http-api §4.6 for the `Gone` stub and its legacy body (see PC-19-02's background for the exact text).

## START HERE (worktree off fresh `origin/main`)

```bash
REPO=<your clone>                       # never write a real machine path into a commit or PR
TASK=pc-19-03-contract-c5
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
| modify | `core/internal/taskhttp/` | aliases → `httpapi.Gone`; legacy kind input no longer translated |
| modify | `docs/compat-surfaces.md`, `internal/books/compat-surfaces.md` (generated) | C5 rows `aliased` → `gone` |
| modify (tests) | alias tests in `core/internal/taskhttp/` and `TestAliasHeadersAndShape` | 410 expectations |
| create | `changelog.d/<date>_contract_c5.md` | fragment |

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
ledger_rows '\bS5\b' http | grep -E '/api/v3/ops|/api/v1/op-defs|/api/v1/tasks' | awk '{print $1}' | sort | uniq -c   # record aliased as N_C5H; expected 17 (12 + 2 + 3, 09 §9.6 rows 1 and 3)
ledger_rows '\bS5\b' task_kind | awk '{print $1}' | sort | uniq -c                                                    # record aliased as N_C5K
grep -rn 'httpapi.Alias(' core/internal/taskhttp --include='*.go' | grep -v _test.go | wc -l                           # expected: >= 1; every C5 row ID appears in exactly one LegacyPath literal
grep -rn 'FormerKinds\|LegacyDefID' core/internal/taskhttp --include='*.go' | grep -v _test.go                         # the input translation sites (record file:line)
grep -rn '"task_kind"' core/ --include='*.go' | grep -v _test.go                                                       # expected: >= 1 (the counter surface)
grep -rn 'protected.*("/operations' internal/server/*.go | wc -l                                                        # informative: books' gin routes, never touched here
```

## Steps

1. Run re-verify; record counts and the base commit. If `N_C5H` ≠ 17, list the differing paths and continue only if
   every row you would convert has exactly one `httpapi.Alias` registration; otherwise STOP.
2. Convert each C5 `http` row's `httpapi.Alias` to `httpapi.Gone` exactly as PC-19-02 step 2 describes (method and path
   from `Pattern`, same group, `Perm`/`Guards` of the target, `Name` = target name + `LegacyGone`, successor = the
   row's canonical path). STOP on the same build-error condition.
3. Legacy kind input: at each translation site found above that serves **API input** (a request body, query or path
   value naming a kind), stop resolving `FormerKinds`/`LegacyDefID` and give the answer the route already gives for a
   kind no definition declares. Find that answer with `grep -rn 'unknown kind\|ErrUnknownKind\|unknown_kind'
   core/tasks core/internal/taskhttp`; if the route has no such answer, STOP and report (the spec names no status for
   this refusal). Do **not** touch resolution of stored rows (`FormerKinds` on read, 05-tasks §5T.3 "resolved on every
   read").
4. Ledger: the C5 `http` rows and the `task_kind` rows: `aliased` → `gone`. `make compat-ledger`.
5. Tests first (below), then "Acceptance". PR description: **Release notes** listing every removed path with its
   successor and "legacy operation IDs are no longer accepted as API input; use the `books.<id>`/`core.<id>` kind";
   the owner's W readings.

## Code contract (from the spec; do not rename)

```go
func Gone(g *Group, rt Route, successor string)   // core/httpapi, 05-http-api §3
// core/tasks (05-tasks §5T.3): FormerKinds []Kind and LegacyDefID stay; resolution on read is unchanged.
```

## Tests to write first (TDD)

- `TestAliasHeadersAndShape`: one case per C5 path — 410 with the legacy gone body for an allowed caller, 401/403
  first otherwise.
- `TestLegacyKindInputRefused` (name coined here): a native task route given a legacy def ID answers the route's
  unknown-kind response; a stored run whose row names a legacy def ID still lists under its live kind.

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
ledger_rows '\bS5\b' http | grep -E '/api/v3/ops|/api/v1/op-defs|/api/v1/tasks' | grep -c '^aliased'   # expected: 0
ledger_rows '\bS5\b' task_kind | grep -c '^aliased'                                                   # expected: 0
go test ./core/internal/taskhttp/... ./core/tasks/... -count=1 2>&1 | grep -c '^FAIL'                 # expected: 0
```

## Out of scope

- Deleting the `Gone` stubs or the alias code: that is PC-20-02, one production minor later (09 §9.5, 05-http-api §4.6).
- Any ledger row outside this contract's set, any row whose gate is `permanent`, and every other contract's rows.
- `ci/stage.txt`: a contract is not a stage release and changes no marker line.
- Stored data of any kind: rows, blob fields, sidecars, levels (09 §9.5; 09 §9.2.1 "Stored data has no window").
- Every `/api/v1/operations*` route and `/api/v1/maintenance-window/*` (books' gin routes, 05-tasks §5T.3.10).
- `FormerKinds`/`LegacyDefID` themselves and stored-row resolution (permanent, 09 §9.6 row 28).
- `/api/v3/ops/events` (a stream, C6).

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

- 09 §9.6 row 1 still lists "the task routes under `operations` that 05-tasks 5T.3.10 replaces"; 05-tasks §5T.3.10
  (R11-3) says no `/api/v1/operations*` route is an alias, so C5 has nothing to retire there.
- The spec names no status for a refused legacy kind on input; the brief reuses the route's existing unknown-kind
  answer and STOPs if there is none.
