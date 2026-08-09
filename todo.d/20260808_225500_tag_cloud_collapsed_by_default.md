- [ ] **"Browse by Tag" should start collapsed, or show only the top few tags.**
      Reported by the owner 2026-08-08: *"Browse by tag should start minimized
      as we have tons of tags or only show the top 5."* On a library this size
      the tag cloud renders as a wall of chips that pushes the actual book grid
      below the fold.

      **Current behaviour** (`web/src/components/library/TagCloud.tsx`):

      - Line 41: `const [expanded, setExpanded] = useState(true)` — it defaults
        to **open**.
      - It renders `availableTags.map(...)` with **no cap**: every tag in the
        library, every time.
      - The collapse machinery already exists (header row toggles, `Collapse`,
        rotating chevron, correct `aria-label`), so "start minimized" is
        essentially a one-word change.

      **Two options the owner offered; they are not exclusive and the good
      version is both:**

      1. **Start collapsed** — flip line 41 to `useState(false)`. Trivial, and
         it makes the component honest: a disclosure control whose default is
         "already disclosed" is not doing anything.
      2. **Show the top N (5) when collapsed-ish** — render a short preview row
         of the highest-count tags with a "Show all (N)" affordance, so the
         feature is still discoverable without costing a screenful. This is the
         better UX of the two, because a fully collapsed panel gives no hint
         that tags are worth browsing.

      **⚠️ Verify sort order before slicing.** `availableTags` is passed
      straight through from `Library.tsx` (lines 1971 and 1993 — note it feeds
      **both** `TagCloud` and `FilterSidebar`) and **it has not been confirmed
      that it arrives sorted by count descending**. `TagCloud` currently only
      uses `count` for font size, where order does not matter, so a latent
      sort bug would be invisible today and would silently make "top 5" mean
      "first 5 alphabetically". Sort explicitly in the component rather than
      trusting the caller.

      **Persist the open/closed choice** in `localStorage` alongside the other
      Library view preferences (`STORAGE_KEYS`), so someone who opens it does
      not have to re-open it on every visit. Without that, "start collapsed"
      trades one annoyance for another.

      **Acceptance:** on a fresh visit to /library the book grid is visible
      without scrolling past the tag cloud; tags remain reachable in one click;
      and if a top-N preview is used, the tags shown are genuinely the most
      common ones, verified against a library with many tags rather than a
      handful of fixtures.
