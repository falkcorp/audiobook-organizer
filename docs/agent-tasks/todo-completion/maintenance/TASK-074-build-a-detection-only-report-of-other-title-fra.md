<!-- file: docs/agent-tasks/todo-completion/maintenance/TASK-074-build-a-detection-only-report-of-other-title-fra.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6e916a46-5a0c-49b0-92bd-c0ab15a09cb3 -->
<!-- last-edited: 2026-08-21 -->

# TASK-074 — Build a detection-only report of other title-fragment author rows (the 57 rows beginning with '-') (TODO.md L3602)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · maintenance subagent · **Why:** requires designing a report-only heuristic (rows beginning with '-' plus a broader dirty-shape scan) and a new maintenance op, but no mutation logic — no prod-data risk · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 3602 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "Check how many other author rows are title fragmen" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-05.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/maintenance-074-build-a-detection-only-report-of-other-title-fra" -b agent/maintenance-074-build-a-detection-only-report-of-other-title-fra origin/main
cd "$REPO/.worktrees/maintenance-074-build-a-detection-only-report-of-other-title-fra"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a new REPORT-ONLY maintenance op (e.g. maintenance.author-title-fragment-scan) that walks every author row, flags any whose name (a) begins with a leading hyphen '-' (the specific giveaway named in this item), or (b) fails the exported looksLikePersonName shape check outright (not just as part of a split decision), and logs a count + sample of flagged author IDs/names/book-counts to the activity log — matching owner decision #11's 'detection-only counter, fix deferred' pattern. No renames, merges, or deletes.

## Background (verify before editing)

- internal/metadata/cover.go:117 already calls validateCoverURL() (scheme allowlist) before the fetch at cover.go:137, and the client used there is built with a custom safeCoverDialContext (cover.go:71-89) that resolves the host and rejects private/loopback/link-local IPv4 AND IPv6 ranges. This is real, comprehensive SSRF protection already in place for alert #662.
- internal/covers/covers.go:82's http.Get(coverURL) has exactly one caller: internal/server/covers.go's handleCoverProxy, which calls covers.IsAllowedCoverSource(coverURL) (a 4-domain allowlist: openlibrary/google books/amazon) and rejects before ever calling FetchAndCacheCover. This is real, comprehensive SSRF protection already in place for alert #645.
- internal/metadata/cover_test.go already asserts a 169.254.169.254 cover URL is SSRF-blocked; internal/covers/covers_test.go already has TestIsAllowedCoverSource asserting both the blocked case and several legitimate URLs pass. Do not re-add these tests — verify they exist and pass.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -rn 'TitleFragmentAuthor\|title-fragment-author\|author.title.fragment.report' internal/plugins/maintenance/*.go   # 0 hits — no existing op reports on title-fragment author names (a loose 'title.fragment|titleFragment' regex false-positives on unrelated prose in author_conjunction_repair.go/_test.go describing a different feature's exclusion rule)
  grep -n 'ID:.*author-split-scan' internal/plugins/maintenance/author.go   # 1 hit ~L89 — the existing author-split-scan op is the closest sibling / registration template
  grep -n 'func looksLikePersonName' internal/dedup/author.go   # 1 hit, L210 (unexported — see step 1 on exporting or duplicating minimally) — looksLikePersonName is the shape-check to reuse for detecting existing rows that would now be rejected
  ```

### Reuse — don't invent

- Use `looksLikePersonName shape heuristic (unexported — must be exported or its logic duplicated for use from the maintenance package)` in `internal/dedup/author.go` (verify: `grep -n 'func looksLikePersonName' internal/dedup/author.go`) — do NOT write a parallel helper.
- Use `author-split-scan op registration pattern (report-style, LivenessManual, no apply path)` in `internal/plugins/maintenance/author.go` (verify: `grep -n 'func (p \*Plugin) authorSplitScanDef' internal/plugins/maintenance/author.go`) — do NOT write a parallel helper.

## Step-by-step

1. Read internal/metadata/cover.go top-to-bottom around lines 20-137: confirm validateCoverURL (scheme allowlist) and safeCoverDialContext (private/loopback/link-local IPv4+IPv6 block via DNS-resolved-IP check) are both wired into the client used at line 137.
2. Read internal/covers/covers.go's only caller (internal/server/covers.go:24-45) and confirm covers.IsAllowedCoverSource(coverURL) gates every call to FetchAndCacheCover.
3. Given both sites have real, host/IP-resolved validation already present, add `// lgtm[go/request-forgery]` directly above cover.go:137 and covers.go:82, each citing the exact validating function/file:line found above (do not add a duplicate/parallel validator).
4. Run `grep -n 169.254 internal/metadata/cover_test.go` and `grep -n TestIsAllowedCoverSource internal/covers/covers_test.go` to confirm the anti-over-suppression and blocked-case tests already exist and pass; do not add duplicate tests.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_maintenance_074.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- author name is empty string — must not flag or panic
- author name is exactly '-' with nothing else — flag it, but don't crash on HasPrefix of a 1-char string
- an author with 0 books that still LOOKS like a title fragment — still worth reporting even though it's cheap to delete outright, since the report's job is enumeration, not judgment

## Tests

- internal/dedup/author_test.go: TestLooksLikePersonName_ExportedNameUnchangedBehavior — the renamed/exported function still passes the existing looksLikePersonName test cases (regression guard on the rename)
- internal/plugins/maintenance/author_title_fragment_report_test.go: TestAuthorTitleFragmentScan_FlagsHyphenPrefixed — an author named '-Something' is flagged
- internal/plugins/maintenance/author_title_fragment_report_test.go: TestAuthorTitleFragmentScan_DoesNotFlagRealNames — an author named 'Ludwig van Beethoven' (particle-containing real name) is NOT flagged (anti-over-suppression: proves the report doesn't just flag everything with a lowercase word)

Anti-over-suppression test: `TestAuthorTitleFragmentScan_DoesNotFlagRealNames` — a known-good input still passes with the new guard active.

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] go test ./internal/plugins/maintenance/... -run AuthorTitleFragmentScan passes
- [ ] running the op against a seeded corpus containing 3 known-bad names and 3 known-good names reports exactly 3 flagged
- [ ] Anti-over-suppression test: `TestAuthorTitleFragmentScan_DoesNotFlagRealNames` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_maintenance_074.md`.

## Commit message

```
feat(maintenance): Build a detection-only report of other title-fragment author (TODO L3602)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`go test ./internal/plugins/maintenance/... -run AuthorTitleFragmentScan passes`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Per owner decision #9/#11 pattern: this is a REPORT op only. Do not add a delete/merge/apply path in this task — that is future work gated on a design decision the owner has not made (see L3586/3588/3589).
