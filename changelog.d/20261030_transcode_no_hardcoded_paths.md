### Fixed

- The malformed-M4B transcode pass no longer pre-marks two install-specific file
  paths as permanently unfixable on every completed run. A file is skipped only
  when a skip setting was recorded for it by an earlier attempt. The
  `pebble-inject-skip` tool now takes the file paths on its command line instead
  of carrying two hashed install-specific keys.
