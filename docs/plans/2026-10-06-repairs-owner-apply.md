<!-- file: docs/plans/2026-10-06-repairs-owner-apply.md -->
<!-- version: 1.2.0 -->
<!-- guid: 0b6f2d4e-8a31-4c57-9e12-7d5a3c9b1f60 -->
<!-- last-edited: 2026-10-07 -->

# Owner-only apply of iTunes-tracked, content-proven fragment copies

Stacked on #3807 (`feat/fragment-copy-hash-proof`).

## Goal

Owner decision 2026-10-06: a library copy the fragment-consolidation fixer
proved by content hash (`content hash equal at plan time`), but which is
manual-only because the fragment's own book_file row carries an iTunes path,
may be applied **by the owner alone, by clicking Apply on that row**. The
write is database rows only: the fragment is demoted, `merged_into` set and
soft-deleted (fragment-only retire). The parent is never written. No file is
written, and no iTunes library write happens.

Today no path can apply such a row:
- `RunApply` records `not_applicable` for any skipped row.
- `ApplyParams` has no override.
- The fixer's `Replan` returns `manual:` rows unchanged, without re-checking.
- `retireOpts.RefuseITunesPath` refuses the fragment under the lock.

## Design

### 1. Eligibility is decided by the fixer at plan time

New `repairs.Row` fields:
- `OwnerApplicable bool`
- `OwnerApplyReason string`

`PlanResult.OwnerApplicable` counts them, and a new `owner_applicable` page
filter lists them. They stay skipped (`Skipped` is unchanged), so bulk
selection, "apply all", the scheduled runs and the plain apply path all still
treat them as `not_applicable`.

The fragment fixer takes a copy claimant onto its parent's owner row,
`owner:<parent>` (one row per parent, holding every eligible fragment: on prod
2026-10-07 one parent's 300 content-proven chapter copies were one manual-only
copy row, and one click applies them), only when **all** of these hold:
- the pair is a copy, and its evidence is a content-hash proof made at plan time (`contentProofOf`);
- the fragment's only hands-off reason is an iTunes path on its own row: `itunesWhy()` names a row iTunes path, and `itunesPID()` is empty;
- the fragment carries no book or file PID and no live `itunes` external id. A PID would queue an iTunes remove at the purge, which is an iTunes write, so PID fragments stay ineligible;
- the framework/fixer guard is clean on the fragment and on the parent. That means nothing under `books/itunes/**` and nothing Doctor Who / Big Finish / Torchwood, by path, series or import path;
- the row iTunes path names the fragment's own file (its file:// URL percent-decoded: prod writes `002%20of%20301.m4b`) and does not point into the iTunes library tree. iTunes Media is never touched;
- `carriesOnto` is empty: no live external id and no listening state, positions or bookmarks;
- the fragment is not a path twin or a twin's donor;
- the fragment's version group holds no OTHER iTunes book (another fragment of the row included), and the parent is not in that group (the primary hand-off must never crown or write the parent);
- the parent is either iTunes-linked or plain, and is read without doubt.

`RunPlan` also runs the framework guard (franchise tags etc.) over owner rows,
and clears the flag if the guard trips.

The owner row carries `fragParentState` (`OwnerParent`, `ContentProofs`,
mode). Its fingerprint hashes the pairing, the proof and the mode, so a re-plan
re-stats both files of every fragment (`restoreContentProofs`). The owner
row's apply is all or nothing at the check: every fragment is re-checked under
the merge lock before the first write, and one refusal refuses the row.

### 2. Request: separate endpoint, interactive sessions only

- `POST /api/v1/repairs/:fixer/owner-apply {plan_op_id, row_id}` takes one row at a time, and the write is implied (`dry_run:false`).
- The handler refuses with 403 unless the request was authenticated interactively. That means a login session (cookie or session token) or a Cloudflare Access SSO identity. The owner's browser reaches prod through CF Access, where `RequireAuth` early-outs with no session, so CF Access SSO has to count. A CF service token never resolves a user (`internal/oauth/cfaccess.go`). API keys (`abk_`) are always refused.
- The caller must also hold the `admin` role.
- **CSRF.** The session cookie is `SameSite=Strict` and CORS grants no cross-origin preflight. On top of that the endpoint requires the custom header `X-Repairs-Owner-Apply: 1` (no form or simple cross-site request can send it), and refuses an `Origin` whose host is not the request host, and a `Sec-Fetch-Site` other than `same-origin`/`none`.
- The bulk `POST /repairs/:fixer/apply` refuses `owner_apply_row_ids` outright.
- **Auth method recorded explicitly.** New `auth.Method` enum on the request context (`auth.WithMethod` / `auth.MethodFromContext`): `session`, `session_delegated`, `api_key`, `cf_access`, `abs`. Only `session` and `cf_access` are `Interactive()`. `WithMethod` is downgrade-only: once set, a later stage can replace it only with a non-interactive method.
- **Session origin (security review of 397a52f11).** A session is not proof of a person's sign-in: temp-login links (mintable with `users.manage`, including by an admin API key) and invite acceptance also create sessions. `Session.Origin` records how it was minted; only `password` and `oauth` sessions are `session`; every other session (temp login, invite, sessions created before this change) is `session_delegated`. It is set by the branch that authenticated the request: the RequireAuth session branch, `handleAPIKeyAuth`, `CloudflareAccessAuth`, and ABS `Bind`. It is not set by token transport, because an `abk_` token in the cookie still goes down the API-key path.
- **Op params are never trusted.** The handler mints a one-shot in-memory grant (`repairs.OwnerGrants`): a 128-bit random nonce bound to user id, auth method, fixer id, plan op id and the row id, with a 2-hour TTL (`repairs.OwnerGrantTTL`; an apply can queue behind the one running apply and its scan stand-down wait). It then enqueues `repairs.apply` with `owner_apply_row_ids` and `owner_grant`. The op **consumes** the grant (`Take`) before any write. Only rows named in a consumed grant can run owner mode. This means:
  - `POST /operations/v2` with hand-written params finds no grant, so the row is `owner_apply_refused`;
  - retry and resume find the grant consumed (and resume is refused explicitly in any case), with the same result;
  - a server restart drops all grants.

