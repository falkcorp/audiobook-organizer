# scope-18 — still-open Tier A items (baseline TODO.md = commit 46628240; line numbers are BASELINE lines)

Each block: the TODO.md text (plus up to 12 continuation lines). Treat every block as ONE item unless it clearly lists several deliverables (then split with `part`).


## todo_line 280
```
- [ ] `syscall.Kill(os.Getpid(), syscall.SIGTERM)` signals the **entire test binary**, not a
      child. Every test in the package shares that process, so this is a global side effect
      fired from one test. It works today; it is a trap for whoever adds parallelism.
```


## todo_line 283
```
- [ ] The unconditional `time.Sleep(6 * time.Second)` before the signal is pure wall-clock
      cost paid on every run, in a package that is already the gate's biggest consumer.
```


## todo_line 476
```
- [ ] **Series DETAIL is still not served.** `/api/series/:id` 301s into the app API, the
      same class of bug that made playlists open empty. It was not fixed alongside
      playlists because populating the series LIST (which the client renders) addressed
      the reported symptom, and claiming `/api/series/` from the redirect is a second
      routing decision that deserves its own change. Upstream also has
      `GET /api/libraries/:id/series/:seriesId`, which sits under the already-reserved
      `/api/libraries/` prefix and therefore needs **no** routing decision at all —
      prefer that route first.
```


## todo_line 697
```
- [ ] `registry.RunItems` label re-render (fixed 2026-08-13) changed shared
      infrastructure used by every op. Progress labels for other ops now advance
      one item later than before — verify none of them assumed the old timing.
```


## todo_line 928
```
- [ ] Not done unattended on purpose: this was left for a human on 2026-08-18
      rather than folded into the CI-wiring PR, because verifying a release
      workflow requires actually cutting a release.
```


## todo_line 3718
```
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
```


## todo_line 3729
```
- [ ] `is_primary_version=false` answers 22,552 — exactly the known
      nil-flag population size. Establish whether explicit-false books are
      currently 0 in prod or whether the false-filter is returning nils
      (memory says the census counted ~765 explicit-false in one path).
```


## todo_line 4064
```
- [ ] **After this deploys**, drop `skip_author_ids: [46627]` from the repair
      invocation and repair that row. Everything else already applied
      2026-08-14 02:0x: 30 merged, 15 renamed, 0 failures, 145/145 book links
      verified *via the memdb-backed API — the Pebble path was not re-read
      post-apply*. Author 46627 is the ONLY remaining stranded-ampersand row;
      the other two survivors, `&#169` and
      `&#169;2013 by HarperCollinsPublishers`, are the separate HTML-entity
      defect.
```


## todo_line 4081
```
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
```


## todo_line 6394
```
- [ ] **`scan-import-organize.spec.ts` (7 failures) — Settings tab deep-link
      fixed, but that was NOT the blocker. Count unchanged at 7.**
      Investigated 2026-08-09.
```


## todo_line 10077
```
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
```


## todo_line 10088
```
- [ ] **TODO-SEC-JWT** Rotate `ABS_JWT_SECRET` — it was pasted in plaintext into
      a chat transcript on 2026-07-31. It signs every ABS session token. Rotate
      it in `deploy/local.conf` (gitignored — never commit or print it; redact
      with `sed -E 's/(SECRET|TOKEN|KEY)=[^ ]*/\1=<redacted>/g'` when dumping a
      unit), redeploy, and confirm previously-issued tokens are rejected.
```


## todo_line 10094
```
- [ ] **TODO-SEC-SYSTEMD** The unit has `User=audiobook`, `NoNewPrivileges`,
      `ProtectKernelTunables`, `ProtectControlGroups` and `PrivateTmp`, but no
      `ProtectSystem=strict`, no `ReadWritePaths`, no `CapabilityBoundingSet`,
      no `SystemCallFilter` and **no egress restriction**. `IPAddressDeny=any`
      plus a narrow allowlist is what stops a compromised process reaching the
      rest of the LAN. It needs the Whisper host on `:19847` and Ollama on
      `:11434`, plus outbound HTTPS for OpenLibrary/AcoustID — an over-tight
      rule silently breaks metadata and transcription, so test before claiming
      it works.
```


## todo_line 10600
```
26. **INTERNAL-SERVER-PKG-STALL structural decision** (H1:849-877) — leak fixed;
    residual needs an owner decision: raise timeout / split package / migrate ~60 call
    sites to `newTestServer` (see DECISIONS-PENDING).
27. **Responses-API migration AI-RESP-A/B/E/F (INIT-7)** (H1:2596-2603) — [hold,
    do-not-start-without-greenlight]. AI-RESP-C/D closed.
