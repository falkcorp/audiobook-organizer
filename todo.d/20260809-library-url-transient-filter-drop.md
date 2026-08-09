<!-- file: todo.d/20260809-library-url-transient-filter-drop.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5a83f7d2-1e94-4c60-b7a5-92c0d4e83f17 -->
<!-- last-edited: 2026-08-09 -->

- [ ] **Library throws the sidebar filter out of the URL and puts it back, on every
      click.** Sampling `location.search` every animation frame across a sidebar filter
      click shows `web/src/pages/Library.tsx` passing through a state where the filter is
      gone — reproduced 3 runs out of 3 with a standalone sampler:

      ```
      ?page=1                                 (initial)
      ?search=read_status:finished            (click applies the filter)
      ?page=1                                 <-- filter thrown away
      ?search=read_status%3Afinished&page=1   (re-applied, settled)
      ```

      This is the same shape #2193 was supposed to have fixed. The comment on the
      "survives the URL settling" test in
      `web/tests/e2e/library-sidebar-filters.spec.ts` says the stuck guard "used to throw
      the search away and restore a bare `page=1`". It still does — it just recovers now,
      within a few frames, so nothing downstream ends up in the wrong state.

      **Measured blast radius, so nobody re-derives it.** Exactly **one** request reaches
      `/api/v1/audiobooks` after the click, and it carries the correct `filters` param.
      The transient drop costs **no wasted query** and never sends the server the wrong
      thing. The cost is confined to the URL and history: a spurious intermediate entry,
      and a flicker for anything reading `searchParams` directly. That is why this is
      filed rather than treated as urgent — but it is also why it should not be closed as
      "works fine", because the URL is the app's shareable state and it is briefly wrong
      on every single filter click.

      **Likely location:** the two `useEffect`s in `Library.tsx` around lines 596-644 —
      one reads `searchParams` into state, the other writes state back into
      `searchParams`. The write effect rebuilds the param set from scratch
      (`const params = new URLSearchParams()`), so any render where `searchQuery` has not
      yet caught up emits a URL with no `search` at all. The `lastWrittenSearch` ref added
      earlier guards against re-reading our own write, not against emitting an incomplete
      one.

      **Regression test already written**, skipped as `test.fixme` in
      `library-sidebar-filters.spec.ts` ("the filter never disappears from the URL while
      the effects settle"). It is `fixme` rather than `fail` on purpose: forced on it
      fails 5 runs out of 8, so `test.fail()` would report a spurious "expected to fail
      but passed" about a third of the time. The recipe to force it is in the comment
      above the test. When the race is genuinely fixed it should pass 8/8 — flip it to a
      normal test then.
