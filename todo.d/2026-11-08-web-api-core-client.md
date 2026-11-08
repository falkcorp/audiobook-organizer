- [ ] **PLATFORM-CORE-R1-30-WEB-API-CORE-TS** Create `web/src/api/core.ts`, ABO's one
      pre-shell client, `createCoreClient({ buildId: readPageBuildId(), shell: false })`
      (07 §7.5.2, 05-http-api §7 say "created in release R1"). R1-30 did not: the
      file is outside its inventory, `core.json` has no operation yet, so nothing
      could call the client, and an unused module is dead code the deadcode and knip
      gates flag. Create it in the first R2 flip that switches a UI call to a
      generated operation (07 §7.19 step 3), with `onUnauthorized` wired to the
      auth reset of 07 §7.9.
