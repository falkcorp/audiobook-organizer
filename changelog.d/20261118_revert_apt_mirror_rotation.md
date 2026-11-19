### Fixed

- CI no longer kills healthy apt downloads of the ffmpeg and fixture packages. The mirror rotation and throughput floor added the same day (#4729) watched the wrong directory, so a download that was going fine looked stalled and was killed, and the mirror rewrite never found the runner's source host. Both are reverted; a corrected version follows separately.
