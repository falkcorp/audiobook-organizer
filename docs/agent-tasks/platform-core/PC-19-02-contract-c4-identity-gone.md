<!-- file: docs/agent-tasks/platform-core/PC-19-02-contract-c4-identity-gone.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6262be5a-2308-4014-a2f0-e6a781d325d7 -->
<!-- last-edited: 2026-11-04 -->

# PC-19-02 — Contract C4 (Gone step): identity: legacy atoms on API input answer 400 naming the canonical permission; the auth and users aliases answer 410

| Field | Value |
|---|---|
| ID | `PC-19-02` |
| Tier | T1 Sonnet/medium — replaces alias registrations with `httpapi.Gone` and turns one accepted input into a 400; the ledger rows decide the set |
| Wave | W19 — contracts C3–C9, the Gone step (no release ID of its own) |
| Depends on | `OWNER-24` (the W owner check for C4) |
| Priority / effort | P2 / S |
| Spec | 09 §9.5 C4; 09 §9.2.1 "Contract" step and W (a)–(c); 09 §9.6 rows 1 (auth 22, users 15) and 10 (13 atoms); 09 S4 "Endpoints" and "Alias targets"; 05-http-api §3 (`Gone`), §4.6; 05-rbac §6.3 "Compatibility window" and "Contract" |
| Digest | `docs/plans/platform-core/digests/S8-S10.md` §5 rule 19 and §8 (contract checkpoints) |
| `Stage-Release:` | `none` (plan §(b) decision 4: contract PRs touch only packages of flipped stages and 09 names no stage for them) |

## Gate (read before anything else)

- **Do not start without the owner's W readings.** `OWNER-24` (NOT AGENT WORK) hands the coordinator the counter readings of `audiobook_organizer_compat_legacy_use_total` for this contract's surfaces, each with its process start time, covering 14 consecutive days with no use, plus the dates for W (a) and the release-note check for W (b) (09 §9.2.1 W, §9.7 P8). If the coordinator has not given you that text to paste into the PR description, STOP and ask. You never take the readings yourself.
- **The date gate is a test.** `TestNoContractBeforeSunset` fails a build whose code removes or retires an entry before its ledger row's earliest removal date (09 §9.2.9). If it fails after your change, the window has not run: STOP, revert your working tree, report.
- **One contract at a time** (plan §(d) "W19 (contracts)"): the coordinator dispatches this PR only when no open PR touches one of its files. Files this task shares: `docs/compat-surfaces.md`, `internal/books/compat-surfaces.md` (with PC-12-06, PC-13-14, PC-16-06, PC-17-04 and the other PC-19 tasks).
- **No stored data changes.** A contract retires surfaces only; it never deletes, rewrites or re-levels stored data (09 §9.5).

## Goal

