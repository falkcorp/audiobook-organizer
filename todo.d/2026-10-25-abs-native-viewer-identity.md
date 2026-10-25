- [ ] **TODO-VIEWER-ID** Make the native and Audiobookshelf collection surfaces resolve the
      per-viewer identity the same way — with authentication off the native routes evaluate as
      `_local` (`CallingUserID`), while the Audiobookshelf surface has no user and falls back to
      "" (no viewer, so a per-user collection shows no books). Falling back to `_local` on the
      Audiobookshelf side is NOT a safe fix on its own: an unresolved caller would read `_local`'s
      state, which fails open. Decide what an unauthenticated Audiobookshelf caller should see
      when auth is off, then give both surfaces one resolver. Left from the PR 4155 review (N5).
