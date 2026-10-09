### Changed

- The errcheck and interface-width ratchets are one-way (owner decision D47, task 07-C1): a count that goes UP still fails CI; a count that goes DOWN now prints a `::notice::` naming the new number and passes, instead of failing until the baseline file is lowered in the same PR. Both baseline headers say so. A merge that improves a count can no longer turn `main` red for the next PR.