### 3. Engine (`internal/repairs`)

- `ApplyParams` gains `OwnerApplyRowIDs` and `OwnerGrant`.
- `ApplyDeps` gains `Owner *OwnerApproval`.
- `RunApply` puts a skipped row into the run only when it is `OwnerApplicable` and listed in the consumed approval, and the run is not a resume.
- `applyOne` (owner rows):
  - framework guards run unchanged;
  - Replan, then the fingerprint must match;
  - `fresh.OwnerApplicable` is required in place of `Applicable()`;
  - a ledger-only journal row `repair_owner_apply` (user id, auth method, row, reason) is written before the fixer's write;
  - `f.Apply` gets a ctx carrying the approval (`repairs.WithOwnerApply`).
- `RowResult.OwnerUserID` and `ApplyResult.OwnerUserID` / `OwnerRows` record who applied.
- New outcome `owner_apply_refused`.

### 4. Fragment fixer

- `replanWith`: an `owner:<parent>` row goes through `replanParent` (`rebuildParentRows` → find `owner:<parent>`), like a copy row. Other `manual:` rows are still returned unchanged.
- `Apply`: under the merge lock the row is re-planned. In owner mode (ctx approval for this row, plus `locked.OwnerApplicable`), it re-checks:
  - `merge.GuardITunesProtected` on the fragment and the parent (FilePath under the configured iTunes roots or `books/itunes`);
  - the fragment's version group excluding the fragment itself, with the parent not in it;
  - `onlyRetireRefusal` (external ids and user state).

  It then retires through `retireIntoWith(..., retireOpts{Only: ..., RefuseITunesPath: false})`. Book and row PIDs and `itunes` external ids are still refused there. The parent is never written.
- Without owner ctx, an owner row is refused exactly as today.

### 5. Audit / undo

- New `undo.ChangeTypeRepairOwnerApply = "repair_owner_apply"`. It is ledger-only, so `NotRestorableLabel` returns `""` and `IsLedgerOnly` is true, and the revert is a no-op. It is kept by the opchange prune.
- The op log line names the user.
- The op revert restores the fragment through the existing soft-delete, merged_into and demote rows.

### 6. UI (`RepairsPanel.tsx`)

- Owner-applicable rows show their proof (the evidence) and a per-row **Apply (owner)** button.
- Its confirm dialog names the book and says "iTunes tracks this file; only database rows change".
- There is no checkbox and no select-all for these rows.
- An `Owner apply (N)` chip uses the `owner_applicable` filter.

## Files

- `internal/auth/` (context auth method)
- `internal/server/middleware/{auth,cfaccess,absauth}.go`
- `internal/repairs/{fixer,engine,owner}.go`, plus tests
- `internal/undo/restorable.go`, `internal/audiobooks/revert.go`, `internal/database/pebble_store_activity.go` (prune keep)
- `internal/plugins/maintenance/{fragment_consolidation_fixer,retire_into,repairs_ops}.go`, plus tests
- `internal/server/handlers/repairs/handler.go`, `internal/server/wire_repairs_routes.go`, plus tests
- `web/src/components/review/RepairsPanel.tsx` (+ test), API client types
- `changelog.d/` fragment (no header)

## Steps

1. Auth method enum and middleware binders, with tests.
2. Engine: row fields, grants, approval, `RunApply`/`applyOne` owner path, journal type, with tests.
3. Fixer: eligibility, owner row, replan, Apply owner path, with tests.
4. Handler, route and op wiring, with tests (API key 403, session 202, `/operations/v2` forged params refused).
5. UI button, confirm dialog and vitest.
6. Gates:
   - `go build ./...`
   - `go vet ./...`
   - `make mocks-check`
   - race tests on `repairs`, `server` and `maintenance`
   - vitest and tsc
7. Changelog fragment, PR.

## Tests

- API-key request → 403, nothing enqueued.
- Forged `owner_apply_row_ids` / grant via `/operations/v2` params → `owner_apply_refused`.
- Session (and CF Access) request → applied: parent unchanged, fragment retired, journal has `repair_owner_apply` with the user id.
- Ineligible rows (no content proof, PID, iTunes Media path, listening state or external id, parent in the fragment's group, DW) are not owner-applicable, and refused if requested.
- Bulk apply with the owner row id in `row_ids` → `not_applicable`.
- Resume (`Resume` set) → owner rows refused; a consumed grant can't be reused.
- Op revert restores the fragment.
- Vitest: the button and confirm dialog, no checkbox; tsc clean.

## Rollback

Revert the PR. The plan rows gain fields that old code ignores. Applied rows
are undone with the op revert (`POST /operations/<id>/revert`). The audit row
is a no-op on revert.

## Open risk (owner's call)

`scripts/manage-credentials.sh` gives Claude per-worktree username/password
logins, which produce real sessions. "Interactive session, not API key"
therefore keeps out API-key automation, but not a Claude that logs in with
those credentials. The admin-role requirement narrows it only if those users
are not admins. The options are to exclude `claude_*` users, require an
SSO-only (CF Access) identity, or add a second factor. Owner decides.
