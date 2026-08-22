# scope-18 handoff

Output file `scout/scope-18.json` currently contains **8 finished objects**, valid JSON array.

## DONE (8)
- todo_line 280 — TestServerStartGracefulShutdown SIGTERM parallelism-trap guard comment — actionable
- todo_line 283 — replace fixed 6s sleep with shutdownArmed channel poll — actionable
- todo_line 476 — GET /api/libraries/:libraryId/series/:seriesId detail route — actionable
- todo_line 697 — audit registry.RunItems Label closures (31 files) for post-fn timing — actionable
- todo_line 928 — rationale note, not independent work — not_a_task
- todo_line 3718 — show_quarantined narrows to is_primary_version=true bug — actionable (root cause NOT located by static read; opus-tier bisection needed, see notes/steps already written)
- todo_line 3729 — measure is_primary_version=false population in prod — prod_run
- todo_line 4064 — re-run author-conjunction-repair for author 46627 — prod_run

## REMAINING (16) — not started, in scope-18.md order
- todo_line 4081 — CORRECTION matcher-writeback background job (metafetch fetch-side singleton, staggered fan-out) — needs investigation of metafetch.chainMu (already ruled out per TODO text), bulk dialog one-book-at-a-time flow, matcher search endpoint serialization
- todo_line 6394 — scan-import-organize.spec.ts (7 failures), Settings tab deep-link fix did NOT resolve — needs playwright/e2e investigation
- todo_line 10077 — TODO-SEC-BIND: bind loopback in deploy/local.conf, tunnel via rpi1-3 — likely actionable (repo file deploy/local.conf) + prod_run verify step
- todo_line 10088 — TODO-SEC-JWT: rotate ABS_JWT_SECRET in deploy/local.conf — prod_run (secret rotation, gitignored file)
- todo_line 10094 — TODO-SEC-SYSTEMD: add ProtectSystem=strict, ReadWritePaths, CapabilityBoundingSet, SystemCallFilter, IPAddressDeny=any + allowlist to systemd unit — find the unit file (search deploy/ or systemd/ dir), likely actionable (repo file) + prod_run verify
- todo_line 10600 — TWO sub-items: #26 INTERNAL-SERVER-PKG-STALL (= same underlying task as 10104, decision #6 already resolved: migrate ~60 call sites to newTestServer helper, actionable) — SPLIT part 1; #27 Responses-API AI-RESP-A/B/E/F (INIT-7) — parked per decision #5 — SPLIT part 2
- todo_line 10447 — iTunes book_file PID uniqueness backfill: deploy, dry-run confirm, owner applies, re-census, review 3 ambiguous groups — prod_run (endpoints already built per TODO text: GET /itunes/pid-integrity, POST /itunes/pid-repair)
- todo_line 10512 — unified.ComposeScore confidence clamping: decision #10 already made (option a: add clamping for primary kinds + route calibrate-composite Round-2 via separate apply_confidence param) — actionable, opus per decision. NOTE also contains items 7 (async breakdown-refresh, latency note only) and 8 (omnibus detection, spec-only not started) — consider splitting into parts; item 8 might be needs_design or not_a_task (spec-only, no code yet, no decision recorded)
- todo_line 7819 — Library "In Progress" nav item: BOTH bugs already fixed per #2193 (2026-08-08) — check "Still open" acceptance items mentioned at end of TODO entry (need git show 46628240:TODO.md around this line for the full continuation to see what's still open) — likely stale_done for the two bugs, actionable for remaining acceptance items only
- todo_line 10622 — CountPrimaryBooks busy-loop — marked "✅ DONE" in TODO.md text itself with regression test TestPebbleCountPrimaryBooksTTLCache — STRONG stale_done candidate, just need to grep-verify the test exists and passes description at HEAD
- todo_line 10104 — TODO-SRVTIMEOUT split/speed up internal/server test package — actionable per decision #6 (option c: migrate ~60 call sites to newTestServer helper) — DUPLICATE/companion of 10600 part 1, cross-reference via depends_on_lines or notes
- todo_line 2125 — ORGANIZE-4TH-COPY: internal/server/handlers/filesystem.go:286 fourth copy of single/multi-file organize routing bug — needs investigation of the other 3 copies (search codebase/TODO/memory for the other 3 instances) to write a real fix goal
- todo_line 2467 — repair books applied from Metadata Review before tag-write fix — owner decision says "no code needed if library-wide run acceptable" — this is likely prod_run (just invoke existing library.bulk-write-back / handleBulkWriteBack library-wide) OR needs_design if owner hasn't actually picked library-wide vs scoped yet — re-read decision list, it's NOT in the explicit decisions-already-made list (items 1-14), so likely needs_design: "run library-wide (simple, some redundant writes) vs derive scoped set from activity log (more code)"
- todo_line 8316 — per-file intro transcription: storage + first-file-sort DONE (#2168), parser/tiered-backfill/wiring OPEN — actionable, needs review of PRs #2168 and current state of internal/... transcription code to scope the open parts precisely (search for intro_transcribe.go, intro_migrate_single_file.go already seen in RunItems grep above — those may already cover backfill; verify what's actually missing)
- todo_line 2267 — ROWCOUNT-REVERIFY: re-measure every prod table row count once row/key-separated warmup counter deployed — check if that counter has already deployed (search for "warmup counter" / row vs key separated counting in internal/database) — if deployed, this becomes prod_run (just run the measurement); if not deployed yet, needs the counter built first (actionable) then prod_run
- todo_line 7209 — library "Sort by" control no longer exists, 4 e2e tests target dead surface — actionable: either restore the control or delete/update the 4 e2e tests (need to find them — likely in web/e2e or web/tests, search for "Sort by" / library-browser.spec.ts references) — needs a design call on restore-vs-delete UNLESS the control's removal was already an intentional product decision (check git log/CHANGELOG for why "Sort by" was removed) — lean needs_design if intent unclear, actionable (delete stale tests) if removal was deliberate

## Notes for whoever continues
- Investigation for 3718 was extensive (opus-tier bisection recommended) — do not re-derive from scratch, the written JSON object already documents which code paths were RULED OUT (service_query.go hasPostFilters branch, both memdb_summaries.go index-selection switches) and where to look next (library_list_warmer.go cache-key collision, Bleve search path).
- For 10600/10104 duplication: both describe the SAME TODO-SRVTIMEOUT task at two different TODO.md locations (a detailed entry at 10104, a one-line summary at 10600 item #26). Write them as two separate JSON objects (different todo_line) but cross-reference each other via "notes", pointing at the same fix (migrate internal/server test package's ~60 call sites to a newTestServer helper per decision #6).
- Use `git show 46628240:TODO.md | sed -n 'START,ENDp'` to get baseline context around any todo_line, same technique used throughout this session (worked well for 928, 3718/3729, 7819 would benefit from this to see the "Still open" continuation).
- gate template used throughout: `go build ./... && go vet ./... && go test ./<pkg>/... -count=1` for Go; `n/a (docs)` for prod_run/not_a_task items with no code; never `make ci`.
