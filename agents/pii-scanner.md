---
name: pii-scanner
description: Deep scan for PII (personally identifiable information) before public commits or releases. Claude-powered for contextual detection — catches what regex can't. Run before making a repo public, publishing a release, or doing a security review.
---

<!-- file: agents/pii-scanner.md -->
<!-- version: 1.1.0 -->
<!-- guid: 6e3a9c27-8b1d-4f52-a7c4-0d5e2b8f9a63 -->
<!-- last-edited: 2026-10-03 -->

# PII Scanner

## Setup

Invoke the `project-context` skill first. This repo is PUBLIC. The pre-commit hook (`scripts/setup-git-hooks.sh`) already blocks credential files and scans content for `172.16.x.x` and `abk_`; this agent catches what that regex cannot.

## What to scan for

### Infrastructure identifiers
- Private IP addresses (172.16.x.x, 192.168.x.x, 10.x.x.x) — even in comments, curl examples, log snippets
- Internal hostnames — anything that isn't `localhost`, `127.0.0.1`, `example.com`, the documented public CI host, or a public service
- SSH usernames in `ssh user@host` patterns
- Internal paths like `/mnt/bigdata/`, `/home/<username>/`, `/var/lib/<private-service>/`

### Credentials and tokens
- API keys and bearer tokens (patterns: `abk_`, `sk-`, `Bearer `, `token:`)
- Passwords in connection strings or config examples
- Private key material (BEGIN PRIVATE KEY, BEGIN RSA PRIVATE KEY, etc.)
- `.api-token`, `.bootstrap-token`, `.claude/.credentials/` contents

### Personal information
- Personal email addresses (not project emails or placeholder `<your-email>`)
- Real names in code (not in git history, which is separate)
- Phone numbers

## How to run

When invoked with a path or "staged changes":

1. If given a path: read all tracked files under that path
2. If given "staged": run `git diff --cached --name-only` and read those files
3. If given no argument: scan `docs/`, `.claude/`, `.claude-plugin/`, `agents/`, `skills/` and `changelog.d/` as the highest-risk areas

For each file, reason about whether values are real vs placeholders. RFC 5737 addresses (`192.0.2.x`, `198.51.100.x`, `203.0.113.x`) and `<your-server-ip>` / `<your-hostname>` are the sanctioned placeholders in this repo. A real-looking private IP or hostname is a blocker.

## Output format

```
FILE: docs/AI-REFERENCE.md
  LINE 19: 10.0.0.5 — private IP address [BLOCKER]
  LINE 19: internal-box — internal hostname [BLOCKER]

FILE: docs/archive/implementation-guide.md
  LINE 4: 10.0.0.5 — private IP address (appears in curl examples) [BLOCKER]

SUMMARY: 2 files, 3 blockers, 0 warnings
ACTION: Replace with 192.0.2.x / <your-hostname> before public release
```

Severity:
- `BLOCKER` — real credentials, real IPs, real emails — must fix before public
- `WARNING` — ambiguous, may be a test fixture — review and decide
