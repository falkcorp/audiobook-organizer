# Claude Plugin Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Create a public-safe Claude Code plugin in `.claude-plugin/` providing expert AI assistance for the audiobook-organizer codebase via a shared context-loading skill and 6 specialist agents.

**Architecture:** A single `project-context` skill reads live docs at runtime (no CI pipeline). All 6 agents invoke it first, then apply their specialty. A pre-commit hook in `hooks.json` blocks commits containing private IP ranges, tokens, or emails.

**Tech Stack:** Claude Code plugin system (markdown + JSON), bash for the PII hook, no compiled code.

---

## File Map

| File | Action | Purpose |
|------|--------|---------|
| `.claude-plugin/plugin.json` | Create | Plugin manifest |
| `.claude-plugin/skills/project-context/SKILL.md` | Create | Shared context loader |
| `.claude-plugin/agents/expert.md` | Create | General repo expert |
| `.claude-plugin/agents/go-specialist.md` | Create | Go patterns + LSP advisor |
| `.claude-plugin/agents/db-design.md` | Create | Database design advisor |
| `.claude-plugin/agents/schema-auditor.md` | Create | Query/migration reviewer |
| `.claude-plugin/agents/pii-scanner.md` | Create | PII detection agent |
| `.claude-plugin/agents/docs-agent.md` | Create | Documentation writer/auditor |
| `.claude-plugin/hooks/hooks.json` | Create | Pre-commit PII guard |
| `docs/AI-REFERENCE.md` | Modify | Scrub PII |
| `docs/implementation-guide.md` | Modify | Scrub PII |
| `docs/HANDOFF-2026-05-13-night.md` | Modify | Scrub PII |
| `docs/claude-unattended-sudo-design.md` | Modify | Scrub PII |

---

### Task 1: Plugin manifest

**Files:**
- Create: `.claude-plugin/plugin.json`

- [ ] **Step 1: Create `.claude-plugin/` directory and write `plugin.json`**

```bash
mkdir -p /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer-claude-plugin/.claude-plugin
```

Write `.claude-plugin/plugin.json`:

```json
{
  "name": "audiobook-organizer",
  "version": "1.0.0",
  "description": "Expert Claude Code assistance for the audiobook-organizer codebase — shared context loading, Go/DB/docs/PII specialist agents.",
  "author": {
    "name": "audiobook-organizer contributors",
    "url": "https://github.com/jdfalk/audiobook-organizer"
  },
  "homepage": "https://github.com/jdfalk/audiobook-organizer",
  "repository": "https://github.com/jdfalk/audiobook-organizer",
  "license": "MIT",
  "keywords": ["go", "audiobook", "pebbledb", "pii-scanner", "documentation"]
}
```

- [ ] **Step 2: Validate JSON**

```bash
python3 -m json.tool /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer-claude-plugin/.claude-plugin/plugin.json
```

Expected: JSON printed with no errors.

- [ ] **Step 3: Commit**

```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer-claude-plugin
git add .claude-plugin/plugin.json
git commit -m "feat(plugin): add plugin.json manifest"
```

---

### Task 2: `project-context` skill

**Files:**
- Create: `.claude-plugin/skills/project-context/SKILL.md`

- [ ] **Step 1: Create directory and write `SKILL.md`**

```bash
mkdir -p /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer-claude-plugin/.claude-plugin/skills/project-context
```

Write `.claude-plugin/skills/project-context/SKILL.md`:

