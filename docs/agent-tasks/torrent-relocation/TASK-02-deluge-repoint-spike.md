<!-- file: docs/agent-tasks/torrent-relocation/TASK-02-deluge-repoint-spike.md -->
<!-- version: 1.0.0 -->
<!-- guid: b8e8c07b-9780-47ac-9f2e-349cbd302e1e -->
<!-- last-edited: 2026-07-10 -->

# TASK-02 — Real-Deluge re-point spike + UpdateStoragePath implementation (INIT-5 T2) [⚠ review-critical]

**Gate:** SPEC -> EXECUTE with a hard human gate: T2 is a REAL-DELUGE SPIKE with STOP-FOR-HUMAN sign-off REQUIRED before T3's call-site migration may start. The new relocation mode is config-gated (tri-state: off / physical-move / re-point-only); defaults STAY on today's behavior until the T2 spike is human-approved.
**File-ownership:** none cross-initiative. Within INIT-5: shares `internal/download/deluge.go` with TASK-01 — TASK-01 must be merged first (wave1=TASK-01, wave2=TASK-02).

**Priority:** P0 · **Effort:** L · **Recommended subagent:** Opus/strong-class · live-system-spike subagent, **SINGLE-AGENT + OPERATOR-IN-LOOP — NEVER weak-tier** · **Why:** exercises destructive RPCs (remove_torrent) against the real Deluge daemon; a fail-open mistake deletes seeding state or payload — the initiative payload mandates strong-model + operator · **Depends on:** TASK-01

**Dispatch-readiness (coordinator):** BLOCKED until TASK-01's PR is merged to `origin/main`.
Verified 2026-07-10 at HEAD `fce58498`: `grep -n 'UpdateStoragePath' internal/download/deluge.go`
returns **0 hits**, so the anchor-block STOP below WILL fire if this brief is dispatched today.
Hold this brief until TASK-01 merge is confirmed (`git log origin/main --oneline | head` shows the
TASK-01 commit, or the grep above hits). The STOP firing before then is expected, not a defect.

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/torrent-relocation-deluge-repoint-spike" -b agent/torrent-relocation-deluge-repoint-spike origin/main
cd "$REPO/.worktrees/torrent-relocation-deluge-repoint-spike"
git rebase origin/main
```

## Goal

Determine, against the REAL Deluge instance, which of two candidate mechanisms re-points a
torrent's storage path with ZERO physical move; implement
`func (d *DelugeClient) UpdateStoragePath(ctx context.Context, id, newPath string) error` in
`internal/download/deluge.go` using the winner (replacing the TASK-01 stub); write the spike
evidence report; then **STOP FOR HUMAN SIGN-OFF** (a real AskUserQuestion decision — a
text-reply approval does not count) before TASK-03 may start. Nothing in any pipeline calls the
new method yet — that is TASK-03's job, after sign-off.

## Background (verify before editing)

- Deluge has **no update-path-only RPC**. Physical moves: `core.move_storage` inside
  `internal/deluge/client.go:215-220` (`MoveStorage`) and inside
  `internal/download/deluge.go:198` (`SetDownloadPath`).
- The client to extend is `internal/download.DelugeClient` — it has ctx-aware RPC plumbing
  (private `call` method, `internal/download/deluge.go:52`) to REUSE; do NOT write a new
  HTTP/RPC layer and do NOT touch `internal/deluge/client.go` (the legacy concrete client keeps
  serving the physical-move path unchanged).
- The "T2 spike protocol" (pre-execution confirmation, both candidate mechanisms, the 4-item
  validation checklist, the STOP-for-human decision point) is reproduced **IN FULL, normatively,
  in this brief** — Steps 1–3 and Step 8 below ARE the protocol; execute from this brief alone.
  (Its source document, `docs/specs/2026-07-10-torrent-relocation-design.md`, lives only in the
  separate PLANNING repo `audiobook-organizer-plan-remaining-work` — it is NOT present in the
  worktree you just created. Do not go looking for it; nothing normative exceeds what is inlined
  below.)
- Spike environment: the real Deluge configured on the server; use a **THROWAWAY test torrent
  only — never a library torrent**. Creating/removing test data on prod over SSH is allowed
  (memory `feedback_ssh_delete`).
- Fail-closed scope, stated precisely: fail-closed on RPC ERRORS (capture state first; on any
  RPC failure restore/leave the OLD path registered and return the error). A process CRASH
  inside mechanism A's remove-before-re-add window is a residual, mechanism-A-only orphan risk —
  no recovery code runs through a crash. Record it in the report; do not claim it away.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n 'func (d \*DelugeClient) SetDownloadPath' internal/download/deluge.go   # ~:193, 1 hit — physical sibling
  grep -n 'core.move_storage' internal/download/deluge.go                          # the physical RPC, ~:198, 1 hit
  grep -n 'func (d \*DelugeClient) call' internal/download/deluge.go               # RPC plumbing to reuse, ~:52, 1 hit
  grep -n 'UpdateStoragePath' internal/download/deluge.go                          # TASK-01 stub to replace, >=1 hit (0 hits = TASK-01 not merged: STOP)
  ```
  Zero hits on the first three = STOP and report; do not guess.

