<!-- file: todo.d/20260809-authors-page-aliases-crash.md -->
<!-- version: 1.0.0 -->
<!-- guid: f47a2c19-6b83-4d50-9e21-8c73b0a5e6d4 -->
<!-- last-edited: 2026-08-09 -->

- [ ] **The Authors page crashes to the error boundary if any author record lacks
      `aliases`.** `web/src/pages/Authors.tsx:89`, `:120` and `:121` all read
      `a.aliases.length` with no guard, so a single author row without the field takes
      the whole page down with "Cannot read properties of undefined (reading 'length')"
      — not a blank column, the entire page. This surfaced in e2e on 2026-08-09 (the
      mock omitted `aliases`), but the same crash would happen against a real response
      if `/api/v1/authors` ever returns a record without it — an older row, a partial
      projection, a new code path that builds the list differently. `a.aliases?.length
      ?? 0` in the three places would make it fail soft. Worth checking whether the Go
      handler guarantees the field is always present and non-null in JSON, since an
      omitted key and a `null` both land the same way here.
