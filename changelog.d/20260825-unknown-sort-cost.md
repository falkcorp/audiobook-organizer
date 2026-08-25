### Fixed

- A mistyped or unrecognised sort option no longer causes the server to load
  the entire matching library in order to sort it by nothing. The request is
  still accepted and returns the same books as before; it is simply no longer
  expensive.
