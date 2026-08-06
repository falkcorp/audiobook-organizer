---
name: pii-scanner
description: Deep scan for PII (personally identifiable information) before public commits or releases. Claude-powered for contextual detection — catches what regex can't. Run before making a repo public, publishing a release, or doing a security review.
---

# PII Scanner

## Setup

Invoke the `project-context` skill first.

## What to scan for

### Infrastructure identifiers
- Private IP addresses (172.16.x.x, 192.168.x.x, 10.x.x.x) — even in comments, curl examples, log snippets
- Internal hostnames — anything that isn't `localhost`, `127.0.0.1`, `example.com`, or a public service
- SSH usernames in `ssh user@host` patterns
- Internal paths like `/mnt/bigdata/`, `/home/<username>/`, `/var/lib/<private-service>/`

### Credentials and tokens
- API keys and bearer tokens (patterns: `abk_`, `sk-`, `Bearer `, `token:`)
- Passwords in connection strings or config examples
- Private key material (BEGIN PRIVATE KEY, BEGIN RSA PRIVATE KEY, etc.)

### Personal information
- Personal email addresses (not project emails or placeholder `<your-email>`)
- Real names in code (not in git history, which is separate)
- Phone numbers

## How to run

When invoked with a path or "staged changes":

1. If given a path: read all tracked files under that path
2. If given "staged": run `git diff --cached --name-only` and read those files
3. If given no argument: scan `docs/` and `.claude-plugin/` as the highest-risk areas

For each file, reason about whether values are real vs placeholders. A value like `192.0.2.99` in a curl example is real PII. A value like `<your-server-ip>` is a safe placeholder.

## Output format

```
FILE: docs/AI-REFERENCE.md
  LINE 19: 192.0.2.99 — private IP address [BLOCKER]
  LINE 19: unimatrixzero — internal hostname [BLOCKER]

FILE: docs/implementation-guide.md
  LINE 4: 192.0.2.99 — private IP address (appears in curl examples) [BLOCKER]

SUMMARY: 2 files, 3 blockers, 0 warnings
ACTION: Replace with <your-server-ip> and <your-hostname> before public release
```

Severity:
- `BLOCKER` — real credentials, real IPs, real emails — must fix before public
- `WARNING` — ambiguous, may be a test fixture — review and decide
