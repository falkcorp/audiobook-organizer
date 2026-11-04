### Fixed

- A repairs test recorded which rows saw the owner-apply marker in a plain map
  while the apply step processes rows in parallel, so it could fail under the
  race detector. The test's recorder now takes a lock.
