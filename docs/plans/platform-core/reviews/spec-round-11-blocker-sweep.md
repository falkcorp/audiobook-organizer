<!-- file: docs/plans/platform-core/reviews/spec-round-11-blocker-sweep.md -->
<!-- version: 1.0.0 -->
<!-- guid: d503ba0b-e5ba-4fd1-9f09-6cfd51e079eb -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 11: whole-spec blocker sweep

Lens: whole-spec blocker sweep (ID prefix BLK11). I walked 09's stages S0–S10, then 05-tasks T0–T8, then 09 §9.13
H1–H3. At each one I asked four questions. Would ABO fail to build or boot? Would it lose or corrupt data? Would it
open a security hole relative to HEAD? Would it become undeployable or un-rollbackable? I checked every claim against
HEAD at `748fd787a`. I read the R10, R9 and advisor dispositions first and treated them as binding.

## Result

**0 BLOCKER. 1 MAJOR, a regression introduced by the R10-7 revision.** No new MINORs are filed (see "Noted, not
filed").

| ID | Sev | Spec file and section | Finding |
|---|---|---|---|
| BLK11-1 | MAJOR | 09 §9.2.1 "Soaks" (`09:194-212`, `:239`), §9.13 (`09:1689-1690`), 04 §8.1 row UI-0 (`04:914`) | R10-7 half-fixed. The trailer and soak rules cover only S0–S10, so UI-0 has no legal trailer, and H1–H3 can never record the soak row that §9.13 requires them to ship behind. |

## BLK11-1 — MAJOR — R10-7's soak machinery cannot express UI-0 or H1–H3, and its level-0 criterion refuses every hardening release by construction

**Root cause.** R10-7 made two rules universal. The `Stage-Release:` trailer is now required on "every PR that
touches a package 04 maps to a stage whose flip has not merged" (`09:210`), with presence checked mechanically by check
(d). The soak pass criteria now apply "for every release", including "every owner at level 0
(`--print-storage-levels`)" (`09:239`); that clause was introduced by the round-10 revision. But the two closed grammars
the rules read stop at S10:
- release IDs `^(S0|S1|S2r[1-3]|S2br[12]|S3|S4r[12]|S5r…|floor|S6|S7|S8|S9|S10)$` (`09:194-199`);
- trailer stages `S0 S1 S2 S2b S3 S4 S5 floor S6 S7 S8 S9 S10` (`09:206-208`).

Two stages the spec schedules fall outside them.

1. **H1–H3 cannot ship behind a soak row, and their soak rows can never pass.**
   - 09 §9.13 says each hardening cutover "ships behind its own soak row (A-9)" (`09:1689-1690`). There is no `H1`,
     `H2` or `H3` release ID. So a cutover's flip PR cannot change `release:`: check (c) refuses "an ID or trailer
     outside its grammar".
   - Extending the grammar is not enough. `--print-storage-levels` prints the **build's supported vector** (05-storage
     §5S.3.7 "Fast-exit flags", `05-storage:1201`). H1 raises `books`, plus `core` when the identity families move;
     H2 raises `books` and `core`; H3 raises `books` (§9.13 table, "Level raised"). Every binary from H1 on therefore
     fails "every owner at level 0", and `soak_record.py` refuses to print the row.
   - Check (a) refuses a flip without its predecessor's current row. So H2, H3 and any later flip can never merge
     once H1 has shipped.
   - The alternative is to ship the cutovers with `Stage-Release: none`. That drops the 14-day soak on exactly the
     three steps that are one-way and floored, the ones `make rollback` cannot undo. A-9 exists for those steps.
2. **UI-0 has no legal trailer.**
   - 04 §8.1 maps packages to UI-0: `@mediacore/api-client` and the Docker frontend stage that copies `core/web`
     (`04:914`).
   - UI-0 never flips (09 UI-0 "Flip PR: None"), so its packages always belong to "a stage whose flip has not
     merged". It has no release ID by design (`09:198`), and `UI-0` is not a `<stage>` value.
   - Every UI-0 PR must therefore either declare `Stage-Release: none` (false against 04's rows, which `09:213` says
     review checks) or carry a value check (c) refuses. UI-0 must ship before S4 (`09:984-986`).
   - Mechanically `none` passes, so this does not block S4 today. But the rule that is meant to stop undeclared
     stage content (OPS10-1) has a hole the size of a whole stage.

**Fix (09 §9.2.1 "Soaks"; §9.8 row 36; 14 K20 if it restates the grammar).**
- Add `UI0` to the release-ID grammar, unordered against `S2br1…S3` as UI-0 is (§9.3). Add `UI-0` to the trailer's
  `<stage>` list. Check (b)'s predecessor for a `UI-0 1` trailer is `S2r3`'s row, because UI-0 runs after S2.
- Add `H1 H2 H3` to both grammars, ordered after `S10` in the owner's chosen order. `H2` and `H3` may share a release,
  so allow `H2H3` or let one release carry both trailers.
- Replace "every owner at level 0" with "every owner at the level the release declares, read from
  `--print-storage-levels` and equal to the store's stamps". S0–S10 declare 0 everywhere (`09:315`). An H release
  declares the levels its §9.13 row raises. `scripts/soak_record.py` reads the expected vector from the stage record
  or from a committed `ci/stage_levels.txt`.
- Add a `test_check_soak.py` case for each: a `UI-0 1` trailer is admitted after `S2r3`'s row; an `H1` flip is
  admitted after `S10`'s row; an `H1` soak row with `books` at the raised level passes.

## What I checked (no blocker found)

