- [ ] Audit existing "we use the wide type because X requires it" comments across the
      codebase. Two were checked on 2026-08-18 and both were stale —
      `handlers.OrganizeStore` (`= database.Store`, 398 methods) and
      `handlers/operations.OperationsStore` both cited call sites that had since been
      narrowed. Grep for justification comments near `database.Store` /
      `database.BookStore` and re-verify each claim against the current signatures.
