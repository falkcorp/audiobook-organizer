<!-- file: docs/agent-tasks/todo-completion/metadata/TASK-088-assess-the-2-critical-go-request-forgery-ssrf-co.md -->
<!-- version: 1.0.0 -->
<!-- guid: 777fa3ad-d1c9-4e7e-aee4-fd82e2b1534c -->
<!-- last-edited: 2026-08-21 -->

# TASK-088 — Assess the 2 critical go/request-forgery (SSRF) CodeQL alerts on cover-fetch paths (SEC-CODEQL-BACKLOG)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · metadata subagent · **Why:** Critical-severity SSRF finding on a path that fetches a URL sourced from third-party metadata-provider responses (untrusted input) — needs careful review of what 'already validated above' / 'validated by caller' actually means before deciding fix vs. dismiss. · **Depends on:** none · **Wave:** 2 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 2595 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**SEC-CODEQL-BACKLOG**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-04.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/metadata-088-assess-the-2-critical-go-request-forgery-ssrf-co" -b agent/metadata-088-assess-the-2-critical-go-request-forgery-ssrf-co origin/main
cd "$REPO/.worktrees/metadata-088-assess-the-2-critical-go-request-forgery-ssrf-co"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Read the full call chain into both cover.go:135 and covers.go:82 to determine what, if anything, validates coverURL before the fetch (the nolint comments claim validation happens elsewhere — confirm or refute this for the SSRF threat model specifically: can a malicious metadata provider return a coverURL pointing at an internal/link-local address like 169.254.169.254 or a file:// scheme?). If no real validation exists, add a scheme allowlist (http/https only) and a private/link-local IP block (using net.IP.IsPrivate/IsLoopback/IsLinkLocalUnicast after resolving the host) before the Get call. If validation genuinely already prevents SSRF, add an `// lgtm[go/request-forgery]` comment citing exactly where the validation happens, since CodeQL does not read #nolint/#nosec-style suppressions.

## Background (verify before editing)

- 302-alert CodeQL backlog counted 2026-08-12 via the code-scanning API across all 4 result pages; these are the only 2 'critical' severity alerts (#662 cover.go:135, #645 covers.go:82).
- Both call sites already carry a lint-suppression comment CLAIMING prior validation ('URL already validated above' / 'URL is validated by caller') but neither comment has been verified against the SSRF threat model specifically — a cover URL originates from a third-party metadata provider response, which is exactly CodeQL's untrusted-input model for this rule.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  sed -n '130,140p' internal/metadata/cover.go   # 'resp, err := client.Get(coverURL) //nolint:noctx // URL already validated above' — cover.go fetches an unvalidated remote URL
  sed -n '78,88p' internal/covers/covers.go   # 'resp, err := http.Get(coverURL) //nolint:gosec // URL is validated by caller' — covers.go fetches an unvalidated remote URL
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Read internal/metadata/cover.go from the top of the function containing line 135 backward to find what 'already validated above' refers to; determine if it checks scheme/host or only file-extension/format.
2. Read internal/covers/covers.go's caller chain to find what 'validated by caller' refers to.
3. If validation is only extension/format-based (not scheme/host), add a small helper (e.g. `isSafeCoverURL(rawURL string) bool` in internal/metadata or a shared internal/httputil location) that parses the URL, rejects non-http(s) schemes, resolves the host, and rejects private/loopback/link-local resolved IPs.
4. Call the helper before both client.Get(coverURL) (cover.go:135) and http.Get(coverURL) (covers.go:82); return a clear error on rejection instead of fetching.
5. If step 1-2 instead finds real SSRF-relevant validation already present upstream, skip step 3-4 and instead add `// lgtm[go/request-forgery]` directly above both fetch lines, with a comment naming the exact validating function and file:line.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_metadata_088.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
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
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] go vet ./... and make ci pass.
- [ ] The two alert lines each have either a fix (scheme/IP validation) or an `lgtm[go/request-forgery]` suppression with a cited validating function — not left silent.
- [ ] Anti-over-suppression test: `cover_test.go / covers_test.go case asserting a normal public HTTPS cover URL still fetches successfully after the fix (not just that the bad case is blocked).` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_metadata_088.md`.

## Commit message

```
refactor(metadata): Assess the 2 critical go/request-forgery (SSRF) CodeQL alert (SEC-CODEQL-BACKLOG)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Read first per the item's own suggested order — these are the only 2 critical-severity alerts in the 326-alert backlog and sit on untrusted-input paths.
