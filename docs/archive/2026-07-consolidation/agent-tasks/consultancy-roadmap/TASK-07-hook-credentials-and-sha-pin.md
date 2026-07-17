<!-- file: docs/agent-tasks/consultancy-roadmap/TASK-07-hook-credentials-and-sha-pin.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5a80e2b2-c3a0-4b57-87fa-11e610be928e -->
<!-- last-edited: 2026-07-03 -->

# TASK-07 — Fix pre-commit hook `.claude/.credentials/` block + SHA-pin security.yml

**Priority:** P0 · **Effort:** S · **Recommended subagent:** Haiku · **Wave:** 1 · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cr-07-hook-credentials-and-sha-pin" -b agent/cr-07-hook-credentials-and-sha-pin origin/main
cd "$REPO/.worktrees/cr-07-hook-credentials-and-sha-pin"
git rebase origin/main
```

## Goal

Two independent, small fixes, both security hygiene (SEC-2, SEC-5, PROC-7, PROC-8 from `docs/consultancy/06-process-and-security.md`):

1. `scripts/setup-git-hooks.sh` documents that the pre-commit hook protects
   `.claude/.credentials/` (a directory of per-worktree username/password
   files), but the hook's match logic can never match anything inside a
   directory — fix the array + matching so staged files under
   `.claude/.credentials/` are actually rejected, add a self-test, and
   reinstall the hook as part of verification (the installed hook has also
   drifted from the script over time — PROC-8 — so reinstalling closes that
   gap too).
2. `.github/workflows/security.yml` references
   `falkcorp/github-common/.github/workflows/reusable-security.yml@main` — the
   only workflow reference in the repo not pinned to a SHA, violating the
   repo's own mandatory SHA-pin policy. Pin it to the current `github-common`
   `main` commit SHA, matching the pattern used by the other reusable-workflow
   callers (`ci.yml`, `nightly-burndown.yml`, etc).

## Background (verify before editing)

- `scripts/setup-git-hooks.sh` writes `.git/hooks/pre-commit` from a heredoc.
  As of this writing (verify with the greps below — do not trust these line
  numbers blindly):
  - `PROTECTED_FILES` array (lines ~17-22) contains only `.api-token`,
    `.claude/.api-token`, `.bootstrap-token`, `.readonly-key` — plain
    filenames, no directory entry for `.claude/.credentials/`.
  - The match (line ~27) is `echo "$STAGED_FILES" | grep -q "^$FILE$"` — an
    **exact full-line match**. A file staged as `.claude/.credentials/foo.json`
    can never equal the literal string `.claude/.credentials/` (or any fixed
    filename), so no entry you could add to the array as a bare string would
    ever match a file *inside* that directory with this matching logic.
  - The success message (line ~53) already claims
    `".claude/.credentials/ (per-worktree username/password)"` is protected —
    it is not, today.
  - `.gitignore` covers `.claude/.credentials/` as the primary defense
    (`grep -n "credentials" .gitignore`), but `git add -f` bypasses `.gitignore`
    and sails straight through the hook that the docs claim would block it.
  - There is no self-test for the hook logic anywhere in the repo (confirmed:
    `grep -rl "setup-git-hooks" scripts/ 2>/dev/null` returns only the script
    itself).

- **Re-verify these anchors before editing:**
  ```bash
  grep -n "PROTECTED_FILES\|grep -q \"\^\$FILE\|\.claude/\.credentials" scripts/setup-git-hooks.sh
  grep -n "credentials" .gitignore
  ```

- `.github/workflows/security.yml` line ~30 (verify with the grep below) has:
  ```yaml
  uses: falkcorp/github-common/.github/workflows/reusable-security.yml@main
  ```
  Every other reusable-workflow caller in this repo pins to a 40-char SHA with
  a trailing version comment, e.g.:
  ```
  .github/workflows/ci.yml:31:               ...reusable-ci-minimal.yml@f602707c96a8c979ecb9d2f4b0b95874673b2392 # v1.12.1
  .github/workflows/nightly-burndown.yml:28:  ...reusable-burndown.yml@66059b7eba8a00bee7e45a114c00251f0a6c3a07 # v1.11.2 + submodules:recursive + triage-poll pin fix
  ```
  `security.yml` is the sole `@main` holdout. The current HEAD of
  `falkcorp/github-common` `main` (verified 2026-07-03) is
  `1dec34cd8d8e2504d9be8298b522eff11448a401` — **re-verify this yourself**,
  it may have moved since this brief was written:
  ```bash
  git ls-remote https://github.com/falkcorp/github-common.git refs/heads/main
  ```
  Use whatever SHA that command returns at the time you do the work, not the
  one printed above, unless they match.

- **Re-verify the security.yml anchor before editing:**
  ```bash
  grep -n "reusable-security.yml@" .github/workflows/security.yml
  grep -rn "falkcorp/github-common" .github/workflows/*.yml
  ```

- **CAUTION — workflow file push restriction (repo rule):** `.github/workflows/*`
  changes cannot be pushed with a plain `GITHUB_TOKEN` (needs `workflows:write`).
  Do **not** attempt to push this change via any MCP GitHub "contents" API —
  that has caused file corruption in this repo before. Push via normal `git
  push` as the authenticated user (same as any other branch); if `git push`
  is rejected for lacking `workflows:write` scope, stop and report that back
  rather than working around it.

## Step-by-step

### Part A — pre-commit hook

1. Open `scripts/setup-git-hooks.sh`. Re-verify the array and match logic with
   the greps above.
2. Add `".claude/.credentials/"` (with trailing slash) to the `PROTECTED_FILES`
   array, immediately after `.readonly-key`.
3. Replace the exact-match check with logic that treats array entries ending
   in `/` as directory prefixes and everything else as before (exact filename
   match). Example replacement for the loop body:
   ```bash
   for FILE in "${PROTECTED_FILES[@]}"; do
       if [[ "$FILE" == */ ]]; then
           MATCH=$(echo "$STAGED_FILES" | grep -q "^$FILE" && echo yes)
       else
           MATCH=$(echo "$STAGED_FILES" | grep -q "^$FILE$" && echo yes)
       fi
       if [[ "$MATCH" == "yes" ]]; then
           echo "❌ Error: Attempting to commit protected file/path: $FILE"
           echo ""
           echo "This file contains secrets and should never be committed."
           echo "It's in .gitignore and is auto-generated by scripts."
           echo ""
           echo "To unstage this file:"
           echo "  git reset HEAD $FILE"
           echo ""
           exit 1
       fi
   done
   ```
   (Keep the rest of the heredoc — the `#!/bin/bash` line, the comment, `exit 0`
   at the end — unchanged. The directory-prefix branch's `grep -q "^$FILE"`
   without a trailing `$` deliberately matches any staged path that starts
   with `.claude/.credentials/`.)
