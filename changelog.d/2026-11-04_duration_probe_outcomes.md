### Changed

- A failed duration probe is now recorded on the file instead of being swallowed. A file that ffprobe reads and cannot give a length for (an error, 0 s, unparseable output, a timeout, or only a size-based estimate) gets one counted attempt with the reason, and reads as "probe failed" after one and "unreadable" after three, while its duration stays unset. A missing ffprobe, a file that is not there, a file or folder this process may not read, a dropped network mount, or a file whose extension is not audio counts for nothing and the file stays "not probed". Every place that measures a duration records it: new-row creation, the scanner's replaced-file check, the repoint-missing-to-folder-audio op, and the duration re-extract op. A successful measurement marks the duration stage done and clears any earlier failures, and a stored duration never sits beside a failure record.
- A file at the three-attempt ceiling is no longer probed again by the scanner or the duration re-extract op; it stays "unreadable" until its duration is reset on purpose. The scanner also skips probing a file whose stored row already has a duration when the file has not changed.
- A rescan that sets only the duration stage no longer wipes the file's other scan stages. The windowed and full-file fingerprint stages are now cleared when the file's audio changes, as the duration stage is.
- The repoint op's dry run logs probe failures at debug instead of one warning per target, and its apply refuses a target that changed between the plan and the write.

### Fixed

- Good .wav, .opus, .aax, .wma, .aac and .mka files were never measured by the header read (it refused any extension without a codec table), so they would have counted failed probes and gone "unreadable". They are now measured like every other audio file.
