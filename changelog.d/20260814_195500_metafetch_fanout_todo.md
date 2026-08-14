### Changed

- Corrected the matcher-blocking diagnosis: the metadata FETCH is the
  serialized, foreground half (writes are already backgrounded). Filed the
  fan-out-with-jitter background-op design todo.
