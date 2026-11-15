- [ ] **TODO-F5-10-NARROW-WRITER-TYPES** Give each F5-10 kind a writer type that has only
      the methods its Effects allow, instead of the shared `t6g5Writer` (30 methods) that
      every kind of the group receives. Today a kind can call a method outside its own
      write set and only the tests and the F5-18 run-time fence refuse it; with narrow
      types the compiler refuses it, and the per-call checks in
      `kinds_t6_g5_storediff_test.go` become a second line rather than the only one.
      Cost, sized by the round-6 review: 13 to 14 files and 150 to 250 lines (one
      interface per kind, the head and native writers split to match, the spy and the
      fixtures adjusted). This is F5-18 row work: that row's fence refuses an
      out-of-Effects write at run time, so it should decide whether the compile-time
      version is worth the churn. Brief:
      `docs/agent-tasks/platform-core/F5-18-tasks-flip-engine-deleted.md`.