4. Add a self-test script at `scripts/test-git-hooks.sh` that exercises the
   hook logic in an isolated scratch git repo (does not touch the real repo
   or its hooks):
   ```bash
   #!/bin/bash
   # file: scripts/test-git-hooks.sh
   # version: 1.0.0
   # Self-test for scripts/setup-git-hooks.sh: verifies the installed
   # pre-commit hook blocks protected files/dirs and allows normal files.
   set -euo pipefail

   SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
   TMPDIR=$(mktemp -d)
   trap 'rm -rf "$TMPDIR"' EXIT

   cd "$TMPDIR"
   git init -q .
   git config user.email test@example.com
   git config user.name test
   bash "$SCRIPT_DIR/setup-git-hooks.sh" >/dev/null

   fail=0

   # Case 1: file directly under the protected credentials directory must be blocked.
   mkdir -p .claude/.credentials
   echo '{"user":"x"}' > .claude/.credentials/some-branch.json
   git add -f .claude/.credentials/some-branch.json
   if git commit -q -m "should be blocked" >/tmp/hook-test-out 2>&1; then
       echo "FAIL: commit of .claude/.credentials/some-branch.json was NOT blocked"
       fail=1
   else
       echo "PASS: .claude/.credentials/some-branch.json blocked"
   fi
   git reset -q HEAD .claude/.credentials/some-branch.json 2>/dev/null || true

   # Case 2: a normal file must NOT be blocked.
   echo "hello" > normal-file.txt
   git add normal-file.txt
   if git commit -q -m "normal commit" >/tmp/hook-test-out2 2>&1; then
       echo "PASS: normal-file.txt allowed"
   else
       echo "FAIL: normal-file.txt was incorrectly blocked"
       cat /tmp/hook-test-out2
       fail=1
   fi

   exit "$fail"
   ```
   Make it executable: `chmod +x scripts/test-git-hooks.sh`.
5. Reinstall the hook in **this worktree** so the fix is actually active
   (addresses PROC-8 — the installed hook drifts from the script until
   re-run):
   ```bash
   bash scripts/setup-git-hooks.sh
   bash scripts/test-git-hooks.sh
   ```
   Both self-test cases must print `PASS`.
