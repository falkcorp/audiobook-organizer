- [ ] **Consolidate the four `IsPlaceholder(StripEditionSuffix(...))` call sites**
      — `internal/scanner/scanner.go:1713`, `internal/scanner/scanner.go:3024`,
      `internal/metadata/metadata.go:733` and `:745` each strip the edition suffix
      before asking `authorname.IsPlaceholder`. The recorded reason for not putting
      the strip inside `IsPlaceholder` was that `authorname` had to stay
      standard-library-only; that stopped being true in PR #3035, which made
      `authorname` import `personname`. So the consolidation is now UNBLOCKED.
      All four sites are correct today — this is not a live bug. It is worth doing
      because the pattern has already produced one omission: `scanner.go:3024`'s own
      comment records that it "was missed when those were fixed".
      **Decide first:** `IsPlaceholder` is also asked about values that are not
      filename parses, where silently stripping a trailing parenthetical would be a
      surprise. A separate `IsPlaceholderDecorated` may be the better shape than
      changing `IsPlaceholder` itself.
