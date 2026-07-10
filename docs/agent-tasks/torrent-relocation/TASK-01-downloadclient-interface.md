<!-- file: docs/agent-tasks/torrent-relocation/TASK-01-downloadclient-interface.md -->
<!-- version: 1.0.0 -->
<!-- guid: cfa9fbdd-6e5e-434d-a51a-63df6abbb219 -->
<!-- last-edited: 2026-07-10 -->

# TASK-01 — Extend download.TorrentClient with UpdateStoragePath + fail-closed stubs (INIT-5 T1)

**Gate:** SPEC -> EXECUTE with a hard human gate: T2 is a REAL-DELUGE SPIKE with STOP-FOR-HUMAN sign-off REQUIRED before T3's call-site migration may start. The new relocation mode is config-gated (tri-state: off / physical-move / re-point-only); defaults STAY on today's behavior until the T2 spike is human-approved.
**File-ownership:** none cross-initiative. Within INIT-5: shares `internal/download/deluge.go` with TASK-02 (serialize wave1=TASK-01, wave2=TASK-02) and `internal/download/qbittorrent.go` with TASK-05 (serialize wave1=TASK-01, wave4=TASK-05).

**Priority:** P1 · **Effort:** S · **Recommended subagent:** Sonnet-class · interface-extension subagent · **Why:** small additive Go interface change on an interface with exactly two in-package implementors, but it defines the semantics every later task builds on — above Haiku, below Opus · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/torrent-relocation-downloadclient-interface" -b agent/torrent-relocation-downloadclient-interface origin/main
cd "$REPO/.worktrees/torrent-relocation-downloadclient-interface"
git rebase origin/main
```

## Goal

Add a re-point-only relocation method
`UpdateStoragePath(ctx context.Context, id, newPath string) error` to the EXISTING
`internal/download.TorrentClient` interface, plus a package-level sentinel
`ErrRePointUnsupported`, and give BOTH existing implementors (`DelugeClient`,
`QBittorrentClient`) fail-closed stubs returning `ErrRePointUnsupported` — until TASK-02
(Deluge) / TASK-05 (qBittorrent, guarded) replace them. Do NOT invent a new interface and do
NOT extend `internal/plugin.DownloadClient` (see Background — it is vestigial and infeasible).

## Background (verify before editing)

- The interface home is `internal/download.TorrentClient` (`internal/download/client.go:54`):
  it already has REAL implementors (`internal/download/deluge.go`, `internal/download/qbittorrent.go`),
  ctx-aware RPC plumbing, a config-keyed factory (`NewTorrentClientFromConfig`,
  `internal/download/factory.go:14`), and a re-point-shaped method slot (`SetDownloadPath` —
  currently PHYSICAL: `core.move_storage` / qBittorrent `setLocation`).
- Why NOT `internal/plugin.DownloadClient` (`internal/plugin/plugin.go:47-52`): it embeds
  `internal/plugin.Plugin` (Capabilities/Init/Shutdown/HealthCheck), but the Deluge plugin
  (`internal/plugins/deluge/plugin.go`) implements a DIFFERENT base interface —
  `pkg/plugin/sdk.Plugin` (ID/Name/Version/Register only). A compile-assert
  `var _ plugin.DownloadClient = (*Plugin)(nil)` can never hold without bridging two plugin
  frameworks. That interface stays untouched except a doc-comment marking it vestigial.
- Adding a method to `TorrentClient` breaks every implementor until the stubs land — the
  package's own `TestTorrentClientInterface` (`internal/download/download_test.go:18`) enforces
  satisfaction at compile time. Only the two implementors above exist
  (verify: `grep -rln 'TorrentClient' --include='*.go' internal/` → only `internal/download/*`
  and the config type name `TorrentClientConfig` in `internal/config`).

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n 'TorrentClient interface' internal/download/client.go                       # interface, ~:54, 1 hit
  grep -n 'SetDownloadPath(ctx context.Context' internal/download/client.go           # physical slot to doc-harden, 1 hit
  grep -n 'func (d \*DelugeClient) SetDownloadPath' internal/download/deluge.go       # implementor 1, ~:193, 1 hit
  grep -n 'func (q \*QBittorrentClient) SetDownloadPath' internal/download/qbittorrent.go  # implementor 2, ~:181, 1 hit
  grep -n 'TestTorrentClientInterface' internal/download/download_test.go             # compile-time satisfaction test, 1 hit
  ```
  Zero hits on any grep = STOP and report; do not guess.

## Step-by-step

1. In `internal/download/client.go`, add to the `TorrentClient` interface (directly under
   `SetDownloadPath`): `UpdateStoragePath(ctx context.Context, id, newPath string) error`, with a
   doc comment stating: re-point ONLY — the caller has already moved the data; implementations
   MUST be fail-closed on RPC errors (on any RPC failure the old path stays registered); a
   process crash inside a remove-before-re-add window (mechanism-A residual) is documented in the
   spec's T2 spike protocol, not promisable away here. Add
   `var ErrRePointUnsupported = errors.New("re-point-only relocation not supported by this client")`
   near the interface (add the `errors` import if missing).
2. Doc-harden `SetDownloadPath` on the interface and both implementors: PHYSICAL move
   (`core.move_storage` / `setLocation`); point at `UpdateStoragePath` for re-point.
3. Add the fail-closed stubs: `func (d *DelugeClient) UpdateStoragePath(ctx context.Context, id, newPath string) error { return ErrRePointUnsupported }`
   in `internal/download/deluge.go`, and the same on `*QBittorrentClient` in
   `internal/download/qbittorrent.go`.
4. In `internal/plugin/plugin.go`, doc-comment ONLY on the `DownloadClient` interface: vestigial
   (zero implementors; wrong plugin framework for `internal/plugins/deluge`); new torrent-client
   work extends `internal/download.TorrentClient` instead. Do NOT add methods, do NOT change any
   signature, do NOT touch anything else in that file.
5. Keep the change purely additive elsewhere — do not touch the factory, usenet side, or any
   other method. Do not change signatures.
6. Tests in `internal/download/download_test.go`: (a) existing `TestTorrentClientInterface`
   still compiles/passes (it IS the compile-time assertion); (b) both stubs return
   `ErrRePointUnsupported` (use `errors.Is`).
   Anti-over-suppression: N/A (no filter/guard/veto/skip/dedupe path is added to any existing flow).
7. Bump the file header (version + last-edited) on every file you touch; keep existing guids.

## How to test

```bash
make ci
```
staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you changed; the merge gate is Minimal CI green.

## Acceptance criteria

- [ ] `grep -n "UpdateStoragePath(ctx context.Context, id, newPath string) error" internal/download/client.go` hits (interface extended)
- [ ] `grep -n "func (d \*DelugeClient) UpdateStoragePath" internal/download/deluge.go` and `grep -n "func (q \*QBittorrentClient) UpdateStoragePath" internal/download/qbittorrent.go` both hit (stubs)
- [ ] `go test ./internal/download/... -short` green; `errors.Is(err, download.ErrRePointUnsupported)` asserted for both stubs
- [ ] `grep -n 'vestigial' internal/plugin/plugin.go` hits (doc-comment); `git diff origin/main -- internal/plugin/plugin.go` contains NO non-comment change
- [ ] Anti-over-suppression: N/A
- [ ] Tests green; vet/lint clean (`make ci` exits 0, staticcheck scoped to changed files).
- [ ] File headers bumped on every changed file (`grep -n "last-edited:" <file>` shows 2026-07-10 or later).

## Commit message

```
feat(download): add UpdateStoragePath re-point semantics to TorrentClient (INIT-5 T1)

Extends the existing internal/download.TorrentClient interface with a
re-point-only relocation method distinct from the physical SetDownloadPath,
with fail-closed ErrRePointUnsupported stubs on the Deluge and qBittorrent
clients until the real-Deluge spike (TASK-02) lands the mechanism. Marks the
vestigial internal/plugin.DownloadClient as not-the-seam (doc-comment only).

Co-Authored-By: Claude <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/torrent-relocation-downloadclient-interface
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -n "UpdateStoragePath" internal/download/client.go` hits, this task is already applied —
run the acceptance checks instead of re-applying. Rollback = revert the commit; the interface
returns to its prior shape and no runtime path is affected (nothing consumes the new method
until TASK-03).
