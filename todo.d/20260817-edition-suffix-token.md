- [ ] **Add an `{edition_suffix}` folder-pattern token.** Two editions of the same
      title sharing a `{print_year}` compute the same target path under the
      current default (`{author}/{series}/{title} ({print_year})`). They do not
      clobber — `OrganizeBook` stats the target, finds a different file owned by a
      different book, fires `OnCollision` to raise a dedup candidate and returns
      `ErrTargetOccupied`, and both `rename.go` and `move.go` refuse to overwrite
      independently — but the second edition simply never gets organized, which
      looks like "organize didn't run" unless someone checks the collision queue.
      `{edition}` already exists in the token vocabulary (`pathbuild.go`), but it
      is a raw value: books with no edition would render a dangling space or empty
      parens. Model the new token on `{series_prefix}`, which is built AFTER the
      trim pass precisely so its separator counts as pattern structure rather than
      metadata and collapses to "" when the value is empty.
      Discussed 2026-08-17; deliberately deferred — collisions are visible and
      safe, so this is an ergonomics fix, not a correctness one.
