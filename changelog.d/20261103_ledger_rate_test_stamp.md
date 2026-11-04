### Fixed

- A timing test for how often a long operation saves its progress could fail on
  a busy CI machine: it timed the test's own fake store instead of the moment
  the operation decided to save. It now records the time as the save starts.
