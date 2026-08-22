# Tier A candidates — 37 items

## L280 (cb) section: ✅ Second, unrelated Coverage Floor failure — FIXED in this PR
- [ ] `syscall.Kill(os.Getpid(), syscall.SIGTERM)` signals the **entire test binary**, not a
      child. Every test in the package shares that process, so this is a global side effect
      fired from one test. It works today; it is a trap for whoever adds parallelism.

## L283 (cb) section: ✅ Second, unrelated Coverage Floor failure — FIXED in this PR
- [ ] The unconditional `time.Sleep(6 * time.Second)` before the signal is pure wall-clock
      cost paid on every run, in a package that is already the gate's biggest consumer.


## L476 (cb) section: ABS surface — what is still missing after the series/playlist fix
- [ ] **Series DETAIL is still not served.** `/api/series/:id` 301s into the app API, the
      same class of bug that made playlists open empty. It was not fixed alongside
      playlists because populating the series LIST (which the client renders) addressed
      the reported symptom, and claiming `/api/series/` from the redirect is a second
      routing decision that deserves its own change. Upstream also has
      `GET /api/libraries/:id/series/:seriesId`, which sits under the already-reserved
      `/api/libraries/` prefix and therefore needs **no** routing decision at all —
      prefer that route first.

## L697 (cb) section: Follow-up on the op itself
- [ ] `registry.RunItems` label re-render (fixed 2026-08-13) changed shared
      infrastructure used by every op. Progress labels for other ops now advance
      one item later than before — verify none of them assumed the old timing.

## L928 (cb) section: ghcommon reusable-workflow pins are a month apart — decide, don't drif
- [ ] Not done unattended on purpose: this was left for a human on 2026-08-18
      rather than folded into the CI-wiring PR, because verifying a release
      workflow requires actually cutting a release.

Note this is drift, not inconsistency for its own sake — the eight point at
several *different* reusable workflows (`reusable-ci`, `reusable-release`,
`reusable-security`, `reusable-burndown`, `reusable-triage-poll`), so a single
shared SHA is a convention, not a correctness requirement.


## L3718 (cb) section: C716 resolved: the "3,954-book API-vs-store gap" decomposes to 3,953 i
- [ ] **`show_quarantined=true` SHRINKS the list.** A flag that can only
      widen the set returned 41,319 books against the default 63,869.
      41,319 = 41,317 (`is_primary_version=true` count) + the 2 quarantined
      — i.e. with the quarantine exclusion off and no explicit
      `is_primary_version` param, the scan path serves only primary-flagged
      books and silently drops the 22,552-book nil-flag population. Same
      family as the filed nil/false `is_primary_version` divergence
      (`effectiveBoolFieldIndex{Default:true}` vs raw `*bool`); this is a
      second concrete symptom, on the main list path. With an explicit
      `is_primary_version=false` the flag behaves (22,552 with or without
      quarantine).

## L3729 (cb) section: C716 resolved: the "3,954-book API-vs-store gap" decomposes to 3,953 i
- [ ] `is_primary_version=false` answers 22,552 — exactly the known
      nil-flag population size. Establish whether explicit-false books are
      currently 0 in prod or whether the false-filter is returning nils
      (memory says the census counted ~765 explicit-false in one path).


## L4064 (cb) section: ✅ Root cause (2026-08-14, fixed in `fix/author-getter-conformance`)
- [ ] **After this deploys**, drop `skip_author_ids: [46627]` from the repair
      invocation and repair that row. Everything else already applied
      2026-08-14 02:0x: 30 merged, 15 renamed, 0 failures, 145/145 book links
      verified *via the memdb-backed API — the Pebble path was not re-read
      post-apply*. Author 46627 is the ONLY remaining stranded-ampersand row;
      the other two survivors, `&#169` and
      `&#169;2013 by HarperCollinsPublishers`, are the separate HTML-entity
      defect.

**Why this blocked the repair:** the merge path relinks the books it can see and
then DELETES the author row. Run through memdb it would relink 0 books for 46627
and delete the author anyway, leaving the 2 Pebble junction rows pointing at an
author id that no longer exists — the orphaning hazard H8 documents on
`maintenance.author-split-scan`. The row was excluded by id for the 2026-08-14
apply rather than papered over with a heuristic. With the fix deployed, the warm
path sees those 2 links and the merge relinks them before deleting.


