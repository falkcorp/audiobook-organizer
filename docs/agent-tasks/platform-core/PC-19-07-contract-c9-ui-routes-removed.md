<!-- file: docs/agent-tasks/platform-core/PC-19-07-contract-c9-ui-routes-removed.md -->
<!-- version: 1.0.0 -->
<!-- guid: 1ee4b2f5-2c06-4818-9690-064701b5a76b -->
<!-- last-edited: 2026-11-04 -->

# PC-19-07 — Contract C9 (Gone step): UI routes: client redirects removed; the legacy invite and temp-login 302s removed at the end of their own windows

| Field | Value |
|---|---|
| ID | `PC-19-07` |
| Tier | T1 Sonnet/medium — deletes list entries and two manifest routes whose ledger dates have passed; no new mechanism |
| Wave | W19 — contracts C3–C9, the Gone step (no release ID of its own) |
| Depends on | `OWNER-29` (the W owner check for C9) |
| Priority / effort | P1 / M |
| Spec | 09 §9.5 C9; 09 §9.2.1 "Contract" step and W (a)–(c); 09 §9.6 rows 31 and 34; 07 §7.4, §7.11, §7.19 step 5; 09 S9 "UI routes" |
| Digest | `docs/plans/platform-core/digests/S8-S10.md` §5 rule 19 and §8 (contract checkpoints) |
| `Stage-Release:` | `none` (plan §(b) decision 4: contract PRs touch only packages of flipped stages and 09 names no stage for them) |

## Gate (read before anything else)

- **Do not start without the owner's W readings.** `OWNER-29` (NOT AGENT WORK) hands the coordinator the counter readings of `audiobook_organizer_compat_legacy_use_total` for this contract's surfaces, each with its process start time, covering 14 consecutive days with no use, plus the dates for W (a) and the release-note check for W (b) (09 §9.2.1 W, §9.7 P8). If the coordinator has not given you that text to paste into the PR description, STOP and ask. You never take the readings yourself.
- **The date gate is a test.** `TestNoContractBeforeSunset` fails a build whose code removes or retires an entry before its ledger row's earliest removal date (09 §9.2.9). If it fails after your change, the window has not run: STOP, revert your working tree, report.
- **One contract at a time** (plan §(d) "W19 (contracts)"): the coordinator dispatches this PR only when no open PR touches one of its files. Files this task shares: `docs/compat-surfaces.md`, `internal/books/compat-surfaces.md`, `internal/books/module.go` (with PC-19-04 … PC-19-06); `internal/server/legacy_manifest.go` (no other W19 task lists it).
- **No stored data changes.** A contract retires surfaces only; it never deletes, rewrites or re-levels stored data (09 §9.5).

## Goal

After window W for the S9 UI routes: books' `UIDescriptor.LegacyRedirects` loses the 19 legacy SPA paths (14 module
paths + 5 absorbed pages), so the client redirects and their `ui_route` entries are gone; and each of the two legacy 302
routes — `GET /invite/{token}` (window S9 + max(W, invite TTL)) and `GET /auth/temp-login?token=` (window S4 +
max(W, temp-login TTL)) — is deleted **only if its own ledger row's earliest removal date has passed**. `localStorage`
keys are untouched (read in place). There is no 410 step for UI routes: the redirect is removed (09 §9.6 row 31).

## Background (the rules, cited)

- 09 §9.5 C9: earliest R(S9) + W; owner check: no use on `ui_route`; action "client redirects removed; the legacy invite and
  temp-login 302s removed at the end of their own window (§9.6 rows 31, 34); `localStorage` keys are untouched".
- 09 §9.6 row 31: 14 module paths + 5 absorbed pages (`/dashboard`, `/files`, `/diagnostics`, `/operations`,
  `/maintenance`) + `/invite/:token`; module paths and pages: W after S9; `/invite/{token}` 302: S9 + max(W, invite TTL);
  "client redirect → removed (C9); the 302 route deleted at the end of its window". Row 34: temp-login link, S4 +
  max(W, temp-login TTL), "302 route deleted at the end of its window".
- 07 §7.11 / FE3-17: `UIDescriptor.LegacyRedirects` (Go, books) is **the one list**; `manifest.gen.ts` exports it; the
  kernel registers one `compat.Entry{Surface: "ui_route"}` per row at P7 and `ui.Handler` records full page loads that
  match one (09 §9.2.9). Removing a Go row removes the client redirect and its entry.
- 09 S9 "UI routes" (digest S8-S10 §5 rule 16): books' legacy manifest serves `GET /invite/{token}` (Public,
  `RateAuth`) with a 302 to `/invite#token=<token>` and `Referrer-Policy: no-referrer`; `invite` joins `login` in
  `LegacyMount.SPAShared`.

## START HERE (worktree off fresh `origin/main`)

