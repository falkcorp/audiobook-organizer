<!-- file: docs/plans/platform-core/reviews/spec-round-6-dispositions.md -->
<!-- version: 1.0.0 -->
<!-- guid: 09a5fd24-8db7-4efc-8333-427a35fa76f4 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 6 — dispositions

Inputs: `spec-round-6-{correctness,data-migration,frontend,modules-build,ops-rollback,security,simplicity}.md`.
Totals: **80 findings — 2 BLOCKER, 30 MAJOR, 48 MINOR.** All accepted (one as unverified,
with a verification step in the plan); 0 rejected. Distinct blockers per round: 20 → 10 → 9 → 6 → 5 → 2.

## Round-6 resolutions (binding)

- **R6-1 Every write to a serialized family is checked at commit.** Progress ticks and chunk-ledger commits, not only
  `Transition`/`Insert`/`Advance`, use a conditional write that fails (and is dropped, not retried blindly) when the run's
  state changed since it was read. A tick can never revert a cancel. Native stores follow the same rule (DATA6-1).
- **R6-2 `internal/operations/state` stays in ABO**, next to `internal/books/taskstore`; core's run states map to it,
  and opsgen's output claim is corrected. Counts become 49 leave / 5 new / 169 remain (MOD6-1).
- **R6-3 One legacy package outside the API promise.** Every identifier that exists only to serve ABO's legacy stores
  (optional capability interfaces, their conformance suites, `LegacyStores` fields, and `library.RootSource` unless it gains
  a native implementation) lives in a single core package excluded from the API-stability gate, like `kernel`. Removing
  it at cutover is therefore not a breaking change (SIMP6-2, SIMP6-3).
- **R6-4 A staged restore is not a foreign open.** The restore-pending record identifies the boot that applies it; that
  boot does not append a gap line or force key confirmation (DATA6-2).
- **R6-5 API-key writes never race.** Books' legacy store writes `last_used`/`use_count` under the same lock as every other
  `apikey:` writer (or to a separate key), and the same fix ships early as the sixth hotfix (SEC6-3).
- **R6-6 Turning auth on is always possible.** The auth-enable switch is exempt from "stored value wins": an operator's
  env/flag that **enables** auth always takes effect, so a stored `false` can never keep auth off (SEC6-1).
- **R6-7 Staging copies are inert.** The staging procedure strips every secret (encrypted and plaintext) and every
  session token, and disables outbound integrations and schedules, before the copy boots (OPS6-4).

## Findings