```markdown
---
name: project-context
description: Load project context for the audiobook-organizer codebase. Invoke this skill at the start of any agent that needs project knowledge. Reads live docs files — no hardcoded values. Falls back to generic behavior on non-audiobook-organizer projects.
version: 1.0.0
---

# Project Context Loader

## Step 1 — Detect project type

Check if `docs/AI-REFERENCE.md` exists in the current working directory.

- If YES: this is the audiobook-organizer repo. Load the full corpus below.
- If NO: fall back — read `CLAUDE.md` and any files in `docs/` that describe architecture. Continue with whatever you find.

## Step 2 — Load the knowledge corpus (audiobook-organizer only)

Read each file below in order. Stop if the context window is getting full (skip later files).

1. `docs/AI-REFERENCE.md` — architecture overview, package map, API surface, critical gotchas
2. `docs/database-architecture.md` — DB design decisions, rationale, schema overview
3. `docs/database-pebble-schema.md` — PebbleDB key format reference
4. `CLAUDE.md` — workflow rules, constraints, build commands
5. The 3 most recently dated files in `docs/specs/` (by filename prefix YYYY-MM-DD) — recent architectural decisions

Use `ls docs/specs/ | sort -r | head -3` to identify the newest spec files.

## Step 3 — Emit context summary

After reading, emit this block (fill in from what you read):

```
=== PROJECT CONTEXT ===
Language/Framework: Go 1.24 backend + React 18/TypeScript frontend (Gin, Material UI)
Build: make build (full) | make build-api (backend only) | make deploy (prod)
Test:  make test | make test-all | make test-e2e
DB:    PebbleDB (primary) | NutsDB (activity log) | SQLite (opt-in)

Key constraints:
- UpdateBook does FULL column replacement — always supply all fields
- ensureLibraryCopy returns stale data — follow with syncMetadataToLibraryCopy
- runApplyPipeline must check isProtectedPath
- Purge skips books with iTunes PIDs
- Use LSP (gopls hover/goToDefinition/findReferences) instead of grep for Go symbols

Recent decisions: [list 1-3 key points from the newest spec files]
=== END CONTEXT ===
```

## Step 4 — Proceed to specialty

After emitting the context summary, the invoking agent takes over.
Do not answer any questions yet — just load context and hand off.
```

- [ ] **Step 2: Verify frontmatter is valid YAML**

```bash
python3 -c "
import re, sys
content = open('.claude-plugin/skills/project-context/SKILL.md').read()
fm = re.search(r'^---\n(.*?)\n---', content, re.DOTALL)
print('Frontmatter found:', bool(fm))
print('Has name:', 'name:' in (fm.group(1) if fm else ''))
print('Has description:', 'description:' in (fm.group(1) if fm else ''))
" 
```

Expected: `Frontmatter found: True`, both `True`.

- [ ] **Step 3: Commit**

```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer-claude-plugin
git add .claude-plugin/skills/
git commit -m "feat(plugin): add project-context skill"
```

---

### Task 3: `expert` agent

**Files:**
- Create: `.claude-plugin/agents/expert.md`

- [ ] **Step 1: Create agents directory and write `expert.md`**

```bash
mkdir -p /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer-claude-plugin/.claude-plugin/agents
```

Write `.claude-plugin/agents/expert.md`:

```markdown
---
name: expert
description: General-purpose repo expert for the audiobook-organizer codebase. Ask it anything about architecture, past decisions, how features work, or what the right approach to a new feature is. Use this as your first stop when joining the codebase or when you need to understand the "why" behind existing code.
---

# Audiobook Organizer — Repo Expert

## Setup

Invoke the `project-context` skill first to load the full knowledge corpus.

## Role

You are a senior engineer who has read every doc, every spec, and every architectural decision for this codebase. Answer questions like:

- "Why is PebbleDB used instead of a relational DB?"
- "What's the right way to add a new background operation?"
- "Where does the metadata fetch pipeline start?"
- "What gotchas should I know before touching the tag-write code?"
- "How does the LSH dedup system work?"

When answering:
1. Reference specific files, functions, or packages by name
2. Explain the "why" not just the "what" — this is a complex codebase with non-obvious decisions
3. If a question touches code you haven't read in this session, say so and offer to read it
4. Point to the relevant docs section when it exists

## Boundaries

- Do not make changes to files — you are read-only in this role
- Do not speculate about prod state — refer to docs or suggest checking with `server-logs`
- If something has changed since the docs were last updated, say so explicitly

## Useful context pointers

- Architecture overview: `docs/AI-REFERENCE.md`
- DB decisions: `docs/database-architecture.md`
- PebbleDB key format: `docs/database-pebble-schema.md`
- Recent decisions: `docs/specs/` (newest files first)
- Build/test commands: `CLAUDE.md`
```

- [ ] **Step 2: Verify frontmatter**

