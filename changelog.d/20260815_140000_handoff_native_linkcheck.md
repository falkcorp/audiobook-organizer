### Fixed

- Handoff doc now states the native-taglib verification status accurately: the
  production CGO build compiles and links with the write-back changes, and the
  de-nesting pattern is runtime-verified through the WASM writer; what remains
  unexecuted is the CGO binding itself.
