- [ ] **TODO-SCANNER-OWNERSHIP-SKIP-TAINT** CodeQL alerts #2022 and #1651
      (go/path-injection) were dismissed as false positives on 2026-10-28. The
      taint reaches the sink because `scanner.handleOwnershipSkip` unpacks a
      wrapped store error with `errors.As` into `ownershipSkipError`, whose
      `Unowned` file list then flows to hashing; the book-id text in the error
      taints the whole struct. Fix: stop carrying `Unowned` and `AppendTo`
      inside the error. Return them alongside it, so CodeQL's taint cannot
      cross, then re-enable the alerts.
