### Added

- A new test set compares what each route's permission row in the upgraded server's route table asks for with the permission checks the route runs today in its own handler chain (the permission atom, the admin gate, the credential and owner guards, the sign-in check), read from the real server rather than a hand-kept list. It fails when a row asks for less than the check it will one day replace, so that removing the old check cannot quietly loosen a route, and it also fails when a row asks for something no check enforces. Two kinds of difference the comparison cannot call weaker or stronger (the admin gate tests the admin role by name where the row names a permission, and a guard that refuses only some requests of a route) are each listed by route, with the test that proves the route's behaviour today; the check fails on a route that is of either kind and not listed, and on a listed route that no longer is.

### Changed

- The four master-list routes now also require the roles-view permission in the upgraded server's route table, which is what the old check on those routes already required; nothing is refused that was allowed before, and nothing is allowed that was refused.