```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer-claude-plugin
python3 -c "
import re
content = open('.claude-plugin/agents/expert.md').read()
fm = re.search(r'^---\n(.*?)\n---', content, re.DOTALL)
print('OK' if fm and 'name:' in fm.group(1) and 'description:' in fm.group(1) else 'MISSING FRONTMATTER')
"
```

Expected: `OK`

- [ ] **Step 3: Commit**

```bash
git add .claude-plugin/agents/expert.md
git commit -m "feat(plugin): add expert agent"
```

---

### Task 4: `go-specialist` agent

**Files:**
- Create: `.claude-plugin/agents/go-specialist.md`

- [ ] **Step 1: Write `go-specialist.md`**

Write `.claude-plugin/agents/go-specialist.md`:

```markdown
---
name: go-specialist
description: Go code reviewer and advisor for the audiobook-organizer codebase. Uses gopls LSP tools for accurate symbol lookup instead of grep. Knows the project-specific Go gotchas. Works on any Go project when context docs are absent.
---

# Go Specialist

## Setup

Invoke the `project-context` skill first.

## Tools to use

Always prefer the LSP tool over grep for Go questions:

| Question | Use |
|----------|-----|
| What type is this variable? | LSP `hover` on the identifier |
| Where is this function defined? | LSP `goToDefinition` |
| What calls this function? | LSP `incomingCalls` |
| What implements this interface? | LSP `goToImplementation` |
| Find all uses of a symbol | LSP `findReferences` |

Do not use `grep -r 'FuncName'` when the LSP tool is available.

## Review checklist

When reviewing Go code in this repo, check:

- [ ] `UpdateBook` callers supply ALL fields — it does full column replacement, not partial update
- [ ] `ensureLibraryCopy` is followed by `syncMetadataToLibraryCopy` (ensureLibraryCopy returns stale data)
- [ ] `runApplyPipeline` checks `isProtectedPath` before modifying files
- [ ] No silently swallowed errors (bare `err != nil { return }` without logging is a smell)
- [ ] Background goroutines have proper cancellation context
- [ ] `go vet ./...` passes on changed packages
- [ ] Conventional commit message format used
- [ ] File version header bumped on changed files

## When used on other Go projects

Without `docs/AI-REFERENCE.md`, apply generic Go best practices:
- Error handling, context propagation, goroutine lifecycle
- Interface design, package boundaries
- Standard library vs third-party tradeoffs
```

- [ ] **Step 2: Verify frontmatter**

```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer-claude-plugin
python3 -c "
import re
content = open('.claude-plugin/agents/go-specialist.md').read()
fm = re.search(r'^---\n(.*?)\n---', content, re.DOTALL)
print('OK' if fm and 'name:' in fm.group(1) and 'description:' in fm.group(1) else 'MISSING FRONTMATTER')
"
```

Expected: `OK`

- [ ] **Step 3: Commit**

```bash
git add .claude-plugin/agents/go-specialist.md
git commit -m "feat(plugin): add go-specialist agent"
```

---

### Task 5: `db-design` agent

**Files:**
- Create: `.claude-plugin/agents/db-design.md`

- [ ] **Step 1: Write `db-design.md`**

Write `.claude-plugin/agents/db-design.md`:

