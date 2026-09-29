- [ ] **FLAKE-WDOGTOUCH** `TestWatchdog_TouchLivenessAloneKeepsOpAlive`
      (`internal/operations/registry/touch_liveness_watchdog_test.go`) failed
      on Woodpecker pipeline 287 (#3619 head 8e2d181f4, unrelated change):
      the op touches liveness every 20ms against a 100ms ProgressTimeout, and
      under `-race` with two test-rest runs sharing the Mac a 20ms sleep
      overran 100ms, so the watchdog struck and canceled it. Widen the margin
      (e.g. ProgressTimeout 1s, touch every 20ms, run 2s) or drive the
      watchdog with an injected clock; done = 50 consecutive `-count=50
      -race` passes under a parallel `make ci-woodpecker` load.
