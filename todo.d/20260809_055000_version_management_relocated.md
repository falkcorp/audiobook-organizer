- [ ] **`version-management.spec.ts` (6 failures) — version management MOVED off
      the book detail page. The spec needs a rewrite, not a selector tweak.**
      Fully diagnosed 2026-08-09; no code changed, because the fix is a real
      rewrite and a half-finished one is worse than none.

      **What the tests do:** `openBookDetail()` navigates to `/library/<id>`
      and each test then clicks `getByRole('button', { name: 'Manage
      Versions' })` to open the linking UI.

      **What the app does now:**

      - `pages/BookDetail.tsx` does **not** import `VersionManagement` at all.
        It renders `components/bookdetail/BookDetailVersionGroup.tsx`, which is
        **read-only** — Bitrate, Duration, File, Origin, Path, Sample Rate,
        Size. There is no link/unlink affordance on book detail.
      - The interactive `VersionManagement` component is rendered from
        `components/library/LibraryDialogs.tsx` and `pages/Library.tsx` — i.e.
        from the **Library** page.
      - "Manage Versions" is a **MenuItem inside the card's overflow menu**
        (`components/audiobooks/AudiobookCard.tsx:336`), so its role is
        `menuitem`, not `button`, and the menu must be opened first.

      So the tests are driving a capability that page no longer has. The book
      detail header still shows a "Version Group Linked" chip, which is why the
      page *looks* right in the snapshot — it displays version state but cannot
      change it.

      **The rewrite:** point `openBookDetail()` (5 call sites) at `/library`,
      open the target card's overflow menu, then click the **menuitem**
      "Manage Versions". The dialog interactions after that point are likely
      still valid, since `VersionManagement.tsx` itself was not replaced — only
      relocated.

      **Worth asking before doing it:** is losing version management from book
      detail intentional? Managing versions of the book you are looking at is a
      natural place for it, and it now requires going back to the library and
      finding the card. That is a product question, not a test question.
