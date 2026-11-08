- [ ] **PLATFORM-CORE-R1-19-BINARYID-CROSS-BINARY** Prove `TestBinaryIDDistinguishesBuilds`'
      second half, "two binaries with different code have different IDs", across two
      real executables. R1-19's `core/app/binaryid_test.go` proves `BinaryID` is
      `buildid.Binary(opts.Version)` and changes with the version, and relies on
      `core/buildid`'s own test that the hash is the executable file's SHA-256; a
      unit test cannot build and run a second binary cheaply. Add the proof to the
      image job (run `--version-json` on the amd64 and cgo builds, which differ in
      code, and assert different `binary_id`) or a core test that `go build`s two
      tiny programs with different `-ldflags -X`.
