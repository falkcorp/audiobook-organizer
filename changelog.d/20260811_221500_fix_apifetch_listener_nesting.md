### Fixed

- The request timeout helper no longer trips the memory-leak scanner. Its abort
  listener was always cleaned up correctly, but sat nested deeply enough that the
  scanner's look-ahead gave up before finding the cleanup and reported a leak
  that did not exist. Flattened the nesting; behaviour is unchanged.