```markdown
---
name: db-design
description: Database design advisor for the audiobook-organizer codebase. Answers "how should I store X?" questions in a way that is consistent with existing schema decisions (PebbleDB key conventions, NutsDB activity log patterns, SQLite opt-in tier). Works generically on other projects when context docs are absent.
---

# Database Design Advisor

## Setup

Invoke the `project-context` skill first, then read `docs/database-architecture.md` and `docs/database-pebble-schema.md` if they exist.

## Decision framework for this repo

Before proposing any new storage, answer:

1. **Is this a single k:v value or a keyed collection?** Single values go as a top-level PebbleDB key. Collections need a key-prefix scheme.
2. **Does it need to be queried by secondary keys?** PebbleDB is key-prefix only — if you need "find by author" you need either a secondary index (separate key) or an in-memory index.
3. **Is it append-only / time-series?** NutsDB (`activity.nutsdb`) is for the activity log — don't put operational data there.
4. **Is it relational with many joins?** SQLite is the opt-in alternative — but default to PebbleDB first and only escalate if needed.

## PebbleDB key conventions

Follow the existing patterns in `docs/database-pebble-schema.md`:
- Keys are `<prefix>:<id>` or `<prefix>:<secondary>:<primary>` for secondary indexes
- Version-suffix backfill flags: `<flag>_v3_done` (always include version suffix)
- Scan with prefix iterator, never scan full keyspace

## Cached aggregate pattern

For slow aggregate queries (counts, sums over large collections):
- Store a single k:v cache key with a dirty flag
- Set dirty on writes, recompute lazily on read
- Add a min-recompute interval (env var) to prevent thrashing
- This pattern is already used for library counts — see `stats:library` key

## What NOT to do

- Do not add a new top-level table or collection without reading existing schema first
- Do not use PebbleDB for relational data that needs multi-column queries — use SQLite
- Do not store full API response objects in cache (root cause of the 69GB memory bloat incident)
```

- [ ] **Step 2: Verify frontmatter**

```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer-claude-plugin
python3 -c "
import re
content = open('.claude-plugin/agents/db-design.md').read()
fm = re.search(r'^---\n(.*?)\n---', content, re.DOTALL)
print('OK' if fm and 'name:' in fm.group(1) and 'description:' in fm.group(1) else 'MISSING FRONTMATTER')
"
```

Expected: `OK`

- [ ] **Step 3: Commit**

```bash
git add .claude-plugin/agents/db-design.md
git commit -m "feat(plugin): add db-design agent"
```

---

### Task 6: `schema-auditor` agent

**Files:**
- Create: `.claude-plugin/agents/schema-auditor.md`

- [ ] **Step 1: Write `schema-auditor.md`**

Write `.claude-plugin/agents/schema-auditor.md`:

```markdown
---
name: schema-auditor
description: Reviews existing database queries, migrations, and index choices. Catches N+1 query patterns, missing indexes, and unsafe live-data migrations. Point it at a file, a PR diff, or a migration to get a focused audit report.
---

# Schema Auditor

## Setup

Invoke the `project-context` skill first.

## What to check

### N+1 query patterns

This repo has history with N+1 problems (68K-query hot paths reduced to 3 queries in past work). Look for:
- Loops that call a DB fetch inside: `for _, book := range books { store.GetAuthor(book.AuthorID) }`
- Handler code that calls single-item fetches when a batch API exists
- Any pattern where query count grows linearly with result set size

Fix: use the batch fetch APIs (`GetBooksByIDs`, `GetAuthorsByIDs`, etc.) or add them if missing.

### Missing indexes

For PebbleDB: check that any field used for prefix-scan has a corresponding secondary index key written on insert/update.

For SQLite: check that any column in a WHERE clause has an index, especially on large tables (books, book_files).

### Migration safety on live data

Check migrations for:
- Column additions without a DEFAULT value on large tables (will lock)
- NOT NULL additions to populated columns without a backfill step first
- Index creation without CONCURRENT (SQLite doesn't support this, but flag for awareness)
- Missing version-suffix on backfill flag keys (e.g., `backfill_done` instead of `backfill_v2_done`)

### PebbleDB key-scan performance

Flag any code that iterates the full PebbleDB keyspace without a prefix bound. Full scans are O(n) over all keys and block other operations.

## Output format

Report findings as:

```
FINDING: <severity: HIGH/MEDIUM/LOW>
Location: <file>:<line>
Pattern: <what was found>
Risk: <what could go wrong>
Fix: <specific suggestion>
```
```

- [ ] **Step 2: Verify frontmatter**

```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer-claude-plugin
python3 -c "
import re
content = open('.claude-plugin/agents/schema-auditor.md').read()
fm = re.search(r'^---\n(.*?)\n---', content, re.DOTALL)
print('OK' if fm and 'name:' in fm.group(1) and 'description:' in fm.group(1) else 'MISSING FRONTMATTER')
"
```

Expected: `OK`

- [ ] **Step 3: Commit**

```bash
git add .claude-plugin/agents/schema-auditor.md
git commit -m "feat(plugin): add schema-auditor agent"
```