After window W for the S4 identity aliases: (1) every `httpapi.Alias` registration whose ledger row is an S4-section
`http` row under `/api/v1/auth/` or `/api/v1/users/` becomes an `httpapi.Gone` stub (auth and permission first, then
410 with HEAD's legacy body), which also retires the `/api/v1/auth/me` alias and its `canonical_permissions` field;
(2) legacy atoms given as **API input** (key creation, role writes) are refused with 400 `unknown_permission` naming the
canonical permission. Atoms stored in role and key rows are untouched, and the alias table
(`app.Options.LegacyPermissionAliases`) stays: it maps stored atoms until the identity cutover H1 (05-rbac §6.3).

## Background (the rules, cited)

- 09 §9.5 C4: earliest R(S4 r2) + W; owner check: no use of the `http` auth/users aliases and of `rbac_atom`; action
  "legacy atoms on API input → 400 naming the canonical permission; HTTP aliases → `Gone` one minor → delete; the
  `/api/v1/auth/me` alias's `canonical_permissions` goes with it. Atoms stored in role and key rows are untouched".
- 05-rbac §6.3 "Contract": "legacy atoms as API input are refused with 400 `unknown_permission` naming the canonical
  permission. The alias table itself stays as long as rows hold atoms, which is until the identity hardening cutover.
  It writes no stored data." During W the key-creation and role-write APIs accepted atoms, counted
  `<namespace>_compat_legacy_use_total{surface="rbac_atom"}` and answered with `Deprecation` and `deprecated_permissions`.
- 05-http-api §4.6: "An alias is replaced by a `Gone` stub only in a contract PR … the stub is deleted one minor release
  later — the existing `gone()` convention … kept, with its auth-first ordering"; legacy-path 410 body is exactly
  `{"error":"gone","message":"this endpoint has been removed; use … instead"}`. HEAD's helper is
  `internal/server/gone.go` `func gone(replacement string)` (lines 28–36 at HEAD; the spec cites `:20-32` at 748fd787a).
- 09 S4 "Alias targets": `PUT /api/v1/auth/me/password` is **not** an alias; it stays one gin route (with and without
  `user_id`). It is out of this contract.

## START HERE (worktree off fresh `origin/main`)

```bash
REPO=<your clone>                       # never write a real machine path into a commit or PR
TASK=pc-19-02-contract-c4
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
| modify | `core/internal/authhttp/` (and `core/internal/rbachttp/` if R-C4.2 finds aliases there) | `httpapi.Alias` → `httpapi.Gone` for the C4 rows |
| modify | `core/rbac/` (the file R-C4.3 finds) | atom input → 400 `unknown_permission` |
| modify | `docs/compat-surfaces.md`, `internal/books/compat-surfaces.md` (generated) | C4 rows `aliased` → `gone` |
| modify (tests) | the files holding `TestAliasHeadersAndShape`, `TestMeAlias_LegacyShape` and the alias tests of PC-11-01 | expectations become 410 / 400 |
| create | `changelog.d/<date>_contract_c4.md` | fragment |

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
# R-C4.1 the rows: S4 section, surface http under /api/v1/auth/ or /api/v1/users/, and surface rbac_atom.
ledger_rows '\bS4\b' http | grep -E '/api/v1/(auth|users)(/|\b)' | awk '{print $1}' | sort | uniq -c   # record aliased count as N_C4H
ledger_rows '\bS4\b' rbac_atom | awk '{print $1}' | sort | uniq -c                                   # record aliased count as N_C4A; expected 13 (09 §9.6 row 10)
#   09 §9.6 row 1 / 09 S4 give 22 auth and 15 users paths at 748fd787a; N_C4H may be lower only by paths that stayed
#   gin routes (PUT /api/v1/auth/me/password). List every difference in the PR description.
# R-C4.2 every C4 http row has exactly one registration. For each row ID <id> from R-C4.1:
#   grep -rn "Ledger: *\"<id>\"" core/ internal/ --include='*.go' | grep -v _test.go | wc -l       # expected: 1, inside an httpapi.Alias(...) LegacyPath literal
grep -rn 'httpapi.Alias(' core/internal/authhttp core/internal/rbachttp --include='*.go' | grep -v _test.go | wc -l   # record as N_REG; expected >= N_C4H
# R-C4.3 where API input atoms are accepted (05-rbac §6.3) and where the counter is bumped.
grep -rn 'deprecated_permissions' core/ --include='*.go' | grep -v _test.go                         # expected: >= 1 (the W-time answer field)
grep -rn '"rbac_atom"' core/ --include='*.go' | grep -v _test.go                                    # expected: >= 1 (the compat surface)
# R-C4.4 the Gone helper of httpapi (05-http-api §3).
grep -rn '^func Gone(g \*Group, rt Route, successor string)' core/httpapi/ | wc -l                  # expected: 1
# R-C4.5 the error code the contract answers with is not yet used for atoms.
grep -rn 'unknown_permission' core/ --include='*.go' | grep -v _test.go | wc -l                     # record; the step adds the atom case
# R-C4.6 HEAD's legacy gone helper (reference only).
grep -n '^func gone(replacement string) gin.HandlerFunc' internal/server/gone.go                     # expected at HEAD: line 28
```

## Steps

1. Run the re-verify block; record `N_C4H`, `N_C4A`, `N_REG` and the base commit in the PR description.
2. For each C4 `http` row (state `aliased`): find its one registration (R-C4.2). Replace that `httpapi.Alias(g,
   httpapi.LegacyPath{…})` call with `httpapi.Gone(g, httpapi.Route{…}, "<successor>")`:
   - `Method` and path: taken from the alias's `Pattern` (`"POST /api/v1/auth/api-keys"` → `Method: http.MethodPost`
     and that path); register on the **same** group `g` the alias used;
   - `Perm` and `Guards`: copied from the alias's target route; for a multi-target alias (the api-keys selector rows of
     09 S4), copy those of the target the alias's `Request` picks when the selector field is **absent**
     (`createOwnAPIKey`, own key list), so a caller who could use the path is the one who learns it is gone;
   - `Name`: the target's `Name` + `LegacyGone` (naming rule coined here; it must be unique per module);
   - `successor`: the canonical path from the row's `canonical` cell, written as HEAD's helper writes it
     (`"POST /api/core/v1/me/api-keys"`).
   If `httpapi.Gone` cannot express the legacy pattern on that group (build error, an `ErrAliasOutsideClaim`-class
   error from `app.New`), STOP and report: the base's `Gone` does not cover legacy patterns.
3. The `/api/v1/auth/me` alias is one of these rows; its `Shape` (which added `canonical_permissions`) goes with it.
   Delete the now-unused `Shape`/`Request` helpers only if the compiler reports them unused (`go vet` /
   `staticcheck` U1000); otherwise leave them for PC-20-02.
