### Fixed

- The missing-primary repair no longer makes a merged-away or deleted book the primary of its version group. It used to pick the oldest member, and the book a merge absorbed is usually the oldest, so a group that lost its primary brought the absorbed copy back and ABS listed it next to the surviving book. A group whose members were all merged away or deleted now keeps no primary, and the run reports how many.