**Locks held across commit, in rank order (R9-1, R10-4, R10-5).**
- I re-derived HEAD's nesting from every `Lock()` site of the identity mutexes at `748fd787a`:
  - `pebble_store_auth.go:209-211,402-404,591-593,668-670,745-760,1566-1568`;
  - `user_merge.go:508-529,659-661,760-762`;
  - `pebble_store_verified_email_backfill.go:321-323`;
  - `internal/server/bootstrap.go:212`.

  Every nest HEAD takes ascends in the 05-storage §5S.3.2 rank table (merge 100 → bootstrap 110 → invite 120 → users
  130 → sessions 140 → API keys 150). `userWriteMu` and `sessionWriteMu` are package-level globals, so they are shared
  across instances anyway.
- `configBlobSaveMu` (`persistence.go:1897,1940-1944`) nests nothing in HEAD, and `SaveConfigToFile` takes no lock,
  so the gap-fill step running inside the hold (R10-5) adds no cycle. 05-config's file mutex is a leaf.
- The books-only mutexes `guestcftoken:` and invite-delivery take `inviteMu` alone, on the one shared instance.
- `opsV2TimelineTrustMu` and `opChangeJournalMu` (`pebble_store.go:195-210`) guard Close-ordering and the
  opchange journal, not run rows that core stages. The `opv2:open:`, `opv2:done:` and `opv2:wait:` index families
  are inside §5S.9.1 row 13.
- The relay's batch commits through `WriteRuns` → `CommitLocked`, and only cursor and run-start writes are staged in
  it (05-events, 05-tasks 5T.3.12). So no in-stage lock is ranked below `opsMu`.
- Offline opens resolve the locks: `kernel.OpenOffline` builds the stores "as step 10 does", and step 10 resolves
  `WriteLocks` (`05-storage:2496-2504`).

**Swap journal and reset (R9-6, R9-7, R10-6).**
- Every machine-1 state has a failure exit. `inflight.json` precedes the first rename. Copy-if-exists is limited to
  the two state files.
- `hook_paths` bound the labelled filesystem without parsing `hook_args`. The legacy aliases set `Kind`,
  `DeleteLibraryFiles` and `LegacyConfirm` only through path values, with the body fields dropped by `Selector`
  (05-http-api §3.1, 06 §6.4.6). `ResetStore` cannot become a factory wipe through a body field.
- Kept-path rule (4) protects a `RootDir` that contains the store. Rollback refuses while a reset is staged or in
  flight, and `ROLLBACK_IGNORE_RESTORE` never passes `restore_inflight`.

**Metrics ownership (R9-3, R10-3).**
- HEAD's exporter uses `WithoutScopeInfo` (`internal/telemetry/telemetry.go:378,446-447`). The two `http_server_*`
  recorders stay disjoint: core records pre-mount refusals with no route, and gin produces no route only for
  `NoRoute` 404 or the SPA's GET 200, which differ by status or method.
- HEAD has exactly four OTel scopes and no duplicated instrument name (`ai`, `aidispatch`, `memdb`,
  `operations`). All client_golang families register in `internal/metrics/metrics.go:302,311`. So the stricter
  `ErrFamilyTaken` admits HEAD's set.
- Core's new `<namespace>_ai_*` and other families do not collide with HEAD's `audiobook_organizer_*` names, e.g.
  `ai_backend_available`.
- `metrics.Shared` requires a Gatherer, as S2 r3 needs.

**Offline opens.**
- `staging-restore`, `staging-scrub`, `users reset-password` and `secrets rotate-derive` open through
  `OpenOffline`. `restore-checkpoint` uses `offlineCloseBatch`.
- None is refused on a store in its normal post-boot state. HEAD's CLI subcommands (`cmd/root.go:102-253`) write
  only books families.

**Staging (A-10, A-12, R10-11).** The `binary_id` chain runs archive manifest → marker → scrub, and a
production store never carries the sidecar. Staging is test infrastructure, and no production path depends on it.

**Soak gate (A-9, R9-8, R10-7).**
- Within S0–S10, checks (a)–(d), the landing-commit rule and the exact-inverse revert rule are consistent with every
  stage table's Flip PR row, including S2b and S6–S8 landing live, and with T0–T8 as `S5r<n>` and `floor`.
- The gaps are BLK11-1's.

**H1–H3.** The checkpoint and old-file deletion runs through compaction, not by path, so it cannot corrupt a live
Pebble directory. R10-2's confinement landed in 05-rbac §6.3 and in the H1 row.

**Security parity at S4 r2.**
- `legacyauth.FromCorePrincipal` refills HEAD's context values (05-http-api §8.3). Handlers that fall back when no
  user is bound, such as `CallingUserID` → `_local` at `handlers/reading.go:69-76`, still see the user.
- HEAD's worker confinement (`middleware/auth.go:265-297`) keeps `/metrics` off confined keys, which matches R10-2.

## Noted, not filed

- **Revert wording.** Check (c) lists "a backwards value" (`09:262`), and the revert rule says "check (c) still
  applies" (`09:271`). Read literally, that refuses every admitted revert. The intent is clear from "although it moves
  the marker backwards" and from §9.8 row 38; exempt the backwards-value clause in (c) explicitly.
- **Reset state name.** 06 P0a's failure edge names the reset state `staged`, but the in-flight rename for a reset
  happens at `set_aside_started`; use one name.
- **`seed` under `OpenOffline`.** 09 S2b lists `seed` among the `OpenOffline` users, while 05-storage §5S.4 says
  step 2 refuses it on an empty store.
- **No mandated backup before H1/H2.** H1 and H2 delete every pre-cutover checkpoint, leaving "the pre-cutover
  backup" as the only way back, but no pre-cutover backup is mandated. Consider a done-check that one exists, since
  S8's pre-update backup is deferred-build.