6. Bump the file header in `scripts/setup-git-hooks.sh` (version + last-edited)
   and add a header to the new `scripts/test-git-hooks.sh`, per
   `.standards/instructions/file-headers.md` (or the format shown in this
   brief's own header if the submodule is absent locally).

### Part B — SHA-pin security.yml

7. Re-run the `git ls-remote` command above to get the current
   `github-common` `main` SHA.
8. In `.github/workflows/security.yml`, change:
   ```yaml
   uses: falkcorp/github-common/.github/workflows/reusable-security.yml@main
   ```
   to:
   ```yaml
   uses: falkcorp/github-common/.github/workflows/reusable-security.yml@<SHA> # main @ 2026-07-03, no tagged release yet
   ```
   substituting the real SHA and today's date. Match the existing comment
   style used by `nightly-burndown.yml` (plain-language note, not necessarily
   a version tag, since this SHA may be ahead of the latest `github-common`
   tag).
9. Bump the file header (`version`, `last-edited`) at the top of
   `security.yml`.

## How to test

```bash
# Part A
bash scripts/setup-git-hooks.sh
bash scripts/test-git-hooks.sh
echo "exit: $?"          # must be 0, both cases must print PASS

# Part B — YAML sanity (no live CI trigger needed for this brief)
python3 -c "import yaml,sys; yaml.safe_load(open('.github/workflows/security.yml'))"
grep -n "reusable-security.yml@" .github/workflows/security.yml   # confirm SHA form, no @main left

# Whole-repo sanity (docs/scripts/workflow change only, but confirm nothing else broke)
go build ./...
go vet ./...
```

## Acceptance criteria

- [ ] `scripts/setup-git-hooks.sh`'s `PROTECTED_FILES` array includes
      `.claude/.credentials/` and the match logic actually rejects staged
      files inside that directory (not just the literal directory name).
- [ ] `scripts/test-git-hooks.sh` exists, is executable, and both its
      self-test cases (block credentials file, allow normal file) print PASS.
- [ ] The hook has been reinstalled in this worktree
      (`bash scripts/setup-git-hooks.sh` run) so the fix is live, not just
      written — this also resolves the PROC-8 drift for this checkout.
- [ ] `.github/workflows/security.yml` no longer references `@main` for the
      `reusable-security.yml` call; it is pinned to a 40-char commit SHA with
      a trailing comment, matching the style of the repo's other pinned
      reusable-workflow calls.
- [ ] File headers bumped on `scripts/setup-git-hooks.sh`,
      `scripts/test-git-hooks.sh` (new), and `.github/workflows/security.yml`.
- [ ] `go build ./...` and `go vet ./...` remain clean (this task touches no
      Go code, but confirms nothing else in the working tree is broken).
- [ ] PR pushed via plain `git push` (not the MCP GitHub contents API) — note
      in the PR description if `workflows:write` permission was required and
      whether the push succeeded normally.

## Commit message

```
fix(security): repair credentials-dir hook match + SHA-pin security.yml (SEC-2, SEC-5, PROC-7, PROC-8)

setup-git-hooks.sh claimed to protect .claude/.credentials/ but its exact-match
grep could never match paths inside a directory, so `git add -f` sailed
through undetected. Add a directory-prefix match branch, a self-test script,
and reinstall the hook. Separately, security.yml was the only workflow still
pinned to @main for a reusable workflow call, violating the repo's SHA-pin
policy; pinned to the current github-common main commit.

Co-Authored-By: Claude Haiku 4.5 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/cr-07-hook-credentials-and-sha-pin
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

- If `scripts/setup-git-hooks.sh`'s `PROTECTED_FILES` array already contains
  a `/`-suffixed directory entry AND the match loop already branches on
  trailing-slash entries (re-verify with
  `grep -n "PROTECTED_FILES\|\*/\*)" scripts/setup-git-hooks.sh`), Part A is
  already done — skip it, do not re-add a duplicate entry.
- If `scripts/test-git-hooks.sh` already exists and passes, skip step 4;
  just run it to confirm (step 5) rather than overwriting it.
- If `.github/workflows/security.yml` no longer contains `@main` for the
  `reusable-security.yml` reference (re-verify with
  `grep -n "reusable-security.yml@" .github/workflows/security.yml`), Part B
  is already done — do not re-pin to a different/older SHA than what is
  already there.
- Rollback: revert the commit. The hook change is purely additive (new array
  entry + new match branch); reverting restores the previous (broken) exact-
  match behavior, which was already the shipped behavior before this task, so
  reverting is safe. The SHA pin revert restores `@main`, which is a security
  regression but not a functional break — prefer a forward-fix (re-pin to a
  newer SHA) over reverting if the pinned SHA turns out to be bad.
