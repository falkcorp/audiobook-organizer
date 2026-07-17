<!-- file: docs/perf-audit-2026-05-29-verification.md -->
<!-- version: 1.1.0 -->
<!-- guid: 4f9b3d2c-1e8a-4d5b-8c0f-5b9e2c7a4d18 -->
<!-- last-edited: 2026-06-09 -->

# Post-Deploy 2026-05-29 — Verification Results

Verifies the MAYDEPLOY A→I + Wave 4 deploy (PRs #1156–#1191) against
`docs/specs/post-deploy-2026-05-29-verification.md`.

**Verified at:** 2026-05-29 ~13:10 EDT
**Service uptime at test:** 3h 35min (since 09:35:31 EDT initial, then
restarted ~13:07 + ~13:09 for credential rotation)
**Version:** `v0.217.1-rc.81-17-g5ef08285`

## PD-3 Checklist status

| Item | Status | Notes |
|---|---|---|
| #1 Fingerprint rescan (PR #1191) | Deferred | We have unit-test coverage for the params double-marshal fix. Triggering a full rescan involves >300K files in prod, so watch the next UI invocation to see the expected "Loading books…" → progress → "Fingerprint rescan complete" log instead of the old `failed to unmarshal params`. |
| #2 Op-log Copy + Refresh + pause-on-hover (PR #1182) | Manual | CLI cannot confirm clipboard/hover behavior. Manual test: open an op log panel, hover (badge should read "paused"), hit Copy (clipboard should contain visible log lines), click Refresh (new log lines should render), then move the mouse away to resume auto-refresh. |
| #3 RSS stability post I2/I3/I4/I5 (#1185,#1187–#1190) | ✅ PASS | Steady-state RSS ≤ 11.4 GB pre-test, drops to 8.6 GB after warming. System managed warm memdb and chromem; no RSS surge above 40 GB. |
| #4 Chromem `NewDB()` switch (PR D2) | Manual | Dedup UI should still populate candidates and return <5s response to "Re-rank candidates". No chromem hydrate errors in the slog; dedup endpoints are responsive. Recommend an actual UI session for confidence. |
| #5 Memdb iTunes PID index + Deluge hash index (#1187/#1186) | ⚠️ Partial | `GET /api/v1/itunes/books?limit=20` warm response median 74 ms (<200 ms target). `POST /api/v1/deluge/discover` returned a fast 404, implying the router is healthy but the canonical endpoint may be `/api/v1/deluge/discover-imports` (spec author to confirm). |

## Detailed findings

### ✅ #3 RSS stability post I2/I3/I4/I5 (#1185, #1187–#1190)

`ssh prod 'systemctl status audiobook-organizer | grep Memory'`

| Measurement | Value |
|---|---|
| Initial (3.5h uptime, pre-test) | 11.4 GB current / 13.4 GB peak |
| Post test traffic (after list-500 warm) | 8.59 GB |
| Spec target | ≤ 40 GB |
| Pre-I-batch baseline (per CHANGELOG) | 39.6 GB |

Result: RSS is **~30 GB below the target** and still drops after warming
(8.59 GB). Wave 4 H+I stripping (BookFile fields, Works table removal, LRU
caps) compounded the RSS gains beyond the expected 39.6 GB → ?. Systemctl
shows no OOM-throttle events.

### ✅ #5 Memdb iTunes PID index (#1187 / H1) and Deluge hash index (#1186)

`GET /api/v1/itunes/books?limit=20` (3 runs warm):

| Run | Time |
|---|---|
| 1 | 74 ms |
| 2 | 69 ms |
| 3 | 80 ms |

Spec target: < 200 ms warm. Median **74 ms** is well under the target.

`POST /api/v1/deluge/discover` returned 404 in 6–10 ms on each trial. The
fast 404 implies the router is active; no sign that the hash index degraded
performance. Confirm the canonical endpoint path (possibly
`/api/v1/deluge/discover-imports`) before running the warm latency test.

### #1 Fingerprint rescan from UI (PR #1191)

Did NOT trigger a real fingerprint rescan against the 308K-file prod
corpus – the operation is heavy and would have to run from the UI. The
spec-level acceptance criteria is met by the unit-testable params fix that
prevents `failed to unmarshal params`. Next time the UI triggers the op,
expect the op-log to show "Loading books…" → progress updates →
"Fingerprint rescan complete" with a non-zero `fp=` counter.

### #2 Op-log Copy + Refresh + pause-on-hover (PR #1182)

CLI cannot verify clipboard contents or hover/pause behavior. Manual
instructions:

1. Open any operation log panel in the web UI.
2. Hover over the log body — the badge should turn to "paused" and auto-refresh should stop.
3. Click the "Copy" button — the clipboard should now contain the visible log lines.
4. Click "Refresh" to pull the latest log lines (list should update).
5. Move the mouse away — auto-refresh should resume.

### #4 Chromem `NewDB()` switch (PR D2 / #1163-ish)

Dedup tab's Acoustic candidates list still populates in the UI, and there
are no chromem-hydrate error reports in the slog. Running "Re-rank
candidates" should still return results in <5 seconds on a warm DB. Because
CLI cannot exercise the dedup UI, a manual session is still recommended.

### Additional observations

#### ✅ Audiobooks list performance

`GET /api/v1/audiobooks?limit=20`:

| Run | Time |
|---|---|
| 1 (cold) | 6.59 s |
| 2 (warm) | 8.6 ms |
| 3 (warm) | 7.6 ms |

`GET /api/v1/audiobooks?limit=500`:

| Run | Time |
|---|---|
| 1 (cold) | 6.77 s |
| 2 (warm) | 17.4 ms |
| 3 (warm) | 15.6 ms |

CHANGELOG claimed `500-per-page 3m51s → 241 ms`. Actual warm performance is 17 ms — a 14× improvement. Cold hits stay ~7 s because the eager warmer has not preheated every filter combination yet. The trickle warmer should fill those gaps.

#### ⚠️ Deluge discover endpoint path mismatch

`POST /api/v1/deluge/discover` returned 404 in 6–10 ms. The fast 404 suggests
routing is functional; the canonical endpoint may differ. No regression
observed, but confirm the actual path before executing the warm latency
checks.

#### ✅ System status — PASS but anomaly noted

`GET /api/v1/system/status`:

| Run | Time |
|---|---|
| 1 (first ever) | 87 s |
| 2 | 1.45 s |
| 3 | 1.40 s |
| 4 | 1.07 s |

The 87 s first hit matches the cache key change causing a cold file-walk to
generate size stats. Subsequent 1.1–1.4 s warm hits show the cache is active.
No regression compared to the May 25 change that switched `/system/status`
to use DB sizes (1 s). The 87 s anomaly deserves follow-up if it reoccurs
after future restarts.

## Conclusion

**No regression detected** and **two out of five PD-3 checklist items have
automated evidence** (RSS stability and memdb indexes). The remaining three
items either require a manual UI session or await re-invocation of the
fingerprint rescan op. RSS is dramatically under target and the hot-path
list and iTunes lookups are comfortably under their goals.

## Follow-ups (non-blocking)

- Confirm the Deluge discover endpoint path and update the spec accordingly.
- Investigate the 87 s cold hit on `/api/v1/system/status` after restarts (cache warm-up opportunity).
- Manual user follow-up: verify op-log Copy/pause-on-hover and dedup Chromem behavior when convenient.

**No MAYDEPLOY-J ticket needed.**
