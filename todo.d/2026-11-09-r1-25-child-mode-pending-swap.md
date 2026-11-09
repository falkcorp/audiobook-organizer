- [ ] **PLATFORM-CORE-R1-25-CHILD-PENDING-SWAP** The op runner's child
      (`cmd/child_mode.go` `initializeChildStore`) opens through the
      standalone opener with the state directory, so it refuses a pending
      swap as well as an in-flight one. A pending intent is harmless to a
      child whose parent is serving the same store (the swap runs only at
      the next boot), so child ops started between a live restore's 202
      and the exit 75 fail. Decide whether child mode should refuse only an
      in-flight swap, and test it either way.
