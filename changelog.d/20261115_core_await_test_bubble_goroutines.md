### Fixed

#### `TestAwait_*` goroutine check — no longer flaky: it counted a goroutine that had already exited

The Await liveness tests in `core/internal/executor` failed once in CI with
"15 goroutines after the runs ended, want at most the 14 from before". They
counted goroutines with `runtime.NumGoroutine` right after `synctest.Wait`,
and that count still includes a goroutine that has just exited: the runtime
tells the bubble the goroutine is gone, which lets `Wait` return, a few
instructions before it frees the goroutine, so one descheduled in that window
is counted. The tests now compare the live goroutines of their own bubble,
read from a stack dump (it omits dead goroutines and other bubbles), and print
the stack of any goroutine that outlives the run. Test-only; the executor does
not leak a goroutine on this path, and no production behaviour changed.
