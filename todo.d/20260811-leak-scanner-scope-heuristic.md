- [ ] **LEAKSCAN-SCOPE** `scripts/check-memory-leaks.py` reports a false
      `addEventListener without removeEventListener` when the add is nested more
      than one brace level deeper than the cleanup. Its look-ahead abandons the
      search once `scope_depth < -1`, so an add inside `if (x) { if (y) {...}
      else { add } }` with the matching remove in a `finally` at function level
      is never paired — the two closing braces end the scan first.

      Hit for real on 2026-08-11 in `web/src/utils/apiFetch.ts`: the listener
      *was* removed in `finally`, and CI failed anyway. Worked around there by
      flattening the nesting, which is not a fix — the next correctly-cleaned-up
      listener at that depth will fail the same way, and the obvious "fix" a
      future contributor reaches for is deleting the check.

      Proper fix: pair by handler identity within the enclosing function rather
      than by brace-depth proximity, or track the function body extent instead
      of a running depth counter. Whatever is chosen, add a regression fixture
      with the add nested two levels below the remove so the heuristic cannot
      silently regress.
