<!-- file: docs/agent-tasks/todo-completion/review/handoff-verify-1.md -->
<!-- version: 1.0.0 -->
<!-- guid: a8c5121e-1f47-4258-addd-5b10ef2038a1 -->
<!-- last-edited: 2026-08-22 -->

# Handoff — verify-1 (group 1: metadata, missing-file-lane)

## Paused by coordinator mid-batch. Resume instructions below.

## Output file
`/private/tmp/claude-501/-Users-jdfalk-repos-github-com-jdfalk-audiobook-organizer/f21a92f9-ff10-4ce5-a715-d13a59db3783/scratchpad/patches/verify-1.json`
— valid JSON array, 4 entries written so far (all `metadata` workstream done except none pending there; `missing-file-lane` only TASK-089 done).

## Briefs examined (4 of 29) — all verdict "fail"
- TASK-079 (metadata) — fail (broken re-find grep [advisory-ish, has working bold-id fallback]; minor test-citation imprecision)
- TASK-080 (metadata) — fail (broken re-find grep; **major**: background is stale — both flagged SSRF sites already have real, verified protection in the code, and the exact tests it says to "add" already exist verbatim; **major**: generic TODO-checkbox instruction would falsely close out a 326-alert backlog item after fixing only 2)
- TASK-081 (metadata) — fail (broken re-find grep with NO working fallback; genuine step ambiguity on where to put the `displayOrNone` helper — two reasonable executors produce different diffs/exact_files)
- TASK-089 (missing-file-lane) — fail (broken re-find grep with NO working fallback; Tests section requires editing `internal/server/handlers/abs/library_fake_test.go` to add error-injection to `fakeLibrary.GetAllSeriesBookCounts()`, which is never named as a file to touch anywhere in the brief)

**metadata workstream: 3/3 examined, DONE.**

## Systemic finding (applies to every remaining brief too — verify but don't re-derive)
Every brief's "Source: TODO.md line ~N (re-find it: `grep -n "<paraphrased title>" TODO.md` or by the bold id)" has a **broken re-find grep** — the quoted pattern is built from the brief's own paraphrased title, not the actual TODO.md wording, and returns 0 hits 100% of the time (verified across all 26 missing-file-lane + 3 metadata patterns with a bulk grep sweep). The bare `line ~N` number is reliable/accurate in every case checked so far. Severity depends on whether the task's "TODO id" (see each workstream README table) is a real bold acronym (e.g. `SCORE-REC`, `TODO-MUI-3`, `SEC-CODEQL-BACKLOG` — these have a working fallback via `grep -n "<ID>" TODO.md`) or just a bare line ref like `L5494` (these have **no working fallback at all** — flag as more severe). Recommended fix for every such finding: regenerate the re-find grep from real TODO.md substring, or at minimum trust the line number.

missing-file-lane TODO-id column (from README, for fallback-severity judgment on remaining tasks):
TASK-090 L5722, TASK-091 L5736, TASK-092 L5742, TASK-093 L5758, TASK-094 L6252, TASK-095 L6701, TASK-096 L7435, **TASK-097 TODO-MUI-3 (has real fallback)**, TASK-098 L7736, TASK-099 L8044, TASK-100 L8177, TASK-101 L8245, TASK-102 L8273, TASK-103 L8433, TASK-104 L8551, TASK-105 L8611, TASK-106/107 L8646 (shared line, two subtasks), TASK-108 L8675, TASK-109 L8707, TASK-110 L8738, TASK-111 L8837, TASK-112/113 L8890 (shared line, two subtasks), TASK-114 L8943 — all line-number-only, no fallback.

Line-number accuracy already spot-checked and confirmed correct for ALL of TASK-090 through TASK-114 (each `line ~N` lands on the right TODO bullet — see prior bash output in transcript if resumed with context; if resumed fresh, re-run: for each task file, extract `line ~N` from the `Source:` line and `sed -n "${N}p" TODO.md` against `/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer/TODO.md`).

## Briefs REMAINING (25, in README order) — none started
missing-file-lane, in this order:
TASK-090, TASK-091, TASK-092, TASK-093, TASK-094, TASK-095, TASK-096, TASK-097, TASK-098, TASK-099, TASK-100, TASK-101, TASK-102, TASK-103, TASK-104, TASK-105, TASK-106, TASK-107, TASK-108, TASK-109, TASK-110, TASK-111, TASK-112, TASK-113, TASK-114

## Priority guidance for remaining (from title/tier scan, not yet verified in depth)
High-risk / deep-dive first (Opus-class, prod-data or dry_run/enforcement/merge-shaped):
- TASK-096 (dry_run enforcement across all mutating ops — registry.go, collides with TASK-115 wave2)
- TASK-104 (version-group acoustic audit op, tier 2 of First Aid)
- TASK-105 (chapters backfill from duplicate)
- TASK-106/107 (playlist import/export — collides with scanner.go TASK-021)
- TASK-109/110 (Deluge torrent parsing / grouping audit)
- TASK-111 (pre-apply snapshot tool for 138 pending multidisc holds — review-critical prod-data path per README)
- TASK-112/113 (First Aid orchestrator + frontend trigger, dry-run by default — check dry-run default is actually enforced)
- TASK-114 (debris-book re-association — must be REPOINT/associate, never delete; check literal wording enforces this, matches owner rule)

Lower-risk, faster pass (Haiku/Sonnet, single-file mechanical):
TASK-090, 091, 092, 093, 094, 095, 097, 098, 099, 100, 101, 103, 108

TASK-102 (TypeScript 6→7 migration, Opus/L) — check scope carefully, likely broad blast radius, verify package.json collision note (TASK-097 vs TASK-102 same file, serialized by wave — confirm both briefs actually declare that collision correctly).

## How to resume
1. Read `/private/tmp/claude-501/-Users-jdfalk-repos-github-com-jdfalk-audiobook-organizer/f21a92f9-ff10-4ce5-a715-d13a59db3783/scratchpad/VERIFIER-INSTRUCTIONS.md` again for the full checklist/output format.
2. Read task files from `/private/tmp/claude-501/-Users-jdfalk-repos-github-com-jdfalk-audiobook-organizer/f21a92f9-ff10-4ce5-a715-d13a59db3783/scratchpad/dryrun/docs/agent-tasks/todo-completion/missing-file-lane/TASK-090-*.md` onward, in the order listed above.
3. Repo to verify against (read-only): `/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer` (main checkout, on `main` branch — confirmed clean at handoff time).
4. Append new entries to the SAME `patches/verify-1.json` array (read it back first, it's valid JSON with 4 entries; add to the array, don't overwrite). Re-validate with `python3 -c "import json; json.load(open('...'))"` after every write.
5. Write the file incrementally (every 5 briefs) as instructed, and re-issue a handoff-style pause file if paused again.
6. When all 29 are done, return the final 3-line summary with counts as instructed in VERIFIER-INSTRUCTIONS.md.
