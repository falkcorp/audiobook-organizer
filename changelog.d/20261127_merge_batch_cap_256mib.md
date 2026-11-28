### Changed

- Merging two user accounts now handles accounts with up to 256 MiB of stored data in one step (it was 64 MiB). A rehearsal on a copy of the production library measured a real account at about 128 MB, which the old limit would have refused with "this merge is too large". Larger merges are still refused before anything changes.
