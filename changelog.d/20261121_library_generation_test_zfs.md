### Fixed

- The source-unchanged test for "another file, told by the generation alone" no longer fails on OpenZFS. ZFS answers the inode-generation ioctl with one value for every file in the dataset, so no replacement can differ by generation there; the subtest now reads the replacement's generation and skips, saying why, when it is not a per-inode value. The check itself is unchanged: an equal generation never proves a file is unchanged, the inode, change time and creation time are still compared.
