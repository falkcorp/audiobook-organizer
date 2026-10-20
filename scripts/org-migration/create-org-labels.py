#!/usr/bin/env python3
# file: scripts/org-migration/create-org-labels.py
# version: 1.0.3
# guid: fc5b0ad9-d1f2-4846-acc0-11dbe893f435
# last-edited: 2026-10-20

"""
create-org-labels.py
====================
Idempotently creates all falkcorp default repository labels via the GitHub API.
Run this against any org to bring its label set in sync.

Usage:
    python3 create-org-labels.py [--org falkcorp] [--dry-run]

Requires: gh CLI authenticated with admin:org scope, OR a GH_TOKEN env var
with `admin:org` scope. Falls back to `gh api` if GH_TOKEN not set.

Exit codes: 0 = success, 1 = partial failure (see stderr).
"""

import argparse
import json
import subprocess
import sys
import time

# ─────────────────────────────────────────────────────────────────────────────
# LABEL INVENTORY
# Source of truth for all falkcorp org default labels.
# Last synced: 2026-06-05 from jdfalk/ghcommon (--limit 400) +
#              jdfalk/burndown-tasks burndown-specific labels.
# ─────────────────────────────────────────────────────────────────────────────
LABELS = [
    # ── GitHub defaults (present in every new org) ──────────────────────────
    {"name": "bug", "color": "d73a49", "description": "Something isn't working"},
    {
        "name": "documentation",
        "color": "0075ca",
        "description": "Improvements or additions to documentation",
    },
    {
        "name": "duplicate",
        "color": "cfd3d7",
        "description": "This issue or pull request already exists",
    },
    {"name": "enhancement", "color": "a2eeef", "description": "New feature or request"},
    {
        "name": "good first issue",
        "color": "7057FF",
        "description": "Good for newcomers to the project",
    },
    {
        "name": "help wanted",
        "color": "008672",
        "description": "Extra attention is needed from the community",
    },
    {"name": "invalid", "color": "e4e669", "description": "This doesn't seem right"},
    {"name": "question", "color": "d876e3", "description": "Further information is requested"},
    {"name": "wontfix", "color": "ffffff", "description": "This will not be worked on"},
    # ── Automation / AI ──────────────────────────────────────────────────────
    {
        "name": "codex",
        "color": "ff6b9d",
        "description": "Created or modified by AI/automation agents",
    },
    {"name": "automation", "color": "1f883d", "description": "Automation scripts and tools"},
    {"name": "ai", "color": "ededed", "description": ""},
    {"name": "machine-learning", "color": "ededed", "description": ""},
    {"name": "auto-generated", "color": "B60205", "description": ""},
    {"name": "dogfooding", "color": "ededed", "description": ""},
    # ── Type taxonomy ────────────────────────────────────────────────────────
    {"name": "type:bug", "color": "d73a49", "description": "Something isn't working"},
    {"name": "type:feature", "color": "0052cc", "description": "New feature development"},
    {
        "name": "type:enhancement",
        "color": "a2eeef",
        "description": "Improvement to existing feature",
    },
    {
        "name": "type:documentation",
        "color": "0075ca",
        "description": "Improvements or additions to documentation",
    },
    {
        "name": "type:refactor",
        "color": "f1c232",
        "description": "Code refactoring without feature changes",
    },
    {"name": "type:security", "color": "ee0701", "description": "Security related issues"},
    {"name": "type:testing", "color": "1d76db", "description": "Testing related work"},
    {"name": "type:maintenance", "color": "6c757d", "description": "Maintenance and housekeeping"},
    {"name": "type:ci", "color": "ededed", "description": ""},
    {"name": "type:docs", "color": "ededed", "description": ""},
    {"name": "type:infrastructure", "color": "ededed", "description": ""},
    {"name": "type:protobuf", "color": "ededed", "description": ""},
    # ── Priority taxonomy (namespaced) ───────────────────────────────────────
    {
        "name": "priority:critical",
        "color": "d73a49",
        "description": "Critical priority - immediate attention required",
    },
    {"name": "priority:high", "color": "d93f0b", "description": "High priority"},
    {"name": "priority:medium", "color": "fbca04", "description": "Medium priority"},
    {"name": "priority:normal", "color": "e4e669", "description": "Standard priority"},
    {"name": "priority:low", "color": "0e8a16", "description": "Low priority"},
    # P0–P3 for burndown bot triage
    {
        "name": "priority:P0",
        "color": "b60205",
        "description": "Production outage / data loss / security",
    },
    {
        "name": "priority:P1",
        "color": "d93f0b",
        "description": "Core feature broken; blocks other work",
    },
    {"name": "priority:P2", "color": "e4e669", "description": "Standard bug fix or feature"},
    {"name": "priority:P3", "color": "c5def5", "description": "Minor improvement or cleanup"},
    # Legacy non-namespaced (kept for backwards compat)
    {"name": "priority-high", "color": "b60205", "description": "High priority issue"},
    {"name": "priority-medium", "color": "fbca04", "description": "Medium priority issue"},
    {"name": "priority-low", "color": "0e8a16", "description": "Low priority issue"},
    {"name": "priority: medium", "color": "ededed", "description": ""},
    {
        "name": "critical",
        "color": "d73a49",
        "description": "Critical priority - immediate attention required",
    },
    {"name": "high", "color": "d93f0b", "description": "High priority"},
    {"name": "medium", "color": "fbca04", "description": "Medium priority"},
    {"name": "low", "color": "0e8a16", "description": "Low priority"},
    {"name": "high-priority", "color": "ededed", "description": ""},
    # ── Status taxonomy ──────────────────────────────────────────────────────
    {"name": "status:ready", "color": "c3e6cb", "description": "Ready for implementation"},
    {"name": "status:in-progress", "color": "d4edda", "description": "Work in progress"},
    {
        "name": "status:on-hold",
        "color": "d93f0b",
        "description": "Paused — not ready for bot pickup",
    },
    {
        "name": "status:blocked",
        "color": "f8d7da",
        "description": "Blocked by external dependencies",
    },
    {"name": "status:needs-review", "color": "fff3cd", "description": "Needs code review"},
    {"name": "status:todo", "color": "ffffff", "description": "To do - not started"},
    {"name": "status:duplicate", "color": "6f42c1", "description": "Duplicate issue"},
    {"name": "status:wontfix", "color": "6c757d", "description": "This will not be worked on"},
    {"name": "status:review", "color": "9B59B6", "description": "Task is ready for review"},
    # Legacy non-namespaced
    {"name": "ready", "color": "c3e6cb", "description": "Ready for implementation"},
    {"name": "in-progress", "color": "d4edda", "description": "Work in progress"},
    {"name": "blocked", "color": "f8d7da", "description": "Blocked by external dependencies"},
    {"name": "needs-review", "color": "fff3cd", "description": "Needs code review"},
    {"name": "needs-triage", "color": "ededed", "description": ""},
    {"name": "needs-info", "color": "ededed", "description": ""},
    {"name": "stale", "color": "ededed", "description": ""},
    {"name": "completed", "color": "ededed", "description": ""},
    # ── Burndown bot ─────────────────────────────────────────────────────────
    {
        "name": "triaged",
        "color": "0e8a16",
        "description": "Triage decision written; ready for dispatch",
    },
    {
        "name": "burndown:batch-pending",
        "color": "e4e669",
        "description": "OpenAI batch triage in flight",
    },
    {
        "name": "[failed-batch]",
        "color": "e4b429",
        "description": "Batch attempt failed; eligible for retry",
    },
    {
        "name": "[failed-batch-hard]",
        "color": "b60205",
        "description": "Hard batch failure; needs manual triage",
    },
    # ── Size taxonomy ────────────────────────────────────────────────────────
    {"name": "size:small", "color": "c2e0c6", "description": "Small change (1-2 hours)"},
    {"name": "size:medium", "color": "bfd4f2", "description": "Medium change (half day)"},
    {"name": "size:large", "color": "f9d0c4", "description": "Large change (1-2 days)"},
    {"name": "size:epic", "color": "d73a49", "description": "Epic change (multiple days/weeks)"},
    # Slash-style size labels (from some tooling)
    {"name": "size/XS", "color": "ededed", "description": ""},
    {"name": "size/S", "color": "ededed", "description": ""},
    {"name": "size/M", "color": "ededed", "description": ""},
    {"name": "size/L", "color": "ededed", "description": ""},
    {"name": "size/XL", "color": "ededed", "description": ""},
    # ── Tech taxonomy ────────────────────────────────────────────────────────
    {"name": "tech:go", "color": "00add8", "description": "Go programming language"},
    {"name": "tech:python", "color": "3572a5", "description": "Python programming language"},
    {"name": "tech:rust", "color": "dea584", "description": "Rust programming language"},
    {
        "name": "tech:typescript",
        "color": "3178c6",
        "description": "TypeScript programming language",
    },
    {
        "name": "tech:javascript",
        "color": "f1e05a",
        "description": "JavaScript programming language",
    },
    {"name": "tech:docker", "color": "2496ed", "description": "Docker containerization"},
    {"name": "tech:kubernetes", "color": "326ce5", "description": "Kubernetes orchestration"},
    {"name": "tech:protobuf", "color": "c5def5", "description": "Protocol buffer definitions"},
    {"name": "tech:grpc", "color": "bfd4f2", "description": "gRPC service implementations"},
    {"name": "tech:shell", "color": "89e051", "description": "Shell scripting (bash, sh)"},
    # Legacy non-namespaced
    {"name": "go", "color": "00add8", "description": "Go programming language"},
    {"name": "golang", "color": "30AEF6", "description": ""},
    {"name": "python", "color": "3572a5", "description": "Python programming language"},
    {"name": "javascript", "color": "f1e05a", "description": "JavaScript programming language"},
    {"name": "docker", "color": "2496ed", "description": "Docker containerization"},
    {"name": "kubernetes", "color": "326ce5", "description": "Kubernetes orchestration"},
    {"name": "grpc", "color": "bfd4f2", "description": "gRPC service implementations"},
    {"name": "protobuf", "color": "c5def5", "description": "Protocol buffer definitions"},
    {"name": "language:go", "color": "ededed", "description": ""},
    {"name": "language: python", "color": "ededed", "description": ""},
    {"name": "language: rust", "color": "ededed", "description": ""},
    # ── Workflow taxonomy ────────────────────────────────────────────────────
    {
        "name": "workflow:ci-cd",
        "color": "28a745",
        "description": "Continuous integration and deployment",
    },
    {
        "name": "workflow:github-actions",
        "color": "2088ff",
        "description": "GitHub Actions workflows",
    },
    {"name": "workflow:automation", "color": "1f883d", "description": "Automation and tooling"},
    {
        "name": "workflow:deployment",
        "color": "0366d6",
        "description": "Deployment and release management",
    },
    # Legacy non-namespaced
    {"name": "ci-cd", "color": "28a745", "description": "Continuous integration and deployment"},
    {"name": "ci/cd", "color": "0052cc", "description": "Continuous integration and deployment"},
    {"name": "ci", "color": "ededed", "description": ""},
    {"name": "github-actions", "color": "2088ff", "description": "GitHub Actions related work"},
    {"name": "workflow", "color": "0366d6", "description": "GitHub workflow improvements"},
    {"name": "workflows", "color": "ededed", "description": ""},
    {"name": "deployment", "color": "ededed", "description": ""},
    # ── Module taxonomy ──────────────────────────────────────────────────────
    {"name": "module:api", "color": "0366d6", "description": "API development and changes"},
    {"name": "module:auth", "color": "0052cc", "description": "Authentication and authorization"},
    {"name": "module:backend", "color": "343a40", "description": "Backend development"},
    {"name": "module:frontend", "color": "6c757d", "description": "Frontend development"},
    {"name": "module:ui", "color": "495057", "description": "User interface development"},
    {"name": "module:database", "color": "fd7e14", "description": "Database related work"},
    {"name": "module:cache", "color": "5319e7", "description": "Caching and data storage"},
    {"name": "module:config", "color": "006b75", "description": "Configuration management"},
    {
        "name": "module:metrics",
        "color": "6f42c1",
        "description": "Metrics collection and monitoring",
    },
    {
        "name": "module:queue",
        "color": "e99695",
        "description": "Message queuing and task management",
    },
    {"name": "module:web", "color": "b60205", "description": "Web services and HTTP handling"},
    {"name": "module:common", "color": "ededed", "description": ""},
    {"name": "module:db", "color": "ededed", "description": ""},
    {"name": "module:deployment", "color": "ededed", "description": ""},
    {"name": "module:grpc", "color": "ededed", "description": ""},
    {
        "name": "module:iam",
        "color": "96CEB4",
        "description": "Identity and Access Management module",
    },
    {
        "name": "module:ledger",
        "color": "FFEAA7",
        "description": "Financial ledger and accounting module",
    },
    {"name": "module:log", "color": "ededed", "description": ""},
    {"name": "module:monitoring", "color": "ededed", "description": ""},
    {
        "name": "module:notification",
        "color": "DDA0DD",
        "description": "Notification and messaging module",
    },
    {
        "name": "module:organization",
        "color": "98D8C8",
        "description": "Organization and tenant management module",
    },
    {
        "name": "module:payment",
        "color": "F7DC6F",
        "description": "Payment processing and gateway module",
    },
    {"name": "module:plugins", "color": "ededed", "description": ""},
    {"name": "module:proto", "color": "ededed", "description": ""},
    {"name": "module:security", "color": "ededed", "description": ""},
    {
        "name": "module:wallet",
        "color": "BB8FCE",
        "description": "Digital wallet and balance management module",
    },
    # ── Project taxonomy ─────────────────────────────────────────────────────
    {
        "name": "project:transcription",
        "color": "ffa8a8",
        "description": "Audio transcription features",
    },
    {"name": "project:whisper", "color": "ff6b6b", "description": "Whisper ASR integration"},
    {"name": "project:media", "color": "74c0fc", "description": "Media processing and handling"},
    {
        "name": "project:subtitles",
        "color": "4c956c",
        "description": "Subtitle processing and conversion",
    },
    {
        "name": "project:gcommon-refactor",
        "color": "f9d0c4",
        "description": "gcommon refactor initiative",
    },
    {
        "name": "project:github-management",
        "color": "6f42c1",
        "description": "GitHub project management and workflows",
    },
    {
        "name": "project:protobuf-implementation",
        "color": "c5def5",
        "description": "Protocol buffer implementation work",
    },
    {
        "name": "project:issue-management",
        "color": "e99695",
        "description": "Issue management and tracking workflows",
    },
    # ── Common standalone ────────────────────────────────────────────────────
    {
        "name": "dependencies",
        "color": "0366d6",
        "description": "Pull requests that update a dependency file",
    },
    {"name": "security", "color": "ee0701", "description": "Security related issues"},
    {"name": "performance", "color": "ff9500", "description": "Performance improvements"},
    {"name": "breaking-change", "color": "c5000b", "description": "Introduces a breaking change"},
    {
        "name": "external-dependency",
        "color": "e4b429",
        "description": "Depends on external systems or libraries",
    },
    {
        "name": "merge-conflict",
        "color": "b60205",
        "description": "Pull request has merge conflicts",
    },
    {"name": "gcommon-refactor", "color": "f9d0c4", "description": "gcommon refactoring work"},
    {"name": "issue-management", "color": "e99695", "description": "Issue tracking and management"},
    {"name": "refactoring", "color": "f9d0c4", "description": "Code refactoring"},
    {"name": "testing", "color": "1d76db", "description": "Testing related work"},
    {"name": "ui/ux", "color": "e99695", "description": "User interface and user experience"},
    {"name": "good-first-issue", "color": "7057ff", "description": "Good for newcomers"},
    {"name": "help-wanted", "color": "008672", "description": "Extra attention is needed"},
    {"name": "bugfix", "color": "ededed", "description": ""},
    {"name": "feature", "color": "0052cc", "description": "New feature development"},
    {"name": "ui", "color": "495057", "description": "User interface development"},
    {"name": "metrics", "color": "6f42c1", "description": "Metrics collection and monitoring"},
    {"name": "queue", "color": "e99695", "description": "Message queuing and task management"},
    {"name": "web", "color": "b60205", "description": "Web services and HTTP handling"},
    {"name": "auth", "color": "0052cc", "description": "Authentication and authorization"},
    {"name": "backend", "color": "343a40", "description": "Backend development"},
    {"name": "frontend", "color": "6c757d", "description": "Frontend development"},
    {"name": "cache", "color": "5319e7", "description": "Caching and data storage"},
    {"name": "config", "color": "006b75", "description": "Configuration management"},
    {"name": "database", "color": "fd7e14", "description": "Database related work"},
    # ── Engineering practice ─────────────────────────────────────────────────
    {"name": "architecture", "color": "ededed", "description": ""},
    {"name": "optimization", "color": "ededed", "description": ""},
    {"name": "observability", "color": "ededed", "description": ""},
    {"name": "monitoring", "color": "ededed", "description": ""},
    {"name": "monitor", "color": "ededed", "description": ""},
    {"name": "infrastructure", "color": "ededed", "description": ""},
    {"name": "devops", "color": "ededed", "description": ""},
    {"name": "containerization", "color": "ededed", "description": ""},
    {"name": "orchestration", "color": "ededed", "description": ""},
    {"name": "migration", "color": "ededed", "description": ""},
    {"name": "integration", "color": "ededed", "description": ""},
    {"name": "integration-testing", "color": "ededed", "description": ""},
    {"name": "quality-assurance", "color": "ededed", "description": ""},
    {"name": "quality", "color": "ededed", "description": ""},
    {"name": "e2e", "color": "ededed", "description": ""},
    {"name": "test", "color": "ededed", "description": ""},
    {"name": "tests", "color": "ededed", "description": ""},
    {"name": "debugging", "color": "ededed", "description": ""},
    {"name": "troubleshooting", "color": "ededed", "description": ""},
    {"name": "benchmarking", "color": "ededed", "description": ""},
    {"name": "audit", "color": "ededed", "description": ""},
    {"name": "compliance", "color": "ededed", "description": ""},
    {"name": "accessibility", "color": "ededed", "description": ""},
    {"name": "i18n", "color": "ededed", "description": ""},
    {"name": "validation", "color": "ededed", "description": ""},
    {"name": "cleanup", "color": "ededed", "description": ""},
    {"name": "refactor", "color": "ededed", "description": ""},
    {"name": "build", "color": "ededed", "description": ""},
    {"name": "sprint", "color": "ededed", "description": ""},
    {"name": "planning", "color": "ededed", "description": ""},
    {"name": "coordination", "color": "ededed", "description": ""},
    {"name": "rebase", "color": "ededed", "description": ""},
    # ── Project management ───────────────────────────────────────────────────
    {"name": "epic", "color": "ededed", "description": ""},
    {"name": "project", "color": "ededed", "description": ""},
    {"name": "milestone", "color": "ededed", "description": ""},
    {"name": "management", "color": "ededed", "description": ""},
    {"name": "collaboration", "color": "ededed", "description": ""},
    {"name": "teamwork", "color": "ededed", "description": ""},
    {"name": "community", "color": "ededed", "description": ""},
    {"name": "area:community", "color": "ededed", "description": ""},
    {"name": "enterprise", "color": "ededed", "description": ""},
    # ── Versioning / releases ────────────────────────────────────────────────
    {"name": "versioning", "color": "ededed", "description": ""},
    {"name": "version-control", "color": "ededed", "description": ""},
    {"name": "changelog", "color": "ededed", "description": ""},
    {"name": "release", "color": "ededed", "description": ""},
    {"name": "1-1-1-migration", "color": "ededed", "description": ""},
    # ── Reliability / ops ────────────────────────────────────────────────────
    {"name": "backup", "color": "ededed", "description": ""},
    {"name": "disaster-recovery", "color": "ededed", "description": ""},
    {"name": "reliability", "color": "ededed", "description": ""},
    {"name": "real-time", "color": "ededed", "description": ""},
    {"name": "missing-files", "color": "ededed", "description": ""},
    {"name": "severity-error", "color": "ededed", "description": ""},
    {"name": "compilation-blocker", "color": "ededed", "description": ""},
    # ── Misc / tools ─────────────────────────────────────────────────────────
    {"name": "api", "color": "ededed", "description": ""},
    {"name": "issues", "color": "ededed", "description": ""},
    {"name": "docs", "color": "ededed", "description": ""},
    {"name": "sdk", "color": "ededed", "description": ""},
    {"name": "cli", "color": "ededed", "description": ""},
    {"name": "scripts", "color": "ededed", "description": ""},
    {"name": "search", "color": "ededed", "description": ""},
    {"name": "proto", "color": "ededed", "description": ""},
    {"name": "providers", "color": "ededed", "description": ""},
    {"name": "ops", "color": "ededed", "description": ""},
    {"name": "npm", "color": "e0ae53", "description": ""},
    {"name": "mysql", "color": "ededed", "description": ""},
    {"name": "db", "color": "ededed", "description": ""},
    {"name": "dependency", "color": "ededed", "description": ""},
    {"name": "devcontainer", "color": "ededed", "description": ""},
    {"name": "cockroachdb", "color": "ededed", "description": ""},
    {"name": "gcommon", "color": "ededed", "description": ""},
    {"name": "metadata", "color": "ededed", "description": ""},
    {"name": "compatibility", "color": "ededed", "description": ""},
    {"name": "cross-platform", "color": "ededed", "description": ""},
    {"name": "mobile", "color": "ededed", "description": ""},
    {"name": "analytics", "color": "ededed", "description": ""},
    {"name": "business-intelligence", "color": "ededed", "description": ""},
    {"name": "advanced-features", "color": "ededed", "description": ""},
    {"name": "developer-experience", "color": "ededed", "description": ""},
    {"name": "developer-tools", "color": "ededed", "description": ""},
    {"name": "apps", "color": "ededed", "description": ""},
    {"name": "marketplace", "color": "ededed", "description": ""},
    {"name": "templates", "color": "ededed", "description": ""},
    {"name": "examples", "color": "ededed", "description": ""},
    {"name": "example", "color": "ededed", "description": ""},
    {"name": "feat", "color": "ededed", "description": ""},
    {"name": "dashboard", "color": "ededed", "description": ""},
    {"name": "configuration", "color": "ededed", "description": ""},
    {"name": "widgets", "color": "ededed", "description": ""},
    {"name": "media", "color": "74c0fc", "description": "Media processing and handling"},
    {"name": "transcription", "color": "ffa8a8", "description": "Audio transcription features"},
    {"name": "whisper", "color": "ff6b6b", "description": "Whisper ASR integration"},
    # ── gcommon-specific modules ─────────────────────────────────────────────
    {
        "name": "module:iam",
        "color": "96CEB4",
        "description": "Identity and Access Management module",
    },
    {
        "name": "module:ledger",
        "color": "FFEAA7",
        "description": "Financial ledger and accounting module",
    },
    {
        "name": "module:notification",
        "color": "DDA0DD",
        "description": "Notification and messaging module",
    },
    {
        "name": "module:organization",
        "color": "98D8C8",
        "description": "Organization and tenant management module",
    },
    {
        "name": "module:payment",
        "color": "F7DC6F",
        "description": "Payment processing and gateway module",
    },
    {
        "name": "module:wallet",
        "color": "BB8FCE",
        "description": "Digital wallet and balance management module",
    },
]

