<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/TASK-100-validate-the-two-unvalidated-client-side-navigat.md -->
<!-- version: 1.0.0 -->
<!-- guid: b2219750-63cb-4267-85a1-32a9a364af68 -->
<!-- last-edited: 2026-08-21 -->

# TASK-100 — Validate the two unvalidated client-side navigation sinks (Login.tsx from-state, BookDetail.tsx library_return_url) the way the Go side already does (TODO.md L8177)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · missing-file-lane subagent · **Why:** small, well-specified port of an existing Go function into a new TS util plus two call-site wirings · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 8177 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Two frontend navigation sinks are unvalidated an" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-12.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/missing-file-lane-100-validate-the-two-unvalidated-client-side-navigat" -b agent/missing-file-lane-100-validate-the-two-unvalidated-client-side-navigat origin/main
cd "$REPO/.worktrees/missing-file-lane-100-validate-the-two-unvalidated-client-side-navigat"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Create web/src/utils/safeReturn.ts exporting sanitizeReturn(path) mirroring oauth_login.go's sanitizeReturn exactly (reject empty, reject not starting with '/', reject any backslash, reject a second leading '/' or '\\'), and use it at both Login.tsx's redirectTo (~L79-82) and BookDetail.tsx's two sessionStorage reads (~L1012, L1050), falling back to '/dashboard' and '/library' respectively when rejected.

## Background (verify before editing)

- Login.tsx ~L80-81 does `const state = location.state as {from?:string}|null; return state?.from || '/dashboard';` — safe today only because nothing in the codebase currently writes location.state.from.
- BookDetail.tsx L1012/L1050 both read sessionStorage's library_return_url and pass it to navigate() unvalidated — safe today only because the writer currently only runs on /library and /fingerprints.
- abs/openid.go:246-257 does the same validation for redirect_uri on the Go side — a second reference implementation if oauth_login.go's is ambiguous.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'state?.from' web/src/pages/Login.tsx   # 1 hit ~L81 (`return state?.from || '/dashboard';`) — Login.tsx's redirectTo passes location.state.from to navigate() with no validation
  grep -n 'library_return_url' web/src/pages/BookDetail.tsx   # 2 hits: L1012, L1050 — BookDetail.tsx reads sessionStorage.getItem('library_return_url') unvalidated at two sites
  grep -n 'func sanitizeReturn' -A 12 internal/server/handlers/oauth_login.go   # 1 hit ~L262-273: rejects empty, non-'/'-prefixed, any-backslash, and a doubled leading slash/backslash — Go already implements the exact guard to mirror
  ```

### Reuse — don't invent

- Use `sanitizeReturn (Go reference implementation to port)` in `internal/server/handlers/oauth_login.go` (verify: `grep -n 'func sanitizeReturn' internal/server/handlers/oauth_login.go`) — do NOT write a parallel helper.

## Step-by-step

1. Create web/src/utils/safeReturn.ts exporting `sanitizeReturn(ret: string | null | undefined): string`, porting oauth_login.go's four checks verbatim: empty→'', not startsWith('/')→'', includes('\\')→'', ret.length>1 && (ret[1]==='/'||ret[1]==='\\')→''; otherwise return ret unchanged.
2. In Login.tsx, import sanitizeReturn and change the redirectTo useMemo to: `const safe = sanitizeReturn(state?.from); return safe || '/dashboard';`
3. In BookDetail.tsx, at both L1012 and L1050, wrap the read: `const returnUrl = sanitizeReturn(sessionStorage.getItem('library_return_url')) || '/library';`
4. Bump file-header versions on all three touched/created files.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_missing-file-lane_100.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A return value of exactly '/' (single char) must be accepted (length>1 guard on index [1] access).
- null/undefined input (sessionStorage.getItem can return null) must be treated as empty/rejected, not throw.

## Tests

- web/src/utils/safeReturn.test.ts: TestSanitizeReturn_RejectsEmptyBackslashAndProtocolRelative — '', '/\\evil.com', '//evil.com', 'https://evil.com', 'evil.com' all return ''.
- web/src/utils/safeReturn.test.ts: TestSanitizeReturn_AcceptsOrdinaryInternalPaths — anti-over-suppression: '/dashboard', '/library', '/book/abc123', '/fingerprints?x=1' all pass through UNCHANGED.
- A Login.test.tsx case: malicious location.state.from ('//evil.com') falls back to '/dashboard' rather than being used.

Anti-over-suppression test: `TestSanitizeReturn_AcceptsOrdinaryInternalPaths` — a known-good input still passes with the new guard active.

## How to test

```bash
npm --prefix web run lint && npm --prefix web test
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] npm --prefix web test -- safeReturn passes.
- [ ] Setting sessionStorage.library_return_url to '//evil.com' then triggering BookDetail's return-to-library action navigates to '/library', not evil.com.
- [ ] Anti-over-suppression test: `TestSanitizeReturn_AcceptsOrdinaryInternalPaths` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_missing-file-lane_100.md`.

## Commit message

```
feat(missing-file-lane): Validate the two unvalidated client-side navigation sinks (L (TODO L8177)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — `the artifact this task adds is present: re-run grep -n 'state?.from' web/src/pages/Login.tsx` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Explicitly do NOT touch or loosen internal/server/handlers/oauth_login.go's sanitizeReturn itself — the TODO item flags a separate TODO-SSO-EDGE entry (~TODO.md L1040) as off-limits for that reason; that entry is a functional gap, not a vulnerability.
