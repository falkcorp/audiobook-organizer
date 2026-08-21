<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/TASK-109-parse-deluge-torrent-release-names-into-structur.md -->
<!-- version: 1.0.0 -->
<!-- guid: beaac381-6ca1-49aa-8872-dd72e14c8c54 -->
<!-- last-edited: 2026-08-21 -->

# TASK-109 — Parse Deluge torrent release names into structured candidate metadata (author/series/volume/narrator/edition/year) as a scored candidate for the existing matcher (TODO.md L8707)

**Priority:** P2 · **Effort:** L · **Recommended subagent:** Opus-class · missing-file-lane subagent · **Why:** richer structured-metadata parsing than the existing title-only parser, feeding an existing matcher as a scored candidate — real design work on parse confidence, not mechanical · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 8707 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Use Deluge as a metadata and identity source** —" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-12.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/missing-file-lane-109-parse-deluge-torrent-release-names-into-structur" -b agent/missing-file-lane-109-parse-deluge-torrent-release-names-into-structur origin/main
cd "$REPO/.worktrees/missing-file-lane-109-parse-deluge-torrent-release-names-into-structur"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add ParseTorrentNameMetadata(name string) (candidate, confidence float64) in internal/deluge extracting author/series/volume/narrator/(Unabridged|Abridged)/year/format from a torrent release name — materially richer than the existing title-only ParseTorrentNameCandidates — and feed the result as a SCORED CANDIDATE into the existing book-identity/metadata matcher, never as an authoritative overwrite.

## Background (verify before editing)

- internal/deluge already has a mature, tested torrent-to-book matching system (client.go, discovery.go's 4-tier hash/path/title/SHA matching, centralization.go, import.go) — but its whole purpose is 'is this torrent already imported', not 'extract identity metadata from this torrent name'.
- ParseTorrentNameCandidates already handles 'Author - Title', 'Title - Author', 'Title by Author', 'Author.Title.Year.M4B' shapes but returns only title strings — this is a genuinely different, additive parser, not a rewrite; the existing one must keep working.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'func ParseTorrentNameCandidates' -A 5 internal/deluge/discovery.go   # 1 hit ~L329, returns []string of normalized titles only — ParseTorrentNameCandidates only returns normalized title strings, not structured metadata
  grep -n 'core.get_torrent_status' internal/deluge/client.go   # 1 hit ~L200 — the Deluge client already exposes torrent name + file list via core.get_torrent_status, reusable as the metadata source
  ```

### Reuse — don't invent

- Use `NormalizeTitle / ParseTorrentNameCandidates (extend, don't duplicate, the parsing primitives)` in `internal/deluge/discovery.go` (verify: `grep -n 'func NormalizeTitle\|func ParseTorrentNameCandidates' internal/deluge/discovery.go`) — do NOT write a parallel helper.
- Use `Client.core.get_torrent_status call` in `internal/deluge/client.go` (verify: `grep -n 'core.get_torrent_status' internal/deluge/client.go`) — do NOT write a parallel helper.

## Step-by-step

1. Locate the existing book-identity/metadata matcher this should feed (search for where multiple candidate metadata sources already get scored and merged) — reuse its candidate shape rather than inventing a new one.
2. Add internal/deluge/metadata_candidate.go with ParseTorrentNameMetadata extracting structured fields via regex patterns layered on top of (not replacing) NormalizeTitle.
3. Wire a new op or extend an existing torrent-processing path to call this on each Deluge torrent name and submit the result to the matcher found in step 1 as one scored candidate — never write it directly to a Book row.
4. Handle credentials the existing way (env vars, per internal/deluge/client.go's existing pattern).

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_missing-file-lane_109.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Scene-naming inconsistency — a name matching NO known shape must return confidence 0, not a partial guess dressed up as structured data.

## Tests

- internal/deluge/metadata_candidate_test.go: TestParseTorrentNameMetadata_StructuredShapes — table test over realistic release-name shapes, asserting each structured field extracts correctly.
- TestParseTorrentNameMetadata_LowConfidenceOnAmbiguousNames — anti-over-suppression: a name with no recognizable structure returns a low/zero confidence score, not a confidently-wrong guess.

Anti-over-suppression test: `TestParseTorrentNameMetadata_LowConfidenceOnAmbiguousNames` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/deluge/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/deluge/... -run ParseTorrentNameMetadata passes.
- [ ] A confidently-wrong parse of a nonsense release name scores below the matcher's normal acceptance threshold.
- [ ] Anti-over-suppression test: `TestParseTorrentNameMetadata_LowConfidenceOnAmbiguousNames` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/deluge/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_missing-file-lane_109.md`.

## Commit message

```
feat(missing-file-lane): Parse Deluge torrent release names into structured candidate (TODO L8707)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`go test ./internal/deluge/... -run ParseTorrentNameMetadata passes.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Pairs with L8738 (same Deluge RPC connection, different purpose — grouping evidence, not identity metadata); build/share the credential/client plumbing once if both are picked up in the same wave.