## Step-by-step

1. The two candidate mechanisms (complete scope — this brief IS the protocol, no external
   document needed):
   - **A — remove + re-add with recheck:** `core.get_torrent_status` (capture save_path/label/
     state) → obtain the `.torrent` filedump (Deluge state dir `<config>/state/<infohash>.torrent`
     on the daemon host, or an RPC-side source you identify) → `core.remove_torrent(id, false)` →
     `core.add_torrent_file(name, filedumpB64, {"download_location": newDir, "add_paused": true})`
     → `core.force_recheck([id])` → poll → `core.resume_torrent([id])` → `label.set_torrent`.
     Pros: works on any Deluge 2.x; data provably untouched. Cons: needs the .torrent blob; a
     torrent-absent window (crash inside it = orphan — RESIDUAL, unrecoverable-in-process);
     per-torrent state (stats, added-time, label — re-apply the label) is irreversibly reset
     even on success; fail-closed demands capture-before-remove and re-add-at-OLD-path recovery.
   - **B — move_completed_path + resume:** `core.pause_torrent([id])` →
     `core.set_torrent_options([id], {"move_completed": false, "move_completed_path": newDir, ...})`
     plus whatever download-location option Deluge accepts without I/O → `core.force_recheck([id])`
     → `core.resume_torrent([id])`. Pros: no remove window; state preserved. Cons: Deluge may
     reject the option or silently trigger a physical move (DISQUALIFYING — observe I/O).
2. **Pre-execution confirmation (REQUIRED, before ANY destructive RPC):** after creating/choosing
   the throwaway torrent, raise an AskUserQuestion that ECHOES the exact torrent hash AND name
   and asks the operator to confirm it is the throwaway. Do not issue `core.remove_torrent` (or
   any destructive call) until this is answered. Record the question + answer in the spike
   report. "Throwaway only" is a prose boundary — a wrong torrent-ID hits a real seeding torrent.
3. Run the spike with the confirmed throwaway torrent. Validation checklist per mechanism (all
   four required):
   - [ ] seeds from the NEW path (`core.get_torrent_status` shows new save_path; state → Seeding)
   - [ ] ZERO physical move on Deluge's side (record `ls -i` inodes + mtimes at the new path
     before/after; old path not recreated; no move-attributable I/O)
   - [ ] recheck completes to 100% with no re-download
   - [ ] no data loss on failure — **fail-closed on RPC errors**: inject a mid-sequence failure;
     verify the procedure aborts, the OLD path stays registered (or is restored via recovery
     re-add), payload intact. Note the mechanism-A crash-window residual explicitly.
4. Implement `UpdateStoragePath(ctx context.Context, id, newPath string) error` on
   `*DelugeClient` in `internal/download/deluge.go` using the winning mechanism, mirroring
   `SetDownloadPath`'s shape (`d.call(ctx, ...)`). Fail-closed on every RPC error path: capture
   state FIRST; on any failure restore/leave the OLD path registered and return the error; never
   leave the torrent removed or half-re-pointed by a HANDLED error.
5. Add tests in `internal/download/`. NOTE: `internal/download/*_test.go` has NO httptest mock
   today (its Deluge tests only assert connection errors against a dead port) — the JSON-RPC
   mock pattern to mirror lives in `internal/deluge/client_test.go` (verify:
   `grep -n 'httptest.NewServer' internal/deluge/client_test.go`, ≥1 hit — 8 at HEAD `fce58498`): an
   `httptest.NewServer(http.HandlerFunc(...))` that decodes the RPC request, switches on
   `req.Method`, and encodes a canned RPC response per method. Your tests are in-package
   (`package download`), so point the client at the mock by constructing
   `c := NewDelugeClient(cfg)` and then overriding the two private fields directly:
   `c.baseURL = srv.URL` and `c.client = srv.Client()` (precedent for in-test assignment of
   `client.client`: `internal/download/download_test.go:311`). Cases: (a) happy path issues the
   winning RPC sequence in order — record `req.Method` values in the handler and assert the
   ordered list (anti-over-suppression: the re-point DOES happen when everything is healthy);
   (b) mid-sequence failure (handler returns an RPC error for one mid-sequence method) → old
   path registered (assert the recovery RPC was issued / no path-changing RPC succeeded), error
   returned; (c) empty id / empty newPath → error, no RPC issued (handler counts stay 0).
