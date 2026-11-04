<!-- file: docs/agent-tasks/platform-core/PC-19-06-contract-c8-backup-system-gone.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7f368575-2f85-4b9e-b792-11b06d42c506 -->
<!-- last-edited: 2026-11-04 -->

# PC-19-06 — Contract C8 (Gone step): backup, update, system and health aliases answer 410

| Field | Value |
|---|---|
| ID | `PC-19-06` |
| Tier | T1 Sonnet/medium — sixteen alias registrations become `httpapi.Gone`; the ledger rows decide the set |
| Wave | W19 — contracts C3–C9, the Gone step (no release ID of its own) |
| Depends on | `OWNER-28` (the W owner check for C8) |
| Priority / effort | P2 / M |
| Spec | 09 §9.5 C8; 09 §9.2.1 "Contract" step and W (a)–(c); 09 §9.6 rows 1 (system 7, backup 4, update 3) and 8 (`/api/health`, `/api/v1/health`); 05-backup §5B.3.8; 05-updater (routes); 05-health 5H.3; 05-http-api §3, §4.6 |
| Digest | `docs/plans/platform-core/digests/S8-S10.md` §5 rule 19 and §8 (contract checkpoints) |
| `Stage-Release:` | `none` (plan §(b) decision 4: contract PRs touch only packages of flipped stages and 09 names no stage for them) |

## Gate (read before anything else)

- **Do not start without the owner's W readings.** `OWNER-28` (NOT AGENT WORK) hands the coordinator the counter readings of `audiobook_organizer_compat_legacy_use_total` for this contract's surfaces, each with its process start time, covering 14 consecutive days with no use, plus the dates for W (a) and the release-note check for W (b) (09 §9.2.1 W, §9.7 P8). If the coordinator has not given you that text to paste into the PR description, STOP and ask. You never take the readings yourself.
- **The date gate is a test.** `TestNoContractBeforeSunset` fails a build whose code removes or retires an entry before its ledger row's earliest removal date (09 §9.2.9). If it fails after your change, the window has not run: STOP, revert your working tree, report.
- **One contract at a time** (plan §(d) "W19 (contracts)"): the coordinator dispatches this PR only when no open PR touches one of its files. Files this task shares: `docs/compat-surfaces.md`, `internal/books/compat-surfaces.md` (with PC-19-07).
- **No stored data changes.** A contract retires surfaces only; it never deletes, rewrites or re-levels stored data (09 §9.5).

## Goal

After window W for the S8 platform aliases: every `httpapi.Alias` whose ledger row is an S8-section `http` row for
`/api/v1/backup` (4), `/api/v1/update` (3), `/api/v1/system` (7), `/api/health` or `/api/v1/health` becomes an
`httpapi.Gone` stub. `/health` (with its `ready` field) and `/metrics` are permanent and untouched (09 §9.6 row 7).
There are no metric aliases to remove (09 §9.5 C8; A-5).

## Background (the rules, cited)

- 09 §9.5 C8: earliest R(S8) + W; owner check: no use of `/api/v1/{backup,update,system}` and the health aliases; action
  "aliases → `Gone` → delete; there are no metric aliases to remove".
- The packages that register them: `core/internal/backuphttp` (05-backup §5B.3.8), `core/internal/updaterhttp`
  (PC-17-02), `core/internal/healthhttp` (PC-17-03); 05-http-api §5 item 2 lists all capability packages.
- `/health` is permanent (deploy scripts and the Docker `HEALTHCHECK` wait on `"ready":true`; 05-health 5H.3,
  09 §9.6 row 7). `/livez` and `/readyz` are native and permanent.
- `Gone` mechanics: see PC-19-02's background.

## START HERE (worktree off fresh `origin/main`)