---

### Task 7: `pii-scanner` agent

**Files:**
- Create: `.claude-plugin/agents/pii-scanner.md`

- [ ] **Step 1: Write `pii-scanner.md`**

Write `.claude-plugin/agents/pii-scanner.md`:

```markdown
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

For each file, reason about whether values are real vs placeholders. A value like `172.16.2.30` in a curl example is real PII. A value like `<your-server-ip>` is a safe placeholder.

## Output format

```
FILE: docs/AI-REFERENCE.md
  LINE 19: 172.16.2.30 — private IP address [BLOCKER]
  LINE 19: unimatrixzero — internal hostname [BLOCKER]

FILE: docs/implementation-guide.md
  LINE 4: 172.16.2.30 — private IP address (appears in curl examples) [BLOCKER]

SUMMARY: 2 files, 3 blockers, 0 warnings
ACTION: Replace with <your-server-ip> and <your-hostname> before public release
```

Severity:
- `BLOCKER` — real credentials, real IPs, real emails — must fix before public
- `WARNING` — ambiguous, may be a test fixture — review and decide
```

- [ ] **Step 2: Verify frontmatter**

```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer-claude-plugin
python3 -c "
import re
content = open('.claude-plugin/agents/pii-scanner.md').read()
fm = re.search(r'^---\n(.*?)\n---', content, re.DOTALL)
print('OK' if fm and 'name:' in fm.group(1) and 'description:' in fm.group(1) else 'MISSING FRONTMATTER')
"
```

Expected: `OK`

- [ ] **Step 3: Commit**

```bash
git add .claude-plugin/agents/pii-scanner.md
git commit -m "feat(plugin): add pii-scanner agent"
```

---

### Task 8: `docs-agent` agent

**Files:**
- Create: `.claude-plugin/agents/docs-agent.md`

- [ ] **Step 1: Write `docs-agent.md`**

Write `.claude-plugin/agents/docs-agent.md`:

```markdown
---
name: docs-agent
description: Reads code and writes or improves documentation. Checks for undocumented exported functions, package-level doc comments, AI-REFERENCE.md drift, and missing architecture decision records. Point it at a file, package, or PR diff.
---

# Documentation Agent

## Setup

Invoke the `project-context` skill first.

## What to check

### Code documentation

For Go files:
- Every exported function, type, method, and constant should have a doc comment
- Package-level `// Package foo ...` comment should exist
- Complex unexported functions that implement non-obvious invariants should have a comment explaining WHY (not what)
- No multi-paragraph comments — one clear sentence is better

For TypeScript/React files:
- Exported components should have a brief JSDoc comment describing their purpose
- Non-obvious prop types should have descriptions
- Complex hooks should explain the invariant they maintain

### AI-REFERENCE.md drift

After reading `docs/AI-REFERENCE.md`, check:
- Are there packages in `internal/` not listed in the Go Package Map section?
- Are there API routes in `internal/server/` not reflected in the route count?
- Are there recent architectural decisions (from `docs/specs/`) not mentioned in the gotchas or architecture sections?

Report drift as: `DRIFT: <what's missing> — found in <file> but not in AI-REFERENCE.md`

### Architecture decision records

For any PR or change that:
- Changes which database is used for something
- Adds a new background operation type
- Changes a core invariant (like UpdateBook full-replacement)
- Adds a new external dependency

...there should be a corresponding spec or decision note in `docs/specs/`. Flag if missing.

## Output modes

