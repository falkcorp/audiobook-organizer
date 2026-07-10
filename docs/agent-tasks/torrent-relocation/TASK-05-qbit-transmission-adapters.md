<!-- file: docs/agent-tasks/torrent-relocation/TASK-05-qbit-transmission-adapters.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7297cb01-2675-4ab5-ac85-c5cbe2d503f6 -->
<!-- last-edited: 2026-07-10 -->

# TASK-05 — qBittorrent re-point + Transmission client in internal/download (INIT-5 T5)

**Gate:** SPEC -> EXECUTE with a hard human gate: T2 is a REAL-DELUGE SPIKE with STOP-FOR-HUMAN sign-off REQUIRED before T3's call-site migration may start. The new relocation mode is config-gated (tri-state: off / physical-move / re-point-only); defaults STAY on today's behavior until the T2 spike is human-approved.
**File-ownership:** none cross-initiative. Within INIT-5: shares `internal/download/qbittorrent.go` with TASK-01 — TASK-01 must be merged first (wave1=TASK-01, wave4=TASK-05). `internal/download/transmission.go` is new and exclusively owned. Does NOT touch `internal/config/config.go` (no new config key — see Goal). Runs parallel to TASK-04/TASK-07 (disjoint files).

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · adapter subagent · **Why:** one new client + one method on an existing client against mock HTTP servers — well-bounded, but protocol mapping needs more care than Haiku-tier · **Depends on:** TASK-01 + TASK-02 (human-approved: `^DECISION: APPROVED` in the spike report). **SKIP this task entirely if the spike REJECTED re-point** — building re-point adapters for a disqualified mode is dead scaffolding.

