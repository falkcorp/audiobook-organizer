- [ ] **Audible metadata upgrade job** — add a dry-run-first maintenance
      operation that revisits accepted metadata originating anywhere other than
      Audible, searches from the now-normalized local title/author/series, and
      records the proposed Audible replacement. Apply only identity-verified,
      higher-quality matches; never overwrite a user/manual choice merely
      because an Audible result exists. Persist one result per book so failures
      remain reviewable and retryable rather than silently changing a library.