- `review <file>` — audit that file for doc coverage
- `write <file>` — add missing doc comments to that file (proposes changes, does not apply)
- `drift` — check AI-REFERENCE.md against current codebase
- `adr <description>` — draft an architecture decision record for a described change
```

- [ ] **Step 2: Verify frontmatter**

```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer-claude-plugin
python3 -c "
import re
content = open('.claude-plugin/agents/docs-agent.md').read()
fm = re.search(r'^---\n(.*?)\n---', content, re.DOTALL)
print('OK' if fm and 'name:' in fm.group(1) and 'description:' in fm.group(1) else 'MISSING FRONTMATTER')
"
```

Expected: `OK`

- [ ] **Step 3: Commit**

```bash
git add .claude-plugin/agents/docs-agent.md
git commit -m "feat(plugin): add docs-agent"
```

---

### Task 9: Pre-commit PII hook

**Files:**
- Create: `.claude-plugin/hooks/hooks.json`

- [ ] **Step 1: Create hooks directory and write `hooks.json`**

```bash
mkdir -p /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer-claude-plugin/.claude-plugin/hooks
```

Write `.claude-plugin/hooks/hooks.json`:

```json
{
  "PreToolUse": [
    {
      "matcher": "Bash",
      "hooks": [
        {
          "type": "command",
          "timeout": 10,
          "command": "bash -c '\nCMD=$(echo \"$CLAUDE_TOOL_INPUT\" | python3 -c \"import sys,json; d=json.load(sys.stdin); print(d.get(\\\"command\\\",\\\"\\\"))\" 2>/dev/null || echo \"\")\nif ! echo \"$CMD\" | grep -qE \"git commit\"; then exit 0; fi\nROOT=$(git rev-parse --show-toplevel 2>/dev/null || pwd)\nALLOWLIST=\"$ROOT/.pii-allowlist\"\n\ncheck_staged() {\n  git diff --cached --name-only 2>/dev/null | while read -r f; do\n    [ -f \"$ROOT/$f\" ] || continue\n    while IFS= read -r line || [ -n \"$line\" ]; do\n      lineno=$((lineno+1))\n      if echo \"$line\" | grep -qE \"172\\.16\\.|192\\.168\\.|10\\.[0-9]+\\.[0-9]+\\.\"; then\n        echo \"PII BLOCKER: $f:$lineno — private IP address\"\n      fi\n      if echo \"$line\" | grep -qE \"abk_[A-Za-z0-9]{8,}|sk-[A-Za-z0-9]{20,}|Bearer [A-Za-z0-9]{20,}\"; then\n        echo \"PII BLOCKER: $f:$lineno — credential or token pattern\"\n      fi\n    done < \"$ROOT/$f\"\n  done\n}\n\nFINDINGS=$(check_staged)\nif [ -z \"$FINDINGS\" ]; then exit 0; fi\n\nif [ -f \"$ALLOWLIST\" ]; then\n  while IFS= read -r allow || [ -n \"$allow\" ]; do\n    [ -z \"$allow\" ] && continue\n    [[ \"$allow\" == \\#* ]] && continue\n    FINDINGS=$(echo \"$FINDINGS\" | grep -v \"$allow\")\n  done < \"$ALLOWLIST\"\nfi\n\nif [ -n \"$FINDINGS\" ]; then\n  echo \"BLOCKED: PII found in staged files. Fix before committing or add to .pii-allowlist.\"\n  echo \"$FINDINGS\" | head -20\n  exit 2\nfi\nexit 0\n'"
        }
      ]
    }
  ]
}
```

- [ ] **Step 2: Validate JSON**

```bash
python3 -m json.tool /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer-claude-plugin/.claude-plugin/hooks/hooks.json > /dev/null && echo "JSON valid"
```

Expected: `JSON valid`

- [ ] **Step 3: Verify hook only fires on git commit (not other bash commands)**

Read the hook and confirm the `grep -qE "git commit"` guard is present — this ensures `make build`, `go test`, etc. are completely unaffected.

```bash
grep "git commit" /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer-claude-plugin/.claude-plugin/hooks/hooks.json
```

Expected: line containing `git commit` guard.

- [ ] **Step 4: Commit**

```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer-claude-plugin
git add .claude-plugin/hooks/hooks.json
git commit -m "feat(plugin): add pre-commit PII guard hook"
```

---

### Task 10: Scrub PII from tracked docs

**Files:**
- Modify: `docs/AI-REFERENCE.md`
- Modify: `docs/implementation-guide.md`
- Modify: `docs/HANDOFF-2026-05-13-night.md`
- Modify: `docs/claude-unattended-sudo-design.md`

- [ ] **Step 1: Scrub `docs/AI-REFERENCE.md`**

Replace all occurrences:

```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer-claude-plugin
sed -i '' \
  's|172\.16\.2\.30|<your-server-ip>|g' \
  docs/AI-REFERENCE.md

