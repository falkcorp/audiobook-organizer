<!-- file: docs/agent-tasks/todo-completion/docs/TASK-057-phase-8-write-the-abs-topology-runbook-and-migra.md -->
<!-- version: 1.0.0 -->
<!-- guid: ef90ed85-aa9d-4fa2-af31-874652e3fdaa -->
<!-- last-edited: 2026-08-21 -->

# TASK-057 — Phase 8 — write the ABS topology, runbook, and migration guide (Cloudflare Access ordering, cover/image bypass, client compat matrix) (ABS-SYNC-Phase8)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · docs subagent · **Why:** Pure documentation synthesis task pulling together several already-known operational facts (CF Access policy ordering, cover bypass, tunnel JWT, client matrix) into one runbook — no code, but requires care to get the security-critical ordering advice right · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 10358 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**ABS-SYNC: Phase 8 — topology, runbook, migration" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-13.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/docs-057-phase-8-write-the-abs-topology-runbook-and-migra" -b agent/docs-057-phase-8-write-the-abs-topology-runbook-and-migra origin/main
cd "$REPO/.worktrees/docs-057-phase-8-write-the-abs-topology-runbook-and-migra"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Write docs/reference/abs-sync-topology-runbook.md covering: (1) the Cloudflare Access service-token requirement to sit in a DEDICATED Service Auth policy ordered FIRST (both AudioBooth and Absorb's issue trackers hit this as a footgun); (2) the cover/image bypass (§1.9.5 of the client contract — grep docs/reference/abs-target-client-contract.md for the exact requirement); (3) tunnel-level JWT enforcement notes; (4) a client compatibility matrix (AudioBooth vs Absorb: websocket need, cover bypass need, etc.); (5) the two operational gotchas the item names explicitly: 'never trust an app's reachability checkmark' (Access returns HTTP 200 with HTML on failure, which looks like a JSON-decode error to the client) and 'AudioBooth's first-server-add cover bug is upstream, not ours.'

## Background (verify before editing)

- docs/reference/abs-target-client-contract.md already documents the byte-level DTO contract (§1.7-1.9) this runbook should cross-reference rather than duplicate.
- docs/reference/abs-implementation-status.md already documents the served-route inventory and gap list this runbook should link to for 'what's implemented' rather than re-deriving it.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  find docs \( -iname '*runbook*' -o -iname '*topology*' \) | grep -i abs   # 0 hits — no existing runbook/topology doc is ABS-scoped — no topology/runbook doc scoped to ABS-sync exists today (generic runbook/topology docs do exist elsewhere, e.g. docs/system/runbooks.md, but none is ABS-specific)
  ls docs/reference/abs-target-client-contract.md docs/reference/abs-implementation-status.md   # both files exist — the sibling reference docs this new doc should match in structure/location already exist
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Read docs/reference/abs-target-client-contract.md §1.9.5 (cover/image bypass) and §1.7-1.8 (DTO contract) to extract the exact requirements to cite, not re-derive.
2. Read docs/reference/abs-client-network-audit.md (if it covers CF Access ordering already) to avoid duplicating content — link to it instead where it already covers a topic.
3. Write docs/reference/abs-sync-topology-runbook.md with sections: Topology (tunnel + CF Access + AO server), CF Access Service Auth Policy Ordering (the dedicated-policy-first requirement, with a worked example of the wrong vs right policy order), Cover/Image Bypass, Tunnel-level JWT Enforcement, Client Compatibility Matrix (a markdown table: feature x AudioBooth x Absorb), and Known Client-Side Gotchas (reachability-checkmark trap, AudioBooth first-server-add cover bug).
4. Add the required file header (docs use the Markdown header format per file-headers.md: file/version/guid/last-edited).
5. Link this new doc from docs/reference/abs-implementation-status.md or the ABS-SYNC section of TODO.md so it's discoverable.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_docs_057.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- The CF Access policy-ordering advice must be stated as an explicit DO/DON'T with a concrete example, since the item calls this out as 'the trap that bit users in both clients' issue trackers' — a vague description would not prevent the same mistake.

## Tests

- N/A — documentation only

Anti-over-suppression: N/A

## How to test

```bash
git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] docs/reference/abs-sync-topology-runbook.md exists with all 5 required sections
- [ ] a markdown linter or `make ci`'s doc-lint step (if any) passes on the new file
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_docs_057.md`.

## Commit message

```
feat(docs): Phase 8 — write the ABS topology, runbook, and migration gui (ABS-SYNC-Phase8)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the first acceptance check below already passes at HEAD (`docs/reference/abs-sync-topology-runbook.md exists with all 5 required sections`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Depends conceptually on TASK-11 (auth core) being complete for the JWT-enforcement section to describe the real implemented behavior rather than aspirational design — TASK-11 is confirmed stale_done (L10337) so this doc can describe the ACTUAL shipped auth flow.
