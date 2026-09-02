<!-- file: docs/agent-tasks/todo-completion/metadata/TASK-080-assess-the-2-critical-go-request-forgery-ssrf-co.md -->
<!-- version: 2.1.0 -->
<!-- guid: cad914e8-4852-4440-9408-d6ea5f781e7d -->
<!-- last-edited: 2026-09-02 -->

# TASK-080 — Assess the 2 critical go/request-forgery (SSRF) CodeQL alerts on cover-fetch paths (SEC-CODEQL-BACKLOG)

> **Status 2026-09-02:** 🟡 OPEN — still worth doing — code-scanning state=open still lists #662 go/request-forgery metadata/cover.go:137 and #645 covers/covers.go:82. Both nolint sites unchanged. No commits on either file since 2026-08-21. Recommendation: keep — REVIEW-CRITICAL; brief v2.0.0 already corrects the inert lgtm[] premise, so work from the rewritten step 3.

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

> ## ⚠️ TWO CORRECTIONS, 2026-08-23 — read before starting
>
> **1. `lgtm[]` suppresses NOTHING in this repo.** It is the legacy LGTM.com
> mechanism, which GitHub code scanning never adopted. Measured twice: on
> PR #2781 the markers were removed and all four affected alerts stayed open
> across the merge (only an API dismissal closed #1429/#1105); and directly on
> 2026-08-23, `internal/audiobooks/service_mutation.go:63` carries
> `// lgtm[go/path-injection]` on that exact line **today** while alert **#1104**
> for that exact `path:line` is still `open`. Reproduce:
> ```bash
> grep -n 'lgtm\[' internal/audiobooks/service_mutation.go
> gh api /repos/falkcorp/audiobook-organizer/code-scanning/alerts/1104 -q '.state'
> ```
> Wherever this brief says "add an `lgtm[go/request-forgery]` comment", the real
> action is **dismiss via the code-scanning API** (syntax in the rewritten step 3).
> A comment is documentation, not suppression, and must never be reported as
> having handled a finding.
>
> **2. This brief pre-decides its own conclusion. Do not let it.** The Background
> section and step 3 assert that both sites "have real, host/IP-resolved
> validation already present". That is the *hypothesis you are being asked to
> test*, not a given — this is a **critical-severity SSRF finding on a path that
> fetches a URL supplied by a third-party metadata provider**. Treat every claim
> in Background as unverified until you have read the code yourself. If
> validation is absent, partial, or bypassable, the correct outcome is a **fix**,
> and reporting that is a success for this task, not a failure.
>
> Do not accept "there is a validator" as "the validator is sufficient". Test it
> against the actual threat model:
> - **Scheme:** `file://`, `gopher://`, `dict://` — is the allowlist positive
>   (http/https only) or a blocklist?
> - **Redirects:** does the client follow a 302 to `169.254.169.254`? A
>   `CheckRedirect` that is absent means the scheme/IP check ran on the *first*
>   URL only.
> - **DNS rebinding:** validate-then-fetch resolves the host twice. Only a
>   `DialContext`-level check (like the claimed `safeCoverDialContext`) closes
>   this; a string check on the URL does not.
> - **Encodings:** IPv6 (`[::1]`, `[::ffff:169.254.169.254]`), decimal/octal IPv4
>   (`2852039166`), and trailing-dot hostnames.
>
> A validator that checks the URL *string* but not the *connection* is
> bypassable. That distinction is the whole finding.

## Goal

Read the full call chain into both cover.go:135 and covers.go:82 to determine what, if anything, validates coverURL before the fetch (the nolint comments claim validation happens elsewhere — confirm or refute this for the SSRF threat model specifically: can a malicious metadata provider return a coverURL pointing at an internal/link-local address like 169.254.169.254 or a file:// scheme?). If no real validation exists, add a scheme allowlist (http/https only) and a private/link-local IP block (using net.IP.IsPrivate/IsLoopback/IsLinkLocalUnicast after resolving the host) before the Get call. If validation genuinely already prevents SSRF — tested against the full bypass list in the banner above, not merely present — dismiss the alert through the code-scanning API, citing exactly where the validation happens. CodeQL does not read `#nolint`/`#nosec`-style suppressions, and it does not read `lgtm[]` either; the API is the only mechanism that works here.

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
3. **Decide, per site, from what you actually found in steps 1–2** — not from
   this brief's Background, which is the hypothesis under test.

   **(a) If validation is genuinely sufficient** against the full threat model
   in the banner above (scheme allowlist, connection-level private/loopback/
   link-local block, redirect handling), then dismiss the alert through the
   code-scanning API — the ONLY mechanism that changes an alert's state here:
   ```bash
   gh api -X PATCH /repos/falkcorp/audiobook-organizer/code-scanning/alerts/<N> \
     -f state=dismissed \
     -f dismissed_reason='false positive' \
     -f dismissed_comment='coverURL is scheme-allowlisted by validateCoverURL and the client dials via safeCoverDialContext, which rejects private/loopback/link-local IPs post-resolution. Re-verified 2026-08-23 (TASK-080).'
   ```
   - Resolve `<N>` by PATH, not by a remembered number — dismissals have not
     always survived line shifts here (#1094 was dismissed and #1105 immediately
     reappeared for the same sink):
     ```bash
     gh api '/repos/falkcorp/audiobook-organizer/code-scanning/alerts?state=open&per_page=100' --paginate \
       -q '.[] | select(.rule.id=="go/request-forgery") | "\(.number)\t\(.most_recent_instance.location.path):\(.most_recent_instance.location.start_line)"'
     ```
   - `dismissed_comment` is **capped at 280 bytes**.
   - `false positive` is the right reason ONLY if the validation really does
     make the flow safe. If the code is intentionally-unsafe-but-accepted, that
     is `won't fix` — and on a critical SSRF finding it almost certainly is not
     acceptable, so prefer (b).
   - **Read the alert back** and confirm `state == "dismissed"`. An accepted
     PATCH is not proof.
   - You may ALSO add a plain code comment citing the validating
     function:file:line. That is useful documentation for the next reader. It is
     **not** the suppression and must not be reported as such.

   **(b) If validation is absent, partial, or bypassable** — including any of
   the banner's bypasses being open — do NOT dismiss. Fix it: a positive scheme
   allowlist (http/https), and a `DialContext` that rejects
   `IsPrivate`/`IsLoopback`/`IsLinkLocalUnicast`/`IsUnspecified` on the
   *resolved* IP for both IPv4 and IPv6, plus a `CheckRedirect` that re-applies
   the same check per hop. Reuse the existing helper if one is really there;
   do not build a second parallel validator.

   **This PR is REVIEW-CRITICAL and stays open for the owner either way.**
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

- [ ] `go build ./... && go vet ./...` exits 0. Do NOT gate on `make ci` — it is
      red on `main` for pre-existing reasons unrelated to this task.
- [ ] Each of the two alerts ends in one of exactly two states, with evidence:
      **fixed** (scheme allowlist + connection-level IP block + redirect
      re-check), or **dismissed via the code-scanning API and read back as
      `state == "dismissed"`**. Neither may be left silent, and a code comment
      alone counts as silent.
- [ ] Zero `lgtm[` markers added: `git diff | grep -c 'lgtm\['` returns 0.
- [ ] The banner's bypass list (scheme, redirects, DNS rebinding, IPv6/decimal
      encodings) was checked per site and the finding for each is stated
      explicitly — "validation exists" is not an answer to any of them.
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
