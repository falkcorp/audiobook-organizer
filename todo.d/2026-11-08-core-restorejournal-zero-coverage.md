- [ ] **CORE-RESTOREJOURNAL-ZERO-COVERAGE** The first `make -C core readiness`
      run measured 0% coverage for `storage/restorejournal` and
      `storage/restorejournaltest`, below SC5's 60% per SDK package. Add tests,
      or decide that the test-helper package is excluded from the SDK list in
      `core/Makefile`'s coverage target.