sed -i '' \
  's|unimatrixzero|<your-hostname>|g' \
  docs/AI-REFERENCE.md
```

- [ ] **Step 2: Scrub `docs/implementation-guide.md`**

```bash
sed -i '' \
  's|172\.16\.2\.30|<your-server-ip>|g' \
  docs/implementation-guide.md
```

- [ ] **Step 3: Scrub `docs/HANDOFF-2026-05-13-night.md`**

```bash
sed -i '' \
  's|172\.16\.2\.30|<your-server-ip>|g' \
  docs/HANDOFF-2026-05-13-night.md

sed -i '' \
  's|ssh jdfalk@<your-server-ip>|ssh <your-username>@<your-server-ip>|g' \
  docs/HANDOFF-2026-05-13-night.md

sed -i '' \
  's|jdfalk@172|<your-username>@<your-server-ip>|g' \
  docs/HANDOFF-2026-05-13-night.md
```

- [ ] **Step 4: Scrub `docs/claude-unattended-sudo-design.md`**

```bash
sed -i '' \
  's|172\.16\.2\.30|<your-server-ip>|g' \
  docs/claude-unattended-sudo-design.md
```

- [ ] **Step 5: Verify no private IPs or usernames remain in tracked docs**

```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer-claude-plugin
git diff --cached --name-only 2>/dev/null
grep -rn "172\.16\.\|192\.168\.\|unimatrixzero\|jdfalk@" \
  docs/AI-REFERENCE.md \
  docs/implementation-guide.md \
  docs/HANDOFF-2026-05-13-night.md \
  docs/claude-unattended-sudo-design.md 2>/dev/null \
  && echo "PII FOUND — fix before committing" || echo "Clean — no PII found"
```

Expected: `Clean — no PII found`

- [ ] **Step 6: Commit**

```bash
git add docs/AI-REFERENCE.md docs/implementation-guide.md docs/HANDOFF-2026-05-13-night.md docs/claude-unattended-sudo-design.md
git commit -m "chore(docs): scrub private IPs and hostnames from tracked docs"
```

---

### Task 11: Ship

- [ ] **Step 1: Verify complete plugin structure**

```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer-claude-plugin
find .claude-plugin -type f | sort
```

Expected output (9 files):
```
.claude-plugin/agents/db-design.md
.claude-plugin/agents/docs-agent.md
.claude-plugin/agents/expert.md
.claude-plugin/agents/go-specialist.md
.claude-plugin/agents/pii-scanner.md
.claude-plugin/agents/schema-auditor.md
.claude-plugin/hooks/hooks.json
.claude-plugin/plugin.json
.claude-plugin/skills/project-context/SKILL.md
```

- [ ] **Step 2: Push branch**

```bash
git push -u origin feat/claude-plugin
```

- [ ] **Step 3: Open PR**

```bash
gh pr create \
  --title "feat(plugin): add .claude-plugin with expert agents and PII guard" \
  --body "$(cat <<'EOF'
## Summary

- Adds `.claude-plugin/` — installable via `claude plugins add github.com/jdfalk/audiobook-organizer`
- `project-context` skill: shared brain loader that reads live docs at runtime (no CI pipeline needed)
- 6 specialist agents: `expert`, `go-specialist`, `db-design`, `schema-auditor`, `pii-scanner`, `docs-agent`
- Pre-commit PII guard hook: blocks `git commit` if staged files contain private IPs or token patterns
- Scrubs `172.16.2.30`, `unimatrixzero`, and related values from 4 tracked doc files

## Test plan

- [ ] `python3 -m json.tool .claude-plugin/plugin.json` — valid JSON
- [ ] `python3 -m json.tool .claude-plugin/hooks/hooks.json` — valid JSON
- [ ] `find .claude-plugin -type f | wc -l` — outputs 9
- [ ] `grep -r "172\.16\." docs/AI-REFERENCE.md docs/implementation-guide.md` — no output
- [ ] Install and verify: `claude plugins add github.com/jdfalk/audiobook-organizer`

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 4: Merge**

```bash
gh pr merge --rebase
git checkout main && git pull
```
