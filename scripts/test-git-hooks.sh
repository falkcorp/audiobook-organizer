#!/bin/bash
# file: scripts/test-git-hooks.sh
# version: 1.0.0
# guid: a1b2c3d4-e5f6-4a8b-9c7d-1e2f3a4b5c6d
# last-edited: 2026-07-03
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
git rm --cached -q .claude/.credentials/some-branch.json 2>/dev/null || true

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
