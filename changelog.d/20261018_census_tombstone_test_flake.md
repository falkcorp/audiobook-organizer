### Fixed

#### `TestRunExactCensus_TombstoneFamilyHeartbeatsAndResumes` — no longer flakes on a slow race runner

The test failed on a slow `-race` CI runner after running 218 seconds. It killed
each rerun of the exact census with a 400 ms deadline measured from the start
of the rerun, which also pays for the flush, the sub-range bisection, the
iterator open and the first sub-range's walk and budget wait. When that alone
outlasted the deadline no rerun ever saved a resume position, and the loop used
up all 200 attempts without finishing. Each rerun is now killed after it has
saved five in-family positions, so every rerun advances and the loop is bounded
by the number of sub-ranges.

The heartbeat check now actually checks something: the read budget is set low
enough that the pass has to wait on it, the test requires that progress was
reported while counting the tombstone family, and the gap between heartbeats
and saves is measured against a 2 second watchdog (40 times the heartbeat).
Deleting every in-family heartbeat from the census now fails the test. No
production code changed.
