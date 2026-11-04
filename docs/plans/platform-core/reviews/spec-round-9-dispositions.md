<!-- file: docs/plans/platform-core/reviews/spec-round-9-dispositions.md -->
<!-- version: 1.0.0 -->
<!-- guid: a0e20025-da1c-45dd-849f-92f6aa067137 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 9 (confirmation after the advisor pass) — dispositions

Inputs: `spec-round-9-{correctness,data-migration,modules-build,ops-rollback,security}.md`.
Totals: **71 findings: 4 BLOCKER (3 distinct), 27 MAJOR, 40 MINOR.** All accepted; 0 rejected.

The three distinct blockers all come from one gap in the advisor pass. "Keep HEAD's behaviour" was applied to data at
rest, but not to the moments when core now drives HEAD's code: the commit point, route ownership and metric
ownership. The resolutions close that gap. They do not restore any deleted machinery.

## Round-9 resolutions (binding)

- **R9-1 A legacy store's lock spans core's commit, and there is one instance per family.**
  - Generalise `legacy.TasksWriteLock` to one capability, `legacy.WriteLock` (`Lock(keys) (unlock func())`), on every
    legacy store whose HEAD code relies on an in-process mutex: identity (`apiKeyMu`, `inviteMu`, `counterMu`), tasks
    (`opsMu`), and any other the owners find.
  - Core's commit path for a batch that a legacy store staged into is one wrapper,
    `storage.CommitLocked(ctx, b, locks...)`. It holds every named lock from staging through commit.
    `Executor.WriteRuns` and `Stager.WriteRuns` become uses of it. Single-use invites, bootstrap tokens and the
    revoke-versus-touch race therefore behave as at HEAD, with early fix 6 intact, and
    `TestLegacyRevokeRacesTouch_NeverUnrevokes` passes.
  - The kernel builds **exactly one** instance of each legacy store family per process and hands the same instance
    to every user, so every HEAD lock is shared. `TestLegacyStoresAreSingletons` covers this.
  - `ReplaceOpRowsPrefix`'s lock rule is stated once, in 05-tasks. 05-storage cites it.
  - (COR9-1, DATA9-1, DATA9-2, DATA9-7.)
- **R9-2 Every route-conflict set has one owner.**
  - The T0+T1 PR moves the four `GET /api/v1/operations/{id}/{activity,changes,result,results}` routes to core,
    together with `…/operations/v2/{id}`. Each is a ledger-listed core alias over the same handlers, with HEAD's
    permission (`library.view`).
  - A-1's rule 1 (refuse a cross-owner conflict set) then holds at every stage. Rule 7 and its contradicting test are
    deleted (COR9-2).
- **R9-3 Every metric family has exactly one owner.**
  - At S5 the T0+T1 PR deletes ABO's `opsmetrics` emission of every family the core executor emits under ABO's
    namespace (`ops_zombies` and its siblings). The executor records them with the same names and labels.
  - From S5 a new test, `TestABOMetricsGatherNoDuplicateOrMissing`, builds ABO's real recorders together with the
    kernel's Provider. It gathers and checks the full family, label and series set against a HEAD golden.
  - Series identity (label names and values) is part of the golden (MOD9-1, MOD9-2).
- **R9-4 HEAD parity on API keys and integrations.**
  - `LegacyAmbientScopes` are added after the key-scope intersection as well, so a scoped `abk_` key keeps HEAD's
    authenticated-only reach (`/api/v1/auth/me`, `/metrics`) (COR9-3).
  - Creating a legacy-backed integration instance, or setting `auth_ref` to an operator-held secret, requires
    ProtectAdmin (SEC9-6).
  - Early fix 2 in 05-authn §11.1 also fails closed on a nil or empty caller permission set (`apikeys.go:205`), and
    the `todo.d` fragment already says so (SEC9-5).
- **R9-5 HEAD parity for the Basic gate and file-only keys.**
  - On ABO, the Basic gate keys keep HEAD's auth-off write rule, the same as `enable_auth`, so an auth-off operator
    can store the gate in the legacy row and it survives a rollback (SEC9-1).
  - H3 never lets an always-set compose env value (`BASIC_AUTH_ENABLED=false`) override a UI-enabled gate. H3 moves
    only auth-enable precedence. Every other `BelowStored` flag keeps HEAD's precedence until a separate, owner-decided
    cutover (SEC9-2). 09 §9.13 and 05-config §1.12.5 are corrected to match.
  - `Alias.Legacy` is split into `LegacyYAML` and `LegacyBlob`. HEAD's file-only keys (the peer allow-list among
    them) keep HEAD's storage: file only, never stored (DATA9-6).
