# Claude Plugin Design — audiobook-organizer

**Date**: 2026-06-11  
**Status**: Approved

## Goal

Add a Claude Code plugin to the audiobook-organizer repo that provides expert AI assistance
for working with the codebase. The plugin is public-safe, installable via
`claude plugins add github.com/jdfalk/audiobook-organizer`, and built around a shared
context-loading skill that makes all agents project-aware without hardcoding any
project-specific or PII-containing data.

## Architecture

### Plugin Location

The plugin lives in `.claude-plugin/` at the repo root — separate from the existing
`.claude/` directory, which contains private operational config (deployment credentials,
server details, prod-facing skills). The two coexist without conflict.

### Directory Layout

```
.claude-plugin/
├── plugin.json              # Manifest — name, version, description
├── agents/
│   ├── expert.md            # General repo expert
│   ├── go-specialist.md     # Go patterns, LSP-aware, project gotchas
│   ├── db-design.md         # PebbleDB/SQLite/NutsDB design advisor
│   ├── schema-auditor.md    # Query/index/migration reviewer
│   ├── pii-scanner.md       # PII scan before public commits/releases
│   └── docs-agent.md        # Documentation reader/writer/auditor
├── skills/
│   └── project-context/
│       └── SKILL.md         # Shared brain loader — invoked by all agents
└── hooks/
    └── hooks.json           # Pre-commit PII guard
```

### Shared `project-context` Skill

The core of the plugin. All agents invoke this skill first. It has no hardcoded project
knowledge — it reads live files at runtime, so it stays current without any CI pipeline.

**Detection**: Checks for `docs/AI-REFERENCE.md`. If present, loads the full corpus.
If not present (used on a different project), falls back to reading whatever `CLAUDE.md`
and `docs/` files exist — still useful, just not audiobook-organizer-specific.

**Knowledge corpus** (loaded in order, stops at context budget):

| File | Purpose |
|------|---------|
| `docs/AI-REFERENCE.md` | Architecture, package map, API surface, key gotchas |
| `docs/database-architecture.md` | DB decisions, rationale, schema overview |
| `docs/database-pebble-schema.md` | PebbleDB key format reference |
| `CLAUDE.md` | Workflow rules, constraints, build commands |
| `docs/superpowers/specs/` (3 newest) | Recent architectural decisions |

**Output**: A structured context summary block that the invoking agent references —
language/framework, key constraints, critical gotchas, recent decisions. Keeps downstream
agents grounded without re-reading everything each time.

### Agents

| Agent | Purpose |
|-------|---------|
| `expert` | General repo expert — architecture, past decisions, feature guidance, "why was X built this way" |
| `go-specialist` | Go code review and advice; uses gopls LSP tool over grep; knows project-specific Go gotchas |
| `db-design` | Design advisor for new data storage; knows PebbleDB key conventions, NutsDB activity log patterns, when to use SQLite |
| `schema-auditor` | Reviews existing queries, migrations, index choices; catches N+1, missing indexes, unsafe live-data migrations |
| `pii-scanner` | Deep scan for PII in files, staged changes, or docs corpus; Claude-powered for contextual detection |
| `docs-agent` | Reads code, writes/improves documentation, checks for AI-REFERENCE.md drift, ensures exported symbols are documented |

All agents follow the same pattern:
1. Invoke `project-context` skill
2. Apply specialty
3. Return focused results

### PII Safety

Two complementary layers ensure no PII ever lands in a commit.

#### Layer 1 — Pre-commit hook (automatic, blocking)

Defined in `.claude-plugin/hooks/hooks.json`. Fires on `Bash` tool calls that contain
`git commit` — no effect on any other commands.

Checks for:
- Private IP ranges: `172\.16\.`, `192\.168\.`, `10\.\d+\.`
- Bearer/API token patterns: `abk_[A-Za-z0-9]`, `sk-[A-Za-z0-9]{20,}`, `Bearer [A-Za-z0-9]{20,}`
- Inline email addresses (non-comment, non-doc context)
- Common hostname patterns (configurable via `.pii-allowlist` at repo root)

On a hit: blocks the commit, reports `file:line — matched pattern`. Developer either
fixes the value or adds it to `.pii-allowlist` if it's a test fixture / false positive.

#### Layer 2 — `pii-scanner` agent (deliberate review)

Claude-powered deep scan run manually before:
- Making the repo public
- Publishing a release
- Security reviews

Catches what regex can't: hostnames in prose, emails in changelogs, IPs in config examples.

#### One-time docs scrub (before plugin ships)

Existing docs contain private values that must be replaced before the repo is public:

| Current value | Replacement |
|--------------|------------|
| `172.16.2.30` | `<your-server-ip>` |
| `unimatrixzero` | `<your-hostname>` |
| Personal email | `<your-email>` |

The `pii-scanner` agent will be used to find all instances.

## Implementation Order

1. Write spec (this file) → commit
2. Create `plugin.json` manifest
3. Create `skills/project-context/SKILL.md`
4. Create `agents/expert.md`
5. Create `agents/go-specialist.md`
6. Create `agents/db-design.md`
7. Create `agents/schema-auditor.md`
8. Create `agents/pii-scanner.md`
9. Create `agents/docs-agent.md`
10. Create `hooks/hooks.json` (pre-commit PII guard)
11. Scrub existing docs of PII values
12. PR + ship

## Non-Goals

- Runtime fetching of agents from external repos — all plugin content is in-tree
- Automated CI knowledge-base updates — project-context reads live docs files
- Replacing the existing `.claude/` operational skills — those stay private and separate
- Modifying `.claude/settings.json` — new hooks live in plugin hooks.json only
