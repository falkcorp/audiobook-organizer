### Changed

- `docs/audits/2026-08-16-store-interface-decomposition.md` — three owed evidence updates:

  - **The "~35 narrowable helpers" is measured and promoted out of ⚠.** In
    `internal/maintenance/...` + `internal/plugins/maintenance/...`, **81** declarations take a
    `database.Store` parameter: **37** `Run` methods (interface-constrained, not narrowable),
    **10** other methods, **34** free functions — of which 33 are narrowable helpers
    (`maintenance.InjectStore` is the framework's injection setter). Corroborated independently:
    `maintenance.Register(` appears exactly 37 times in non-test code (40 raw — three are test
    probes in `internal/server`).

  - **A circulated derivation is retracted.** The figure had been justified as *"55 functions
    minus 20 `Run` implementations = 35."* Both operands are wrong — 81 and 37 — and the answer
    only looked right because two errors roughly cancelled. Related: a single-line grep for
    `Run` methods returns 35, missing two that declare parameters one-per-line; the AST count is
    the correct one.

  - **PR #2503's effect is measured A/B, not claimed.** It narrowed **19 declarations = 16 free
    helpers + 3 methods** (not "19 of the 35 free helpers"), leaving **24**. The same
    miscategorisation had listed a method among the "free helpers" still to do. Monotonicity
    (§8) is now n=2: 19 narrowings at once, zero call-site and zero test edits.

  - **§7 gains Options B/C/D/E/F.** The section previously presented narrow interfaces as the
    only shape. **Option C — split the pure decision out of the I/O — is stronger where it
    applies**, because it removes the dependency rather than narrowing it, and it produces
    Option D as a by-product. Its cost is documented too: composition and any count-agreement
    invariant move up into `Run`, which is the one function in the package that cannot be
    narrowed. Worked example: `deleteOldOperations`.
