<!-- file: docs/pd3-prod-verification.md -->
<!-- version: 1.1.0 -->
<!-- guid: 9f1b0c2a-6ebd-43b8-94e9-8d2d1c4b5f7a -->
<!-- last-edited: 2026-07-17 -->

# PD-3 / Post-deploy verification — plan + results capture

This checklist captures the manual confirmation steps mentioned in
`TODO.md` (PD-3) for the MAYDEPLOY A⃗I / Wave 4 rollout, focusing on the
post-deploy behaviours that only production can prove.

## Scope

| Item | Description |
|---|---|
| 1 | Fingerprint rescan from the UI now runs the acoustid.fingerprint-rescan op without `failed to unmarshal params`. |
| 2 | Operation log panels support Copy + Refresh actions and pause auto-refresh on hover. |
| 3 | RSS after I2/I3/I4/I5 stays at or below the 40 GB target (ideally dropping further). |
| 4 | Chromem hydration switched from `NewPersistentDB` to `NewDB` without regressing dedup recall. |

## Verification steps

### 1. Fingerprint rescan

1. Open **System → Fingerprint Library** in the prod UI.
2. Click **Rescan missing** (or select the “books” scope and provide IDs).
3. Watch the activity log for the acoustid.fingerprint-rescan operation. A healthy run logs:
   - `Loading books for fingerprint rescan…` → progress updates.
   - Periodic `fp=<count>` / `skip=<count>` status messages.
   - Final line: `Fingerprint rescan complete… fp=<n>` with `fp` > 0.
4. Confirm there is **no** `failed to unmarshal params` error and that the progress message shows actual fingerprinted files.

### 2. Operation log Copy + Refresh + pause-on-hover

1. Open any running/completed operation and view the activity panel.
2. Hover over the log body until the “paused” badge appears (auto-refresh is suppressed).
3. Click the **Copy** icon; the clipboard should now contain the log text (paste into a scratch pad to prove it).
4. Click **Refresh**; the list should re-fetch from `/api/v1/operations/:id/activity` and show the latest entries.
5. Move the mouse away; auto-refresh should resume and the “paused” badge should disappear.

### 3. RSS stability post I2/I3/I4/I5

1. SSH into prod and run:
   ```bash
   systemctl status audiobook-organizer | grep -i memory
   ```
2. Target: RSS ≤ 40 GB steady-state (post warm-up). If RSS climbs above ~45 GB, gather:
   ```bash
   curl -s http://localhost:8080/api/v1/system/status | jq '.memdb_bytes, .bleve_bytes, .rss_bytes'
   ```
3. Document the `rss_bytes` readout + timestamp. Repeat the check after warm traffic to ensure RSS does not rebound.

### 4. Chromem `NewDB()` switch and dedup recall

1. Visit the Dedup tab and confirm the acoustic candidate list populates as usual.
2. Trigger **Re-rank candidates** for a non-empty scan result; expect completion within ~5 seconds on a warm hydrating DB.
3. Watch the logs for Chromem hydration warnings/errors (`internal/dedup/chromem` package names).
4. If the candidate list empties or re-rank fails, collect the dedup HTTP response + Chrome logs for triage.

## Recording results

Fill in this table each time PD-3 is exercised in prod. Attach `systemctl status` / `curl /system/status` snippets when relevant.

| Item | Status (pass / fail / blocked) | Verified at (UTC) | Notes + evidence |
|---|---|---|---|
| 1. Fingerprint rescan | | | |
| 2. Operation log Copy + Refresh | | | |
| 3. RSS ≤ 40 GB post hotfixes | | | |
| 4. Chromem dedup recall (NewDB) | | | |

## Reference

* Spec: [`docs/archive/2026-07-consolidation/specs/post-deploy-2026-05-29-verification.md`](docs/archive/2026-07-consolidation/specs/post-deploy-2026-05-29-verification.md)
* Result log: append new observations to `docs/archive/2026-07-consolidation/perf-audit-2026-05-29-verification.md` with timestamps and `/system/status` output.