```


## todo_line 10447
```
- [ ] **iTunes book_file PID uniqueness — apply the backfill repair (gated).** Forward
  invariant shipped (`CreateBookFile` transfers a duplicate PID to the new row) + census
  (`GET /itunes/pid-integrity`) + repair (`POST /itunes/pid-repair`) endpoints built and
  tested. Prod census: 8,987 duplicate PIDs (8,762 same-file, 225 diff-file, 94 on multiple
  primaries). Repair dry-run auto-resolves 8,984 (3 ambiguous left for review), clears 9,050
  redundant PID copies, DB-field-only (no file/row deletion). REMAINING: deploy → run
  `pid-repair?dry_run=true` on prod to confirm → owner runs the apply with `!` → re-run the
  census to confirm 0 duplicates → review the 3 ambiguous groups by hand. See
  `docs/specs/2026-07-23-itunes-2way-sync-continuation-findings.md` §1.5b.
```


## todo_line 10512
```
6. **INIT-1 T05 follow-up — per-kind confidence field in `DedupSignalConfig`** (H1:250)
   — **persistence scaffolding DONE** (2026-07-18): `config.DedupSignalConfig.Confidence`
   + `unified.SetKindConfidenceOverrides` (mirrors `SetBandThresholds`) + `registry_wire.go`
   wiring, so a per-kind confidence bound now survives `UpdateConfig`/restart. **Still
   blocked**: `unified.ComposeScore` ignores `cfg.Signals[kind]` bounds entirely (reads
   `Signal.Confidence` verbatim), so the field has no effect on live scoring yet, and
   `dedup.calibrate-composite`'s Round 2 sweep still doesn't write it — decision needed
   on whether `ComposeScore` should clamp against it (see
   [`docs/plans/DECISIONS-PENDING.md`](docs/plans/DECISIONS-PENDING.md) row 10).
7. **Async breakdown-refresh for bulk/cluster dismiss** (H1:1877) — per-pair synchronous
   refresh may need an async variant at scale (latency note).
8. **Omnibus detection + dedup** — spec-only
   ([`docs/superpowers/specs/`](docs/superpowers/specs/) 2026-05-31); not started.
```


## todo_line 7819
```
- [ ] **Fix the Library "In Progress" nav item — the selection highlight never
      moves, and the click is a genuine no-op.**
      🟡 **BOTH BUGS FIXED in #2193 (2026-08-08); two acceptance items remain —
      see "Still open" at the end of this entry.** Reported 2026-08-08, root-caused
      the same night. These are **two independent bugs** that happen to share a
      symptom. The control is not on the Library page: it is the Library sub-nav
      in the sidebar, `web/src/components/layout/Sidebar.tsx:53-62`:
```


## todo_line 10622
```
37. **CPU busy-loop: `CountPrimaryBooks` full-scan on the 5s metrics ticker** — ✅ DONE
    (2026-07-18): the server burned ~2 cores continuously while idle because
    `CountPrimaryBooks` (`internal/database/pebble_store.go`) full-scans + `json.Unmarshal`s
    all ~44K books (~5.6s) and the 5s status ticker
    (`internal/server/server_lifecycle.go`) called it every tick, running scans
    back-to-back (presented as ~189% CPU with only `sweep tick waiting_count=0` logs; also
    made `/api/v1/health` ~5.6s). Fixed with a 30s in-memory TTL cache + recompute gate on
    `CountPrimaryBooks` (regression test `TestPebbleCountPrimaryBooksTTLCache`). Diagnosed
    while health-checking the (now torn-down) dedup sandbox.
```


## todo_line 10104
```
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
```


## todo_line 2125
```
- [ ] **ORGANIZE-4TH-COPY** `internal/server/handlers/filesystem.go:286` is a
      FOURTH copy of the single-file/multi-file organize routing bug, and it is
      the worst-behaved of the four.
```


## todo_line 2467
```
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
```


## todo_line 8316
```
- [ ] **Per-file intro transcription as the primary book-identity signal** — owner
  design 2026-08-06. Storage and the first-file sort fix are **DONE** (PRs #2168);
  the parser, the tiered backfill, and the wiring are open.
```


## todo_line 2267
```
- [ ] **ROWCOUNT-REVERIFY** Re-measure every production table row count once the
      row/key-separated warmup counter is deployed, and correct what it moves.
```


## todo_line 7209
```
- [ ] **The library "Sort by" control no longer exists — 4 e2e tests target a
      surface that is gone.** Found 2026-08-09 while repairing
      `library-browser.spec.ts`.
```