4. Atom input (05-rbac §6.3): in the decode path R-C4.3 found, replace "accept the atom, store through the write
   mapping, count `rbac_atom`, answer `deprecated_permissions`" with: refuse the request with 400, problem code
   `unknown_permission`, whose detail names the canonical permission(s) the atom expands to (the alias table's
   `LegacyAlias.New`, 05-rbac §6.1). Nothing is written. Keep `app.Options.LegacyPermissionAliases`, the read mapping
   and the write mapping of stored rows unchanged.
5. Ledger: the C4 `http` rows and the 13 `rbac_atom` rows: `state` `aliased` → `gone`. `make compat-ledger`.
6. Update the tests of "Tests" first (they must fail before steps 2–5 and pass after).
7. Run "Acceptance". PR description: **Release notes** listing every removed path and its canonical successor, and
   "legacy permission names are no longer accepted as API input; use the canonical name"; the owner's W readings.

## Code contract (from the spec; do not rename)

```go
// core/httpapi (05-http-api §3)
func Alias(g *Group, legacy LegacyPath)                 // what is removed for each C4 row
func Gone(g *Group, rt Route, successor string)         // what replaces it: auth and permission first, then 410;
                                                        // legacy body {"error":"gone","message":"this endpoint has been removed; use <successor> instead"}
```
Error answer for atom input (05-rbac §6.3): HTTP 400, problem `code: "unknown_permission"`, naming the canonical
permission. Ledger state for C4 rows: `gone`.

## Tests to write first (TDD)

- Extend `TestAliasHeadersAndShape` (09 §9.10) with one case per C4 path: with a credential that may use the old path,
  410 and exactly the legacy gone body above; without a credential, 401; with a credential lacking the permission,
  403 (auth first, 05-http-api §4.6).
- `TestMeAlias_LegacyShape` and the PC-11-01 alias tests (`TestAlias_UnknownLegacyFieldRefused`,
  `TestAlias_CreateKeyForOtherUserKeepsTarget`, `TestAlias_ListAllKeysNeedsUsersManage`) now expect 410 on the legacy
  path; rename nothing, change the expectations and say so in the test comments.
- New `TestLegacyAtomInputRefused` (name coined here): for each of the 13 atoms, key creation and a role write through
  the native routes answer 400 `unknown_permission` naming the canonical permission, and nothing is written; a role row
  that already holds atoms still reads with the same mapped set (`TestRoleRow_PrevBuildSeesAdmittedAtomsOnly` and
  `TestLegacyRoleReachUnchanged` keep passing).

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
ledger_rows '\bS4\b' rbac_atom | grep -c '^gone'                                             # expected: 13
ledger_rows '\bS4\b' http | grep -E '/api/v1/(auth|users)(/|\b)' | grep -c '^aliased'        # expected: 0
grep -rn 'httpapi.Alias(' core/internal/authhttp core/internal/rbachttp --include='*.go' | grep -v _test.go | grep -c -E '/api/v1/(auth|users)'   # expected: 0
go test ./... -run '^(TestAliasHeadersAndShape|TestLegacyAtomInputRefused|TestLegacyRoleReachUnchanged|TestRoleRow_PrevBuildSeesAdmittedAtomsOnly)$' -count=1 2>&1 | grep -c '^FAIL'   # expected: 0
```

## Out of scope

- Deleting the `Gone` stubs or the alias code: that is PC-20-02, one production minor later (09 §9.5, 05-http-api §4.6).
- Any ledger row outside this contract's set, any row whose gate is `permanent`, and every other contract's rows.
- `ci/stage.txt`: a contract is not a stage release and changes no marker line.
- Stored data of any kind: rows, blob fields, sidecars, levels (09 §9.5; 09 §9.2.1 "Stored data has no window").
- `PUT /api/v1/auth/me/password` (a gin route, not an alias, 09 S4).
- `/api/v1/me`, `/api/v1/setup`, `/api/v1/config`, `/api/v1/preferences` (S4 aliases that 09 §9.5 C4 does not name;
  list them in the PR description).
- `app.Options.LegacyPermissionAliases`, `rbac.LegacyAtom`, the read/write mapping of stored rows and every stored atom
  (until H1, 05-rbac §6.3); `/api/v1/system/reset` and `/system/factory-reset` aliases (C8 or unassigned, not C4);
  the temp-login and invite 302s (C9).

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

- **Files:** users, roles and api-keys aliases may be registered in `core/internal/rbachttp/` (PC-11-01 creates both
  `authhttp` and `rbachttp`), which the inventory row does not list; the brief allows it when R-C4.2 finds aliases there.
- **Unassigned window rows:** `/api/v1/me`, `/api/v1/setup`, `/api/v1/config` (2) and `/api/v1/preferences` (3) are S4
  aliases (09 S4 "Endpoints") that no contract in 09 §9.5 names; see also PC-19-01's note.
