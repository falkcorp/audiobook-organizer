### Changed

- Docker Compose now gives the server 60 seconds to stop and sets a 25-second shutdown budget; the macOS launchd file waits 60 seconds.
- `serve` reads `ABO_CORE_SHUTDOWN_BUDGET` and passes it to the core app as its shutdown budget. A value that is not a positive duration (for example `abc` or `0s`) now stops `serve` at startup with an error naming the variable; unset keeps the default of 25 seconds.
