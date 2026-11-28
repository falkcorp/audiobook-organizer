### Fixed

- A server started with the Audiobookshelf-compatible API switched on but a missing or too-short signing secret (or an unknown sign-in mode, or Cloudflare-only sign-in without its Access settings) now stops with a clear error naming the problem. Before, it quit with exit code 1 from deep inside startup, and the reason could be lost with the log.
