### Fixed

- Removed the unreachable exported `metadata.WriteMetadataToFileInPlace` that turned the deadcode ratchet red after #3968; the write-back tests now exercise `metadata.WriteMetadataToFileAtomic`, the function the write-back actually calls.
