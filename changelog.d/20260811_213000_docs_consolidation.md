### Changed

- **Consolidated the `docs/` tree.** Archived 67 superseded documents via `git mv` —
  56 completed fleet task/status pairs to `docs/archive/superpowers/fleet-done/`, plus 11
  superseded singles including `technical_design.md` and `implementation-guide.md`. Merged
  the two independently-written `slog-prod-verify.md` runbooks into the canonical
  `docs/operations/` copy. No document was deleted.
- **Rewrote `docs/agent-tasks/README.md`**, which listed 9 archived folders and only 1 of the
  10 packages actually in that directory. All 10 are verified ACTIVE or PARTIAL against code
  artifacts at HEAD — the archiving discipline had in fact been followed, so nothing there
  was archivable.

### Fixed

- **`docs/agent-tasks/ai-responses-migration/README.md` and `orchestration.md` were corrupt
  at HEAD** — committed JSON-string-encoded, beginning with a literal `"` and containing
  literal `\n` sequences with no line terminators. Decoded to real Markdown.
- Repointed 9 live files whose links the archival broke, including an acceptance check in
  `TASK-08-slog-prod-verify.md` that would have silently failed, and the front-door links in
  `README.md` / `.github/README.md`.
- Corrected stale `<!-- file: -->` header paths in `docs/BUILD.md`,
  `docs/BUILD_TAGS_GUIDE.md`, `docs/CODING_STANDARDS.md`, `docs/MOCKERY_GUIDE.md`.
