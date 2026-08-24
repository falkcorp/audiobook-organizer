- [ ] **METADATA-GUARD-ASYMMETRY** Decide whether
      `handlers/metadata/handler.go:1001` should also check `HasProviderValue()`.
      Today it checks only `HasUserOverride()`, and nobody knows if that is
      deliberate.

      PR #2817 introduced two guard methods on `MetadataFieldState` —
      `HasUserOverride()` (`OverrideLocked || OverrideValue != nil`) and
      `HasProviderValue()` (`FetchedValue != nil`) — and repointed three
      predicate sites at them. Two check **both**:

      - `plugins/maintenance/repair_junk_titles.go:141` —
        `HasUserOverride() || HasProviderValue()`
      - `plugins/maintenance/title_repair.go:117,120` — both, separately

      The third checks only the first:

      - `server/handlers/metadata/handler.go:1001` — `HasUserOverride()` alone

      **The asymmetry predates #2817 and was preserved verbatim.** It was not
      introduced by the guard refactor and was deliberately not "fixed", because
      no justification for it could be found. The comments near that call site
      (`handler.go:31`, `:122`) explain something else entirely — why
      `loadMetadataState` is injected as a concrete type — and say nothing about
      the predicate.

      **Record of what is and is not known:** it is *unexplained*, not
      *established as intentional*. Whoever touches this next would otherwise
      face the identical choice with no more information than was available on
      2026-08-23, which is the reason this fragment exists rather than the note
      living only in a merged PR description.

      **To settle it:** determine whether a field with a provider value but no
      user override should be treated as "has state" by that handler. If yes,
      the site is a bug and should check both. If no, add a comment saying so,
      naming the two sites that differ — the divergence is otherwise invisible
      from any one of the three.

      Filed at the suggestion of a parallel session that hit the same three
      files from the series-merge side and had no evidence either way.
