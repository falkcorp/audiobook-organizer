### Fixed

- **Maintenance ops now accept the path-prefix param under either spelling and
  reject unknown keys.** `mark-missing-files`, `merge-same-path-dupes` and
  `missing-file-repoint` read `pathPrefix`, while `missing-file-audit` and
  `missing-file-repair` read `path_prefix`. `encoding/json` ignores unknown
  keys, so sending the other spelling (for example
  `{"path_prefix": "/lib", "apply": true}` to `missing-file-repoint`) left the
  prefix empty and the apply ran over the whole library. All five now accept
  `pathPrefix` or `path_prefix`. If both are sent with different values the op
  fails, and any unknown key (such as a typo like `path_prefx`) fails the op
  with the key named, before any store read or write. An empty body still
  means no filter. Params stored by earlier runs decode unchanged on retry,
  whichever spelling they used.
