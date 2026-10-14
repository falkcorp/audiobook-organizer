### Fixed

#### Startup no longer spends 30 minutes in the garbage collector

After every restart the library-list cache warmer forced two full garbage collections after each of its roughly 180 queries, which kept the process at 3.5 to 4 cores for about half an hour; the runtime now paces collection itself and the warmer's memory guard reads the live heap without stopping the world. The once-a-minute primary-book count is now answered from the in-memory tables instead of rescanning every book in Pebble, and the authors and series cache warmers wait for those tables to load instead of running full Pebble scans while they are still loading.