6. Write NEW `docs/reports/2026-07-torrent-repoint-spike.md` (4-line header per house rules):
   the pre-execution confirmation record, RPC transcripts per mechanism, before/after
   `core.get_torrent_status`, inode/mtime proof, failure-injection result, recommendation
   (A or B) + residual risks (crash-window orphan; state reset under A).
7. Bump the file header (version + last-edited) on every file you touch; keep existing guids.
8. **STOP FOR HUMAN:** present the report and ask for sign-off via a real AskUserQuestion
   (options e.g. "Approve mechanism A — unblock TASK-03" / "Approve B" / "Reject — hardening-only
   fallback"). **The AskUserQuestion OUTCOME is the source of truth.** Record it in the report as
   ONE canonical decision line, written ONLY after the human answers:
   - on approval: `DECISION: APPROVED mechanism <A|B> via AskUserQuestion YYYY-MM-DD`
   - on rejection: `DECISION: REJECTED — hardening-only fallback via AskUserQuestion YYYY-MM-DD`
   Never write the word sequence `DECISION: APPROVED` anywhere else in the report (TASK-03's
   precondition greps `^DECISION: APPROVED` — the bare word "Approve" appears in option labels
   and must not be what gates anything). Do NOT proceed to merge-enabling-TASK-03 without an
   APPROVED line. If both mechanisms fail validation (or the human rejects), write the REJECTED
   line and report BLOCKED: TASK-03/05/07 re-point wiring is blocked; the initiative falls back
   to hardening-only (TASK-04 re-parents to `origin/main` per the plan's fallback note).

## How to test

```bash
make ci
```
staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you changed; the merge gate is Minimal CI green.

## Acceptance criteria

- [ ] `grep -n "func (d \*DelugeClient) UpdateStoragePath" internal/download/deluge.go` hits with a non-stub body (no `ErrRePointUnsupported` return in it)
- [ ] Pre-execution AskUserQuestion (exact throwaway hash + name echoed) recorded in the report BEFORE the first destructive RPC transcript
- [ ] `test -f docs/reports/2026-07-torrent-repoint-spike.md` and all four checklist items are evidenced inside it
- [ ] Anti-over-suppression: happy-path test proves the re-point RPC sequence IS issued when healthy (`grep -n "TestUpdateStoragePath" internal/download` -r hits)
- [ ] Fail-closed test: injected failure leaves old path registered (test present + green)
- [ ] Human sign-off recorded as the canonical line: `grep -n '^DECISION: APPROVED' docs/reports/2026-07-torrent-repoint-spike.md` hits (or `^DECISION: REJECTED` + BLOCKED reported)
- [ ] Tests green; vet/lint clean (`make ci` exits 0, staticcheck scoped to changed files).
- [ ] File headers bumped on every changed file.

## Commit message

```
feat(download): implement Deluge UpdateStoragePath re-point via spike-validated mechanism (INIT-5 T2)

Real-Deluge spike compared remove+re-add-with-recheck vs move_completed_path+
resume; evidence in docs/reports/2026-07-torrent-repoint-spike.md. Fail-closed
on RPC errors: the old storage path stays registered on any handled failure
(crash-in-remove-window residual documented). Not wired to any pipeline yet —
call-site migration is TASK-03, gated on human sign-off.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/torrent-relocation-deluge-repoint-spike
gh pr create --fill
gh pr merge <number> --rebase
```
Merge only AFTER the AskUserQuestion sign-off — the coordinator gives this PR line-by-line review (⚠ review-critical).

## Idempotency / Rollback

If `grep -n "func (d \*DelugeClient) UpdateStoragePath" internal/download/deluge.go` hits with a
non-stub body AND `test -f docs/reports/2026-07-torrent-repoint-spike.md` succeeds, this task is
already applied — run the acceptance checks (including the `^DECISION:` line) instead of
re-applying. Rollback = revert the commit; nothing calls `UpdateStoragePath` until TASK-03, so
runtime behavior is unchanged; the throwaway spike torrent is deleted from Deluge as part of
spike cleanup.