```bash
REPO=<your clone>                       # never write a real machine path into a commit or PR
TASK=pc-19-07-contract-c9
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
| modify | the Go file that declares books' `UIDescriptor.LegacyRedirects` (R-C9.2; expected `internal/books/module.go` or a sibling under `internal/books/`) | delete the 19 rows |
| modify (generated) | the `manifest.gen.ts` R-C9.2 finds under `web/src/` | regenerated, never hand-edited |
| modify | `internal/server/legacy_manifest.go` | delete the 302 routes whose window has ended; drop `invite` from `LegacyMount.SPAShared` with the invite route |
| inventory lists | `web/src/modules/books/redirects.ts` | edit only if R-C9.2 shows it holds a hand-written copy of the list; see Planner notes |
| modify | `docs/compat-surfaces.md`, `internal/books/compat-surfaces.md` (generated) | rows `aliased` → `removed`, removal PR = this PR |
| create | `changelog.d/<date>_contract_c9.md` | fragment |

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
ledger_rows '\bS9\b' ui_route | awk '{print $1}' | sort | uniq -c                               # expected: 19 aliased rows of 09 §9.6 row 31 (plus any other ui_route row; see step 2)
# R-C9.2 where the one list lives and its generated export:
grep -rn 'LegacyRedirects' internal/books --include='*.go' | grep -v _test.go                   # expected: >= 1; record file:line of the list literal
grep -rln 'legacyRedirects' web/src | head                                                      # expected: the generated manifest.gen.ts (and readers)
grep -rn 'manifest.gen.ts' Makefile web/package.json tools/ 2>/dev/null | head                  # the regeneration command; record it
test -f web/src/modules/books/redirects.ts && echo PRESENT || echo ABSENT                       # informative (see Planner notes)
# R-C9.3 the two 302 routes and their rows (the ledger section of each may be S4 or S9):
grep -rn '/invite/{token}\|/auth/temp-login' internal/server/legacy_manifest.go | wc -l         # expected: >= 2
ledger_rows '.' http,ui_route | grep -E '/invite/\{token\}|/auth/temp-login'                     # record each row's state and earliest removal date
# R-C9.4 localStorage keys are not in scope:
grep -rn 'legacyStorageKeys' web/src | wc -l                                                    # informative; never touched
```

## Steps

1. Run re-verify; record counts, dates and the base commit.
2. From the S9 `ui_route` rows, select exactly the 19 rows whose legacy path is one of 09 §9.6 row 31's module paths
   or the five absorbed pages. Any other `ui_route` row (for example the ABS web-client paths HEAD keeps in
   `web/src/utils/absRedirects.ts`) is **not** removed; list it in the PR description. If you cannot find exactly 19,
   STOP.
3. Delete those 19 entries from the Go `LegacyRedirects` literal. Regenerate `manifest.gen.ts` with the command R-C9.2
   found. If `web/src/modules/books/redirects.ts` exists and holds a hand-written list, delete the same 19 entries there.
4. For each 302 route: if `TestNoContractBeforeSunset` passes with its row set to `removed` (run it after the ledger edit
   of step 5), delete the manifest route (and, for the invite route, `invite` from `LegacyMount.SPAShared`); otherwise
   leave the route and its row unchanged and say so in the PR description ("window not ended").
5. Ledger: each removed row `aliased` → `removed`, removal PR cell = this PR's number (fill it after `gh pr create`,
   amend, push). `make compat-ledger`.
6. Tests first (below), then "Acceptance". PR description: **Release notes** listing every removed redirect and route
   ("old bookmarks to /library, … now show the not-found page; use /books/…"); the owner's W readings.

## Code contract (from the spec; do not rename)

```go
// core/module (06 §6.4.2; 07 §7.6.1)
LegacyRedirects []Redirect   // module.UIDescriptor.LegacyRedirects — the one list (FE3-17)
```

## Tests to write first (TDD)

- `TestHandler_RecordsLegacyRedirectUse` (07 §7.18): with the 19 rows gone, a full page load of a removed path records
  nothing and gets the SPA's not-found page; a remaining row still records.
- If the invite 302 was removed: `TestLegacyInviteRedirect_SPASharedSegment` and `TestLegacyInviteLink_302NoToken`
  change to expect the route's absence (404) — change the expectation, not the name. The fragment reader
  (`TestLegacySPA_InviteFragmentReader`) keeps passing.
- `legacyStorageKeys.test.tsx` and `theme-boot.spec.ts` keep passing unchanged.

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
ledger_rows '\bS9\b' ui_route | grep -c '^removed'                                            # expected: 19
grep -rn 'LegacyRedirects' internal/books --include='*.go' | grep -v _test.go                  # the literal no longer holds any of the 19 paths
cd web && npm run build && npm test -- --run; echo "web exit=$?"; cd ..                       # expected: web exit=0
go test ./internal/server/... ./internal/books/... ./core/ui/... -count=1 2>&1 | grep -c '^FAIL'   # expected: 0
```

## Out of scope

- Deleting the `Gone` stubs or the alias code: that is PC-20-02, one production minor later (09 §9.5, 05-http-api §4.6).
- Any ledger row outside this contract's set, any row whose gate is `permanent`, and every other contract's rows.
- `ci/stage.txt`: a contract is not a stage release and changes no marker line.
- Stored data of any kind: rows, blob fields, sidecars, levels (09 §9.5; 09 §9.2.1 "Stored data has no window").
- `localStorage` keys (read in place forever, 09 §9.5 C9; 07 §7.4.2).
- The `/login/temp` and `/invite` shell pages and the fragment readers (permanent shell pages).
- Any `ui_route` row outside the 19 (ABS web-client paths); a 302 whose window has not ended.

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

- `web/src/modules/books/redirects.ts` is not a spec-named file and no inventory row creates it: the one list is Go
  `UIDescriptor.LegacyRedirects`, exported through `manifest.gen.ts` (07 §7.11, FE3-17). The missing files are the Go
  file that declares the list and `manifest.gen.ts`.
- The inventory's "TestAliasHeadersAndShape for the 410 answers" does not apply: C9 has no 410 step (redirects and 302s
  are removed).
- 09 §9.6 row 31 does not say whether the ABS web-client redirects (HEAD's `web/src/utils/absRedirects.ts`, which 07 §7.11
  folds into `LegacyRedirects`) are window or permanent rows; owner to classify.
- The temp-login 302's own window (S4 + max(W, TTL)) can end long before C9, and either 302's window can end after C9's
  PR; no inventory row removes a 302 left in place here (PC-20-02 does not list `internal/server/legacy_manifest.go`).
