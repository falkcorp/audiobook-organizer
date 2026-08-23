- [ ] **COLLECTION-CAS-SENTINEL** `PebbleStore.UpdateCollection`'s new
      Version compare-and-swap (TODO L4501, closed by the CAS PR) signals a
      conflict with a plain `fmt.Errorf("collection %s version conflict: ...")`
      string, matched at call sites via
      `strings.Contains(err.Error(), "version conflict")` — the same
      string-matching pattern already used for the "already in use" duplicate-name
      conflict in the same file. That pattern is brittle (a future wording change
      silently stops matching, with no compiler or test failure pointing at the
      break) and was reused deliberately for this task to stay consistent with
      the existing convention rather than introduce a second one. Consider adding
      a sentinel `var ErrCollectionVersionConflict = errors.New(...)`, wrapping it
      with `%w` in `pebble_store_collections.go`, and switching call sites to
      `errors.Is`. Do the same for the existing "already in use" name-conflict
      error while touching this if it is in scope, so the file has one pattern
      instead of two.
