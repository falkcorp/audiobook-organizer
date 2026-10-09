<!-- file: docs/proposals/2026-10-holistic/00-charter.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7d2f4c1e-9a3b-4e8f-b6d1-2c5a8e0f3b47 -->
<!-- last-edited: 2026-10-08 -->

# Holistic review, October 2026: charter

**Owner's goal:** optimize and simplify without losing useful functionality, and treat the whole project as one system instead of separate pieces.

**This is a planning exercise only.**
- No code is changed.
- Every workstream produces three things:
  - findings;
  - a proposed specification;
  - implementation files: a phased PR list with the exact files each PR touches.
- A coordinator then turns the seven workstreams into one roadmap that fits together.

## Workstreams

| # | File | Scope |
|---|------|-------|
| 01 | `01-legacy-and-dead-code.md` | Code still on an old pattern that should have moved to the newer one. Dead code paths. Logic that does nothing useful. Go and TypeScript. |
| 02 | `02-filter-identification-pipeline.md` | The Review and Library filters, plus search, matching and identification, reviewed from a search-algorithms and efficiency angle. |
| 03 | `03-dedup-page-retirement.md` | Retire the old `/dedup` page: a full parity matrix against the Review page, the gaps to close, and the removal plan. |
| 04 | `04-operations-census.md` | Redundant operations to prune. Jobs that run outside the operations system and should move into it. |
| 05 | `05-operations-v3.md` | Operations system v3: a new SDK, easier to write new operations for, fixes past incidents, and includes a migration from v1/v2. |
| 06 | `06-bleeding-edge-go-node.md` | Recent Go and Node/TypeScript features that would make this app better. |
| 07 | `07-design-decisions-and-modularity.md` | Design decisions to revisit for performance, reliability, quality and developer experience. Modularity and ease of upgrading. |
| 08 | `08-integrated-roadmap.md` | Written by the coordinator: overlaps resolved, a file-collision matrix, PR order, and the decisions the owner must make. |

## Rules for every analyst

1. **Read-only.**
   - Write only your own file under `docs/proposals/2026-10-holistic/`.
   - You may also add a sub-folder `NN-<topic>/` for appendices and implementation briefs.
   - No code edits, no git commands that change anything, no subagents, and no calls to production.
2. **Save often.** Write a skeleton first, then fill it in at least every 15 minutes, so that a crash loses nothing.
3. **Back every finding with evidence:**
   - a `file:line` anchor;
   - the command that proves it (a grep, LSP query or `go build`);
   - a confidence of high, medium or low.
   Never state a count you didn't measure.
4. **A "no references" result is not proof of dead code in this repo.** Before calling anything dead, check all of these:
   - operations dispatched by string name through the registry, scheduler, config or `childop`;
   - HTTP routes that only the frontend (`web/src/services/api*.ts`) or AudioBooth (Swift, external) calls;
   - code behind build tags (`embed_frontend`, test-only tags; see `docs/BUILD_TAGS_GUIDE.md`);
   - reflection and JSON field names;
   - `go:linkname` and generated mocks.
5. **Use this outline for your doc:**
   1. Summary, at most 10 bullets.
   2. Findings: a table with ID, finding, evidence, confidence and impact.
   3. Proposed specification.
   4. Implementation plan: phased PRs. Each PR lists the exact files it touches, its tests, its rollback, and a size of S, M or L.
   5. Risks and what must not break.
   6. Dependencies on other workstreams, cited by number.
   7. Open questions for the owner. Each one needs a recommended answer.
6. **Hard bans:**
   - Leave `internal/writeback/` alone, because iTunes still uses it.
   - No iTunes writes, removals or rebuilds.
   - No audio decoding on the server: fingerprinting and transcription run on the Macs only, ffprobe is allowed.
   - Never delete `book_file` rows; repoint them.
   - Never propose running fix-library-states.
   - Never split the scan ConcurrencyKey.
   - No `go work init`.
   - Never use the unsigned Cloudflare email header as identity.
   - The word "honest" must not appear anywhere.
7. **The repo is public:**
   - no real book titles, author names from the library or real paths;
   - no internal IPs (use `192.0.2.x`);
   - no emails, tokens or hostnames beyond role names.
8. **Docs need a version header like this one.**
9. **If your work depends on another workstream,** add a line to your section 6. If it blocks you, use SendMessage to that analyst by name (names are listed below). Don't wait on the reply: write down your assumption and keep going.

## Analyst names, for SendMessage

`legacy`, `search`, `dedup`, `census`, `opsv3`, `bleeding`, `design`, and later `coordinator`.

## Background notes

The notes live at `~/.claude/projects/-Users-jdfalk-repos-github-com-jdfalk-audiobook-organizer/memory/`. Before relying on a note, check it against the code at HEAD.
