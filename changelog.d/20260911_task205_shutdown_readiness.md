### Changed

#### `TestServerStartGracefulShutdown` waits for readiness instead of sleeping 6 s (TASK-205)

`Server.Start` now closes an unexported `shutdownArmed` channel once every subsystem is up and only the signal wait remains. The graceful-shutdown test sends its SIGTERM on that signal (with a 30 s ceiling and an early-exit check) instead of an unconditional 6-second sleep, so the test is both faster and deterministic: it always exercises a fully-started server, and a `Start` that fails early is reported rather than raced. Production code never reads the channel.
