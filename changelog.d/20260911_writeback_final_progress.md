### Fixed

#### Bulk tag write-back reports its true final count

The bulk write-back operation's last progress row could show fewer books than it processed, because workers reported their running count without a lock and an older report could land after a newer one. The operation now restates the final tally once the worker pool has drained, so the Activity page ends on the real number.