# ─────────────────────────────────────────────────────────────────────────────


def gh_api(method, path, data=None, dry_run=False):
    cmd = ["gh", "api", "-X", method, path]
    if data:
        for k, v in data.items():
            cmd += ["-f", f"{k}={v}"]
    if dry_run:
        print(f"  [DRY RUN] {method} {path} {data or ''}")
        return {"dry_run": True}
    result = subprocess.run(cmd, capture_output=True, text=True)
    if result.returncode == 0:
        return json.loads(result.stdout) if result.stdout.strip() else {}
    return {"error": result.stderr.strip(), "code": result.returncode}


def get_existing_labels(org):
    """Fetch all existing org labels (paginates via gh api --paginate)."""
    cmd = [
        "gh",
        "api",
        "--paginate",
        f"/orgs/{org}/labels",
        "-H",
        "Accept: application/vnd.github+json",
    ]
    result = subprocess.run(cmd, capture_output=True, text=True)
    if result.returncode != 0:
        # org labels API returns 404 — fall back to empty set (all will be created)
        return set()
    try:
        data = json.loads(result.stdout)
        return {label_info["name"].lower() for label_info in data}
    except Exception:
        return set()


def create_label(org, label, dry_run=False):
    color = label["color"].lstrip("#")
    cmd = [
        "gh",
        "api",
        "-X",
        "POST",
        f"/orgs/{org}/labels",
        "-H",
        "Accept: application/vnd.github+json",
        "-f",
        f"name={label['name']}",
        "-f",
        f"color={color}",
        "-f",
        f"description={label['description']}",
    ]
    if dry_run:
        print(f"  [DRY RUN] POST /orgs/{org}/labels  name={label['name']!r}")
        return True
    result = subprocess.run(cmd, capture_output=True, text=True)
    return result.returncode == 0


