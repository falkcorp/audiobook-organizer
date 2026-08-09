<!-- file: todo.d/20260809-version-group-navigation-and-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6d5b81af-30c7-4e92-8fa4-19bc7d206e53 -->
<!-- last-edited: 2026-08-09 -->

- [ ] **You can no longer navigate between versions of a book.** Book Detail used to
      have a "Versions" tab listing the group's other versions, each clickable to jump
      to it. `web/src/pages/BookDetail.tsx:1014-1015` now renders only Info and
      Files & History, and `BookDetailVersionGroup.tsx` contains no `RouterLink` — the
      version titles are plain text. `VersionManagement.tsx` (the dialog) has no
      `navigate()` call either. The only per-version action left is
      **"Move to: \<title\>"** (`BookDetailVersionGroup.tsx:457-464`), which moves
      *files* between versions — a destructive operation, not navigation, sitting where
      users previously clicked to browse. Getting from the M4B to the MP3 of the same
      book now means going back to the library and finding the other card.

- [ ] **The version-group summary lost its count and its "you are here" marker.**
      `Part of version group with N books.` and `(Current)` appear nowhere in `web/src`.
      All that survives is a bare **"Version Group Linked"** chip
      (`BookDetailHeader.tsx:172`) — it tells you a group exists but not how big it is
      or which member you are looking at.

- [ ] **The library card's overflow menu button has no accessible name.**
      `web/src/components/audiobooks/AudiobookCard.tsx:183` is an `IconButton` with only
      a `<MoreVertIcon/>` inside — no `aria-label`, no tooltip. Screen readers announce
      it as an unlabelled button, and it is now the **only** route to Manage Versions,
      Edit, Fetch Metadata and Parse with AI. The e2e suite has to locate it via
      `button:has([data-testid="MoreVertIcon"])` because there is nothing else to match on.

Context: `version-management.spec.ts` was repointed at the surviving entry point on
2026-08-09 (4 of 6 tests). The two covering navigation and the group summary were
deleted rather than rewritten, since the capabilities themselves are gone. Related:
`todo.d/20260809-changelog-row-compare-affordance.md`,
`todo.d/20260809-per-field-use-fetched-affordance.md`.
