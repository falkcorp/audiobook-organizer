### Changed

- Test-only: the books test servers get their free loopback port from one place, the boot helper every running test server is built through, instead of each test naming the port itself. Boots that never start serving bind nothing and set no port.
