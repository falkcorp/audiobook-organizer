Fixed a flaky test, `TestRunExactCensus_TombstoneFamilyHeartbeatsAndResumes`,
that failed on a slow `-race` CI runner after running 218 seconds. It killed
each rerun of the exact census with a 400 ms deadline measured from the start
of the rerun, which also pays for the flush, the sub-range bisection and the
iterator open; when that setup alone outlasted the deadline no rerun ever saved
a resume position, and the loop used up all 200 attempts without finishing.
Each rerun is now killed after it has saved two in-family positions, so every
rerun advances, the loop is bounded by the number of sub-ranges, and the
heartbeat gap is measured only across counting a sub-range. No production code
changed.