```bash
REPO=<your clone>                       # never write a real machine path into a commit or PR
TASK=pc-19-06-contract-c8
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
| modify | `core/internal/backuphttp/`, `core/internal/updaterhttp/`, `core/internal/healthhttp/` (and any other package R-C8.2 finds holding a C8 row) | aliases → `httpapi.Gone` |
| modify | `docs/compat-surfaces.md`, `internal/books/compat-surfaces.md` (generated) | C8 rows `aliased` → `gone` |
| create | `changelog.d/<date>_contract_c8.md` | fragment |

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
ledger_rows '\bS8\b' http | grep -E '/api/v1/(backup|update|system|health)(/|\b)|/api/health\b' | awk '{print $1}' | sort | uniq -c   # expected: "16 aliased" (4 + 3 + 7 + 2); a different count: STOP
# R-C8.2 each row ID has exactly one registration; record the package of each:
#   grep -rn "Ledger: *\"<id>\"" core/ --include='*.go' | grep -v _test.go       # expected: 1 per row
ls -d core/internal/backuphttp core/internal/updaterhttp core/internal/healthhttp                 # expected: three directories (see Planner notes)
grep -rn '"/health"' internal/ core/ --include='*.go' | grep -v _test.go | wc -l                   # expected: >= 1 (permanent; never touched)
```

## Steps

1. Run re-verify; STOP if the row count is not 16 or a row has zero or several registrations.
2. Convert each of the 16 rows' `httpapi.Alias` to `httpapi.Gone` exactly as PC-19-02 step 2 describes; STOP on the same
   build-error condition. Never convert `/health`, `/metrics`, `/livez` or `/readyz`.
3. Ledger: the 16 rows `aliased` → `gone`; `make compat-ledger`.
4. Check the deploy scripts still wait on the permanent path:
   `grep -rn '/api/health\|/api/v1/health' scripts/ deploy/ Dockerfile* docker-compose.yml | wc -l` → expected 0; a
   non-zero count means a shipped file still uses an alias: STOP and report.
5. Tests first (below), then "Acceptance". PR description: **Release notes** listing the 16 removed paths and their
   successors; the owner's W readings.

## Code contract (from the spec; do not rename)

```go
func Gone(g *Group, rt Route, successor string)   // core/httpapi, 05-http-api §3
```

## Tests to write first (TDD)

- `TestAliasHeadersAndShape`: one case per C8 path (410 + legacy gone body for an allowed caller; 401/403 first). The
  two health aliases had no credential requirement at HEAD (`server_lifecycle.go:1746-1782` @748fd787a); their stub keeps
  the alias's own `Perm`, so the case is: 410 with no credential when that `Perm` is public.
- `TestProbes_VariantsAreNotSPA` and `TestReadyz_CriticalDownIs503_LivezStays200` (05-health) keep passing.

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
ledger_rows '\bS8\b' http | grep -E '/api/v1/(backup|update|system|health)(/|\b)|/api/health\b' | grep -c '^gone'   # expected: 16
go test ./... -run '^TestProbes_VariantsAreNotSPA$|^TestReadyz_CriticalDownIs503_LivezStays200$' -count=1 -v 2>&1 | grep -c -- '--- PASS'   # expected: >= 2 (/health and the probes untouched)
go test ./core/internal/backuphttp/... ./core/internal/updaterhttp/... ./core/internal/healthhttp/... -count=1 2>&1 | grep -c '^FAIL'   # expected: 0
```

## Out of scope

- Deleting the `Gone` stubs or the alias code: that is PC-20-02, one production minor later (09 §9.5, 05-http-api §4.6).
- Any ledger row outside this contract's set, any row whose gate is `permanent`, and every other contract's rows.
- `ci/stage.txt`: a contract is not a stage release and changes no marker line.
- Stored data of any kind: rows, blob fields, sidecars, levels (09 §9.5; 09 §9.2.1 "Stored data has no window").
- `/health`, `/metrics`, `/livez`, `/readyz` (permanent).
- The factory-reset aliases `POST /api/v1/system/reset` and `POST /api/v1/system/factory-reset` if their ledger rows are in
  the S4 section (registered by `core/internal/storagehttp`, PC-11-03): they are not S8 rows; list them in the PR.

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

- **Missing creator:** no inventory row creates `core/internal/backuphttp/` (05-backup §5B.3.8 names it; PC-17-01 creates
  `core/backup/`, PC-17-04 adds the routes). PC-19-06 and PC-20-02 both list it.
- The reset aliases of `/api/v1/system/*` were registered at S4 r2 by `storagehttp` (PC-11-03); whether 09 §9.6 row 1's
  "system 7" counts them is unclear, so the brief selects by the ledger's S8 section and STOPs on a count other than 16.
