- [ ] **UI-SEARCH-IDENTITY** The per-book UI metadata search caches rows with
      empty identity fields. `internal/server/handlers/metadata/handler.go:556`
      calls `FetchAndCache` only on a plain fetch, where query, author,
      narrator and series are all `""`. The cached row's `SourceHash` is
      therefore `hashSearchInputs(id, "", "", "", "")`, which no
      current-fields recompute ever matches. Every UI-fetched candidate then
      fails the bulk apply's identity leg as `identity_stale`, which no owner
      review can lift. Done means the plain fetch hashes the book's actual
      title and author (the same shape the batch fetch records), with a test
      that a UI-fetched row passes `ValidateCachedIdentityForBook`.