| ID | Sev | Finding (reviewer's title) | Disposition | Note |
|---|---|---|---|---|
| COR6-1 | MAJOR | `rbac.LegacyAtom` lands at S4, but every `s.perm` entry of the S2 legacy manifest must already carry it | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| COR6-2 | MAJOR | COR5-3 half-fixed: the `owed` duty ledger has no API, so none of its consumers can read it or delete its entry "in the batch that performs i | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| COR6-3 | MAJOR | HEAD's Start-time identity writers (`seedRolesAndTokens`) have no home in the dark S4 r1 release | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| COR6-4 | MAJOR | The seed-step rule runs a seed after the later steps that reshape its rows have been stamped applied | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| COR6-5 | MAJOR | HEAD's settings load writes, but the legacy `Open` that runs it is specified to write nothing; read as "read transforms", a stored `embed_co | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| COR6-6 | MINOR | `journal.StorageSpec()`'s `Serialized` list differs between 06 and 05-backup | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| COR6-7 | MINOR | COR5-12 residue: 06 names two batch-rule exceptions, 05-storage and 05-metadata three | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| COR6-8 | MINOR | `config.LegacyOpener` lands "from S2" in 05-config but at S2b in 06 | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| COR6-9 | MINOR | Public SDK sub-packages used by owner files are missing from 03's package tree and R4 layer list | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| COR6-10 | MINOR | 03 §3.7's boot diagram and 05-authn use stale phase and step names | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| COR6-11 | MINOR | hello's `main` calls `signal.NotifyContext`, but R5 forbids `signal.Notify` in core and `TestNoGlobals` scans `core/examples/...` | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| COR6-12 | MINOR | 09 S2b says books' legacy migrations run "before claims are enforced", but 05-storage runs them on claim-checked handles | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| DATA6-1 | BLOCKER | DATA5-1 half-fixed. Only `Transition`/`Insert`/`Advance` get commit-time checks. Progress ticks, the chunk-ledger commit and the other run w | Accepted (resolution R6-1) | See resolution. |
| DATA6-2 | MAJOR | Every staged restore (archive, revert, checkpoint) reads as a foreign open and a behind lineage. P4d step 2 then appends a `gap` line before | Accepted (resolution R6-4) | See resolution. |
| DATA6-3 | MAJOR | A restore whose hook fails swaps back, but `served_boot`, `backup.location.json` and the `restore` line still describe the restored store. T | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| DATA6-4 | MAJOR | SEC5-6 half-fixed. The content marker is written once per boot, so it misses the common case: an archive taken during the last journaling bo | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| DATA6-5 | MAJOR | DATA5-7 half-fixed. 06's normative P4d step 2 and 05-authn §4.14/§6 still classify "generation differs ⇒ replaced". 06 omits `served_boot`.  | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| DATA6-6 | MAJOR | Factory reset from S2b is unspecified. HEAD's `Reset` cannot run under claims: strict mode gives a 500, the shadow release wipes `c/` and th | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| DATA6-7 | MAJOR | The W gate restarts at `since`, the start of the unrecorded interval, so the days a rollback build ran count as clean. The restart point is  | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| DATA6-8 | MAJOR | Coalesced last-use writers write whole records through `PutSession`/`PutAPIKey` with no precondition. A flush after a logout or revoke re-cr | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| DATA6-9 | MINOR | `gen` is one owed entry per owner, cleared by the first index's rebuild enqueue, so other derived indexes accept stale artifacts. A crash af | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| DATA6-10 | MINOR | DATA5-13's install-id inheritance contradicts 05-backup's DATA4-15 error row and test. K22 names the wrong source (journal header, not the l | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| DATA6-11 | MINOR | DATA5-11 residue: 05-config says `Open` runs the load without the corrupt-secret repair, but 05-storage row 7 and 09 S2b call that repair a  | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| DATA6-12 | MINOR | `CheckedWrites` timing: the flags get no discovery period, the "secret save persists" check sits at S2b where no flag exists, and no S3/S7 c | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| DATA6-13 | MINOR | 05-authn's replaced-store rule depends on "the state directory recorded an r2 boot", but no file records that | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| DATA6-14 | MINOR | Drift after DATA5-4/R5-6: 03 (users confirmable), 09 §9.8 (every stale extension suspended), 05-tasks ("default guarded projection"), and 05 | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| SEC6-1 | MAJOR | The documented way to turn auth on with auth off (`enable_auth=true` in env or the file) is a `BelowStored` alias under R5-1(a), so a blob h | Accepted (resolution R6-6) | See resolution. |
| SEC6-2 | MAJOR | Legacy `abk_` keys acquire extension records (restore fallback, `needs_confirmation` stamp). From then on they are neither `LegacyFullScope` | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| SEC6-3 | MAJOR | Lost revocation: HEAD's unlocked `SetAPIKeyStatus` races `TouchAPIKeyLastUsed`'s whole-row write-back, and the spec's last-use write in book | Accepted (resolution R6-5) | Confirmed in code: `TouchAPIKeyLastUsed` holds `apiKeyMu` (`internal/database/pebble_store_auth.go:1284-1302`) while `SetAPIKeyStatus` (`:1226-1247`) does not; filed as a hotfix in `todo.d/2026-11-04_platform_core_followups.md`. |
| SEC6-4 | MAJOR | SEC5-5 half-fixed: the boot lineage is not a foreign-open signal, and a failed HEAD sidecar write stays undetected while 05-authn §4.3 and K | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| SEC6-5 | MAJOR | Integration-bound legacy env names (e.g. `OPENAI_BASE_URL`) are `BelowStored` under 05-config §1.12.4 but a locked, private-network-trusted  | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| SEC6-6 | MINOR | The content-marker residual is misstated: every archive made during the last journaling boot carries the current pair. A fix makes any core- | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| SEC6-7 | MINOR | "The state directory recorded an r2 boot" (05-authn §3.4) names no record | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| SEC6-8 | MINOR | The lineage and content-marker files are keyed by state directory, not by store, so two stores sharing one state directory trip each other's | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| SEC6-9 | MINOR | `core.http.basic_gate.username` is missing from 05-http-api §10, although 05-config maps `basic_auth_username` to it and stage 8 compares it | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| SEC6-10 | MINOR | Whole-volume snapshot rollback (store, backup directory and fallback state directory in one volume) reverts the journal and both markers tog | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| OPS6-1 | MAJOR | — The floor release ships at S5, but only the S8 updater checks the stepping stone; the S0–S7 updater and the `deploy` recipe swap it in unc | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| OPS6-2 | MAJOR | — The S8 process-gauge aliases target `go_goroutines`/`go_memstats_alloc_bytes`, which `metrics.AddAliases` refuses with `ErrAliasNotCore`,  | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| OPS6-3 | MAJOR | — Every per-stage series-name done-check is a live scrape, which lists only families that have recorded a sample since boot. HEAD already ha | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| OPS6-4 | MAJOR | — The staging procedure claims no production credential reaches staging, but key regeneration drops only the four encrypted rows. The restor | Accepted (resolution R6-7) | See resolution. |
| OPS6-5 | MINOR | — Exit 78: the plist change does not stop launchd restarting it, `deploy-wait-ready.sh` cannot see an exit status, and 09 omits 05-updater's | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| OPS6-6 | MINOR | — The Basic gate's health token and test have two names each | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| OPS6-7 | MINOR | — OPS5-1 residue: 09 dates `BelowStored` "from the S3 façade" and extends it to flags. 05-config applies it from S2 and keeps C3 for flags,  | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| OPS6-8 | MINOR | — "One flip PR per stage" is defined for only five stages; S2 ships three behaviour-changing releases; S2b's rehearsal gate lands on the rel | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| OPS6-9 | MINOR | — The §9.2.1 "one complete list" still misses three rollback consequences that the revision introduced or kept | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| OPS6-10 | MINOR | — The image job's volume preparation command does not run against the ABO image | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| OPS6-11 | MINOR | — The S0 done-check requires `TestS2bOpensStoreWrittenByS0`, which needs core storage from S2b | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| MOD6-1 | BLOCKER | Taskstore cannot compile at T0+T1, and opsgen's S5 claim cannot hold. Taskstore keeps HEAD's run-row code "unchanged in format", and that co | Accepted (resolution R6-2) | See resolution. |
| MOD6-2 | MAJOR | The taskstore split breaks the package's tests, and no gate or count sees it. 396 of `internal/database`'s 400 test files are in-package (`p | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| MOD6-3 | MAJOR | As written, the embed-context check fails on HEAD and on the S0 PR that adds it. Under `embed_frontend`, which is in the tag union the check | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| MOD6-4 | MAJOR | MOD5-4 half-fixed: a third persisted ABO format in a moved engine file has no store. HEAD persists the scan stand-down as a JSON setting row | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| MOD6-5 | MAJOR | The two release callers of a reusable workflow with a frontend half are missing from the MOD4-10 precondition. `release-prod.yml:45-58` and  | Accepted as unverified | The shared release workflow lives in another repository this session cannot read; the plan adds a verification step (read the reusable workflow, confirm the claim) before the task it affects. |
| MOD6-6 | MAJOR | A core-written definition carries an ABO op ID, so R1(c)'s test fails at S8, and (h) cannot see it beforehand. `diagnostics.export` is in `o | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| MOD6-7 | MINOR | The generator and its test twin use different match rules, and the test row is stale. 04 (h) fails a literal that is "an ABO setting or pref | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| MOD6-8 | MINOR | The cited cycle does not exist. The three middleware files sent to `internal/cfaccess` import `internal/auth`, `internal/oauth`, `internal/l | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| MOD6-9 | MINOR | The table claims to be "computed from its moved files", but taskstore's import list misses `internal/operations/state` (MOD6-1). Its importe | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| MOD6-10 | MINOR | 04 §8.1 S5 table row `mediacore/internal/scheduler` ← `internal/scheduler` (stage S5, 5 importers); 04 §3 row and §8.6 (`internal/scheduler` | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| MOD6-11 | MINOR | Two packages named `configstore` play opposite roles (core's native store and committer; ABO's legacy blob store), and 05-config uses the ba | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| MOD6-12 | MINOR | 05-tasks:514, :1570 (typed `ModifyBook*` → `books/taskwrite`) vs 04 §2.4/§8.6 (`internal/books/writer`, the `opswriter` rename; Writer contr | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| MOD6-13 | MINOR | Both checks need a Go type-checker, which a Python script cannot run, and the spec names no Go helper. Under `tools/cmd/…` in the root modul | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| MOD6-14 | MINOR | 03 §3.5.1 item 1 ("`internal/books/contract` … at S5, when every source of contract's content (`pkg/ops`, `pkg/plugin/sdk`, `internal/operat | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| FE6-1 | MAJOR | `checkBuild` compares against "the bundle's define `MEDIACORE_BUILD_ID`", but from UI-0 it lives in `@mediacore/api-client`, which Vite libr | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| FE6-2 | MINOR | The preset injects the theme boot script, but `mediacore(options)` has no options: the boot script cannot know ABO's `app-theme-mode` key or | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| FE6-3 | MINOR | S0's guard writes binary-ID `build` fields and S0's `--version-json` prints `binary_id`, but `app.BinaryID` is "computed by the kernel befor | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| FE6-4 | MINOR | `--version-json` fields: 09 lists `version, commit, ui_build_id, binary_id`. 05-updater's self-test requires `app`. `commit` has no source w | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| FE6-5 | MINOR | "An invite minted on S9 stays valid after a rollback to S4–S8" includes S4 r1, which is dark and has no reader. One S4 test also has two nam | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| FE6-6 | MINOR | `legacyStorageKeys?: Readonly<Record<string, string>>` drops the type check: a misspelt shell name silently resets that preference at S9 | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| FE6-7 | MINOR | The UI-build-ID rule takes `BUILD_ID` "when the caller set it": an empty value and an unsafe charset are not covered | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| FE6-8 | MINOR | `checkBuild` has two signatures: `checkBuild(response)` in 05-http-api §7 and 07 §7.5.2/§7.8; `checkBuild(response, { shell })` in the 07 ru | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| SIMP6-1 | MAJOR | The config "basis" extension-record mechanism (`config.Basis`, `BasisClass`, `config.Suspender`, `Store.Confirm`, `AttachReport.Suspended`,  | Accepted | The config "basis" mechanism moves under a "Deferred-build members" heading in 05-config; nothing in S0–S10 builds it. |
| SIMP6-2 | MAJOR | The cutover is designed to remove optional capability interfaces, their conformance suites and `module.LegacyStores` fields. Under 12 §12.4. | Accepted (resolution R6-3) | See resolution. |
| SIMP6-3 | MAJOR | `library.RootSource` has no native implementation, so the whole frozen SDK interface (with `Covers` as a member, `librarytest.RootSourceSuit | Accepted (resolution R6-3) | See resolution. |
| SIMP6-4 | MINOR | SIMP5-4 residue. One operation, `Extension.Confirm`, is reached through four capability shapes. `storage.ExtConfirmer`'s doc ("A store witho | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| SIMP6-5 | MINOR | The `Covers` default is stated two ways (03: the *native half* lacks it; owners: a *legacy* store without it covers everything), the tests a | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| SIMP6-6 | MINOR | Capability conformance follows three patterns (a separate suite, cases inside the store suite, the implementor's own test). `storage.Coverin | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| SIMP6-7 | MINOR | Legacy-only data in frozen native types is grouped in `integrations.StoredInstance.Legacy *LegacyRecord` (nil for native), but sits directly | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| SIMP6-8 | MINOR | The deferred-members tables put `—` or an empty cell in the first column, which 03's generator self-test refuses. `module.Registrar.Download | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| SIMP6-9 | MINOR | 04 names the S2 capability for `Changed` as `CommitObserver` (it is `ExternalWriter`), puts `CommitObserver` at S2 (it is S3), and omits `Su | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| SIMP6-10 | MINOR | 06's `LegacyStores` comment lists two exceptions to the batch rule. 05-storage and 05-metadata have a third: `providerhttp.GateStore`. | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
| SIMP6-11 | MINOR | 03 says API keys and users are re-confirmed through "the audited re-enable". 05-authn says the user record has no Widening field, is never s | Accepted | Reviewer's concrete fix applied as written, unless a round-6 resolution supersedes it. |
