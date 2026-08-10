<!-- TODO.md evidence only — no code, no behaviour change, so this fragment is
     deliberately a no-op comment. See changelog.d/README.md.

     Records a measurement of the internal/server test package for
     TODO-SRVTIMEOUT. The package now runs 543s (was recorded as 434-480s),
     leaving 9.5% headroom under Go's 600s default rather than ~30%. ~85% of
     wall time is idle, there is no slow test to fix (4 tests >=5s; the mass is
     296 tests at 1-5s), and setupTestServer+cleanup measures 1.44s mean across
     261 static call sites -- about 69% of the package.

     Phase breakdown: RunMigrations 57.4%, NewServer 32.8%, NewPebbleStore 9.3%.
     opRegistry.Shutdown -- the goroutine the #2083 panic dump named -- is 297us,
     0.0%, so the slowness and the deadlock-shaped panic are separate phenomena.

     This redirects the entry's proposed fix: sharding redistributes the fixture
     charge without removing it. Amortising migrations and NewServer is the
     lever. No code change here; the refactor spans ~260 call sites and wants its
     own plan. -->
