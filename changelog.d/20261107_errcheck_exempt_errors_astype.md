### Changed

- The errcheck lint no longer counts `errors.AsType` type tests as dropped errors. They were 37 of the 726 tolerated findings, and the Go modernizer rewrites code into exactly that form, so the two checks contradicted each other. The tolerated count drops to 689; no code changed.
