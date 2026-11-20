- [ ] **TODO-F5-18M-LOAD-WIRING** Load the wiring packages (`internal/server`,
      `internal/plugins/...`) into the write-set oracle's SSA program
      (`internal/maintenance/jobs/writes_oracle_test.go`, `loadOracleProgram`)
      and measure the VTA cost against today's ~5.6 s. Today a module
      interface with one loaded implementation hides implementations declared
      in those packages (neither walked nor recorded), and the 3
      unloaded-interface keys and 11 exported-field keys rest on hand-traced
      reasons. With the wiring loaded, `declaredImpls` and the walk read those
      bodies. A types-only scan of the module would key most open-receiver
      reader calls instead (about 20 non-test `GetBookByID` implementers
      outside the loaded set, mocks included), so loading is the preferred
      fix. From the F5-18m round-3 review (M1, graded m).