- **R9-6 One contract for factory-reset confirmation.**
  - The request carries `{kind, confirm: "RESET", delete_library_files?, library_root?, library_file_count?}`.
    The server answers 422 `library_files_confirm_required` when `delete_library_files` is true but the root or count
    does not match a fresh preflight count.
  - The same struct, `storage.ResetRequest`, flows through `Stager.StageReset`, `kernel.stageReset` and
    `legacy.ConfigResetArgs.ResetArgs`. 05-storage owns it, and 05-config, 05-rbac and 05-http-api cite it.
  - A reset journal stopped mid-set-aside always moves back what it moved before it journals `rejected` (DATA9-4).
- **R9-7 The swap journal and crash matrix are consistent and reach ABO.**
  - `restore/<id>/inflight.json` is created before the first rename in every kind and every file (05-storage
    §5S.3.7, §5S.3.16, 05-backup).
  - A restore boot with no P4d steps 2–3 reaches `applied` through one legal edge (DATA9-3, DATA9-5).
  - The crash-matrix harness is a public test kit (`coretest/crashmatrix`). Core runs it over the kernel's writers. ABO
    runs the same kit over its full boot from S2b, using the complete writer table in 06 §6.5.1 (COR9-5).
- **R9-8 Soaks gate content, not markers.**
  - Every PR that ships behaviour for a multi-release stage carries a `Stage-Release: <stage> <n>` commit trailer.
    `check_soak.py` refuses a trailer whose predecessor soak row is missing or younger than 14 days.
  - A soak row records the `binary_id` from `--version-json` running on the reference host. The command verifies
    that the binary is built from a commit on `main` at or after the stage's first PR. Pass criteria: zero
    `restore_*`/boot errors, and no failed done-check in the stage's list.
  - A revert of a flip PR (detected by `Revert` and the trailer of the reverted commit) is always admitted (OPS9-1 to
    OPS9-3).
- **R9-9 Every done-check has an environment.**
  - The four checks that need the committed systemd unit run in a systemd-enabled container (`--network none`) in CI.
    The live ABS-client check (P7 branch B) is an owner checkpoint.
  - The staging binary is identified by the archive manifest's `binary_id`, and `staging-restore` refuses a mismatch.
  - `app.Offline` exposes the staging marker read and write and the core offline commands to ABO's `cmd` (OPS9-6,
    OPS9-8, COR9-6).
- **R9-10 Builds and history.**
  - A required PR job runs `go vet` for `GOOS=darwin` and with `-tags integration` over both modules.
    `TestCoreImportsOnlyCore` reads the tag union (MOD9-3).
  - 12's filter-repo path file lists the moved files' **pre-move** paths with renames, then `core/`. Its dry run
    asserts that a moved file's `git log --follow` reaches its pre-move first commit (MOD9-4).
- **R9-11 Hardening cutovers state their real reach.**
  - H1 and H2 rewrite the live store, then delete the pre-cutover checkpoint and old Pebble files through a forced
    compaction.
  - Their done-checks say plainly that existing archives still hold the old representation. The release note tells
    the operator to rotate secrets and re-issue keys if old archives are a concern (SEC9-3).
  - The guard models every owner whose level H1–H3 raise (DATA9-8).
- **Also binding:** OPS9-4 and OPS9-5 get §9.2.1 rows and release notes. One is S1's staged restore, which is
  restart-bound. The other covers the effect of a restore taken from S4 r2: keys go `inactive` and sessions are
  deleted, as 05-backup choice (b) says.

## Findings

