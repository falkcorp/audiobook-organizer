- [ ] **F7-09-MD-ADAPTER-FIELDS** Whoever writes or reshapes the hardcover and google-books
      metadata adapters must keep declaring the Settings fields `api_token` (hardcover) and
      `api_key` (google-books): core keys an operator value by the passthrough path's last
      segment and drops a value whose field the adapter does not declare, so the two rows
      `integrations.metadata.hardcover.api_token` and `integrations.metadata.google-books.api_key`
      would otherwise stop applying with no error. `TestOperatorURLPaths_FieldsAreAdapterSettings`
      (internal/books/cutover/m6) enforces it against the adapters' descriptors and fails
      for any integrations passthrough path whose adapter it cannot resolve.