def main():
    parser = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    parser.add_argument("--org", default="falkcorp", help="GitHub org (default: falkcorp)")
    parser.add_argument(
        "--dry-run", action="store_true", help="Print what would be created without making changes"
    )
    parser.add_argument(
        "--skip-existing-check",
        action="store_true",
        help="Skip fetching existing labels and attempt to create all (safe — 422s are ignored)",
    )
    args = parser.parse_args()

    print(f"Target org: {args.org}  |  Labels in inventory: {len(LABELS)}")

    if args.skip_existing_check:
        existing = set()
        print("Skipping existing-label check (--skip-existing-check)")
    else:
        print("Fetching existing labels...")
        existing = get_existing_labels(args.org)
        print(f"  {len(existing)} labels already exist")

    # Deduplicate inventory by name (keep first occurrence)
    seen = set()
    unique_labels = []
    for entry in LABELS:
        key = entry["name"].lower()
        if key not in seen:
            seen.add(key)
            unique_labels.append(entry)

    to_create = [entry for entry in unique_labels if entry["name"].lower() not in existing]
    print(f"  {len(to_create)} labels to create\n")

    created, failed = [], []

    for label in to_create:
        ok = create_label(args.org, label, dry_run=args.dry_run)
        if ok:
            created.append(label["name"])
            print(f"  ✓ {label['name']}")
        else:
            failed.append(label["name"])
            print(f"  ✗ {label['name']}", file=sys.stderr)
        if not args.dry_run:
            time.sleep(0.1)  # stay under secondary rate limits

    print(f"\nDone.  created={len(created)}  failed={len(failed)}")
    if failed:
        print("Failed labels:", failed, file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
