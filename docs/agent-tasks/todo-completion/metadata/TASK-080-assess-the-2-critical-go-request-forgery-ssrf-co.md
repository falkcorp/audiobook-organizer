<!-- file: docs/agent-tasks/todo-completion/metadata/TASK-080-assess-the-2-critical-go-request-forgery-ssrf-co.md -->
<!-- version: 1.0.0 -->
<!-- guid: 99a1c793-b27d-49f9-bd56-90a977fe7bbe -->
<!-- last-edited: 2026-08-21 -->

# TASK-080 — Assess the 2 critical go/request-forgery (SSRF) CodeQL alerts on cover-fetch paths (SEC-CODEQL-BACKLOG)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · metadata subagent · **Why:** Critical-severity SSRF finding on a path that fetches a URL sourced from third-party metadata-provider responses (untrusted input) — needs careful review of what 'already validated above' / 'validated by caller' actually means before deciding fix vs. dismiss. · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 2595 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**SEC-CODEQL-BACKLOG**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-04.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/metadata-080-assess-the-2-critical-go-request-forgery-ssrf-co" -b agent/metadata-080-assess-the-2-critical-go-request-forgery-ssrf-co origin/main
cd "$REPO/.worktrees/metadata-080-assess-the-2-critical-go-request-forgery-ssrf-co"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Read the full call chain into both cover.go:135 and covers.go:82 to determine what, if anything, validates coverURL before the fetch (the nolint comments claim validation happens elsewhere — confirm or refute this for the SSRF threat model specifically: can a malicious metadata provider return a coverURL pointing at an internal/link-local address like 169.254.169.254 or a file:// scheme?). If no real validation exists, add a scheme allowlist (http/https only) and a private/link-local IP block (using net.IP.IsPrivate/IsLoopback/IsLinkLocalUnicast after resolving the host) before the Get call. If validation genuinely already prevents SSRF, add an `// lgtm[go/request-forgery]` comment citing exactly where the validation happens, since CodeQL does not read #nolint/#nosec-style suppressions.

## Background (verify before editing)

- internal/metadata/cover.go:117 already calls validateCoverURL() (scheme allowlist) before the fetch at cover.go:137, and the client used there is built with a custom safeCoverDialContext (cover.go:71-89) that resolves the host and rejects private/loopback/link-local IPv4 AND IPv6 ranges. This is real, comprehensive SSRF protection already in place for alert #662.
- internal/covers/covers.go:82's http.Get(coverURL) has exactly one caller: internal/server/covers.go's handleCoverProxy, which calls covers.IsAllowedCoverSource(coverURL) (a 4-domain allowlist: openlibrary/google books/amazon) and rejects before ever calling FetchAndCacheCover. This is real, comprehensive SSRF protection already in place for alert #645.
- internal/metadata/cover_test.go already asserts a 169.254.169.254 cover URL is SSRF-blocked; internal/covers/covers_test.go already has TestIsAllowedCoverSource asserting both the blocked case and several legitimate URLs pass. Do not re-add these tests — verify they exist and pass.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  sed -n '130,140p' internal/metadata/cover.go   # 'resp, err := client.Get(coverURL) //nolint:noctx // URL already validated above' — cover.go fetches an unvalidated remote URL
  sed -n '78,88p' internal/covers/covers.go   # 'resp, err := http.Get(coverURL) //nolint:gosec // URL is validated by caller' — covers.go fetches an unvalidated remote URL
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Read internal/metadata/cover.go top-to-bottom around lines 20-137: confirm validateCoverURL (scheme allowlist) and safeCoverDialContext (private/loopback/link-local IPv4+IPv6 block via DNS-resolved-IP check) are both wired into the client used at line 137.
2. Read internal/covers/covers.go's only caller (internal/server/covers.go:24-45) and confirm covers.IsAllowedCoverSource(coverURL) gates every call to FetchAndCacheCover.
3. Given both sites have real, host/IP-resolved validation already present, add `// lgtm[go/request-forgery]` directly above cover.go:137 and covers.go:82, each citing the exact validating function/file:line found above (do not add a duplicate/parallel validator).
4. Run `grep -n 169.254 internal/metadata/cover_test.go` and `grep -n TestIsAllowedCoverSource internal/covers/covers_test.go` to confirm the anti-over-suppression and blocked-case tests already exist and pass; do not add duplicate tests.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_metadata_080.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A legitimate cover host that resolves via DNS to a public IP must still work — do not block by hostname string alone if it could break real providers (e.g. archive.org, openlibrary.org covers); validate the RESOLVED IP, not just the URL string, to prevent DNS-rebinding bypasses.
- IPv6 link-local/unique-local ranges must be checked too, not just IPv4 private ranges.

## Tests

- internal/metadata/cover_test.go: add a case asserting a coverURL of `http://169.254.169.254/latest/meta-data/` (or file://, or a private-range IP) is rejected before any network call.
- internal/covers/covers_test.go: same pattern.

Anti-over-suppression test: `cover_test.go / covers_test.go case asserting a normal public HTTPS cover URL still fetches successfully after the fix (not just that the bad case is blocked).` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/covers/... ./internal/metadata/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go vet ./... and make ci pass.
- [ ] The two alert lines each have either a fix (scheme/IP validation) or an `lgtm[go/request-forgery]` suppression with a cited validating function — not left silent.
- [ ] Anti-over-suppression test: `cover_test.go / covers_test.go case asserting a normal public HTTPS cover URL still fetches successfully after the fix (not just that the bad case is blocked).` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/covers/... ./internal/metadata/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_metadata_080.md`.

## Commit message

```
refactor(metadata): Assess the 2 critical go/request-forgery (SSRF) CodeQL alert (SEC-CODEQL-BACKLOG)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Read first per the item's own suggested order — these are the only 2 critical-severity alerts in the 326-alert backlog and sit on untrusted-input paths.