**Dispatch-readiness (coordinator):** BLOCKED until BOTH (a) TASK-01's PR is merged to
`origin/main` AND (b) TASK-02's spike report exists with a `DECISION:` line. Verified 2026-07-10
at HEAD `fce58498`: `grep -n 'UpdateStoragePath' internal/download/client.go` returns **0 hits**
and `docs/reports/2026-07-torrent-repoint-spike.md` does not exist, so several anchors below WILL
fail if this brief is dispatched today — that is expected, not a defect. Hold this brief until
both preconditions are confirmed on `origin/main`.

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/torrent-relocation-qbit-transmission-adapters" -b agent/torrent-relocation-qbit-transmission-adapters origin/main
cd "$REPO/.worktrees/torrent-relocation-qbit-transmission-adapters"
git rebase origin/main
```

**Precondition check — evaluate IN THIS ORDER; the three outcomes are different (BLOCKED vs
SKIPPED vs proceed) and must not be conflated:**

1. `test -f docs/reports/2026-07-torrent-repoint-spike.md` — file **MISSING** ⇒ TASK-02 has not
   run yet (grep on a missing file errors with "no such file", which is NOT a rejection): STOP
   and report **BLOCKED on TASK-02**. Do not skip, do not guess.
2. `grep -c '^DECISION:' docs/reports/2026-07-torrent-repoint-spike.md` — **0** ⇒ the spike ran
   but the human gate is unanswered: STOP and report **BLOCKED on TASK-02 sign-off**.
3. `grep -n '^DECISION: APPROVED' docs/reports/2026-07-torrent-repoint-spike.md` — hits ⇒
   proceed. 0 hits here (the line present is `^DECISION: REJECTED`) ⇒ **SKIP this task
   entirely** per the plan's fallback note: report SKIPPED (not blocked), make no changes.
4. `grep -n 'UpdateStoragePath(ctx context.Context' internal/download/client.go` — 0 hits ⇒
   TASK-01 not merged: STOP and report **BLOCKED on TASK-01**.

## Goal

Additive (P2), REUSING the existing `internal/download` family — the repo already has TWO
half-wired client abstractions and this task must not create a third:

1. Implement `UpdateStoragePath` on the EXISTING `QBittorrentClient`
   (`internal/download/qbittorrent.go`) via the EXISTING `setLocation` request shape (replace
   TASK-01's stub).
2. NEW `internal/download/transmission.go`: a full `download.TorrentClient` implementation
   (Transmission has a TRUE no-move re-point: `torrent-set-location {"move": false}`).
3. Extend the existing factory switch (`internal/download/factory.go`) with `"transmission"`.

NO new packages (`internal/plugins/qbittorrent` and `internal/plugins/transmission` must NOT be
created). NO new config key: the client type is the EXISTING `cfg.DownloadClient.Torrent.Type`
(single source of truth — do not add a parallel selector). Deluge stays the default; both new
re-point paths remain unreachable behind TASK-03's validated-client hard guard (allowlist:
`deluge` only) until a T2-class validation per client.

## Background (verify before editing)

- The interface (TASK-01): `internal/download/client.go` `TorrentClient` — `Connect`,
  `GetTorrent`, `GetUploadStats`, `SetDownloadPath` (PHYSICAL), `RemoveTorrent`, ...,
  `UpdateStoragePath` (re-point). REUSE `download.ErrRePointUnsupported`; invent no parallel
  types.
- The existing qBittorrent client ALREADY speaks WebAPI v2 including
  `POST /api/v2/torrents/setLocation` (`internal/download/qbittorrent.go:181`, inside
  `SetDownloadPath`) — reuse its request/auth/error shape verbatim for `UpdateStoragePath`; do
  not rewrite the client.
- qBittorrent caveat (spell it in code comments AND tests): `setLocation` MAY physically move
  data when the source still exists. Our contract is "we already moved the files". This is why
  the TASK-03 hard guard keeps re-point-only unreachable for qBittorrent until validated against
  a real qBittorrent (spec Decision 7 / Open question 5) — the guard, not documentation, is the
  protection. Fail-closed: non-2xx ⇒ error, old path considered still registered.
- Transmission RPC: `POST /transmission/rpc`, `X-Transmission-Session-Id` handshake on 409;
  `torrent-get` for status; `torrent-set-location {"location": newDir, "move": false}` =
  re-point (`UpdateStoragePath`); same call with `"move": true` = physical (`SetDownloadPath`).
  Same unvalidated-until-proven guard treatment as qBittorrent.
- Factory: `NewTorrentClientFromConfig` (`internal/download/factory.go:14`) switches on
  `cfg.DownloadClient.Torrent.Type` (`deluge` / `qbittorrent` / `""`→nil) — add `"transmission"`.
  Transmission connection config: add a `Transmission` block to the EXISTING
  `config.TorrentClientConfig` struct ONLY if one does not already exist — check first
  (`grep -n 'Transmission' internal/config/config.go`); if a config change is genuinely required,
  it is a struct-field addition inside `TorrentClientConfig`, NOT a new top-level key, and must
  be coordinated with the coordinator because `internal/config/config.go` is TASK-06 territory
  in wave 1 (merged long before wave 4 — a post-merge additive field is collision-free).

- **Re-verify these anchors before editing** — line numbers drift. The first and fourth anchors
  land with TASK-01 and are 0-hit until it merges (see Dispatch-readiness); the middle three are
  verified present at today's HEAD `fce58498`:
  ```bash
  grep -n 'UpdateStoragePath(ctx context.Context' internal/download/client.go   # interface (TASK-01), 1 hit — 0 hits = TASK-01 not merged: STOP (BLOCKED)
  grep -n 'func NewTorrentClientFromConfig' internal/download/factory.go        # switch to extend, ~:14, 1 hit (exists today)
  grep -n 'setLocation' internal/download/qbittorrent.go                        # request shape to reuse, >=1 hit, ~:183/:190/:194 (exists today)
  grep -n 'func (q \*QBittorrentClient) UpdateStoragePath' internal/download/qbittorrent.go  # TASK-01 stub to replace, 1 hit — 0 hits = TASK-01 not merged: STOP (BLOCKED)
  grep -n 'func TestTorrentClientInterface' internal/download/download_test.go  # satisfaction test to extend, ~:18, 1 hit (exists today)
  ```

## Step-by-step

1. `internal/download/qbittorrent.go`: replace the TASK-01 stub body of `UpdateStoragePath` with
   the `setLocation` POST (mirror `SetDownloadPath`'s request/auth/error handling one-for-one).
   Doc comment carries the caveat + guard pointer from Background.
2. NEW `internal/download/transmission.go` (+ fresh guid header): `TransmissionClient`
   implementing the FULL `TorrentClient` interface — session-id handshake, `torrent-get`
   (GetTorrent/GetUploadStats/ListTorrents-equivalents per the interface), `torrent-set-location`
   with `move:true` for `SetDownloadPath`, `move:false` for `UpdateStoragePath`, `torrent-remove`
   for `RemoveTorrent`. Mirror the struct/constructor shape of the existing clients
   (`NewTransmissionClient(cfg ...)`).
3. `internal/download/factory.go`: add `case "transmission":`. If a Transmission connection
   config block is needed, follow the Background note (additive field inside
   `TorrentClientConfig` only).
4. Extend `TestTorrentClientInterface` (`internal/download/download_test.go`) to include the
   Transmission client. Add `httptest.NewServer`-mock tests: transmission `UpdateStoragePath`
   sends a body containing `"move": false` (and `SetDownloadPath` sends `"move": true`);
   qbittorrent `UpdateStoragePath` posts to `/api/v2/torrents/setLocation`; non-2xx / RPC-error
   responses return an error (fail-closed); unconfigured client errors without panicking;
   factory: `"transmission"` returns the new client, empty type still returns nil,nil.
   Anti-over-suppression: N/A (no filter/guard/veto/skip/dedupe path is added — the TASK-03
   guard is not this task's code).
5. Do NOT wire anything into any pipeline and do NOT touch the TASK-03 allowlist — lifting the
   guard for qbittorrent/transmission is a future, per-client T2-class validation task.
6. Bump/add the 4-line file header on every file (new files get fresh guids via
   `uuidgen | tr 'A-Z' 'a-z'`).

## How to test

```bash
make ci
```
staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you changed; the merge gate is Minimal CI green.

## Acceptance criteria

- [ ] `test -f internal/download/transmission.go` and `grep -n 'case "transmission"' internal/download/factory.go` hits
- [ ] NO new packages: `test ! -d internal/plugins/qbittorrent && test ! -d internal/plugins/transmission`
- [ ] `grep -rn '"move": *false\|"move":false' internal/download/transmission.go` hits (native no-move re-point)
- [ ] qbittorrent `UpdateStoragePath` is non-stub (`grep -n 'ErrRePointUnsupported' internal/download/qbittorrent.go` → 0 hits in its body) and its doc comment carries the may-physically-move caveat
- [ ] NO new config selector key: `grep -rn 'torrent_relocation_client' internal/ web/` → 0 hits
- [ ] TASK-03's allowlist untouched: `git diff origin/main -- internal/deluge/integration.go` is empty for this task
- [ ] Anti-over-suppression: N/A
- [ ] Tests green (`TestTorrentClientInterface` covers 3 clients); vet/lint clean (`make ci` exits 0, staticcheck scoped to changed files).
- [ ] File headers present/bumped on every file.

## Commit message

```
feat(download): qBittorrent re-point + Transmission TorrentClient (INIT-5 T5)

Reuses the existing internal/download family instead of forking a new plugin
package: implements UpdateStoragePath on the existing qBittorrent client via
its existing setLocation shape (with a documented may-physically-move caveat)
and adds a full Transmission client with the native no-move re-point
(torrent-set-location move:false), plus the factory case. Client type stays
cfg.DownloadClient.Torrent.Type — no new config key. Both re-point paths stay
behind the validated-client hard guard (allowlist: deluge) until a T2-class
validation per client.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/torrent-relocation-qbit-transmission-adapters
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `test -f internal/download/transmission.go` succeeds AND the qbittorrent `UpdateStoragePath`
is non-stub, this task is already applied — run the acceptance checks instead of re-applying.
Rollback = revert the commit; the transmission factory case disappears and qbittorrent re-point
returns to the fail-closed stub; nothing is reachable past the TASK-03 hard guard by default, so
removal has zero runtime effect.
