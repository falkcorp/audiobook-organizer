- [ ] **PLATFORM-CORE-R1-25-RESET-PLAN-KEPT** From F4-05 the core reset
      route stages a factory reset from the running app. It must set
      `module.WithResetPlanKept` on the context it calls the reset hook's
      `Plan` with, as the offline `storage reset` does
      (`core/kernel/reset.go`). Without it `ResetPlanKept` reports false,
      books' preflight counts every library entry, the kept ones included,
      and the confirmed `--library-file-count` no longer equals what the
      wipe removes. `WithResetPlanStorage` is not needed there: the running
      module's `Plan` reads its own `Deps.Storage`
      (`core/module/resetplan.go`, "Release R1 only" on both).
