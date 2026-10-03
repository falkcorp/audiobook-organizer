### Fixed

- Two tests that check a repair keeps its hold on the library scan while it
  waits for another job to finish failed now and then on a busy build machine
  (six times in one day), though nothing was wrong with the app. They gave the
  hold a 400 millisecond life on the real clock, and a slow machine sometimes
  took longer than that over a single database write later in the same repair.
  The tests now use a clock that only they move, and keep the other job
  running until they have seen the repair renew its hold, so the result no
  longer depends on how fast the machine is.
