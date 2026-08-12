# Task 004: SEC-AUDIT--1 — CodeQL Models-as-Data pack for existing sanitizers

**Depends on:** none
**Estimated effort:** M (3 hours)
**Wave:** 2 (run in parallel with Wave 1)

## Goal

Teach CodeQL about the project's existing `util.SafeJoin` and `util.WithinRoot` sanitizers
using GitHub's Models-as-Data (MaD) feature. This will cause CodeQL to stop flagging code
paths that already use these sanitizers as path-injection alerts — expected to drop ~80–100
of the 217 open path-injection alerts (false positives).

## Context

- Existing sanitizers live in `internal/util/path.go`:
  - `SafeJoin(root string, parts ...string) (string, error)` — returns error if path escapes root
  - `WithinRoot(path, root string) bool` — returns false if path escapes root
- CodeQL doesn't know about project-specific sanitizers unless declared via MaD
- GitHub's MaD feature was introduced 2026-04-21 and supports Go sanitizer declarations
- Reference: https://github.blog/changelog/2026-04-21-codeql-now-supports-sanitizers-and-validators-in-models-as-data/
- 17 already-dismissed alerts in `internal/maintenance/jobs/` (#544–#560) confirm SafeJoin
  is a working sanitizer CodeQL doesn't recognize

## Files to create

- `.github/codeql/codeql-pack.yml` (new)
- `.github/codeql/models/go-sanitizers.yml` (new)
- `.github/workflows/codeql.yml` (edit — reference local pack)

## Instructions

### 1. Create `.github/codeql/codeql-pack.yml`

```yaml
# file: .github/codeql/codeql-pack.yml
# version: 1.0.0
name: jdfalk/audiobook-organizer-sanitizers
version: 0.1.0
library: true
dependencies:
  codeql/go-all: "*"
```

### 2. Create `.github/codeql/models/go-sanitizers.yml`

```yaml
# file: .github/codeql/models/go-sanitizers.yml
# version: 1.0.0
# Declares project-specific path sanitizers so CodeQL stops flagging
# code paths that already use SafeJoin / WithinRoot.
extensions:
  - addsTo:
      pack: codeql/go-all
      extensible: pathInjectionSanitizer
    data:
      # SafeJoin: return value is a sanitized path (path injection barrier)
      - ["github.com/jdfalk/audiobook-organizer/internal/util", "SafeJoin", "ReturnValue"]
  - addsTo:
      pack: codeql/go-all
      extensible: pathInjectionSanitizerGuard
    data:
      # WithinRoot: when this returns true, the path has been validated
      - ["github.com/jdfalk/audiobook-organizer/internal/util", "WithinRoot", "true"]
```

### 3. Edit `.github/workflows/codeql.yml`

Find the CodeQL init step (usually `uses: github/codeql-action/init@...`) and add the
local pack reference to its `with:` block:

```yaml
- uses: github/codeql-action/init@<existing-sha>
  with:
    languages: go, javascript
    packs: ./.github/codeql   # <-- add this line
```

Check the exact parameter name in the action's documentation — it may be `packs:` or
`config-file:`. If the workflow already uses `config-file:`, add the pack reference inside
that YAML config file instead.

### 4. Verify the pack structure is valid

```bash
# If codeql CLI is available locally:
codeql pack ls .github/codeql/
```

If not available, the CI run will validate it.

## Test

Push the branch. Wait for the CodeQL scan to complete on GitHub. Open:
```
https://github.com/jdfalk/audiobook-organizer/security/code-scanning
```
The open path-injection alert count should drop noticeably (from ~217 toward ~120–140).

## Commit

```
feat(security): add CodeQL MaD pack for SafeJoin/WithinRoot sanitizers (SEC-AUDIT--1)
```

## PR title

`feat(security): CodeQL sanitizer pack — SEC-AUDIT--1`

## After merging

Mark `- [ ] **SEC-AUDIT--1**` as `- [x]` in `TODO.md`.
Note the actual alert count drop in the PR description for tracking.