## L4081 (cb) section: ✅ Root cause (2026-08-14, fixed in `fix/author-getter-conformance`)
- [ ] **CORRECTION to `20260814-matcher-writeback-background-job.md`: the
      blocking half of the matcher is the metadata FETCH, not the file
      write.** Owner (2026-08-14): the write side is already backgrounded;
      the fetch side is effectively a singleton and must (1) be dispatched
      as a background operation visible in the ops system, and (2) fan out
      across books — WITH staggered start delays/jitter per request so the
      fan-out doesn't flood the metadata providers all at once.
      Evidence so far: `metafetch.chainMu` is NOT the singleton — it only
      guards cached-chain construction, and the chain is documented safe
      for concurrent worker pools (per-source rate limiter + circuit
      breaker carry their own mutexes). Look instead at (a) the bulk
      dialog's one-book-at-a-time interactive flow, and (b) whatever the
      matcher's search endpoint serializes server-side. Design notes:
      bounded worker pool per the concurrency mandate sized for
      NETWORK-bound work (small fixed concurrency, e.g. 3-4), plus
      per-worker start jitter (e.g. 250-500 ms spread) layered UNDER the
      existing per-source limiters so providers see a ramp, not a burst;
      progress per book surfaces through the op reporter so the UI polls
      the op instead of holding a request open (kills the false sign-out
      symptom at the root).


## L4470 (cb) section: Search placeholder hint missing when navigating to All Books from Fini
- [ ] **Backfill legacy operation rows stuck at `pending`.** #2483 fixed the forward path
      (terminal status now mirrors from `publishOpTerminal`), but rows created before it
      stay frozen at whatever status they started with. `/api/v1/operations` shows several
      on page one alone (`archive-sweep`, `trash-cleanup`, `temp-file-cleanup`,
      `cleanup_activity_log`, `maintenance-window`, `purge-deleted`). Needs a one-off
      supervised pass — it rewrites historical records, so run it watching, not unattended.


## L6394 (cb) section: Missing-file lane — follow-ups after the report-only change (#2614)
- [ ] **`scan-import-organize.spec.ts` (7 failures) — Settings tab deep-link
      fixed, but that was NOT the blocker. Count unchanged at 7.**
      Investigated 2026-08-09.

      **Applied and kept (correct, but insufficient):** the tests navigated to
      `/settings` and immediately clicked "Add Import Path". Settings is tabbed
      now and defaults to **Library**; the button is rendered by
      `components/settings/PathsSettingsTab.tsx:229`, mounted from
      `pages/Settings.tsx:832`, i.e. only when the **Paths** tab is active.
      `tabFromHash()` (`Settings.tsx:96`) maps a URL hash to a tab index via
      `TAB_KEYS`, so `'/settings#paths'` is the app's own supported deep link.
      All four navigations now use it.

      **It did not help — still 7 failures**, all still timing out on
      `getByRole('button', { name: 'Add Import Path' })`. So the Paths tab is
      not rendering, or the Settings page is not reaching a usable state at
      all. The change is kept because it is verifiably more correct than
      `/settings`, not because it fixed anything.

      **Next step, and do this before writing any code:** capture the DOM for
      one of these failures specifically. `test-results/` was dominated by
      other tests' directories, so the Settings page snapshot was never
      actually read — which, given that reading the snapshot has found every
      real cause in this effort, is the obvious gap. Run just this spec and
      open `test-results/<dir>/error-context.md` for the workflow test.

      Candidates worth checking once the snapshot is in hand: whether Settings
      renders an error boundary from an unmocked endpoint, whether it redirects
      (auth), and whether the tab panel is lazily mounted such that the
      hash-selected index is applied after the click is attempted.

