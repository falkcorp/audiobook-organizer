<!-- TODO.md evidence only — no code, no behaviour change, so this fragment is
     deliberately a no-op comment. See changelog.d/README.md.

     Records a measurement of the internal/server test package for
     TODO-SRVTIMEOUT. The package now runs 543s (was recorded as 434-480s),
     leaving 9.5% headroom under Go's 600s default rather than ~30%. ~85% of
     wall time is idle, there is no slow test to fix (4 tests >=5s; the mass is
     296 tests at 1-5s), and setupTestServer+cleanup measures 1.44s mean across
     261 static call sites -- about 69% of the package.

     This redirects the entry's proposed fix: sharding redistributes the fixture
     charge without removing it. Amortising the fixture is the lever. No code
     change here; the refactor spans ~260 call sites and wants its own plan. -->
