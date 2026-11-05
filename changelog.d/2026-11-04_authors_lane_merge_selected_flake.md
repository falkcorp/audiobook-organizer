### Fixed

- Tests: two Authors-lane series tests ("merge selected reports the failures, with counts" and "a failure that names no ids keeps the group and re-reads") asserted the groups right after the failure toast. The toast fires when the re-read is requested, not when it lands, so on a loaded CI agent the lane was still loading and held no groups. Both now wait for the re-read. Reproduced deterministically by delaying the re-read by 200 to 300 ms.