## L8220 (cb) section: Missing-file lane — follow-ups after the report-only change (#2614)
- [ ] **`UpsertBookToMemDB` holds go-memdb's global writer mutex across Pebble
  I/O.** Found 2026-08-06 while profiling `dedupe-book-file-rows` (fixed in
  #2161). This is a **system-wide ceiling on every `UpdateBook`**, not something
  specific to that op, and it is the natural next performance win.

  go-memdb has a single global writer mutex (`memdb.go:34-35`, `:73-76` — one
  writer at a time, `Txn(true)` takes `db.writer.Lock()`). Inside that lock,
  `UpsertBookToMemDB` performs three Pebble reads: `GetBookAuthors`
  (`memdb_sync.go:72`), `GetBookNarrators` (`:85`), and `loadBookFilesForBookID`
  (`:98` — a full prefix scan that unmarshals every remaining fingerprint-bearing
  row). Every other writer in the process waits on that I/O.

  Fix: fetch first, then take `Txn(true)`. Consequence worth stating — this is
  also why adding worker pools to book-level maintenance ops buys far less than
  `NumCPU×`: the workers serialize here regardless.


## L10074 (cb) section: Decision needed
- [ ] **TODO-DEPS-VULN** GitHub reports 5 Dependabot vulnerabilities on the
      default branch (2 high, 3 moderate). Triage and bump.


## L10077 (cb) section: Decision needed
- [ ] **TODO-SEC-BIND** The service binds every interface
      (`ExecStart=… serve --host 0.0.0.0 --port 8484`), so anything on the LAN
      reaches the origin directly and **Cloudflare Access is not a boundary** —
      the edge is only enforced for traffic that arrives through the tunnel.
      Bind loopback (or the tunnel-facing interface only) in
      `deploy/local.conf` so Access becomes the single front door, then verify
      the tunnel still serves `books.jdfalk.com`. Note in the PR that
      direct-to-LAN verification is no longer possible **by design** after this.
      The tunnel connector runs on rpi1-3, not on the origin host, so the
      loopback bind must account for that hop.


## L10088 (cb) section: Decision needed
- [ ] **TODO-SEC-JWT** Rotate `ABS_JWT_SECRET` — it was pasted in plaintext into
      a chat transcript on 2026-07-31. It signs every ABS session token. Rotate
      it in `deploy/local.conf` (gitignored — never commit or print it; redact
      with `sed -E 's/(SECRET|TOKEN|KEY)=[^ ]*/\1=<redacted>/g'` when dumping a
      unit), redeploy, and confirm previously-issued tokens are rejected.


## L10094 (cb) section: Decision needed
- [ ] **TODO-SEC-SYSTEMD** The unit has `User=audiobook`, `NoNewPrivileges`,
      `ProtectKernelTunables`, `ProtectControlGroups` and `PrivateTmp`, but no
      `ProtectSystem=strict`, no `ReadWritePaths`, no `CapabilityBoundingSet`,
      no `SystemCallFilter` and **no egress restriction**. `IPAddressDeny=any`
      plus a narrow allowlist is what stops a compromised process reaching the
      rest of the LAN. It needs the Whisper host on `:19847` and Ollama on
      `:11434`, plus outbound HTTPS for OpenLibrary/AcoustID — an over-tight
      rule silently breaks metadata and transcription, so test before claiming
      it works.


## L10600 (nb) section: Workflow / ops (4)
26. **INTERNAL-SERVER-PKG-STALL structural decision** (H1:849-877) — leak fixed;
    residual needs an owner decision: raise timeout / split package / migrate ~60 call
    sites to `newTestServer` (see DECISIONS-PENDING).

## L10665 (nb) section: UX (4)
39. **Fleet-tasks reconciliation** — real residuals = 030/031/036 (≡ 4.10/4.8/1.16);
    032–035 are stale-shipped and need closing in the fleet tracker.


## L10715 (nb) section: Other / close-out (10)
46. **Duration/filesize aggregation** — Book fields show snapshots instead of sums;
    likely stale (F5-T026 shipped) — verify then close.
    - ~~**46b. `/audiobooks` LIST endpoint mis-serializes `duration`** (found
      2026-07-19)~~ **DONE 2026-08-03 (#2125).** The reported symptom — list says
      `duration: 4` where the detail endpoint says `4680` — was the arithmetic itself:
      `4680 / 1000` truncates to `4`. `service_filtering.go:923` divided every
      duration by 1000 unconditionally while aggregating, so the rows it corrupted
      were the *correct* second-valued ones. Now routed through
      `database.NormalizeDurationSec`, which classifies per row from the implied
      bitrate. Same fix applied to `handlers/versions.go` and
      `handlers/audiobooks/handler_files.go` (×2). Far from low-severity: it affected
      25,938 books.

## L10057 (cb) section: Decision needed
- [ ] **TODO-SSO-EDGE** Neither native-app auth mode is actually configured at
      the Cloudflare edge, despite both being fully written up in
      `jdfalk/cloudflare-one` `access/audiobook-app-policies.md`. Measured via
      the CF API on 2026-07-31: the `books.jdfalk.com` Access app has exactly
      **one** policy (precedence 1, `allow`, email allowlist) — there is **no
      `non_identity` service-token policy** and **no service tokens exist on the
      account at all**; app-level `allow_authenticate_via_warp` is unset and
      org-level is `false`; and no cover-art bypass app exists (confirmed live —
      the cover path 302s to Access instead of reaching the origin). That fully
      explains the measured `service_token_status:false, is_warp:false,
      auth_status:NONE`. So `scripts/setup-audiobook-apps.sh` never ran against
      this account, or was rolled back — the doc describes a **design**, not the
      live state. Recommended path is **Mode C (WARP)**: it delivers a real
      identity JWT with an `email` claim, which satisfies `cf` mode exactly as
      already coded — no app changes, no `/status` change, no password. Mode B
      additionally needs TODO-ABS-MODEB fixed before it can work.


## L10447 (cb) section: SEC: origin is reachable from the LAN — "bind loopback" is NOT achieva
- [ ] **iTunes book_file PID uniqueness — apply the backfill repair (gated).** Forward
  invariant shipped (`CreateBookFile` transfers a duplicate PID to the new row) + census
  (`GET /itunes/pid-integrity`) + repair (`POST /itunes/pid-repair`) endpoints built and
  tested. Prod census: 8,987 duplicate PIDs (8,762 same-file, 225 diff-file, 94 on multiple
  primaries). Repair dry-run auto-resolves 8,984 (3 ambiguous left for review), clears 9,050
  redundant PID copies, DB-field-only (no file/row deletion). REMAINING: deploy → run
  `pid-repair?dry_run=true` on prod to confirm → owner runs the apply with `!` → re-run the
  census to confirm 0 duplicates → review the 3 ambiguous groups by hand. See
  `docs/specs/2026-07-23-itunes-2way-sync-continuation-findings.md` §1.5b.


## L10512 (nb) section: Dedup (10)
6. **INIT-1 T05 follow-up — per-kind confidence field in `DedupSignalConfig`** (H1:250)
   — **persistence scaffolding DONE** (2026-07-18): `config.DedupSignalConfig.Confidence`
   + `unified.SetKindConfidenceOverrides` (mirrors `SetBandThresholds`) + `registry_wire.go`
   wiring, so a per-kind confidence bound now survives `UpdateConfig`/restart. **Still
   blocked**: `unified.ComposeScore` ignores `cfg.Signals[kind]` bounds entirely (reads
   `Signal.Confidence` verbatim), so the field has no effect on live scoring yet, and
   `dedup.calibrate-composite`'s Round 2 sweep still doesn't write it — decision needed
   on whether `ComposeScore` should clamp against it (see
   [`docs/plans/DECISIONS-PENDING.md`](docs/plans/DECISIONS-PENDING.md) row 10).

## L10584 (nb) section: Pipeline (8)
22. **Torrent relocation INIT-5 T2–T7** ([plan](docs/plans/2026-07-10-torrent-relocation.md))
    — T1 shipped (18570a39); T2 = human-gated Deluge spike blocks T3–T7.

## L10639 (nb) section: Infra (5)
35. **Consultancy wave 4+ residuals** ([roadmap](docs/consultancy/00-ROADMAP.md)) —
    unverified; needs a close-out sweep against shipped work.

## L6087 (cb) section: Missing-file lane — follow-ups after the report-only change (#2614)
- [ ] **Replace library sorting with server-side Go sorting.** Owner decision 2026-08-09:
      *"I want the system to not suck and I want sorting replaced, and done by go."*
      Recorded in full with the code evidence in §0a of
      `docs/design/2026-08-09-search-backend-options.md`.

      ## Where sorting lives today — three places, one of them correct

      | where | what | verdict |
      |---|---|---|
      | `internal/audiobooks/service_filtering.go:130` `applySorting` | Go, server-side, over the full filtered set | **Correct.** Keep and extend |
      | `web/src/components/common/ConfigurableTable.tsx:201` | `[...rows].sort(...)` on the client | **Replace.** Sorts the *current page* |
      | `web/src/services/api.ts` `searchBooksPage` | sends no `sort_by` | **Fix.** Search drops the sort order entirely |

      **Why the client-side one is broken by design, not merely misplaced:** it sorts the
      rows already fetched. On a paginated library that is the 50 books you can see, not
      the library. "Sort by title descending" hands you the *wrong 50 books*, correctly
      ordered among themselves — which looks plausible, which is why it survives.

      ## Scope carefully — not every client sort is wrong

      There are **15** `.sort()` sites in `web/src`. Most are legitimate: a book's own file
      list by track number (`BookDetailFilesTab.tsx:250`), tag clouds by count
      (`TagCloud.tsx:76`), metadata candidates by score. Those sort complete, small,
      already-fetched sets. **The rule is: a sort over a paginated slice of the library is
      wrong; a sort over a complete set the client already holds is fine.**

      ## Two things that must land with it

      1. **Sort must be applied BEFORE pagination**, which is the same defect as filters
         being applied after pagination (§2.2 of the design doc). Moving the sort to Go

## L7819 (cb) section: Missing-file lane — follow-ups after the report-only change (#2614)
- [ ] **Fix the Library "In Progress" nav item — the selection highlight never
      moves, and the click is a genuine no-op.**
      🟡 **BOTH BUGS FIXED in #2193 (2026-08-08); two acceptance items remain —
      see "Still open" at the end of this entry.** Reported 2026-08-08, root-caused
      the same night. These are **two independent bugs** that happen to share a
      symptom. The control is not on the Library page: it is the Library sub-nav
      in the sidebar, `web/src/components/layout/Sidebar.tsx:53-62`:

          56: { text: 'All Books',   path: '/library?reset=1', matchPath: '/library' },
          57: { text: 'In Progress', path: '/library?search=read_status:in_progress' },
          58: { text: 'Finished',    path: '/library?search=read_status:finished' },

      **Bug 1 — the highlight can never move.** `Sidebar.tsx:163`:

          selected={location.pathname === (item.matchPath ?? item.path)}

      `location.pathname` never contains the query string. "In Progress" has no
      `matchPath`, so this compares `'/library'` against
      `'/library?search=read_status:in_progress'` — **always false**. "All Books"
      declares `matchPath: '/library'`, so it is **always true on any /library
      URL**. The indicator is therefore permanently pinned to "All Books".
      "Finished" is broken identically.

      Note: the obvious "compare pathname + search" fix is a trap. Once bug 2 is
      fixed the URL settles at `?search=read_status%3Ain_progress&page=1` — the
      write effect re-encodes the colon (`Library.tsx:605`) and unconditionally
      appends `page` (`614`) — so a raw string compare still fails. **Match on
      the decoded `search` param value, not the path string.**

      **Bug 2 — the click is a permanent no-op.** There is no dedicated

## L1815 (cb) section: Config
- [ ] ✅ **Confirm playlists are book-level, not file-level — and delete the dead
      file-level path.** Owner requirement 2026-08-10: *"we need to be sure
      playlists operate at the book level not the file level."*

      **Checked 2026-08-10 — the live model is already book-level.**
      `database.UserPlaylist` (`internal/database/store.go:391`) stores
      `BookIDs []string` (book ULIDs) for static playlists and
      `MaterializedBookIDs []string` for evaluated smart ones. `Type` is
      `"static"` or `"smart"`. `MaterializeSmartPlaylist` evaluates to book IDs.
      No `book_file` reference anywhere in the type. ✅

      **But a legacy file-level path still exists** in `internal/playlist/playlist.go`:

          type PlaylistItem struct {
              BookID   int      // ← book IDs are ULID strings, not ints
              FilePath string   // ← ONE path per book; audiobooks are multi-file
              ...
          }

      `generatePlaylistFile` writes an M3U with a single `FilePath` line per
      item, which is wrong for any multi-file audiobook, and `BookID int` is a
      leftover from the removed SQLite schema. Its only sibling,
      `GeneratePlaylistsForSeries`, was already gutted in fable5 TASK-022 and now
      just returns an error telling callers to use the Store-backed API.

      `generatePlaylistFile` has **no non-test callers** — its only references
      are in `internal/playlist/playlist_test.go`. So the file-level model is
      dead code that four tests keep alive, and it is exactly the sort of thing
      that gets copied back into service later because it looks like the
      playlist implementation. **Delete it and its tests**, or, if M3U export is

## L10622 (nb) section: Infra (5)
37. **CPU busy-loop: `CountPrimaryBooks` full-scan on the 5s metrics ticker** — ✅ DONE
    (2026-07-18): the server burned ~2 cores continuously while idle because
    `CountPrimaryBooks` (`internal/database/pebble_store.go`) full-scans + `json.Unmarshal`s
    all ~44K books (~5.6s) and the 5s status ticker
    (`internal/server/server_lifecycle.go`) called it every tick, running scans
    back-to-back (presented as ~189% CPU with only `sweep tick waiting_count=0` logs; also
    made `/api/v1/health` ~5.6s). Fixed with a 30s in-memory TTL cache + recompute gate on
    `CountPrimaryBooks` (regression test `TestPebbleCountPrimaryBooksTTLCache`). Diagnosed
    while health-checking the (now torn-down) dedup sandbox.


## L3734 (cb) section: C716 resolved: the "3,954-book API-vs-store gap" decomposes to 3,953 i
- [ ] **CA12 wave 2: model `logging.Sanitize`/`SanitizeErr`/`logger.sanitizeLogLine`
      as CodeQL log-injection sanitizers via the model pack.** #2445 removed
      the fast-path bypass, but the conduit's own alerts
      (`internal/logging/structured.go:51/58/65`) are STILL open at 316 total:
      taint through the variadic `args ...any` slice survives because CodeQL
      treats per-element slice writes as weak updates — no code shape fixes
      that. The repo already has the mechanism
      (`.github/codeql/models/*.model.yml`, `pathInjectionSanitizer` rows) —
      BEFORE adding rows, verify the extensible predicate name for log
      injection actually exists in the pinned `codeql/go-all` (an unknown
      `extensible:` fails the pack). If none exists, the alternatives are a
      custom .ql suite or bulk dismissal-with-justification of
      conduit-routed alerts. Baseline to beat: 316 open go/log-injection.


## L10641 (nb) section: Infra (5)
36. **Op-progress Prometheus metric (T12 follow-up)** — ✅ DONE (PR #2014,
    2026-07-18): added `audiobook_organizer_op_items_processed{op_id,op_type}`
    + companion `audiobook_organizer_op_items_total{op_id,op_type}` gauges
    (`internal/metrics/metrics.go`, `SetOpProgress`/`ClearOpProgress`), set on
    every `dbReporter.UpdateProgress` call
    (`internal/operations/registry/reporter_db.go`) and deleted on every
    terminal transition via `registry.publishOpTerminal`
    (`internal/operations/registry/registry.go`) so stale op_ids never
    accumulate. Uncommented + finalized the "op stalled" alert in
    `deploy/prometheus/alert-rules.yml` (`AudiobookOrganizerOpStalled`,
    `rate(audiobook_organizer_op_items_processed[30m]) == 0` for 30m —
    existence of the series itself proxies "op is active" since it's deleted
    at terminal, so no separate `op_active` gauge was needed). Closes the
    observability gap behind the 3+ hour `dedup.full-scan` hang and the 9hr
    Pebble write-stall freeze — both were only noticed by a human watching
    the UI.


## L10038 (cb) section: Decision needed
- [ ] **TODO-ABS-MODEB** A Cloudflare **service-token** assertion is rejected as
      invalid, so the documented "Mode B" (edge service token + our own bearer
      token) cannot work at all. A `non_identity` Access JWT carries
      `common_name` and **no `email` claim**, so
      `internal/oauth/cfaccess.go:59-60` fails it, and
      `internal/server/middleware/absauth.go:166-171` turns *any* Verify error
      into a terminal 401 that deliberately never falls through to the bearer
      path — so the request 401s **even when it also carries a valid ABS bearer
      token**, and `internal/server/handlers/abs/login.go:53-55` makes password
      login unreachable too. Fix: have `Verify` distinguish a cryptographically
      *valid* but non-identity assertion (sig/iss/aud/exp all pass, no email)
      from an invalid one via a typed sentinel (`ErrNonIdentityAssertion`), and
      map only that sentinel to a `(nil, nil)` fall-through in
      `ResolveCFAssertion` — every other Verify failure must stay a terminal
      401. Tests: (a) forged assertion still 401; (b) valid non-identity + valid
      bearer → 200 via jwt mode; (c) valid non-identity, no bearer → 401
      `no-credential`; (d) login with non-identity assertion + password body
      reaches the password path. Revert-validate (b) and (d).


## L10104 (cb) section: Decision needed
- [ ] **TODO-SRVTIMEOUT** Split or speed up the `internal/server` test package —
      it runs 434–480 s against Go's 600 s default per-package timeout, leaving
      under 30% headroom. Any concurrent load on the machine tips the whole
      package into a timeout that is indistinguishable from a deadlock: the
      panic dump names whichever goroutine happened to be mid-teardown
      (`operations/registry.(*Registry).Shutdown` blocked on `sync.WaitGroup.Wait`
      at `registry.go:1030` in the observed case), which reads as a real hang and
      sent a 2026-07-31 investigation down a false trail on PR #2083. Verified
      not a deadlock: the same commit passes in 480 s when run without competing
      load. Either shard the package, or set an explicit generous `-timeout` in
      the Makefile test targets so a slow run fails as "too slow" rather than
      masquerading as a lock bug.

      **The `-timeout` half is DONE.** #2270 put `-timeout 25m` on the Makefile's
      four `./...` targets (with a comment above `coverage:` explaining this exact
      masquerade); #2278 did the last live invocation that lacked it,
      `scripts/run-all-tests.sh`. A repo-wide sweep found no other. A bare
      `go test ./internal/server/` still runs on Go's 10m-per-package default.

      **Measured 2026-08-10 — the premise has drifted and the proposed fix is
      aimed at the wrong thing.**

      - Runtime is now **543 s** solo (`real 553 s`), not 434–480 s. Headroom
        against the 600 s default is **9.5%**, not "under 30%" — it has gotten
        worse, and that is *without* the `./...` contention the entry blames.
      - **~85% of wall time is idle**: `user 40.7 s + sys 40.0 s ≈ 81 s` CPU
        against 553 s wall. The package is not compute-bound; it is waiting.
      - **There is no slow test to fix.** 855 top-level tests summing to 540.5 s
        — so the time is inside tests, not compile or global fixture. The
        distribution: **4** tests ≥5 s (slowest `TestServerStartGracefulShutdown`

## L2125 (cb) section: Config
- [ ] **ORGANIZE-4TH-COPY** `internal/server/handlers/filesystem.go:286` is a
      FOURTH copy of the single-file/multi-file organize routing bug, and it is
      the worst-behaved of the four.

      **Not fixed here on purpose.** That file belongs to **Wave 12** of the
      silent-failure plan, and the plan's rule is that every wave's file set is
      disjoint from every other's. Wave 3 leaving it alone is the rule working,
      not an oversight — but the audit characterises that line as bucket (d)
      "per-path DB resolution during a filesystem browse", which undersells it.
      Re-rank it 🔴 and fix it in Wave 12, or pull it forward on its own.

      **The defect.** The auto-organize block after a filesystem browse calls
      `org.OrganizeBook(dbBook)` — the SINGLE-FILE path — for every book. Any
      book whose `file_path` is a directory fails with
      `file_path %s is a directory but single-file organize was requested`.
      Multi-file books are most of the library.

      **Why it is worse than the other three:** the error is discarded by a
      bare `continue` with **no log at all**, so unlike `server.go` (which at
      least logged a warning) this copy fails completely silently. It also
      collapses `if err != nil || dbBook == nil` into one branch, hiding a DB
      lookup error and a missing row as the same non-event.

      **Fix:** `organizeService.OrganizeOneBook(org, dbBook, log)` plus
      organized/failed/notInDB/lookupErrors counters, exactly as
      `server.go`'s `AutoOrganizeFn` (#2303) and
      `folder_autoscan_op.go` (this wave) now do.

      ---


## L2467 (cb) section: Config
- [ ] **Repair books applied from the Metadata Review screen before the tag-write fix.**
  `BatchApplyFromCache` updated the database without ever writing tags or
  embedding cover art (fixed in `fix/review-apply-writes-tags`). Every book
  applied from that screen while the defect was live has correct metadata in
  the DB and stale tags on disk. A repair path already exists and does not need
  to be built: the `library.bulk-write-back` operation
  (`internal/server/metadata_ops.go:808` `runBulkWriteBack`, HTTP entry
  `handleBulkWriteBack` in `internal/server/handlers/metadata/handler.go:1175`)
  re-writes tags for a filtered set of books with a worker pool and resume.
  What is missing is the *selection*: there is no record of which books were
  applied from the review screen specifically, so scoping the repair means
  either running it library-wide or deriving the set from the activity log.
  Owner decision — no code needed if a library-wide run is acceptable.


## L8316 (cb) section: Missing-file lane — follow-ups after the report-only change (#2614)
- [ ] **Per-file intro transcription as the primary book-identity signal** — owner
  design 2026-08-06. Storage and the first-file sort fix are **DONE** (PRs #2168);
  the parser, the tiered backfill, and the wiring are open.

  **The idea.** An audiobook opens with a spoken *"&lt;Title&gt; by &lt;Author&gt;, read by
  &lt;Narrator&gt;"* announcement. That announcement marks a book **start**. A file
  without one is a continuation. That is direct identity evidence, where the
  current classifier only has runtime — a proxy.

  **Why it needed per-file storage.** Transcripts lived on `Book`, so only ONE
  file's opening was ever captured and "12 files that are one book" was
  indistinguishable from "12 files that are 12 books". Measured on prod, one
  folder's files read:

  ```
  file 1: "This is a reading of Overlord, Book 7. This part includes the prologue and Chapter 1."
  file 2: "This is a reading of Overlord Volume 7. This part includes Chapter 2."
  file 3: "Hello... This is Overlord Volume 7, Chapter 3."
  ```

  Per-file that sequence is proof of continuation; per-book it is invisible. It
  also explains the measured **45.8%** credit-parse rate across 1,476 review-queue
  members — the op sampled one arbitrary file per book.

  ### Remaining work

  - [x] **Three-outcome parser.** ✅ DONE 2026-08-07 — `ClassifyIntro`
        (`internal/transcribe/classify.go`) returns credits / chapter / prose /
        unknown with a typed reason, confidence, and chapter number. **Position
        is a weight, never a veto**: credits at ordinal >0 IS the shattered-book

## L2267 (cb) section: Config
- [ ] **ROWCOUNT-REVERIFY** Re-measure every production table row count once the
      row/key-separated warmup counter is deployed, and correct what it moves.

      **Why this is open rather than done.** The `books` figure that appears
      throughout this repo (392,962 → 366,922 → 366,916, depending on vintage)
      was never a book count. It was the number of Pebble KEYS under the
      `book:` prefix, which is shared with roughly seven secondary-index
      families — `book:path:`, `book:hash:`, `book:originalhash:`,
      `book:organizedhash:`, `book:versiongroup:`, `book:work:`,
      `book:asin:`/`book:isbn13:` — at about 7.5 keys per row. The warmup now
      reports rows and keys separately, pinned by
      `TestWarmupCounts_CountRowsNotPebbleKeys`, but production has not yet
      run the fixed counter.

      **Best current number: ~48,900 books.** From the organizer's own full
      paging enumeration on 2026-08-11 (`Fetched 48896 total books from
      database`), corroborated by system-status readings of 46,221 and 54,734.
      This is the strongest available evidence, not a verified count.

      **What has already been corrected** (2026-08-11): the inflated figure was
      removed or annotated in `memdb_sort_index_cost_test.go`, `config.go`,
      `memdb_sort_indexers.go`, `pebble_store_versiongroup_backfill.go`,
      `library_list_warmer.go`, `bleve_translator.go`, `dedup/lifecycle.go`,
      `web/src/pages/Library.tsx`, `docs/design/2026-08-09-search-backend-options.md`
      and `docs/perf-audit-2026-05-29-heap-breakdown.md`.

      **The one that changed an answer, and needs an owner decision:** the sort
      index memory cost. The per-book measurement (+3,750 B/book for all nine
      indexes, measured at 100,000 books) was always correct; only the
      population it was multiplied by was wrong. Re-extrapolated:

## L7209 (cb) section: Missing-file lane — follow-ups after the report-only change (#2614)
- [ ] **The library "Sort by" control no longer exists — 4 e2e tests target a
      surface that is gone.** Found 2026-08-09 while repairing
      `library-browser.spec.ts`.

      **Test side ✅ DONE (#2230); product decision ⏳ STILL OPEN.** The four
      tests now drive sort through the URL (`?sort=…&order=…`), which is the
      only surviving mechanism, so the sort *behaviour* stays covered. What is
      unresolved is whether losing the control was intentional. Hard evidence:
      `SearchBarProps` (`web/src/components/audiobooks/SearchBar.tsx:124-131`)
      has no `onSortChange` prop at all, and
      `web/src/components/library/LibraryBookGrid.tsx:133` receives the handler
      as `_handleSortChange` — underscore-prefixed to mark it deliberately
      unused. `SearchBar.test.tsx:43` asserts "does not render sort controls
      when `onSortChange` is absent", which now passes vacuously because the
      prop cannot be supplied. Either restore the control, or delete the dead
      state and that vacuous unit test.
      Full write-up: `todo.d/20260809-library-sort-control-missing.md`.

      `sorts books by title ascending` / `title descending` / `author` /
      `date added` all do:

          await page.getByRole('combobox', { name: 'Sort by' }).click();
          await page.getByRole('option', { name: 'Title' }).click();

      There is **no such control anywhere in the library UI**. Grepping the
      components turns up no `Sort by` label and no sort dropdown in
      `FilterPanel`, `LibraryToolbar` or `SearchBar`. Sorting now happens
      through the table view's column headers
      (`LibraryBookGrid`'s `handleColumnSortChange` → `ConfigurableTable` /
      `AudiobookList`), which the default grid view does not show at all.

