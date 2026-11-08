- [ ] **PLATFORM-CORE-R1-30-STALE-BUILD-E2E** Add the production-`dist/` e2e cell for the
      stale-build rule (07 §7.18 `stale-build.spec.ts`, FE6-1): against a server
      whose `X-App-Build` equals the page's meta no reload is offered, against one
      where it differs the reload is offered, which proves the value the api-client's
      `dist/` uses is the page's and not one baked at the library build. R1-30's
      tests run under vitest, which resolves the built `dist/` of the api-client but
      not a production page bundle; the cell belongs with the ABO e2e suite and the
      PR image job.