| ID | Sev | Finding (reviewer's title) | Disposition | Note |
|---|---|---|---|---|
| COR9-1 | BLOCKER | HEAD's identity mutexes cannot span the caller's commit, so A-3 reopens early fix 6 and breaks single-use invites and bootstrap from S4 r2 | Accepted (resolution R9-1) | See resolution. |
| COR9-2 | BLOCKER | ADV-1 half-fixed: rule 1 fails the cross-owner component that rule 7 relies on at S5 | Accepted (resolution R9-2) | See resolution. |
| COR9-3 | MAJOR | `LegacyAmbientScopes` is added to roles, and the key intersection removes it, so scoped `abk_` keys lose HEAD's authenticated-only routes | Accepted (resolution R9-4) | See resolution. |
| COR9-4 | MAJOR | The A-17 confirmation contract differs across four files, and `stageReset`/`Stager`/`ResetArgs` cannot carry it | Accepted (resolution R9-6) | See resolution. |
| COR9-5 | MAJOR | ADV-4 half-fixed: the crash matrix lives in core and cannot exercise ABO's boot writers, and the writer table is incomplete | Accepted (resolution R9-7) | See resolution. |
| COR9-6 | MAJOR | `app.Offline` exposes only module handles, so ABO cannot write or read the staging marker (A-10) or reach core's offline commands | Accepted (resolution R9-9) | See resolution. |
| COR9-7 | MINOR | P−1 is deferred, but four files still run it | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| COR9-8 | MINOR | The `legacy.TasksWriteLock` contract names a struct as the capability holder and leaves the other three stores open | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| COR9-9 | MINOR | 03 contradicts itself on two A-era rules | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| COR9-10 | MINOR | `legacy.AuthAtomWriter` is missing from the one list of legacy identifiers | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| COR9-11 | MINOR | Config is called "exclusive", yet A-2 makes it routed on ABO | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| COR9-12 | MINOR | The reset set-aside destination and the failure rule differ between 05-storage and 06 | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| COR9-13 | MINOR | `restore/pending.json` has two writers and no shared type | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| COR9-14 | MINOR | 04 and 05-ai disagree on who emits HEAD's unprefixed AI families, and `Observer.Attempt` cannot reproduce their semantics | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| COR9-15 | MINOR | The open-checkpoint offline finish must commit `OpenOffline`'s close batch, which `OpenOffline` refuses | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| DATA9-1 | BLOCKER | A-3 as applied to 05-authn deleted the compare-and-set that HEAD's identity locks need once writes stage into a caller's batch | Accepted (resolution R9-1) | See resolution. |
| DATA9-2 | MAJOR | HEAD's locks are per instance; the spec builds several instances and never requires them to share locks | Accepted (resolution R9-1) | See resolution. |
| DATA9-3 | MAJOR | `inflight.json` timing contradicts itself, and the documented override lands an older build on a half-swapped directory | Accepted (resolution R9-7) | See resolution. |
| DATA9-4 | MAJOR | a reset journal mid-set-aside may be rejected without a move-back | Accepted (resolution R9-6) | See resolution. |
| DATA9-5 | MAJOR | no legal journal path to `applied` for a restore boot without P4d steps 2–3 | Accepted (resolution R9-7) | See resolution. |
| DATA9-6 | MAJOR | `Alias.Legacy` conflates the YAML key with the blob field; HEAD's file-only keys have no defined storage, and the peer allow-list is lost on rollback | Accepted (resolution R9-5) | See resolution. |
| DATA9-7 | MAJOR | `ReplaceOpRowsPrefix`'s lock rule contradicts itself across 05-storage and 05-tasks | Accepted (resolution R9-1) | See resolution. |
| DATA9-8 | MAJOR | H1–H3's one-way property rests on owners the guard does not model | Accepted (resolution R9-11) | See resolution. |
| DATA9-9 | MINOR | machine 4 omits the R8-2 transition, and I5 forbids a documented verdict | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| DATA9-10 | MINOR | the list of new core rows written on ABO is incomplete | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| DATA9-11 | MINOR | 09 S2b's reset text is stale against 06 and 05-storage | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| DATA9-12 | MINOR | references to the deferred P−1 boot check are still live | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| DATA9-13 | MINOR | §9.2.1 is "the one complete list" but omits the replaced-store and lost-coverage costs | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| DATA9-14 | MINOR | staging a pre-S0 archive: 09 and 05-storage disagree | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| DATA9-15 | MINOR | drift | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| SEC9-1 | MAJOR | An auth-off operator's Basic gate cannot survive a rollback | Accepted (resolution R9-5) | See resolution. |
| SEC9-2 | MAJOR | H3 turns off a UI-enabled Basic gate on compose installs | Accepted (resolution R9-5) | See resolution. |
| SEC9-3 | MAJOR | H1 and H2 cannot meet their "nothing left" done-checks | Accepted (resolution R9-11) | See resolution. |
| SEC9-4 | MAJOR | A-17 is specified three incompatible ways | Accepted (resolution R9-6) | See resolution. |
| SEC9-5 | MAJOR | Early fix 2 leaves the empty-effective-set bypass open | Accepted (resolution R9-4) | See resolution. |
| SEC9-6 | MAJOR | Creating a legacy-backed instance, or setting `auth_ref`, is not ProtectAdmin | Accepted (resolution R9-4) | See resolution. |
| SEC9-7 | MINOR | Three statements of H3's scope | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| SEC9-8 | MINOR | H1 drops the ambient scopes without carrying them over | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| SEC9-9 | MINOR | The role-name check inventory is incomplete | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| SEC9-10 | MINOR | A real tightening is recorded as "no change" | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| SEC9-11 | MINOR | Dangling and inconsistent references | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| SEC9-12 | MINOR | The interlock makes a pre-S0 archive unstageable | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| SEC9-13 | MINOR | Nothing selects HEAD's auth-off rule in code | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| SEC9-14 | MINOR | The early-fix tracking entries drift from §11.1 | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| OPS9-1 | MAJOR | The soak gate checks only the PR that changes a marker, so the content of the next release can merge and auto-promote before the soak | Accepted (resolution R9-8) | See resolution. |
| OPS9-2 | MAJOR | A soak row is not bound to the code it soaked, and its evidence check has no pass criteria | Accepted (resolution R9-8) | See resolution. |
| OPS9-3 | MAJOR | `check_soak.py` refuses a revert of a flip PR, so a bad flip cannot be backed out of `main` | Accepted (resolution R9-8) | See resolution. |
| OPS9-4 | MAJOR | S1's staged restore has rollback consequences and a user-visible change that no §9.2.1 row or release note carries | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| OPS9-5 | MAJOR | From S4 r2 a restore disables every API key and deletes every session; row 20's deletion removed the only release note, and 09:156-158 now says such a restore costs nothing | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| OPS9-6 | MAJOR | A-12 left done-checks with no environment: four need systemd running the committed unit, and one needs a network-reachable ABS client | Accepted (resolution R9-9) | See resolution. |
| OPS9-7 | MINOR | Early fix 5 locks out an install that has the Basic gate on and an empty saved password; the S0 note gives no recovery | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| OPS9-8 | MAJOR | "The binary of the release that wrote the archive" cannot be identified, and nothing enforces it | Accepted (resolution R9-9) | See resolution. |
| OPS9-9 | MINOR | 09 says a pre-S0 archive is staged with `cur`; 05-storage and 05-backup say `staging-restore` refuses it | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| OPS9-10 | MINOR | `compat-rehearsal` never runs on S2b r2, S5 r2–r7 or the floor release | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| OPS9-11 | MINOR | S2's `TimeoutStopSec=30` applies to older builds after a rollback; HEAD's 30 s drain plus store close can exceed it | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| OPS9-12 | MINOR | Ops records go into the embedded compat ledger, so every one of them becomes a code change | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| OPS9-13 | MINOR | The 35-minute CI budget has no slack, its remedy conflicts with required checks, and its p50 is measured only once | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| OPS9-14 | MINOR | The release-ID grammar is not closed (spelling, "…", bootstrap case) | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| OPS9-15 | MINOR | Stale cross-references: the S4 overview omits row 3, and the owner-checkpoint list names S8 instead of S1 | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| OPS9-16 | MINOR | ADV-11 residue: `TestUpgradeFloor_RefusedBelowBooks66` and P13 "books ≥ 66" | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| OPS9-17 | MINOR | S8 updater move: ABO's source wiring is untested, row 2 mislabels `core.updater.*`, and S8's new state is unlisted | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| OPS9-18 | MINOR | `prerelease.yml`: bookkeeping-only PRs mint RCs, and S0's added Docker build is redundant | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| MOD9-1 | BLOCKER | From the S5 (T0+T1) release, ABO's `/metrics` serves two samples of `audiobook_organizer_ops_zombies{}` and every scrape fails. | Accepted (resolution R9-3) | See resolution. |
| MOD9-2 | MAJOR | Series identity is not kept, and the normative check cannot see it. | Accepted (resolution R9-3) | See resolution. |
| MOD9-3 | MAJOR | The guards that remain do not compile every tag set, so the A-4 claim is false for darwin and `integration`. | Accepted (resolution R9-10) | See resolution. |
| MOD9-4 | MAJOR | As written, the history split drops all pre-move history of every moved file, and no check notices. | Accepted (resolution R9-10) | See resolution. |
| MOD9-5 | MINOR | "Every commit of a move PR builds" is now load-bearing, because A-4's history split needs the move in commit 1 and `go fix` later, but nothing enforces it. | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| MOD9-6 | MINOR | `TestCoreHoldsNoAppKeyNames` has no landing stage. | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| MOD9-7 | MINOR | Dangling authority. | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| MOD9-8 | MINOR | Pre-A-5 residue, and contradictions on metric names. | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
| MOD9-9 | MINOR | A-6 contradiction on what moves. | Accepted | Reviewer's concrete fix applied as written, unless a round-9 resolution supersedes it. |
