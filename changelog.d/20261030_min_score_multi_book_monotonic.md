### Fixed

- The review queue's minimum match score no longer hides rows when it is lowered. With "hide multi-book" on, books that share a candidate were counted after the score filter, so lowering the minimum let a second book into a group and hid the first one (nothing listed at 150, 40 rows at 200). Groups are now counted among the rows every other filter keeps, so a lower minimum always lists a superset.
