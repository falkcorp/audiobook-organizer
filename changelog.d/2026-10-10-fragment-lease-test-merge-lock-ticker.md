### Fixed

- The fragment-fixer lease tests no longer fail intermittently on a slow CI runner. They spend an exact lease-renewal budget, but the merge-lock wait also renews the lease every 30 seconds, so a runner that kept a parallel test's merge lock for longer than that used up the budget early and the lease was refused at the retire's soft-delete instead of the hand-off. The shared `noWait` test options now switch the wait's ticker off. No production code changed: the 30 second renewal during a lock wait is correct.
