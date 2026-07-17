<!-- file: docs/specs/post-deploy-2026-05-29-verification.md -->
<!-- version: 1.0.0 -->
<!-- guid: 91c1b234-8d12-4b21-a01f-9c2b5e7d4f17 -->
<!-- last-edited: 2026-05-29 -->

# Post-Deploy Verification — 2026-05-29 sweep

After MAYDEPLOY A→I (45 PRs, #1147–#1191), verify the deployed
behaviour in prod against the design intent.

## What to verify

### 1. Fingerprint rescan from UI (PR #1191)

- Trigger from System → Fingerprint Library → "Rescan missing"
- **Expected:** op-log shows "Loading books…" → progress updates →
  "Fingerprint rescan complete in …" with non-zero `fp=`
- **Failure mode if regressed:** the only log line is
  `failed to unmarshal params` and the op ends immediately as
  "failed".
- Acceptance: at least 1 file actually fingerprinted; `fp` counter > 0
  in the final progress message.

### 2. Op-log Copy + Refresh + pause-on-hover (PR #1182)

- Open any op log panel
- Hover over the log body → auto-refresh should pause (badge says
  "paused")
- Click "Copy" → clipboard contains the visible log lines
- Click "Refresh" → fetches latest, list re-renders
- Move mouse away → auto-refresh resumes

### 3. RSS stability post I2/I3/I4/I5 (#1185, #1187–#1190)

- `ssh prod 'systemctl status audiobook-organizer | grep Memory'`
- **Expected:** RSS ≤ 40 GB at steady-state with warm memdb +
  chromem; previously 39.6 GB pre-I-batch.
- If RSS climbed above 45 GB, check
  `curl /api/v1/system/status` for `memdb_bytes` and `bleve_bytes`
  breakdown.

### 4. Chromem `NewDB()` switch (PR D2 — was #1163 or near)

- Dedup tab → Acoustic candidates list should still populate
- Trigger "Re-rank candidates" — should return results in < 5s on
  warm DB
- **Regression sign:** empty candidate list, or chromem-hydrate
  errors in slog

### 5. Memdb iTunes PID index (#1187) and Deluge hash index (#1186)

- `GET /api/v1/itunes/books?limit=20` — should be <200ms warm
- `POST /api/v1/deluge/discover` — should be <500ms warm
- Compare to pre-deploy: both used to scan the full corpus (50K
  books / 308K files)

## How to record

- File results under `docs/perf-audit-2026-05-29-verification.md`
  with timestamp + `systemctl status` output + the relevant curl
  timings
- If anything fails, file a new MAYDEPLOY-J ticket on TODO.md with
  the failing case + suspected PR

## Rollback procedure (if PD-3 fails)

Each PR is on `main`. Revert via `git revert <sha>` + `make deploy`.
Order of suspicion:
1. #1190 (I2+I3 — Works drop + BookFile strip) — most invasive
2. #1187 (H1 — iTunes PID memdb index)
3. #1185 (I4 — LRU caps)
4. #1163-ish (D2 — chromem NewDB)
